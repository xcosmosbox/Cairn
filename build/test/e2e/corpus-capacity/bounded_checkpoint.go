//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"golang.org/x/sys/unix"
)

// This is a controller checkpoint, NOT another delivery ledger. Every response
// is authenticated against the bridge's complete existing trace-ledger inventory.
// A pure extraction seal, a response hash or a ReceiptID is never delivery proof.
type boundedSavedCall struct {
	Request      llm.CompleteRequest
	Metadata     stagedBridgeMetadata
	Binding      incremental.StagedMappingWireBinding
	WireSHA256   string
	MappingUnits []string
	Result       *incremental.StagedMappingDispatchResult
}
type boundedInventoryAttempt struct {
	Metadata                  stagedBridgeMetadata
	LogicalSHA256, WireSHA256 string
	Binding                   incremental.StagedMappingWireBinding
}
type boundedInventoryWork struct {
	ID               string
	NodeIDs, Members []string
	Pairs            []extract.BoundedPair
	Attempts         []boundedInventoryAttempt
}
type boundedPhaseInventory struct {
	Key, Stage, ParentRoot                               string
	AcceptedIncidences, MappingUniqueUnits, MaximumCalls int
	Ceiling                                              bool
	Works                                                []boundedInventoryWork
	Cross                                                *boundedCrossRecall
}

// A publication pin is independent of the generation it authenticates. It is
// fsynced in this authoritative controller before rename. Integrity of that
// controller is required; this is not protection against jointly replacing all
// trusted independent evidence.
type boundedPublicationReady struct{ RunSHA256, ManifestSHA256, CheckpointHeadSHA256, PrivatePath, TargetPath string }
type boundedCheckpointEvent struct {
	Kind        string
	Root        string                   `json:"root,omitempty"`
	Call        *boundedSavedCall        `json:"call,omitempty"`
	Inventory   *boundedPhaseInventory   `json:"inventory,omitempty"`
	Publication *boundedPublicationReady `json:"publication_ready,omitempty"`
}
type boundedCheckpointLine struct {
	Sequence int
	Previous string
	Event    boundedCheckpointEvent
}
type boundedCheckpoint struct {
	file          *os.File
	head          string
	sequence      int
	root          string
	calls         map[string]*boundedSavedCall
	order         []string
	replayed      map[string]bool
	authenticated map[string]string
	inventories   map[string]boundedPhaseInventory
	publication   *boundedPublicationReady
	// fault is test-only, after the named durable boundary.
	fault func(string) error
}

