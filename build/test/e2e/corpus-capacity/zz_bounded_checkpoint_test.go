//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// A file-backed, explicitly OFFLINE fixture. It is not a provider serializer,
// counter or delivery proof. Production bridge/trace interop is tested separately.
type boundedTestObserved struct {
	Record   stagedBridgeObservedRecord
	Response llm.CompleteResponse
}
type boundedTestBridge struct {
	construction                incremental.StagedMappingIdentity
	path                        string
	pins                        incremental.StagedMappingAdmissionIdentity
	resolve                     func(context.Context, string, string, string, int, string) ([]string, error)
	meta                        func(string, string, string, int, llm.CompleteRequest, []string) (stagedBridgeMetadata, error)
	prepareCount, dispatchCount int
	prepareError                func(llm.CompleteRequest) error
	reply                       func(llm.CompleteRequest, string, int) (llm.CompleteResponse, error)
	authCount                   int
	cachedRaw                   []byte
	cachedRows                  []boundedTestObserved
}
type boundedTestHandle struct {
	bridge  *boundedTestBridge
	request llm.CompleteRequest
	meta    stagedBridgeMetadata
	wire    []byte
	binding incremental.StagedMappingWireBinding
	used    bool
}

func boundedTestPins() incremental.StagedMappingAdmissionIdentity {
	return incremental.StagedMappingAdmissionIdentity{Endpoint: "https://api.deepseek.com/chat/completions", Model: "deepseek-flash", ProfileSHA256: boundedDigest("test-profile"), SerializerSHA256: boundedDigest("test-serializer"), CounterID: "deepseek-recipe@935817c66f5d6e64838f419692545f06787e485f/v41", RuntimeSHA256: boundedDigest("test-runtime")}
}
func (b *boundedTestBridge) Identity() incremental.StagedMappingAdmissionIdentity { return b.pins }
func (b *boundedTestBridge) ConstructionIdentity() incremental.StagedMappingIdentity {
	return b.construction
}
func (b *boundedTestBridge) Prepare(ctx context.Context, call incremental.StagedMappingCall) (incremental.StagedMappingPreparedCall, error) {
	m, e := b.meta("mapping", call.BatchID, call.CatalogueSHA256, call.Attempt, call.Request, call.Units)
	if e != nil {
		return nil, e
	}
	return b.PrepareRequest(ctx, call.Request, m.Stage, m.WorkID, m.ParentCatalogueRoot, m.Attempt, m.UnitSourceDocIDs, m.SourceBindingsSHA256)
}
func (b *boundedTestBridge) PrepareRequest(ctx context.Context, request llm.CompleteRequest, stage, work, parent string, attempt int, docs []string, sources string) (incremental.StagedMappingPreparedCall, error) {
	b.prepareCount++
	if b.prepareError != nil {
		if e := b.prepareError(request); e != nil {
			return nil, e
		}
	}
	if b.resolve != nil && stage != "mapping" {
		if _, e := b.resolve(ctx, stage, work, parent, attempt, boundedDigest(request)); e != nil {
			return nil, e
		}
	}
	client, e := llm.NewOpenAICompatClient(llm.Config{Model: b.pins.Model, Endpoint: b.pins.Endpoint, MaxTokens: 65536})
	if e != nil {
		return nil, e
	}
	_, wire, e := client.RenderRequest(request)
	if e != nil {
		return nil, e
	}
	return &boundedTestHandle{bridge: b, request: request, meta: stagedBridgeMetadata{Stage: stage, WorkID: work, ParentCatalogueRoot: parent, Attempt: attempt, UnitSourceDocIDs: append([]string(nil), docs...), SourceBindingsSHA256: sources}, wire: wire, binding: incremental.StagedMappingWireBinding{HandleID: fmt.Sprintf("fake-handle-%d", b.prepareCount), RequestSHA256: boundedDigestBytes(wire), Identity: b.pins, InputTokens: 100, ReservedOutputTokens: 65536, ContextLimit: 1000000}}, nil
}
func (h *boundedTestHandle) Wire() []byte                                  { return append([]byte(nil), h.wire...) }
func (h *boundedTestHandle) Binding() incremental.StagedMappingWireBinding { return h.binding }
func (b *boundedTestBridge) read() ([]boundedTestObserved, error) {
	raw, e := os.ReadFile(b.path)
	if os.IsNotExist(e) {
		return []boundedTestObserved{}, nil
	}
	if e != nil {
		return nil, e
	}
	if len(raw) == 0 {
		return []boundedTestObserved{}, nil
	}
	if raw[len(raw)-1] != '\n' {
		return nil, errors.New("fixture actual file has incomplete tail")
	}
	// The fixture still reads and compares EVERY actual byte on each scan.
	// Avoid repeatedly decoding an unchanged file during the 609-doc race test;
	// this optimization is not part of production authentication.
	if bytes.Equal(raw, b.cachedRaw) {
		return boundedTestCopyObserved(b.cachedRows), nil
	}
	var result []boundedTestObserved
	for _, line := range bytes.Split(raw[:len(raw)-1], []byte{'\n'}) {
		var item boundedTestObserved
		if e = jsonUnmarshalCanonical(line, &item); e != nil {
			return nil, e
		}
		result = append(result, item)
	}
	b.cachedRaw = raw
	b.cachedRows = boundedTestCopyObserved(result)
	return result, nil
}
func boundedTestCopyObserved(rows []boundedTestObserved) []boundedTestObserved {
	copy := append([]boundedTestObserved(nil), rows...)
	for i := range copy {
		copy[i].Record.Metadata.UnitSourceDocIDs = append([]string(nil), copy[i].Record.Metadata.UnitSourceDocIDs...)
	}
	return copy
}
func (h *boundedTestHandle) Dispatch(ctx context.Context, expected incremental.StagedMappingWireBinding) (incremental.StagedMappingDispatchResult, error) {
	b := h.bridge
	if h.used || h.binding != expected {
		return incremental.StagedMappingDispatchResult{}, errors.New("fixture duplicate/capability mismatch")
	}
	h.used = true
	old, e := b.read()
	if e != nil {
		return incremental.StagedMappingDispatchResult{}, e
	}
	for _, row := range old {
		if boundedCallKey(row.Record.Metadata) == boundedCallKey(h.meta) {
			return incremental.StagedMappingDispatchResult{}, errors.New("fixture durable attempt already dispatched")
		}
	}
	b.dispatchCount++
	response := llm.CompleteResponse{Text: `{"ok":true}`, InputTokens: 100, OutputTokens: 10, FinishReason: "stop"}
	if b.reply != nil {
		response, e = b.reply(h.request, h.meta.Stage, h.meta.Attempt)
		if e != nil {
			return incremental.StagedMappingDispatchResult{}, e
		}
	}
	receipt := "observed:" + boundedDigest(struct {
		Metadata stagedBridgeMetadata
		Response llm.CompleteResponse
	}{h.meta, response})
	record := stagedBridgeObservedRecord{SegmentID: "offline-fixture", CallID: int64(len(old) + 1), Metadata: h.meta, Binding: h.binding, LogicalRequestSHA256: boundedDigest(h.request), ReceiptID: receipt, ResponseContentSHA256: boundedDigestBytes([]byte(response.Text)), FinishReason: response.FinishReason, ReturnedModel: b.pins.Model, InputTokens: response.InputTokens, OutputTokens: response.OutputTokens, Complete: response.FinishReason == "stop" && response.InputTokens == 100 && response.OutputTokens > 0 && response.OutputTokens <= 65536}
	file, e := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return incremental.StagedMappingDispatchResult{}, e
	}
	line, _ := json.Marshal(boundedTestObserved{record, response})
	_, e = file.Write(append(line, '\n'))
	e = errors.Join(e, file.Sync(), file.Close())
	if e != nil {
		return incremental.StagedMappingDispatchResult{}, e
	}
	return incremental.StagedMappingDispatchResult{Binding: expected, ReceiptID: receipt, Response: response}, nil
}
func (b *boundedTestBridge) WholeInventory(context.Context) (stagedBridgeInventory, error) {
	rows, e := b.read()
	if e != nil {
		return stagedBridgeInventory{}, e
	}
	inv := stagedBridgeInventory{ActiveSegmentID: "offline-fixture", Records: []stagedBridgeObservedRecord{}}
	seen := map[string]bool{}
	for _, row := range rows {
		key := boundedCallKey(row.Record.Metadata)
		if seen[key] || !row.Record.Complete {
			return inv, errors.New("fixture duplicate/unknown ledger outcome")
		}
		seen[key] = true
		inv.Records = append(inv.Records, row.Record)
	}
	return inv, nil
}
func (b *boundedTestBridge) AuthenticateObserved(ctx context.Context, x stagedBridgeObservedExpectation) (llm.CompleteResponse, error) {
	b.authCount++
	if x.ResponseSHA256 != boundedDigest(x.SavedResponse) {
		return llm.CompleteResponse{}, errors.New("fixture response digest changed")
	}
	if b.resolve != nil && x.Metadata.Stage != "mapping" {
		if _, e := b.resolve(ctx, x.Metadata.Stage, x.Metadata.WorkID, x.Metadata.ParentCatalogueRoot, x.Metadata.Attempt, boundedDigest(x.Request)); e != nil {
			return llm.CompleteResponse{}, e
		}
	}
	rows, e := b.read()
	if e != nil {
		return llm.CompleteResponse{}, e
	}
	for _, row := range rows {
		if row.Record.ReceiptID == x.ReceiptID && row.Record.Complete && row.Record.Binding == x.Binding && reflect.DeepEqual(row.Record.Metadata, x.Metadata) && row.Record.LogicalRequestSHA256 == boundedDigest(x.Request) && reflect.DeepEqual(row.Response, x.SavedResponse) {
			return x.SavedResponse, nil
		}
	}
	return llm.CompleteResponse{}, errors.New("fixture actual ledger authentication failed")
}
func (b *boundedTestBridge) ProviderName() string { return "offline-no-call-client" }
func (b *boundedTestBridge) Complete(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return nil, errors.New("forbidden materializer model call")
}