func boundedDigestBytes(b []byte) string  { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func boundedEncode(v any) ([]byte, error) { return json.Marshal(v) }
func boundedDigest(v any) string {
	b, e := boundedEncode(v)
	if e != nil {
		return ""
	}
	return boundedDigestBytes(b)
}
func boundedCallKey(m stagedBridgeMetadata) string {
	return fmt.Sprintf("%s/%s/%s/%d", m.Stage, m.WorkID, m.ParentCatalogueRoot, m.Attempt)
}
func boundedCopyCall(c *boundedSavedCall) *boundedSavedCall {
	b, _ := json.Marshal(c)
	var out boundedSavedCall
	_ = json.Unmarshal(b, &out)
	return &out
}

func openBoundedCheckpoint(dir, root string, resume bool, fault func(string) error) (*boundedCheckpoint, error) {
	if len(root) != 64 {
		return nil, fmt.Errorf("bounded checkpoint: missing independently frozen run root")
	}
	if e := boundedSafeExistingDirectory(dir); e != nil {
		return nil, e
	}
	flags := os.O_RDWR | os.O_CREATE | os.O_EXCL
	if resume {
		flags = os.O_RDWR
	}
	fd, e := unix.Open(filepath.Join(dir, "controller.jsonl"), flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir, "controller.jsonl"))
	if e = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("bounded checkpoint: controller already owned: %w", e)
	}
	if st, se := f.Stat(); se != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("bounded checkpoint: expected regular checkpoint file")
	}
	c := &boundedCheckpoint{file: f, root: root, calls: map[string]*boundedSavedCall{}, replayed: map[string]bool{}, authenticated: map[string]string{}, inventories: map[string]boundedPhaseInventory{}, fault: fault}
	failed := true
	defer func() {
		if failed {
			_ = f.Close()
		}
	}()
	if resume {
		b, e := io.ReadAll(f)
		if e != nil {
			return nil, e
		}
		if len(b) == 0 || b[len(b)-1] != '\n' {
			return nil, fmt.Errorf("bounded checkpoint: incomplete crash tail; reconciliation required")
		}
		for _, line := range bytes.Split(b[:len(b)-1], []byte{'\n'}) {
			var row boundedCheckpointLine
			if json.Unmarshal(line, &row) != nil {
				return nil, fmt.Errorf("bounded checkpoint: corrupt line")
			}
			canonical, e := json.Marshal(row)
			if e != nil || !bytes.Equal(canonical, line) || row.Sequence != c.sequence+1 || row.Previous != c.head {
				return nil, fmt.Errorf("bounded checkpoint: noncanonical/corrupt chain")
			}
			if e = c.consume(row.Event); e != nil {
				return nil, e
			}
			c.sequence = row.Sequence
			c.head = boundedDigestBytes(line)
		}
		if c.sequence == 0 {
			return nil, fmt.Errorf("bounded checkpoint: missing header")
		}
	} else if e = c.append(boundedCheckpointEvent{Kind: "header", Root: root}); e != nil {
		return nil, e
	}
	if !resume {
		d, de := os.Open(dir)
		if de != nil {
			return nil, de
		}
		if de = errors.Join(d.Sync(), d.Close()); de != nil {
			return nil, de
		}
	}
	failed = false
	return c, nil
}
func (c *boundedCheckpoint) consume(event boundedCheckpointEvent) error {
	if event.Kind != "header" && event.Root != "" || event.Kind != "publication-ready" && event.Publication != nil {
		return fmt.Errorf("bounded checkpoint: unexpected transition fields")
	}
	if event.Kind == "header" {
		if c.sequence != 0 || event.Root != c.root || event.Call != nil || event.Inventory != nil || event.Publication != nil {
			return fmt.Errorf("bounded checkpoint: run identity mismatch")
		}
		return nil
	}
	if c.publication != nil {
		return fmt.Errorf("bounded checkpoint: work cannot extend publication-ready state")
	}
	if event.Kind == "publication-ready" {
		p := event.Publication
		if c.sequence == 0 || event.Call != nil || event.Inventory != nil || p == nil || p.RunSHA256 != c.root || p.CheckpointHeadSHA256 != c.head || len(p.ManifestSHA256) != 64 || p.PrivatePath == p.TargetPath || !filepath.IsAbs(p.PrivatePath) || filepath.Clean(p.PrivatePath) != p.PrivatePath || !filepath.IsAbs(p.TargetPath) || filepath.Clean(p.TargetPath) != p.TargetPath || filepath.Dir(p.PrivatePath) != filepath.Dir(p.TargetPath) || !strings.HasPrefix(filepath.Base(p.PrivatePath), ".bounded-generation-") {
			return fmt.Errorf("bounded checkpoint: invalid publication-ready transition")
		}
		for _, call := range c.calls {
			if call.Result == nil {
				return fmt.Errorf("bounded checkpoint: unknown paid outcome before publication")
			}
		}
		copy := *p
		c.publication = &copy
		return nil
	}
	if event.Kind == "inventory" {
		inv := event.Inventory
		if c.sequence == 0 || event.Call != nil || inv == nil || inv.Key == "" || inv.Stage == "" || inv.ParentRoot == "" || inv.MaximumCalls < 0 {
			return fmt.Errorf("bounded checkpoint: malformed phase inventory")
		}
		if _, exists := c.inventories[inv.Key]; exists {
			return fmt.Errorf("bounded checkpoint: duplicate inventory")
		}
		raw, e := boundedEncode(inv)
		if e != nil {
			return e
		}
		var copy boundedPhaseInventory
		if e = json.Unmarshal(raw, &copy); e != nil {
			return e
		}
		c.inventories[copy.Key] = copy
		return nil
	}
	if c.sequence == 0 || event.Call == nil || event.Inventory != nil {
		return fmt.Errorf("bounded checkpoint: missing header/call")
	}
	x := event.Call
	k := boundedCallKey(x.Metadata)
	if x.Metadata.WorkID == "" || x.Metadata.ParentCatalogueRoot == "" || x.WireSHA256 != x.Binding.RequestSHA256 || boundedDigest(x.Request) == "" {
		return fmt.Errorf("bounded checkpoint: malformed prepared call")
	}
	old := c.calls[k]
	switch event.Kind {
	case "intent":
		if old != nil || x.Result != nil {
			return fmt.Errorf("bounded checkpoint: duplicate work-attempt")
		}
		if e := c.authorizeInventory(*x); e != nil {
			return e
		}
		c.calls[k] = boundedCopyCall(x)
		c.order = append(c.order, k)
	case "response":
		if old == nil || old.Result != nil || x.Result == nil {
			return fmt.Errorf("bounded checkpoint: response without unique intent")
		}
		a, b := boundedCopyCall(old), boundedCopyCall(x)
		a.Result = nil
		b.Result = nil
		if !reflect.DeepEqual(a, b) || x.Result.Binding != x.Binding || x.Result.ReceiptID == "" {
			return fmt.Errorf("bounded checkpoint: response binding changed")
		}
		c.calls[k] = boundedCopyCall(x)
	default:
		return fmt.Errorf("bounded checkpoint: unknown transition")
	}
	return nil
}
func (c *boundedCheckpoint) append(event boundedCheckpointEvent) error {
	row := boundedCheckpointLine{c.sequence + 1, c.head, event}
	b, e := json.Marshal(row)
	if e != nil {
		return e
	}
	// Validate a prospective transition without changing the authoritative state.
	probe := *c
	probe.calls = map[string]*boundedSavedCall{}
	for k, v := range c.calls {
		probe.calls[k] = v
	}
	probe.order = append([]string(nil), c.order...)
	probe.inventories = map[string]boundedPhaseInventory{}
	for k, v := range c.inventories {
		probe.inventories[k] = v
	}
	if e = probe.consume(event); e != nil {
		return e
	}
	n, e := c.file.Write(append(b, '\n'))
	if e == nil && n != len(b)+1 {
		e = io.ErrShortWrite
	}
	if e != nil {
		return e
	}
	if e = c.file.Sync(); e != nil {
		return e
	}
	c.calls = probe.calls
	c.order = probe.order
	c.inventories = probe.inventories
	c.publication = probe.publication
	c.sequence++
	c.head = boundedDigestBytes(b)
	if c.fault != nil {
		return c.fault(event.Kind)
	}
	return nil
}

func boundedInventoryCall(call boundedSavedCall) boundedInventoryAttempt {
	binding := call.Binding
	binding.HandleID = ""
	return boundedInventoryAttempt{Metadata: call.Metadata, LogicalSHA256: boundedDigest(call.Request), WireSHA256: call.WireSHA256, Binding: binding}
}
func (c *boundedCheckpoint) freezeInventory(inv boundedPhaseInventory) error {
	if old, ok := c.inventories[inv.Key]; ok {
		if boundedDigest(old) != boundedDigest(inv) {
			return fmt.Errorf("bounded recovery STOP: recreated phase inventory changed")
		}
		return nil
	}
	if c.publication != nil {
		return fmt.Errorf("bounded recovery STOP: publication-ready generation lacks reconstructed phase inventory")
	}
	return c.append(boundedCheckpointEvent{Kind: "inventory", Inventory: &inv})
}
func (c *boundedCheckpoint) authorizeInventory(call boundedSavedCall) error {
	actual := boundedInventoryCall(call)
	matches := 0
	matchedBudget := -1
	var matchedKeys map[string]bool
	for _, inv := range c.inventories {
		for _, work := range inv.Works {
			for _, a := range work.Attempts {
				if boundedCallKey(a.Metadata) == boundedCallKey(call.Metadata) {
					if !reflect.DeepEqual(a, actual) {
						return fmt.Errorf("bounded: call changed from durable pre-spend inventory")
					}
					matches++
					matchedBudget = inv.MaximumCalls
					matchedKeys = map[string]bool{}
					for _, w := range inv.Works {
						for _, attempt := range w.Attempts {
							matchedKeys[boundedCallKey(attempt.Metadata)] = true
						}
					}
				}
			}
		}
	}
	if matches != 1 {
		return fmt.Errorf("bounded: call lacks unique durable pre-spend phase inventory")
	}
	used := 0
	for key := range c.calls {
		if matchedKeys[key] {
			used++
		}
	}
	if used >= matchedBudget {
		return fmt.Errorf("bounded: phase exceeds its frozen actual-call ceiling")
	}
	if call.Metadata.Stage == "mapping" {
		count, budget := 0, -1
		for _, inv := range c.inventories {
			if inv.Stage == "mapping-budget" && inv.Ceiling {
				if budget != -1 {
					return fmt.Errorf("bounded: ambiguous mapping ceiling")
				}
				budget = inv.MaximumCalls
			}
		}
		for _, old := range c.calls {
			if old.Metadata.Stage == "mapping" {
				count++
			}
		}
		if count >= budget {
			return fmt.Errorf("bounded: mapping exceeds frozen2*N unique-unit call ceiling")
		}
	}
	return nil
}