func boundedTestPrepared(t *testing.T, b *boundedTestBridge) (boundedSavedCall, incremental.StagedMappingPreparedCall) {
	t.Helper()
	req := llm.CompleteRequest{System: "offline system", User: "complete material", MaxTokens: 65536}
	meta := stagedBridgeMetadata{Stage: "description", WorkID: boundedDigest("work"), ParentCatalogueRoot: boundedDigest("parent"), Attempt: 1, UnitSourceDocIDs: []string{"doc"}, SourceBindingsSHA256: boundedDigest("sources")}
	p, e := b.PrepareRequest(context.Background(), req, meta.Stage, meta.WorkID, meta.ParentCatalogueRoot, 1, meta.UnitSourceDocIDs, meta.SourceBindingsSHA256)
	if e != nil {
		t.Fatal(e)
	}
	return boundedSavePrepared(req, meta, p), p
}

func TestBoundedCheckpointAuthenticatesReplayAndWholeInventory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := boundedDigest("frozen run")
	b := &boundedTestBridge{path: filepath.Join(dir, "actual.jsonl"), pins: boundedTestPins()}
	c, e := openBoundedCheckpoint(dir, root, false, nil)
	if e != nil {
		t.Fatal(e)
	}
	call, p := boundedTestPrepared(t, b)
	response, e := boundedTestExecute(t, c, ctx, b, call, p)
	if e != nil {
		t.Fatal(e)
	}
	if b.dispatchCount != 1 {
		t.Fatal("missing dispatch")
	}
	c.file.Close()
	// A genuinely new controller and bridge instance read actual files. The
	// ephemeral prepare sequence is deliberately different across instances.
	b2 := &boundedTestBridge{path: b.path, pins: b.pins, prepareCount: 50}
	c2, e := openBoundedCheckpoint(dir, root, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c2.file.Close()
	call2, p2 := boundedTestPrepared(t, b2)
	replayed, e := boundedTestExecute(t, c2, ctx, b2, call2, p2)
	if e != nil || !reflect.DeepEqual(response, replayed) || b2.dispatchCount != 0 {
		t.Fatalf("replayed paid call: %v", e)
	}
	if e = c2.reconcile(ctx, b2); e != nil {
		t.Fatal(e)
	}
	c2.replayed[boundedCallKey(call2.Metadata)] = false
	if e = c2.reconcile(ctx, b2); e == nil {
		t.Fatal("actual paid work omitted by recreated semantic schedule was accepted")
	}
	c2.replayed[boundedCallKey(call2.Metadata)] = true
	// A ledger-complete call omitted by the checkpoint is still detected.
	other := call2
	other.Metadata.WorkID = boundedDigest("omitted")
	ph, e := b2.PrepareRequest(ctx, other.Request, other.Metadata.Stage, other.Metadata.WorkID, other.Metadata.ParentCatalogueRoot, 1, other.Metadata.UnitSourceDocIDs, other.Metadata.SourceBindingsSHA256)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ph.Dispatch(ctx, ph.Binding()); e != nil {
		t.Fatal(e)
	}
	if e = c2.reconcile(ctx, b2); e == nil || !strings.Contains(e.Error(), "checkpoint gap") {
		t.Fatalf("omitted actual admission escaped whole scan: %v", e)
	}
}
func TestBoundedCheckpointCrashBoundariesNeverRepay(t *testing.T) {
	for _, point := range []string{"intent", "response"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			b := &boundedTestBridge{path: filepath.Join(dir, "actual.jsonl"), pins: boundedTestPins()}
			root := boundedDigest("run")
			fired := false
			c, e := openBoundedCheckpoint(dir, root, false, func(p string) error {
				if p == point && !fired {
					fired = true
					return errors.New("power loss")
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			call, p := boundedTestPrepared(t, b)
			if _, e = boundedTestExecute(t, c, context.Background(), b, call, p); e == nil {
				t.Fatal("missing fault")
			}
			c.file.Close()
			before := b.dispatchCount
			fresh := &boundedTestBridge{path: b.path, pins: b.pins}
			recovered, e := openBoundedCheckpoint(dir, root, true, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer recovered.file.Close()
			if point == "intent" {
				if e = boundedReconcileInventoryOnly(context.Background(), recovered, fresh); e == nil {
					t.Fatal("unknown intent was repayable")
				}
			} else {
				call, p = boundedTestPrepared(t, fresh)
				if _, e = boundedTestExecute(t, recovered, context.Background(), fresh, call, p); e != nil {
					t.Fatal(e)
				}
			}
			if fresh.dispatchCount != 0 || b.dispatchCount != before {
				t.Fatal("recovery repaid")
			}
		})
	}
}
func TestBoundedCheckpointTamperTailAndChangedResponseStop(t *testing.T) {
	dir := t.TempDir()
	b := &boundedTestBridge{path: filepath.Join(dir, "actual.jsonl"), pins: boundedTestPins()}
	c, e := openBoundedCheckpoint(dir, boundedDigest("run"), false, nil)
	if e != nil {
		t.Fatal(e)
	}
	call, p := boundedTestPrepared(t, b)
	if _, e = boundedTestExecute(t, c, context.Background(), b, call, p); e != nil {
		t.Fatal(e)
	}
	key := boundedCallKey(call.Metadata)
	c.calls[key].Result.Response.Text = "forged"
	if e = c.reconcile(context.Background(), b); e == nil {
		t.Fatal("response hash alone became delivery authority")
	}
	c.file.Close()
	f, e := os.OpenFile(filepath.Join(dir, "controller.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString(`{"partial":`)
	f.Close()
	if _, e = openBoundedCheckpoint(dir, boundedDigest("run"), true, nil); e == nil {
		t.Fatal("partial checkpoint tail accepted")
	}
	f, e = os.OpenFile(b.path, os.O_APPEND|os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString(`{"partial":`)
	f.Close()
	if _, e = b.WholeInventory(context.Background()); e == nil {
		t.Fatal("partial actual ledger tail accepted")
	}
}
func TestBoundedPublicationNoReplaceAndProcessCrash(t *testing.T) {
	if os.Getenv("CAIRN_BOUNDED_TEST_CRASH") == "1" {
		root := os.Getenv("CAIRN_BOUNDED_TEST_DIR")
		b := &boundedTestBridge{path: filepath.Join(root, "actual.jsonl"), pins: boundedTestPins()}
		c, e := openBoundedCheckpoint(root, boundedDigest("subprocess"), false, nil)
		if e != nil {
			os.Exit(4)
		}
		call, p := boundedTestPrepared(t, b)
		boundedTestFreezeCall(t, c, call)
		if e = c.append(boundedCheckpointEvent{Kind: "intent", Call: &call}); e != nil {
			os.Exit(5)
		}
		if _, e = p.Dispatch(context.Background(), p.Binding()); e != nil {
			os.Exit(6)
		}
		os.Exit(0) // actual response exists; local RESPONSE was never saved
	}
	dir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestBoundedPublicationNoReplaceAndProcessCrash$")
	command.Env = append(os.Environ(), "CAIRN_BOUNDED_TEST_CRASH=1", "CAIRN_BOUNDED_TEST_DIR="+dir)
	if out, e := command.CombinedOutput(); e != nil {
		t.Fatalf("subprocess %v: %s", e, out)
	}
	fresh := &boundedTestBridge{path: filepath.Join(dir, "actual.jsonl"), pins: boundedTestPins()}
	c, e := openBoundedCheckpoint(dir, boundedDigest("subprocess"), true, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.file.Close()
	if e = boundedReconcileInventoryOnly(context.Background(), c, fresh); e == nil {
		t.Fatal("actual delivery missing local response was automatically reconciled")
	}
	if fresh.dispatchCount != 0 {
		t.Fatal("paid after crash")
	}
	private := filepath.Join(dir, "private")
	target := filepath.Join(dir, "published")
	os.Mkdir(private, 0700)
	os.WriteFile(filepath.Join(private, "data"), []byte("complete"), 0600)
	os.Mkdir(target, 0700)
	os.WriteFile(filepath.Join(target, "human"), []byte("preserve"), 0600)
	if e = boundedPublishGeneration(private, target); e == nil {
		t.Fatal("overwrote existing generation")
	}
	if data, _ := os.ReadFile(filepath.Join(target, "human")); string(data) != "preserve" {
		t.Fatal("existing output changed")
	}
	target = filepath.Join(dir, "absent")
	if e = boundedPublishGeneration(private, target); e != nil {
		t.Fatal(e)
	}
	if data, _ := os.ReadFile(filepath.Join(target, "data")); string(data) != "complete" {
		t.Fatal("partial publication")
	}
}

// This test crosses an actual OS-process boundary and uses #91's real existing
// production client/renderer/trace observer with an offline HTTP transport and
// fake official-counter fixture. It does not substitute a second ledger format.
func TestBoundedActualBridgeTraceProcessRecoveryAndOrphanStop(t *testing.T) {
	makeCall := func(t *testing.T, f *bridgeFixture, call incremental.StagedMappingCall) (boundedSavedCall, incremental.StagedMappingPreparedCall) {
		t.Helper()
		if f.b == nil {
			var e error
			f.b, e = newStagedWireBridge(f.config)
			if e != nil {
				t.Fatal(e)
			}
		}
		ids, rows, e := f.b.directDocuments(call.Units)
		if e != nil {
			t.Fatal(e)
		}
		p, e := f.b.Prepare(context.Background(), call)
		if e != nil {
			t.Fatal(e)
		}
		m := stagedBridgeMetadata{Stage: "mapping", WorkID: call.BatchID, ParentCatalogueRoot: call.CatalogueSHA256, Attempt: call.Attempt, UnitSourceDocIDs: ids, SourceBindingsSHA256: stagedBridgeSourcesSHA(rows)}
		saved := boundedSavePrepared(call.Request, m, p)
		saved.MappingUnits = append([]string(nil), call.Units...)
		return saved, p
	}
	root := boundedDigest("real-trace-process")
	if os.Getenv("CAIRN_BOUNDED_ACTUAL_TRACE") == "1" {
		dir := os.Getenv("CAIRN_BOUNDED_ACTUAL_DIR")
		f := bridgeFixtureConfig(t, "pipeline-segment-1", nil)
		c, e := openBoundedCheckpoint(dir, root, false, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer c.file.Close()
		call, p := makeCall(t, f, f.call)
		if _, e = boundedTestExecute(t, c, context.Background(), f.b, call, p); e != nil {
			t.Fatal(e)
		}
		raw, e := os.ReadFile(f.file.Name())
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "history.jsonl"), raw, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	dir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestBoundedActualBridgeTraceProcessRecoveryAndOrphanStop$")
	command.Env = append(os.Environ(), "CAIRN_BOUNDED_ACTUAL_TRACE=1", "CAIRN_BOUNDED_ACTUAL_DIR="+dir)
	if out, e := command.CombinedOutput(); e != nil {
		t.Fatalf("actual trace subprocess: %v %s", e, out)
	}
	file, e := os.Open(filepath.Join(dir, "history.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	raw, e := os.ReadFile(file.Name())
	if e != nil {
		t.Fatal(e)
	}
	history := []stagedBridgeLedgerSegment{{ID: bridgePin("pipeline-segment-1"), File: file, Bytes: int64(len(raw)), SHA256: boundedDigestBytes(raw)}}
	fresh := bridgeFixtureConfig(t, "pipeline-segment-2", history)
	c, e := openBoundedCheckpoint(dir, root, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	call, p := makeCall(t, fresh, fresh.call)
	if _, e = boundedTestExecute(t, c, context.Background(), fresh.b, call, p); e != nil {
		t.Fatal(e)
	}
	if fresh.sends.Load() != 0 {
		t.Fatal("actual production bridge repaid completed call across process")
	}
	if e = c.reconcile(context.Background(), fresh.b); e != nil {
		t.Fatal(e)
	}
	other := fresh.call
	other.BatchID = boundedDigest("orphan actual work")
	orphan, handle := makeCall(t, fresh, other)
	boundedTestFreezeCall(t, c, orphan)
	if e = c.append(boundedCheckpointEvent{Kind: "intent", Call: &orphan}); e != nil {
		t.Fatal(e)
	}
	if _, e = handle.Dispatch(context.Background(), handle.Binding()); e != nil {
		t.Fatal(e)
	}
	c.file.Close()
	activeBytes, e := os.ReadFile(fresh.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	history = append(history, stagedBridgeLedgerSegment{ID: bridgePin("pipeline-segment-2"), File: fresh.file, Bytes: int64(len(activeBytes)), SHA256: boundedDigestBytes(activeBytes)})
	third := bridgeFixtureConfig(t, "pipeline-segment-3", history)
	third.b, e = newStagedWireBridge(third.config)
	if e != nil {
		t.Fatal(e)
	}
	recovered, e := openBoundedCheckpoint(dir, root, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer recovered.file.Close()
	if e = boundedReconcileInventoryOnly(context.Background(), recovered, third.b); e == nil {
		t.Fatal("complete actual crash-tail delivery bypassed local checkpoint gap")
	}
	if third.sends.Load() != 0 {
		t.Fatal("orphan was automatically repaid")
	}
}

func boundedTestFreezeCall(t *testing.T, c *boundedCheckpoint, call boundedSavedCall) {
	t.Helper()
	if call.Metadata.Stage == "mapping" {
		found := false
		for _, inv := range c.inventories {
			if inv.Stage == "mapping-budget" {
				found = true
			}
		}
		if !found {
			if e := c.freezeInventory(boundedPhaseInventory{Key: "test-mapping-budget", Stage: "mapping-budget", ParentRoot: "test-parent", MappingUniqueUnits: 1, AcceptedIncidences: len(call.Metadata.UnitSourceDocIDs), MaximumCalls: 2, Ceiling: true}); e != nil {
				t.Fatal(e)
			}
		}
	}
	inv := boundedPhaseInventory{Key: "test/" + boundedCallKey(call.Metadata), Stage: call.Metadata.Stage, ParentRoot: call.Metadata.ParentCatalogueRoot, MaximumCalls: 1, Works: []boundedInventoryWork{{ID: call.Metadata.WorkID, Members: call.MappingUnits, Attempts: []boundedInventoryAttempt{boundedInventoryCall(call)}}}}
	if e := c.freezeInventory(inv); e != nil {
		t.Fatal(e)
	}
}
func boundedTestExecute(t *testing.T, c *boundedCheckpoint, ctx context.Context, b boundedPipelineBridge, call boundedSavedCall, p incremental.StagedMappingPreparedCall) (llm.CompleteResponse, error) {
	t.Helper()
	boundedTestFreezeCall(t, c, call)
	return c.execute(ctx, b, call, p)
}

func TestBoundedProductionLedgerRecoveryCost(t *testing.T) {
	if os.Getenv("CAIRN_BOUNDED_MEASURE_RECOVERY") != "1" {
		t.Skip("explicit finite zero-network production-ledger measurement")
	}
	const calls = 24
	f := newBridgeFixture(t)
	var expected []stagedBridgeObservedExpectation
	start := time.Now()
	for i := 0; i < calls; i++ {
		call := f.call
		call.BatchID = boundedDigest(fmt.Sprintf("measured-work-%d", i))
		call.Request.User = fmt.Sprintf("complete offline measurement source %d", i)
		p := f.prepare(t, call)
		result, e := p.Dispatch(context.Background(), p.Binding())
		if e != nil {
			t.Fatal(e)
		}
		expected = append(expected, stagedBridgeObservedExpectation{Request: call.Request, Metadata: p.snapshot.Metadata, Binding: result.Binding, ReceiptID: result.ReceiptID, SavedResponse: result.Response, ResponseSHA256: boundedDigest(result.Response), Members: call.Units})
	}
	creation := time.Since(start)
	raw, e := os.ReadFile(f.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	offset, e := f.file.Seek(0, 1)
	if e != nil {
		t.Fatal(e)
	}
	counterBefore := f.counter.calls.Load()
	start = time.Now()
	for i := 0; i < 3; i++ {
		inv, e := f.b.WholeInventory(context.Background())
		if e != nil || len(inv.Records) != calls {
			t.Fatalf("full inventory: %v", e)
		}
	}
	inventory := time.Since(start)
	start = time.Now()
	for _, x := range expected {
		if _, e = f.b.AuthenticateObserved(context.Background(), x); e != nil {
			t.Fatal(e)
		}
	}
	authentication := time.Since(start)
	after, e := f.file.Seek(0, 1)
	if e != nil || after != offset || f.sends.Load() != calls || f.counter.calls.Load() != counterBefore {
		t.Fatal("read-only measurement changed actual calls/counter/append offset")
	}
	report := struct {
		Format                                                                     string
		CompleteCalls, LedgerBytes, WholeInventoryScans, AuthenticateObservedScans int
		CreateSeconds, WholeInventorySeconds, AuthenticateAllSeconds               float64
		ZeroNetwork                                                                bool
		LedgerSHA256                                                               string
	}{"cairn-production-ledger-cost/offline-v1", calls, len(raw), 3, calls, creation.Seconds(), inventory.Seconds(), authentication.Seconds(), true, boundedDigestBytes(raw)}
	b, _ := json.Marshal(report)
	t.Logf("RECOVERY_COST %s", b)
}

func TestBoundedPreSpendInventoryAndAuthenticatedHistoryMutation(t *testing.T) {
	ctx := context.Background()
	_, f := boundedSmallGraph(1)
	doc := f.Documents[0]
	root := f.DocRoots[boundedDigest("doc")]
	f.Input = incremental.StagedMappingInput{Identity: incremental.StagedMappingIdentity{CodeSourceCommit: strings.Repeat("a", 40), CorpusSHA256: boundedDigest("corpus"), MethodSHA256: boundedDigest("method"), ExpectedDocuments: 1, Admission: boundedTestPins()}, Roster: []incremental.StagedMappingDocumentRoot{root}, Documents: []incremental.StagedMappingDocument{{Root: root, Annotation: *doc}}}
	r, a, i, e := incremental.StagedMappingInputDigests(f.Input)
	if e != nil {
		t.Fatal(e)
	}
	f.Input.Identity.RosterSHA256 = r
	f.Input.Identity.AcceptedSetSHA256 = a
	f.Input.Identity.InputSHA256 = i
	resolver := &boundedResolver{works: map[string]boundedResolvedWork{}, frozen: f}
	dir := t.TempDir()
	c, e := openBoundedCheckpoint(dir, boundedDigest("test-run"), false, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.file.Close()
	b := &boundedTestBridge{path: filepath.Join(dir, "actual.jsonl"), pins: boundedTestPins(), construction: f.Input.Identity, resolve: resolver.resolve, meta: resolver.metadata, reply: boundedSchemaReply}
	b.resolve = nil
	call, p := boundedTestPrepared(t, b)
	b.resolve = resolver.resolve
	// Arbitrary prepared requests cannot start a phase without its full durable inventory.
	if _, e = c.execute(ctx, b, call, p); e == nil || b.dispatchCount != 0 {
		t.Fatal("call spent without frozen phase inventory")
	}
	budget := boundedPhaseInventory{Key: "mapping-budget/test", Stage: "mapping-budget", ParentRoot: f.Input.Identity.InputSHA256, MappingUniqueUnits: 1, AcceptedIncidences: 1, MaximumCalls: 2, Ceiling: true}
	if e = c.freezeInventory(budget); e != nil {
		t.Fatal(e)
	}
	stage := filepath.Join(dir, "mapping")
	journal := filepath.Join(stage, "journal.jsonl")
	wrapper := &boundedMappingBridge{b, c, resolver, journal}
	if _, e = incremental.RunStagedMapping(ctx, f.Input, incremental.StagedMappingOptions{StageDir: stage, Admitter: wrapper}); e != nil {
		t.Fatal(e)
	}
	if e = boundedAuthenticateMappingJournal(ctx, journal, c, b, resolver); e != nil {
		t.Fatal(e)
	}
	controllerBytes, e := os.ReadFile(c.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"Members", "MaximumCalls"} {
		previous := ""
		var rewritten []byte
		for _, line := range bytes.Split(controllerBytes[:len(controllerBytes)-1], []byte{'\n'}) {
			var row boundedCheckpointLine
			if e = json.Unmarshal(line, &row); e != nil {
				t.Fatal(e)
			}
			if row.Event.Kind == "inventory" && row.Event.Inventory.Stage == "mapping" {
				if field == "Members" {
					row.Event.Inventory.Works[0].Members = []string{"invented"}
				} else {
					row.Event.Inventory.MaximumCalls = 2
				}
			}
			row.Previous = previous
			line, _ = json.Marshal(row)
			previous = boundedDigestBytes(line)
			rewritten = append(rewritten, append(line, '\n')...)
		}
		coldDir := t.TempDir()
		if e = os.WriteFile(filepath.Join(coldDir, "controller.jsonl"), rewritten, 0600); e != nil {
			t.Fatal(e)
		}
		cold, e := openBoundedCheckpoint(coldDir, c.root, true, nil)
		if e != nil {
			t.Fatal(e)
		}
		e = boundedAuthenticateMappingJournal(ctx, journal, cold, b, resolver)
		cold.file.Close()
		if e == nil || !strings.Contains(e.Error(), "mapping-next inventory changed") || b.dispatchCount != 1 {
			t.Fatalf("self-rehashed historical mapping %s escaped exact reconstruction: %v", field, e)
		}
	}
	before := b.authCount
	key := c.order[0]
	saved := c.calls[key]
	saved.Result.Response.Text += " "
	raw, e := os.ReadFile(journal)
	if e != nil {
		t.Fatal(e)
	}
	var updated []byte
	for _, line := range bytes.Split(raw[:len(raw)-1], []byte{'\n'}) {
		var record map[string]json.RawMessage
		if e = json.Unmarshal(line, &record); e != nil {
			t.Fatal(e)
		}
		var event map[string]json.RawMessage
		json.Unmarshal(record["Event"], &event)
		var kind string
		json.Unmarshal(event["Kind"], &kind)
		if kind == "response" {
			event["Response"], _ = json.Marshal(saved.Result)
			record["Event"], _ = json.Marshal(event)
		}
		line, _ = json.Marshal(record)
		updated = append(updated, append(line, '\n')...)
	}
	if e = os.WriteFile(journal, updated, 0600); e != nil {
		t.Fatal(e)
	}
	// The two local histories now agree, while the actual ledger does not.
	// A cache keyed only by work/receipt would miss this. Full call fingerprint
	// changes force actual reauthentication, which must fail before any wire.
	if e = boundedAuthenticateMappingJournal(ctx, journal, c, b, resolver); e == nil || b.authCount <= before || b.dispatchCount != 1 {
		t.Fatalf("cached modified history escaped reauthentication: %v", e)
	}
	budget.MaximumCalls = 3
	if e = c.freezeInventory(budget); e == nil {
		t.Fatal("resume silently changed frozen phase ceiling")
	}
}

// Small file-layer fault probes complement the actual complete609 replay test.
// They make no graph/LLM validity claim; the production path also reruns its
// existing full semantic DB/source/sidecar audit against authenticated replay.
func boundedReadyFixture(t *testing.T, fault func(string) error) (*boundedCheckpoint, string, string, string) {
	t.Helper()
	root := t.TempDir()
	control := filepath.Join(root, "control")
	private := filepath.Join(root, ".bounded-generation-fixture")
	target := filepath.Join(root, "published")
	for _, p := range []string{control, private, filepath.Join(private, "repository")} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for p, b := range map[string]string{"graph.json": `{"original":"graph"}`, "repository/doc.md": "original markdown", "repository/doc.md.kg.yaml": "original sidecar"} {
		if e := os.WriteFile(filepath.Join(private, p), []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
	}
	c, e := openBoundedCheckpoint(control, boundedDigest("publication-fixture"), false, fault)
	if e != nil {
		t.Fatal(e)
	}
	files, e := boundedGenerationFiles(private)
	if e != nil {
		t.Fatal(e)
	}
	m := boundedGenerationManifest{boundedGenerationFormat, c.root, boundedDigest("graph"), c.workHead(), files}
	raw, _ := boundedEncode(m)
	if e = os.WriteFile(filepath.Join(private, "manifest.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = boundedSyncTree(private); e != nil {
		t.Fatal(e)
	}
	ready := boundedPublicationReady{c.root, boundedDigestBytes(raw), c.workHead(), private, target}
	e = c.append(boundedCheckpointEvent{Kind: "publication-ready", Publication: &ready})
	if fault == nil && e != nil {
		t.Fatal(e)
	}
	if fault != nil && e == nil {
		t.Fatal("publication-ready fault missing")
	}
	return c, control, private, target
}
func TestBoundedPublicationReadyCrashBeforeAndAfterRename(t *testing.T) {
	c, control, private, target := boundedReadyFixture(t, func(point string) error {
		if point == "publication-ready" {
			return errors.New("power loss after durable pin")
		}
		return nil
	})
	run := c.root
	work := c.workHead()
	readyHash := c.publication.ManifestSHA256
	c.file.Close()
	cold, e := openBoundedCheckpoint(control, run, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	source, m, _, e := cold.pinnedPublication(target)
	if e != nil || source != private || m.CheckpointHeadSHA256 != work || cold.head == work {
		t.Fatalf("pre-rename recovery lost work-head separation: %v", e)
	}
	if e = boundedPublishGeneration(source, target); e != nil {
		t.Fatal(e)
	}
	cold.file.Close() // process may die after rename without another event
	after, e := openBoundedCheckpoint(control, run, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer after.file.Close()
	source, _, raw, e := after.pinnedPublication(target)
	if e != nil || source != target || boundedDigestBytes(raw) != readyHash {
		t.Fatalf("post-rename recovery changed pinned bytes: %v", e)
	}
}
func TestBoundedPublicationReadyRejectsSelfRehashedTamperAndMissingPrivate(t *testing.T) {
	for _, kind := range []string{"graph", "markdown-sidecar", "extra-rehashed", "extra-unlisted", "format", "missing-private"} {
		t.Run(kind, func(t *testing.T) {
			c, _, private, target := boundedReadyFixture(t, nil)
			defer c.file.Close()
			raw, e := os.ReadFile(filepath.Join(private, "manifest.json"))
			if e != nil {
				t.Fatal(e)
			}
			var m boundedGenerationManifest
			if e = jsonUnmarshalCanonical(raw, &m); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "graph":
				e = os.WriteFile(filepath.Join(private, "graph.json"), []byte(`{"Graph":null,"Hidden":null}`), 0600)
			case "markdown-sidecar":
				e = os.WriteFile(filepath.Join(private, "repository/doc.md"), []byte("modified markdown"), 0600)
				if e == nil {
					e = os.WriteFile(filepath.Join(private, "repository/doc.md.kg.yaml"), []byte("matching changed doc hash"), 0600)
				}
			case "extra-rehashed", "extra-unlisted":
				e = os.WriteFile(filepath.Join(private, "unexpected.txt"), []byte("extra"), 0600)
			case "format":
				m.Format = "other-format"
			case "missing-private":
				e = os.Rename(private, private+"-unowned")
			}
			if e != nil {
				t.Fatal(e)
			}
			if kind != "extra-unlisted" && kind != "missing-private" {
				m.Files, e = boundedGenerationFiles(private)
				if e != nil {
					t.Fatal(e)
				}
				if e = boundedWriteJSON(filepath.Join(private, "manifest.json"), m); e != nil {
					t.Fatal(e)
				}
			}
			if _, _, _, e = c.pinnedPublication(target); e == nil {
				t.Fatalf("%s was accepted by self-rehashed manifest", kind)
			}
			if _, e = os.Lstat(target); !os.IsNotExist(e) {
				t.Fatal("tampered/unknown generation published")
			}
		})
	}
}