// A full scan/bijection catches an omitted local response as well as orphaned,
// unknown, duplicate and crash-tail admissions. It is called before any wire.
func (c *boundedCheckpoint) reconcile(ctx context.Context, bridge boundedPipelineBridge) error {
	inventory, e := bridge.WholeInventory(ctx)
	if e != nil {
		return fmt.Errorf("bounded ledger inventory: %w", e)
	}
	seen := map[string]bool{}
	for _, record := range inventory.Records {
		key := boundedCallKey(record.Metadata)
		call := c.calls[key]
		if seen[key] || !record.Complete || record.Rejection != "" {
			return fmt.Errorf("bounded recovery STOP: duplicate/unknown ledger outcome for %s", key)
		}
		seen[key] = true
		if call == nil || call.Result == nil {
			return fmt.Errorf("bounded recovery STOP: local checkpoint gap for admitted %s (including mapping INTENT gaps)", key)
		}
		if call.Result.ReceiptID != record.ReceiptID || call.Binding != record.Binding || !reflect.DeepEqual(call.Metadata, record.Metadata) || boundedDigest(call.Request) != record.LogicalRequestSHA256 {
			return fmt.Errorf("bounded recovery STOP: ledger/checkpoint binding mismatch")
		}
		if _, e = bridge.AuthenticateObserved(ctx, stagedBridgeObservedExpectation{Members: call.MappingUnits, Request: call.Request, Metadata: call.Metadata, Binding: call.Binding, ReceiptID: call.Result.ReceiptID, SavedResponse: call.Result.Response, ResponseSHA256: boundedDigest(call.Result.Response)}); e != nil {
			return fmt.Errorf("bounded recovery STOP: unauthenticated response: %w", e)
		}
	}
	for k, call := range c.calls {
		if !seen[k] || call.Result == nil {
			return fmt.Errorf("bounded recovery STOP: local intent/response has no complete actual ledger match: %s", k)
		}
		if !c.replayed[k] {
			return fmt.Errorf("bounded recovery STOP: actual paid work was not consumed by the recreated phase schedule: %s", k)
		}
	}
	return nil
}

func boundedCompleteResponse(result incremental.StagedMappingDispatchResult, binding incremental.StagedMappingWireBinding) error {
	r := result.Response
	if result.Binding != binding || result.ReceiptID == "" || r.FinishReason != "stop" || r.InputTokens <= 0 || r.InputTokens != binding.InputTokens || r.OutputTokens <= 0 || r.OutputTokens > binding.ReservedOutputTokens {
		return fmt.Errorf("bounded: incomplete/ambiguous bound response; explicit reconciliation required")
	}
	return nil
}
func (c *boundedCheckpoint) execute(ctx context.Context, bridge boundedPipelineBridge, call boundedSavedCall, prepared incremental.StagedMappingPreparedCall) (llm.CompleteResponse, error) {
	key := boundedCallKey(call.Metadata)
	if old := c.calls[key]; old != nil {
		// Handle IDs are ephemeral prepare capabilities; saved calls retain their
		// actual original binding. Recreated logical request/source roots must match.
		a, b := boundedCopyCall(old), boundedCopyCall(&call)
		a.Result = nil
		b.Result = nil
		a.Binding.HandleID = ""
		b.Binding.HandleID = ""
		if !reflect.DeepEqual(a, b) || old.Result == nil {
			return llm.CompleteResponse{}, fmt.Errorf("bounded checkpoint: recreated call changed or outcome unknown")
		}
		response, e := bridge.AuthenticateObserved(ctx, stagedBridgeObservedExpectation{Members: old.MappingUnits, Request: old.Request, Metadata: old.Metadata, Binding: old.Binding, ReceiptID: old.Result.ReceiptID, SavedResponse: old.Result.Response, ResponseSHA256: boundedDigest(old.Result.Response)})
		if e == nil {
			c.replayed[key] = true
			c.authenticated[key] = c.authDigest(old, bridge)
		}
		return response, e
	}
	if c.publication != nil {
		return llm.CompleteResponse{}, fmt.Errorf("bounded recovery STOP: publication-ready state cannot pay new work")
	}
	for k := range c.calls {
		if !c.replayed[k] {
			return llm.CompleteResponse{}, fmt.Errorf("bounded recovery STOP: new dispatch precedes replay of historical %s", k)
		}
	}
	if e := boundedReconcileInventoryOnly(ctx, c, bridge); e != nil {
		return llm.CompleteResponse{}, e
	}
	if prepared == nil || prepared.Binding() != call.Binding || boundedDigestBytes(prepared.Wire()) != call.WireSHA256 {
		return llm.CompleteResponse{}, fmt.Errorf("bounded: prepared capability changed")
	}
	if e := c.append(boundedCheckpointEvent{Kind: "intent", Call: &call}); e != nil {
		return llm.CompleteResponse{}, e
	}
	result, e := prepared.Dispatch(ctx, call.Binding)
	if e != nil {
		return llm.CompleteResponse{}, fmt.Errorf("bounded dispatch STOP (intent retained): %w", e)
	}
	call.Result = &result
	if e = c.append(boundedCheckpointEvent{Kind: "response", Call: &call}); e != nil {
		return llm.CompleteResponse{}, e
	}
	if e = boundedCompleteResponse(result, call.Binding); e != nil {
		return llm.CompleteResponse{}, e
	}
	if e = boundedReconcileInventoryOnly(ctx, c, bridge); e != nil {
		return llm.CompleteResponse{}, e
	}
	response, e := bridge.AuthenticateObserved(ctx, stagedBridgeObservedExpectation{Members: call.MappingUnits, Request: call.Request, Metadata: call.Metadata, Binding: call.Binding, ReceiptID: result.ReceiptID, SavedResponse: result.Response, ResponseSHA256: boundedDigest(result.Response)})
	if e == nil {
		c.replayed[key] = true
		c.authenticated[key] = c.authDigest(&call, bridge)
	}
	return response, e
}
func (c *boundedCheckpoint) workHead() string {
	if c.publication != nil {
		return c.publication.CheckpointHeadSHA256
	}
	return c.head
}

// Resolve only the independently pinned generation. A missing/uncertain pinned
// private directory cannot be regenerated, and a published target cannot adopt
// a self-rehashed manifest. All checks precede any attempted rename.
func (c *boundedCheckpoint) pinnedPublication(target string) (string, *boundedGenerationManifest, []byte, error) {
	bad := func(reason string) (string, *boundedGenerationManifest, []byte, error) {
		return "", nil, nil, fmt.Errorf("bounded recovery STOP: %s", reason)
	}
	p := c.publication
	if p == nil {
		return bad("publication has no independent controller pin")
	}
	if p.TargetPath != target || p.RunSHA256 != c.root {
		return bad("publication target/run pin changed")
	}
	exists := func(path string) (bool, error) {
		st, e := os.Lstat(path)
		if os.IsNotExist(e) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("unsafe generation path")
		}
		return true, nil
	}
	private, pe := exists(p.PrivatePath)
	published, te := exists(target)
	if pe != nil || te != nil || private == published {
		return bad("pinned private/target generation missing or ambiguous")
	}
	source := p.PrivatePath
	if published {
		source = target
	}
	if e := boundedSafeExistingDirectory(source); e != nil {
		return bad("unsafe pinned generation directory")
	}
	raw, e := os.ReadFile(filepath.Join(source, "manifest.json"))
	if e != nil || boundedDigestBytes(raw) != p.ManifestSHA256 {
		return bad("exact pinned manifest bytes changed or missing")
	}
	var manifest boundedGenerationManifest
	if e = jsonUnmarshalCanonical(raw, &manifest); e != nil || manifest.Format != boundedGenerationFormat || manifest.RunSHA256 != c.root || manifest.CheckpointHeadSHA256 != p.CheckpointHeadSHA256 {
		return bad("pinned manifest format/run/work head changed")
	}
	files, e := boundedGenerationFiles(source)
	if e != nil || !reflect.DeepEqual(files, manifest.Files) {
		return bad("pinned complete generation file inventory/bytes changed")
	}
	return source, &manifest, raw, nil
}

func boundedSafeExistingDirectory(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return fmt.Errorf("bounded: expected clean absolute private directory")
	}
	for p := dir; ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil || !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bounded: unsafe directory component %s", p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
func boundedSyncTree(root string) error {
	var dirs []string
	e := filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bounded: symlink in generation")
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("bounded: nonregular generation file")
		}
		f, e := os.OpenFile(path, os.O_RDONLY, 0)
		if e != nil {
			return e
		}
		return errors.Join(f.Sync(), f.Close())
	})
	if e != nil {
		return e
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, p := range dirs {
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		if e = errors.Join(f.Sync(), f.Close()); e != nil {
			return e
		}
	}
	return nil
}
func boundedPublishGeneration(private, target string) error {
	if filepath.Dir(private) != filepath.Dir(target) || filepath.Clean(target) != target || !filepath.IsAbs(target) || strings.ContainsAny(filepath.Base(target), "\\\x00") {
		return fmt.Errorf("bounded: generation/target must be clean absolute siblings")
	}
	if e := boundedSafeExistingDirectory(filepath.Dir(target)); e != nil {
		return e
	}
	if _, e := os.Lstat(target); !os.IsNotExist(e) {
		return fmt.Errorf("bounded: publication target exists or cannot be inspected")
	}
	if e := boundedSyncTree(private); e != nil {
		return e
	}
	// No os.Rename fallback: Linux NOREPLACE is part of the frozen method.
	if e := unix.Renameat2(unix.AT_FDCWD, private, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE); e != nil {
		return fmt.Errorf("bounded: atomic NOREPLACE publication unavailable/failed: %w", e)
	}
	f, e := os.Open(filepath.Dir(target))
	if e != nil {
		return e
	}
	return errors.Join(f.Sync(), f.Close())
}

func (c *boundedCheckpoint) authDigest(call *boundedSavedCall, bridge boundedPipelineBridge) string {
	return boundedDigest(struct {
		Run     string
		Call    *boundedSavedCall
		Profile incremental.StagedMappingAdmissionIdentity
		Source  incremental.StagedMappingIdentity
	}{c.root, call, bridge.Identity(), bridge.ConstructionIdentity()})
}
