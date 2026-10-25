package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	buildmeta "github.com/xcosmosbox/cairn/build/internal"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

const stagedBridgeMode = "staged-exact-wire/v1"
const stagedBridgeCounter = "deepseek-recipe@935817c66f5d6e64838f419692545f06787e485f/v41"

type stagedBridgeDocument struct {
	DocumentID     string `json:"document_id"`
	OriginalSHA256 string `json:"original_sha256"`
}

// 构造参数是已独立认证的快照，摘要检查不授予调用方自身真实性。
// ResolveMembers is a read-only authority over actual validated leaf descriptors and request roots.
type stagedBridgeConfig struct {
	Client                                                    llm.Config
	Identity, Expected                                        incremental.StagedMappingIdentity
	Documents                                                 []stagedBridgeDocument
	MemberDocuments                                           map[string][]string
	SourceBindingsSHA256, MemberDocumentsSHA256, DriverSHA256 string
	Counter                                                   tokenCounter
	CounterIdentity                                           seededCounterIdentity
	Ledger                                                    *durableLedger
	BaseTransport                                             http.RoundTripper
	ResolveMembers                                            func(context.Context, string, string, string, int, string) ([]string, error)
	LedgerSegmentID                                           string
	History                                                   []stagedBridgeLedgerSegment
	HistorySHA256                                             string
}

type stagedBridgeLedgerSegment struct {
	ID     string
	File   *os.File
	Bytes  int64
	SHA256 string
}
type stagedBridgeHistoryRoot struct {
	ID     string
	Bytes  int64
	SHA256 string
}
type stagedBridgeObservedExpectation struct {
	Request        llm.CompleteRequest
	Metadata       stagedBridgeMetadata
	Binding        incremental.StagedMappingWireBinding
	ReceiptID      string
	SavedResponse  llm.CompleteResponse
	ResponseSHA256 string
	Members        []string
}
type stagedBridgeObservedRecord struct {
	SegmentID                                                                           string
	CallID                                                                              int64
	Metadata                                                                            stagedBridgeMetadata
	Binding                                                                             incremental.StagedMappingWireBinding
	LogicalRequestSHA256, ReceiptID, ResponseContentSHA256, FinishReason, ReturnedModel string
	MembersSHA256                                                                       string
	InputTokens, OutputTokens                                                           int
	Complete                                                                            bool
	Rejection                                                                           string
}
type stagedBridgeInventory struct {
	HistorySHA256, ActiveSegmentID, ActiveSHA256 string
	ActiveBytes                                  int64
	Records                                      []stagedBridgeObservedRecord
}

type stagedBridgeMetadata struct {
	Stage, WorkID, ParentCatalogueRoot string
	Attempt                            int
	UnitSourceDocIDs                   []string
	SourceBindingsSHA256               string
}
type stagedBridgeSnapshot struct {
	Identity                                                            incremental.StagedMappingIdentity
	Metadata                                                            stagedBridgeMetadata
	Members                                                             []string
	Documents                                                           []stagedBridgeDocument
	AllSourcesSHA256, MembersSHA256, DriverSHA256, LogicalRequestSHA256 string
	Endpoint, Method                                                    string
	Count                                                               tokenCount
	SegmentID, HistorySHA256, MaterialRole                              string
	CounterIdentity                                                     seededCounterIdentity
}
type stagedBridgeAttempt struct {
	Last     int
	Complete bool
}
type stagedWireBridge struct {
	client                            *llm.OpenAICompatClient
	bounded                           *boundedClient
	observer                          *traceTransport
	ledger                            *durableLedger
	counter                           tokenCounter
	counterIdentity                   seededCounterIdentity
	identity                          incremental.StagedMappingIdentity
	documents                         map[string]string
	members                           map[string][]string
	sourcesSHA, membersSHA, driverSHA string
	resolve                           func(context.Context, string, string, string, int, string) ([]string, error)
	mu                                sync.Mutex
	err                               error
	sequence                          uint64
	handles                           map[string]*stagedBridgePrepared
	attempts                          map[string]stagedBridgeAttempt
	dispatchGate                      chan struct{}
	segmentID, historySHA             string
	history                           []stagedBridgeLedgerSegment
}
type stagedBridgePrepared struct {
	owner           *stagedWireBridge
	request         llm.CompleteRequest
	wire            []byte
	binding         incremental.StagedMappingWireBinding
	snapshot        stagedBridgeSnapshot
	seal            string
	used, forwarded atomic.Bool
	logicalID       atomic.Int64
	cap             *stagedBridgeDispatch
	mu              sync.Mutex
	cancel          context.CancelFunc
	observed        bool
	receipt         string
	response        llm.CompleteResponse
	observationErr  error
}
type stagedBridgeDispatchKey struct{}
type stagedBridgeDispatch struct {
	prepared *stagedBridgePrepared
	seal     string
}

func stagedBridgeJSON(v any) ([]byte, error) { return json.Marshal(v) }
func stagedBridgeHash(v any) string {
	b, e := stagedBridgeJSON(v)
	if e != nil {
		return ""
	}
	return sha(b)
}
func stagedBridgeHex(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == n && s == strings.ToLower(s)
}
func stagedBridgeCopyRequest(r llm.CompleteRequest) llm.CompleteRequest {
	r.StopSequences = append([]string(nil), r.StopSequences...)
	return r
}

// 逻辑请求摘要是json.Marshal(CompleteRequest)，与实际生产wire SHA明确区分。
// This recipe matches the immutable verifier descriptor; it is not proof of provider delivery.
func stagedBridgeLogicalSHA(r llm.CompleteRequest) (string, error) {
	if !utf8.ValidString(r.System) || !utf8.ValidString(r.User) {
		return "", fmt.Errorf("bridge: invalid UTF-8 request")
	}
	for _, s := range r.StopSequences {
		if !utf8.ValidString(s) {
			return "", fmt.Errorf("bridge: invalid UTF-8 stop sequence")
		}
	}
	b, e := json.Marshal(r)
	if e != nil {
		return "", fmt.Errorf("bridge: invalid logical request")
	}
	return sha(b), nil
}
func stagedBridgeSourcesSHA(documents []stagedBridgeDocument) string {
	return stagedBridgeHash(documents)
}
func stagedBridgeMembersSHA(members map[string][]string) string { return stagedBridgeHash(members) }
func stagedBridgeDriverSHA() string                             { return strings.TrimPrefix(driverSourceDigest(), "sha256:") }
func stagedBridgeSerializerSHA() string {
	return strings.TrimPrefix(buildmeta.SourceDigest("llm/"), "sha256:")
}

func stagedBridgeProfile(client *llm.OpenAICompatClient) (incremental.StagedMappingAdmissionIdentity, error) {
	endpoint, wire, e := client.RenderRequest(llm.CompleteRequest{MaxTokens: 65536})
	if e != nil {
		return incremental.StagedMappingAdmissionIdentity{}, e
	}
	var parsed wireRequest
	if e = json.Unmarshal(wire, &parsed); e != nil || endpoint != "https://api.deepseek.com/chat/completions" || parsed.Model != "deepseek-flash" || parsed.MaxTokens != 65536 || parsed.Thinking.Type != "enabled" || parsed.ResponseFormat.Type != "json_object" {
		return incremental.StagedMappingAdmissionIdentity{}, fmt.Errorf("bridge: unsupported effective production profile")
	}
	profile := stagedBridgeHash(struct {
		Endpoint, Method string
		Body             json.RawMessage
	}{endpoint, http.MethodPost, wire})
	return incremental.StagedMappingAdmissionIdentity{Endpoint: endpoint, Model: parsed.Model, ProfileSHA256: profile, SerializerSHA256: stagedBridgeSerializerSHA()}, nil
}

// newStagedWireBridge 只连接既有客户端/观察器/账本；构造与Prepare均没有HTTP或准入事件。
// Defaults keep their old path; this bridge pins an explicit observer and immutable base transport.
func newStagedWireBridge(config stagedBridgeConfig) (*stagedWireBridge, error) {
	if config.Ledger == nil || config.Ledger.file == nil || config.Counter == nil || config.Identity != config.Expected {
		return nil, fmt.Errorf("bridge: missing independent configuration")
	}
	identity := config.Identity
	if !stagedBridgeHex(identity.CodeSourceCommit, 20) || identity.ExpectedDocuments < 1 || len(config.Documents) != identity.ExpectedDocuments {
		return nil, fmt.Errorf("bridge: incomplete authenticated source universe")
	}
	for _, pin := range []string{identity.CorpusSHA256, identity.RosterSHA256, identity.AcceptedSetSHA256, identity.InputSHA256, identity.MethodSHA256, config.SourceBindingsSHA256, config.MemberDocumentsSHA256, config.DriverSHA256} {
		if !stagedBridgeHex(pin, 32) {
			return nil, fmt.Errorf("bridge: invalid independent root")
		}
	}
	if config.DriverSHA256 != stagedBridgeDriverSHA() {
		return nil, fmt.Errorf("bridge: compiled driver source changed")
	}
	counterID, e := counterSeedIdentity(config.Counter)
	if e != nil || counterID != config.CounterIdentity || counterID.CounterID != stagedBridgeCounter {
		return nil, fmt.Errorf("bridge: counter configuration mismatch")
	}
	for _, pin := range []string{counterID.PythonSHA, counterID.AdapterSHA, counterID.RuntimeSHA} {
		if !stagedBridgeHex(pin, 32) {
			return nil, fmt.Errorf("bridge: unpinned counter runtime")
		}
	}
	b := &stagedWireBridge{ledger: config.Ledger, counter: config.Counter, counterIdentity: counterID, identity: identity, documents: map[string]string{}, members: map[string][]string{}, sourcesSHA: config.SourceBindingsSHA256, membersSHA: config.MemberDocumentsSHA256, driverSHA: config.DriverSHA256, resolve: config.ResolveMembers, handles: map[string]*stagedBridgePrepared{}, attempts: map[string]stagedBridgeAttempt{}}
	b.segmentID, b.historySHA = config.LedgerSegmentID, config.HistorySHA256
	b.dispatchGate = make(chan struct{}, 1)
	b.history = append([]stagedBridgeLedgerSegment(nil), config.History...)
	if !stagedBridgeHex(b.segmentID, 32) || !stagedBridgeHex(b.historySHA, 32) || stagedBridgeHistorySHA(b.history) != b.historySHA {
		return nil, fmt.Errorf("bridge: invalid ordered ledger segment roots")
	}
	active, ok := b.ledger.file.(stagedBridgeLedgerFile)
	if !ok {
		return nil, fmt.Errorf("bridge: readable existing durable ledger file required")
	}
	activeStat, e := active.Stat()
	if e != nil || activeStat.Size() != 0 || b.ledger.rows != 0 {
		return nil, fmt.Errorf("bridge: active ledger segment must be a fresh empty file")
	}
	seenSegments := map[string]bool{b.segmentID: true}
	for _, segment := range b.history {
		if !stagedBridgeHex(segment.ID, 32) || seenSegments[segment.ID] || segment.File == nil || segment.Bytes < 0 || !stagedBridgeHex(segment.SHA256, 32) {
			return nil, fmt.Errorf("bridge: reused or invalid historical segment")
		}
		stat, e := segment.File.Stat()
		if e != nil || os.SameFile(activeStat, stat) {
			return nil, fmt.Errorf("bridge: active ledger must not alias a historical file")
		}
		seenSegments[segment.ID] = true
	}
	last := ""
	docs := append([]stagedBridgeDocument(nil), config.Documents...)
	for _, doc := range docs {
		if !stagedBridgeHex(doc.DocumentID, 32) || !stagedBridgeHex(doc.OriginalSHA256, 32) || doc.DocumentID <= last {
			return nil, fmt.Errorf("bridge: source bindings must be complete ordered unique originals")
		}
		b.documents[doc.DocumentID] = doc.OriginalSHA256
		last = doc.DocumentID
	}
	if stagedBridgeSourcesSHA(docs) != b.sourcesSHA {
		return nil, fmt.Errorf("bridge: authenticated original bindings mismatch")
	}
	for member, documents := range config.MemberDocuments {
		if strings.TrimSpace(member) == "" || !utf8.ValidString(member) || len(documents) == 0 {
			return nil, fmt.Errorf("bridge: invalid authenticated member")
		}
		copyDocs := append([]string(nil), documents...)
		sort.Strings(copyDocs)
		for i, doc := range copyDocs {
			if b.documents[doc] == "" || (i > 0 && copyDocs[i-1] == doc) {
				return nil, fmt.Errorf("bridge: invalid member direct-document ownership")
			}
		}
		b.members[member] = copyDocs
	}
	if len(b.members) == 0 || stagedBridgeMembersSHA(b.members) != b.membersSHA {
		return nil, fmt.Errorf("bridge: authenticated member map mismatch")
	}
	base := config.BaseTransport
	if base == nil {
		base = http.DefaultTransport
	}
	b.observer = &traceTransport{next: base, ledger: b.ledger, counter: b.counter, seedPolicy: b}
	b.client, e = llm.NewOpenAICompatClientWithTransport(config.Client, b.observer)
	if e != nil {
		return nil, e
	}
	actual, e := stagedBridgeProfile(b.client)
	if e != nil {
		return nil, e
	}
	actual.CounterID, actual.RuntimeSHA256 = counterID.CounterID, counterID.RuntimeSHA
	if actual != identity.Admission {
		return nil, fmt.Errorf("bridge: effective renderer/profile/runtime identity mismatch")
	}
	b.bounded = &boundedClient{next: b.client, sem: make(chan struct{}, 1), ledger: b.ledger}
	inventory, replayErr := b.WholeInventory(context.Background())
	if replayErr != nil {
		b.stop(fmt.Errorf("bridge: historical ledger uncertain; dispatch stopped"))
	} else {
		for _, record := range inventory.Records {
			key := record.Metadata.Stage + "/" + record.Metadata.WorkID + "/" + record.Metadata.ParentCatalogueRoot
			b.attempts[key] = stagedBridgeAttempt{Last: record.Metadata.Attempt, Complete: record.Complete}
		}
	}
	return b, nil
}

func (b *stagedWireBridge) Identity() incremental.StagedMappingAdmissionIdentity {
	return b.identity.Admission
}
func (b *stagedWireBridge) ConstructionIdentity() incremental.StagedMappingIdentity {
	return b.identity
}
func (b *stagedWireBridge) failure() error {
	b.mu.Lock()
	err := b.err
	b.mu.Unlock()
	if err != nil {
		return err
	}
	return b.ledger.failure()
}
func (b *stagedWireBridge) stop(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.mu.Unlock()
}
func (b *stagedWireBridge) directDocuments(members []string) ([]string, []stagedBridgeDocument, error) {
	seenMembers := map[string]bool{}
	seenDocs := map[string]bool{}
	for _, member := range members {
		docs, ok := b.members[member]
		if !ok || seenMembers[member] {
			return nil, nil, fmt.Errorf("bridge: unknown/duplicate direct source member")
		}
		seenMembers[member] = true
		for _, doc := range docs {
			seenDocs[doc] = true
		}
	}
	if len(seenMembers) == 0 {
		return nil, nil, fmt.Errorf("bridge: empty direct source coverage")
	}
	var ids []string
	for doc := range seenDocs {
		ids = append(ids, doc)
	}
	sort.Strings(ids)
	rows := make([]stagedBridgeDocument, 0, len(ids))
	for _, doc := range ids {
		rows = append(rows, stagedBridgeDocument{doc, b.documents[doc]})
	}
	return ids, rows, nil
}

// Prepare将Mapping冻结Units展开为全部直接来源，绝不从prompt解析路径或猜测历史目录贡献者。
func (b *stagedWireBridge) Prepare(ctx context.Context, call incremental.StagedMappingCall) (incremental.StagedMappingPreparedCall, error) {
	if len(call.Units) < 1 || len(call.Units) > 32 {
		return nil, fmt.Errorf("bridge: invalid mapping unit count")
	}
	members := append([]string(nil), call.Units...)
	docs, rows, e := b.directDocuments(members)
	if e != nil {
		return nil, e
	}
	return b.prepare(ctx, call.Request, stagedBridgeMetadata{"mapping", call.BatchID, call.CatalogueSHA256, call.Attempt, docs, stagedBridgeSourcesSHA(rows)}, members)
}

// PrepareRequest是跨package的窄结构能力；pipeline可用相同签名定义interface，无需import main。
// The authority resolves actual validated descriptors and exact logical request roots, never supplied doc claims.
func (b *stagedWireBridge) PrepareRequest(ctx context.Context, request llm.CompleteRequest, stage, workID, parentCatalogueRoot string, attempt int, unitSourceDocIDs []string, sourceBindingsSHA string) (incremental.StagedMappingPreparedCall, error) {
	if b.resolve == nil {
		return nil, fmt.Errorf("bridge: authenticated leaf resolver required")
	}
	logical, e := stagedBridgeLogicalSHA(request)
	if e != nil {
		return nil, e
	}
	members, e := b.resolve(ctx, stage, workID, parentCatalogueRoot, attempt, logical)
	if e != nil {
		return nil, fmt.Errorf("bridge: leaf source authority rejected request")
	}
	return b.prepare(ctx, request, stagedBridgeMetadata{stage, workID, parentCatalogueRoot, attempt, append([]string(nil), unitSourceDocIDs...), sourceBindingsSHA}, append([]string(nil), members...))
}

func (b *stagedWireBridge) prepare(ctx context.Context, request llm.CompleteRequest, metadata stagedBridgeMetadata, members []string) (incremental.StagedMappingPreparedCall, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if b.failure() != nil {
		return nil, fmt.Errorf("bridge: prior outcome or persistence failure")
	}
	switch metadata.Stage {
	case "mapping", "description", "relation", "cross_relation_verify":
	default:
		return nil, fmt.Errorf("bridge: unknown stage")
	}
	if !stagedBridgeHex(metadata.WorkID, 32) || !stagedBridgeHex(metadata.ParentCatalogueRoot, 32) || (metadata.Attempt != 1 && metadata.Attempt != 2) {
		return nil, fmt.Errorf("bridge: invalid work/catalogue/attempt")
	}
	request = stagedBridgeCopyRequest(request)
	logical, e := stagedBridgeLogicalSHA(request)
	if e != nil {
		return nil, e
	}
	if sourceUUIDPatternBridge.MatchString(request.System) || sourceUUIDPatternBridge.MatchString(request.User) {
		return nil, fmt.Errorf("bridge: unsupported UUID-form literal in final model input")
	}
	documents, rows, e := b.directDocuments(members)
	if e != nil {
		return nil, e
	}
	if !reflect.DeepEqual(documents, metadata.UnitSourceDocIDs) || stagedBridgeSourcesSHA(rows) != metadata.SourceBindingsSHA256 {
		return nil, fmt.Errorf("bridge: complete direct-document/source hash mismatch")
	}
	endpoint, wire, e := b.client.RenderRequest(request)
	if e != nil {
		return nil, e
	}
	var parsed wireRequest
	var fields map[string]json.RawMessage
	if json.Unmarshal(wire, &parsed) != nil || json.Unmarshal(wire, &fields) != nil || len(fields) != 5 || parsed.MaxTokens != 65536 || parsed.Model != b.identity.Admission.Model || endpoint != b.identity.Admission.Endpoint || parsed.Thinking.Type != "enabled" || parsed.ResponseFormat.Type != "json_object" {
		return nil, fmt.Errorf("bridge: request profile override")
	}
	count, e := b.counter.Count(ctx, append([]byte(nil), wire...))
	if e != nil {
		return nil, fmt.Errorf("bridge: official token count unavailable")
	}
	actual, e := counterSeedIdentity(b.counter)
	if e != nil || actual != b.counterIdentity || count.CounterID != b.counterIdentity.CounterID || count.RuntimeSHA != b.counterIdentity.RuntimeSHA || count.Context != 1000000 || count.Reserved != 65536 || count.Input < 1 {
		return nil, fmt.Errorf("bridge: counter/runtime or context reservation mismatch")
	}
	if count.Input > count.Context-count.Reserved {
		return nil, incremental.ErrStagedMappingOverflow
	}
	metadata.UnitSourceDocIDs = append([]string(nil), documents...)
	sort.Strings(members)
	p := &stagedBridgePrepared{owner: b, request: request, wire: append([]byte(nil), wire...), snapshot: stagedBridgeSnapshot{Identity: b.identity, Metadata: metadata, Members: append([]string(nil), members...), Documents: rows, AllSourcesSHA256: b.sourcesSHA, MembersSHA256: b.membersSHA, DriverSHA256: b.driverSHA, LogicalRequestSHA256: logical, Endpoint: endpoint, Method: http.MethodPost, Count: count, SegmentID: b.segmentID, HistorySHA256: b.historySHA, MaterialRole: stagedBridgeMaterialRole(metadata.Stage), CounterIdentity: b.counterIdentity}}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return nil, fmt.Errorf("bridge: prior uncertain outcome")
	}
	b.sequence++
	handle := stagedBridgeHash(struct {
		Snapshot stagedBridgeSnapshot
		Sequence uint64
	}{p.snapshot, b.sequence})
	p.binding = incremental.StagedMappingWireBinding{HandleID: handle, RequestSHA256: sha(wire), Identity: b.identity.Admission, InputTokens: count.Input, ReservedOutputTokens: count.Reserved, ContextLimit: count.Context}
	p.seal = p.digest()
	b.handles[handle] = p
	return p, nil
}

// 与既有sourceUUIDPattern一致，完整wire前再次禁止字面量；不掩码、不截断任何素材。
var sourceUUIDPatternBridge = uuidLiteralPattern()

func uuidLiteralPattern() *regexp.Regexp {
	return regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
}

func (p *stagedBridgePrepared) workKey() string {
	return p.snapshot.Metadata.Stage + "/" + p.snapshot.Metadata.WorkID + "/" + p.snapshot.Metadata.ParentCatalogueRoot
}
func (p *stagedBridgePrepared) digest() string {
	return stagedBridgeHash(struct {
		Snapshot stagedBridgeSnapshot
		Binding  incremental.StagedMappingWireBinding
		Request  llm.CompleteRequest
		Wire     []byte
	}{p.snapshot, p.binding, p.request, p.wire})
}
func (p *stagedBridgePrepared) Wire() []byte                                  { return append([]byte(nil), p.wire...) }
func (p *stagedBridgePrepared) Binding() incremental.StagedMappingWireBinding { return p.binding }
func (p *stagedBridgePrepared) integrity() error {
	if p == nil || p.owner == nil {
		return fmt.Errorf("bridge: missing prepared owner")
	}
	if p.seal == "" || p.digest() != p.seal || p.snapshot.Identity != p.owner.identity || p.snapshot.DriverSHA256 != stagedBridgeDriverSHA() || p.binding.Identity.SerializerSHA256 != stagedBridgeSerializerSHA() || p.snapshot.CounterIdentity != p.owner.counterIdentity || p.snapshot.AllSourcesSHA256 != p.owner.sourcesSHA || p.snapshot.MembersSHA256 != p.owner.membersSHA || p.snapshot.SegmentID != p.owner.segmentID || p.snapshot.HistorySHA256 != p.owner.historySHA || p.snapshot.MaterialRole != stagedBridgeMaterialRole(p.snapshot.Metadata.Stage) {
		return fmt.Errorf("bridge: prepared private binding changed")
	}
	p.owner.mu.Lock()
	registered := p.owner.handles[p.binding.HandleID] == p
	p.owner.mu.Unlock()
	if !registered {
		return fmt.Errorf("bridge: forged/stale handle")
	}
	return nil
}
func (p *stagedBridgePrepared) recheckAuthority(ctx context.Context) error {
	if p.snapshot.Metadata.Stage == "mapping" {
		return nil
	}
	if p.owner.resolve == nil {
		return fmt.Errorf("bridge: leaf authority unavailable")
	}
	m := p.snapshot.Metadata
	members, e := p.owner.resolve(ctx, m.Stage, m.WorkID, m.ParentCatalogueRoot, m.Attempt, p.snapshot.LogicalRequestSHA256)
	if e != nil {
		return fmt.Errorf("bridge: stale leaf authority")
	}
	members = append([]string(nil), members...)
	sort.Strings(members)
	if !reflect.DeepEqual(members, p.snapshot.Members) {
		return fmt.Errorf("bridge: leaf membership authority changed")
	}
	return nil
}

// Dispatch消费不可重用句柄；ReceiptID只定位已持久化OBSERVED事件，不是语义验收或检查点。
func (p *stagedBridgePrepared) Dispatch(ctx context.Context, expected incremental.StagedMappingWireBinding) (incremental.StagedMappingDispatchResult, error) {
	result := incremental.StagedMappingDispatchResult{Binding: p.binding}
	if !p.used.CompareAndSwap(false, true) {
		return result, fmt.Errorf("bridge: prepared handle already consumed")
	}
	if expected != p.binding || p.integrity() != nil || p.owner.failure() != nil || p.recheckAuthority(ctx) != nil {
		return result, fmt.Errorf("bridge: dispatch binding/authority unavailable")
	}
	// This bridge supports exactly one in-flight logical call. A legitimate concurrent
	// caller waits before inventory, so its predecessor is never mistaken for a crash tail.
	if err := p.owner.acquireDispatch(ctx); err != nil {
		return result, err
	}
	defer p.owner.releaseDispatch()
	if _, err := p.owner.wholeInventory(ctx); err != nil {
		p.owner.stop(fmt.Errorf("bridge: complete ledger inventory unavailable"))
		return result, fmt.Errorf("bridge: historical or crash-tail evidence uncertain")
	}
	if p.owner.bounded.next != p.owner.client || p.owner.bounded.ledger != p.owner.ledger || p.owner.observer.ledger != p.owner.ledger || p.owner.observer.seedPolicy != p.owner {
		return result, fmt.Errorf("bridge: client/observer/ledger connection changed")
	}
	// Prepare可重复并提前形成两个固定尝试；真正的逻辑尝试只能在此原子领取一次。
	// Eligibility is dispatch state, independent of zero-HTTP phase preflight and handle reconstruction.
	p.owner.mu.Lock()
	key := p.workKey()
	old, exists := p.owner.attempts[key]
	attempt := p.snapshot.Metadata.Attempt
	if (attempt == 1 && exists) || (attempt == 2 && (!exists || old.Last != 1 || !old.Complete)) {
		p.owner.mu.Unlock()
		return result, fmt.Errorf("bridge: duplicate dispatch or second attempt lacks complete observed predecessor")
	}
	p.owner.attempts[key] = stagedBridgeAttempt{Last: attempt}
	p.owner.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.cancel = cancel
	p.cap = &stagedBridgeDispatch{p, p.seal}
	ctx = context.WithValue(ctx, stagedBridgeDispatchKey{}, p.cap)
	ctx = context.WithValue(ctx, seededBindingKey{}, p)
	response, err := p.owner.bounded.Complete(ctx, p.request)
	p.mu.Lock()
	result.ReceiptID = p.receipt
	result.Response = p.response
	observationErr := p.observationErr
	observed := p.observed
	p.mu.Unlock()
	if response != nil {
		result.Response = *response
	}
	if err != nil || observationErr != nil || !observed || result.ReceiptID == "" || p.owner.ledger.failure() != nil {
		p.owner.stop(fmt.Errorf("bridge: uncertain or failed observed outcome"))
		return result, fmt.Errorf("bridge: uncertain or failed observed outcome")
	}
	// Authenticate the full completed event chain before handing an observed result
	// to the controller, including attribution on response and logical_end.
	if _, err := p.owner.wholeInventory(ctx); err != nil {
		p.owner.stop(fmt.Errorf("bridge: completed ledger evidence contradictory"))
		return result, fmt.Errorf("bridge: completed ledger evidence unavailable; STOP")
	}
	return result, nil
}

func (p *stagedBridgePrepared) bind(request llm.CompleteRequest, info *callInfo) error {
	logical, e := stagedBridgeLogicalSHA(request)
	if e != nil || logical != p.snapshot.LogicalRequestSHA256 || !p.used.Load() || p.integrity() != nil || !p.logicalID.CompareAndSwap(0, info.ID) {
		return fmt.Errorf("bridge: private logical call binding mismatch")
	}
	m := p.snapshot.Metadata
	info.Stage = m.Stage
	info.Mode = stagedBridgeMode
	info.Phase = m.Stage
	info.Documents = append([]string(nil), m.UnitSourceDocIDs...)
	info.Document = ""
	info.SourceSHA = ""
	if len(p.snapshot.Documents) == 1 {
		info.Document = p.snapshot.Documents[0].DocumentID
		info.SourceSHA = p.snapshot.Documents[0].OriginalSHA256
	}
	info.staged = p
	return nil
}
func (p *stagedBridgePrepared) addIdentity(row map[string]any) {
	m := p.snapshot.Metadata
	row["document_ids"] = append([]string(nil), m.UnitSourceDocIDs...)
	row["document_id"] = ""
	if len(p.snapshot.Documents) == 1 {
		row["document_id"] = p.snapshot.Documents[0].DocumentID
	}
	row["work_id"], row["schema_attempt"], row["parent_catalogue_sha256"] = m.WorkID, m.Attempt, m.ParentCatalogueRoot
	row["source_bindings_sha256"] = m.SourceBindingsSHA256
	row["unit_source_document_ids"] = append([]string(nil), m.UnitSourceDocIDs...)
	originals := map[string]string{}
	for _, doc := range p.snapshot.Documents {
		originals[doc.DocumentID] = doc.OriginalSHA256
	}
	row["unit_original_sha256"] = originals
	row["authenticated_source_universe_sha256"], row["authenticated_member_documents_sha256"] = p.snapshot.AllSourcesSHA256, p.snapshot.MembersSHA256
	row["handle_id"], row["logical_request_sha256"], row["driver_source_sha256"] = p.binding.HandleID, p.snapshot.LogicalRequestSHA256, p.snapshot.DriverSHA256
	row["code_source_commit"], row["corpus_sha256"], row["roster_sha256"], row["accepted_set_sha256"], row["method_sha256"] = p.snapshot.Identity.CodeSourceCommit, p.snapshot.Identity.CorpusSHA256, p.snapshot.Identity.RosterSHA256, p.snapshot.Identity.AcceptedSetSHA256, p.snapshot.Identity.MethodSHA256
	row["input_sha256"], row["ledger_segment_id"], row["material_provenance_role"] = p.snapshot.Identity.InputSHA256, p.snapshot.SegmentID, p.snapshot.MaterialRole
	row["history_root_sha256"], row["staged_identity"], row["wire_binding"] = p.snapshot.HistorySHA256, p.snapshot.Identity, p.binding
	row["counter_configuration"], row["direct_member_ids_sha256"] = p.snapshot.CounterIdentity, stagedBridgeHash(p.snapshot.Members)
	row["endpoint"], row["http_method"], row["profile_sha256"], row["serializer_sha256"] = p.snapshot.Endpoint, p.snapshot.Method, p.binding.Identity.ProfileSHA256, p.binding.Identity.SerializerSHA256
}

func stagedBridgeMaterialRole(stage string) string {
	switch stage {
	case "mapping":
		return "direct_annotation_incidences_with_catalogue_context"
	case "description":
		return "direct_annotation_incidences"
	case "relation":
		return "endpoint_generated_descriptions"
	case "cross_relation_verify":
		return "full_original_endpoint_sources"
	}
	return "unsupported"
}

// 历史分段只读，活动分段沿用原durableLedger；分段ID区分重启后的call_id。
type stagedBridgeLedgerFile interface {
	syncWriter
	io.ReaderAt
	Stat() (os.FileInfo, error)
}

func stagedBridgeHistorySHA(segments []stagedBridgeLedgerSegment) string {
	roots := make([]stagedBridgeHistoryRoot, 0, len(segments))
	for _, segment := range segments {
		roots = append(roots, stagedBridgeHistoryRoot{segment.ID, segment.Bytes, segment.SHA256})
	}
	return stagedBridgeHash(roots)
}
func stagedBridgeReadFile(file interface {
	io.ReaderAt
	Stat() (os.FileInfo, error)
}) ([]byte, error) {
	before, e := file.Stat()
	if e != nil {
		return nil, fmt.Errorf("bridge: ledger stat unavailable")
	}
	raw, e := io.ReadAll(io.NewSectionReader(file, 0, before.Size()))
	if e != nil {
		return nil, fmt.Errorf("bridge: ledger snapshot unavailable")
	}
	after, e := file.Stat()
	if e != nil || after.Size() != before.Size() || int64(len(raw)) != before.Size() {
		return nil, fmt.Errorf("bridge: advancing ledger snapshot")
	}
	return raw, nil
}
func (l *durableLedger) stagedSnapshot() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	file, ok := l.file.(stagedBridgeLedgerFile)
	if !ok {
		return nil, fmt.Errorf("bridge: ledger is not readable")
	}
	raw, e := stagedBridgeReadFile(file)
	if e != nil {
		return nil, e
	}
	if l.err != nil {
		return raw, fmt.Errorf("bridge: ledger persistence unavailable")
	}
	return raw, nil
}

type stagedBridgeReplayCall struct {
	record                        stagedBridgeObservedRecord
	start, request, response, end bool
	httpID                        int64
	uncertain                     bool
}

// WholeInventory扫描全部实际分段，不只扫描checkpoint声称拥有的ReceiptID。
// It performs no append or token count; orphan admissions, unknown outcomes and incomplete tails stop.
func (b *stagedWireBridge) WholeInventory(ctx context.Context) (stagedBridgeInventory, error) {
	if err := b.acquireDispatch(ctx); err != nil {
		return stagedBridgeInventory{}, err
	}
	defer b.releaseDispatch()
	return b.wholeInventory(ctx)
}
func (b *stagedWireBridge) acquireDispatch(ctx context.Context) error {
	select {
	case b.dispatchGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		b.releaseDispatch()
		return err
	}
	return nil
}
func (b *stagedWireBridge) releaseDispatch() { <-b.dispatchGate }
func (b *stagedWireBridge) wholeInventory(ctx context.Context) (stagedBridgeInventory, error) {
	result := stagedBridgeInventory{HistorySHA256: b.historySHA, ActiveSegmentID: b.segmentID, Records: []stagedBridgeObservedRecord{}}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if stagedBridgeHistorySHA(b.history) != b.historySHA {
		return result, fmt.Errorf("bridge: history root changed")
	}
	type segmentBytes struct {
		id  string
		raw []byte
	}
	var segments []segmentBytes
	for _, segment := range b.history {
		raw, e := stagedBridgeReadFile(segment.File)
		if e != nil || int64(len(raw)) != segment.Bytes || sha(raw) != segment.SHA256 {
			return result, fmt.Errorf("bridge: historical segment changed or incomplete")
		}
		segments = append(segments, segmentBytes{segment.ID, raw})
	}
	active, snapshotErr := b.ledger.stagedSnapshot()
	result.ActiveBytes = int64(len(active))
	result.ActiveSHA256 = sha(active)
	segments = append(segments, segmentBytes{b.segmentID, active})
	seenWork := map[string]bool{}
	seenHandles := map[string]bool{}
	previous := map[string]int{}
	uncertain := snapshotErr != nil
	for segmentIndex, segment := range segments {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if len(segment.raw) > 0 && segment.raw[len(segment.raw)-1] != '\n' {
			return result, fmt.Errorf("bridge: incomplete ledger tail")
		}
		calls := map[int64]*stagedBridgeReplayCall{}
		httpIDs := map[int64]bool{}
		expectedHistory := b.historySHA
		if segmentIndex < len(b.history) {
			expectedHistory = stagedBridgeHistorySHA(b.history[:segmentIndex])
		}
		var order []int64
		for _, line := range bytes.Split(segment.raw, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			if stagedBridgeCheckJSON(line, false) != nil {
				return result, fmt.Errorf("bridge: ambiguous ledger event")
			}
			var row map[string]json.RawMessage
			if json.Unmarshal(line, &row) != nil {
				return result, fmt.Errorf("bridge: invalid ledger JSON")
			}
			text := func(key string) string { var s string; _ = json.Unmarshal(row[key], &s); return s }
			integer := func(key string) int64 {
				var n int64
				if json.Unmarshal(row[key], &n) != nil {
					return -1
				}
				return n
			}
			callID := integer("call_id")
			if callID <= 0 || text("ledger_segment_id") != segment.id || text("operation_mode") != stagedBridgeMode {
				return result, fmt.Errorf("bridge: unbound ledger segment/call")
			}
			var identity incremental.StagedMappingIdentity
			var binding incremental.StagedMappingWireBinding
			var counterIdentity seededCounterIdentity
			if json.Unmarshal(row["counter_configuration"], &counterIdentity) != nil || counterIdentity != b.counterIdentity || !stagedBridgeSameJSON(row["counter_configuration"], b.counterIdentity) || text("history_root_sha256") != expectedHistory {
				return result, fmt.Errorf("bridge: counter configuration or ordered history changed")
			}
			if json.Unmarshal(row["staged_identity"], &identity) != nil || identity != b.identity || !stagedBridgeSameJSON(row["staged_identity"], b.identity) || json.Unmarshal(row["wire_binding"], &binding) != nil || !stagedBridgeSameJSON(row["wire_binding"], binding) || binding.Identity != b.identity.Admission || !stagedBridgeHex(binding.HandleID, 32) || !stagedBridgeHex(binding.RequestSHA256, 32) || binding.InputTokens < 1 || binding.ReservedOutputTokens != 65536 || binding.ContextLimit != 1000000 || binding.InputTokens > 1000000-65536 {
				return result, fmt.Errorf("bridge: ledger source/profile/runtime roots mismatch")
			}
			metadata := stagedBridgeMetadata{Stage: text("stage"), WorkID: text("work_id"), ParentCatalogueRoot: text("parent_catalogue_sha256"), Attempt: int(integer("schema_attempt")), SourceBindingsSHA256: text("source_bindings_sha256")}
			if json.Unmarshal(row["unit_source_document_ids"], &metadata.UnitSourceDocIDs) != nil || !stagedBridgeHex(metadata.WorkID, 32) || !stagedBridgeHex(metadata.ParentCatalogueRoot, 32) || (metadata.Attempt != 1 && metadata.Attempt != 2) || stagedBridgeMaterialRole(metadata.Stage) == "unsupported" || text("material_provenance_role") != stagedBridgeMaterialRole(metadata.Stage) || text("authenticated_source_universe_sha256") != b.sourcesSHA || text("authenticated_member_documents_sha256") != b.membersSHA || text("driver_source_sha256") != b.driverSHA || text("endpoint") != binding.Identity.Endpoint || text("http_method") != "POST" || text("profile_sha256") != binding.Identity.ProfileSHA256 || text("serializer_sha256") != binding.Identity.SerializerSHA256 {
				return result, fmt.Errorf("bridge: ledger work/context identity mismatch")
			}
			var rows []stagedBridgeDocument
			last := ""
			for _, doc := range metadata.UnitSourceDocIDs {
				if b.documents[doc] == "" || doc <= last {
					return result, fmt.Errorf("bridge: invalid ledger document ownership")
				}
				rows = append(rows, stagedBridgeDocument{doc, b.documents[doc]})
				last = doc
			}
			if len(rows) == 0 || stagedBridgeSourcesSHA(rows) != metadata.SourceBindingsSHA256 {
				return result, fmt.Errorf("bridge: ledger direct source bindings mismatch")
			}
			var originals map[string]string
			if json.Unmarshal(row["unit_original_sha256"], &originals) != nil || len(originals) != len(rows) {
				return result, fmt.Errorf("bridge: missing original source map")
			}
			for _, doc := range rows {
				if originals[doc.DocumentID] != doc.OriginalSHA256 {
					return result, fmt.Errorf("bridge: original source hash changed")
				}
			}
			original := ""
			if len(rows) == 1 {
				original = rows[0].OriginalSHA256
			}
			var attributed []string
			single := ""
			if len(rows) == 1 {
				single = rows[0].DocumentID
			}
			if json.Unmarshal(row["document_ids"], &attributed) != nil || !reflect.DeepEqual(attributed, metadata.UnitSourceDocIDs) || text("document_id") != single || text("phase") != metadata.Stage || text("original_sha256") != original || text("parent_proof_sha256") != "" {
				return result, fmt.Errorf("bridge: false single-original attribution")
			}
			if _, present := row["parent_proof_sha256"]; present {
				return result, fmt.Errorf("bridge: unexpected parent proof attribution")
			}
			for key, expected := range map[string]string{
				"code_source_commit": identity.CodeSourceCommit, "corpus_sha256": identity.CorpusSHA256, "roster_sha256": identity.RosterSHA256,
				"accepted_set_sha256": identity.AcceptedSetSHA256, "input_sha256": identity.InputSHA256, "method_sha256": identity.MethodSHA256,
				"handle_id": binding.HandleID, "stage": metadata.Stage, "phase": metadata.Stage, "operation_mode": stagedBridgeMode,
				"original_sha256": original, "document_id": single,
			} {
				if !stagedBridgeSameJSON(row[key], expected) {
					return result, fmt.Errorf("bridge: contradictory ledger identity alias")
				}
			}
			for key, expected := range map[string]any{
				"requested_model": binding.Identity.Model, "max_tokens": 65536,
				"thinking": map[string]string{"type": "enabled"}, "response_format": map[string]string{"type": "json_object"},
			} {
				_, present := row[key]
				if (text("event") == "request" || present) && !stagedBridgeSameJSON(row[key], expected) {
					return result, fmt.Errorf("bridge: contradictory rendered profile alias")
				}
			}
			logical := text("logical_request_sha256")
			memberRoot := text("direct_member_ids_sha256")
			if !stagedBridgeHex(logical, 32) || !stagedBridgeHex(memberRoot, 32) {
				return result, fmt.Errorf("bridge: missing logical request binding")
			}
			current := calls[callID]
			if current == nil {
				current = &stagedBridgeReplayCall{record: stagedBridgeObservedRecord{SegmentID: segment.id, CallID: callID, Metadata: metadata, Binding: binding, LogicalRequestSHA256: logical, MembersSHA256: memberRoot}}
				calls[callID] = current
				order = append(order, callID)
			} else if !reflect.DeepEqual(current.record.Metadata, metadata) || current.record.Binding != binding || current.record.LogicalRequestSHA256 != logical || current.record.MembersSHA256 != memberRoot {
				return result, fmt.Errorf("bridge: call identity changed between ledger events")
			}
			switch text("event") {
			case "logical_start":
				if current.start || current.request || current.response || current.end || seenHandles[binding.HandleID] {
					return result, fmt.Errorf("bridge: duplicate/out-of-order logical start")
				}
				seenHandles[binding.HandleID] = true
				current.start = true
			case "request":
				var admitted bool
				_ = json.Unmarshal(row["admitted"], &admitted)
				if !current.start || current.end || current.request || !admitted || integer("provider_attempt") != 1 {
					current.uncertain = true
					current.record.Rejection = "rejected_or_duplicate_request"
					continue
				}
				var count tokenCount
				if json.Unmarshal(row["token_count"], &count) != nil || !stagedBridgeSameJSON(row["token_count"], count) || count.Input != binding.InputTokens || count.Reserved != 65536 || count.Context != 1000000 || count.CounterID != binding.Identity.CounterID || count.RuntimeSHA != binding.Identity.RuntimeSHA256 || text("request_sha256") != binding.RequestSHA256 || integer("request_bytes") <= 0 {
					return result, fmt.Errorf("bridge: admitted wire/count changed")
				}
				current.request = true
				current.httpID = integer("http_attempt")
				if current.httpID <= 0 || httpIDs[current.httpID] {
					return result, fmt.Errorf("bridge: missing or duplicate admitted wire identity")
				}
				httpIDs[current.httpID] = true
			case "response":
				if !current.request || current.response || current.end || integer("http_attempt") != current.httpID || integer("provider_attempt") != 1 {
					return result, fmt.Errorf("bridge: orphan/duplicate response")
				}
				current.response = true
				current.record.ReceiptID = "observed:" + sha(line)
				current.record.ResponseContentSHA256 = text("final_content_sha256")
				current.record.FinishReason = text("finish_reason")
				current.record.ReturnedModel = text("returned_model")
				current.record.InputTokens = int(integer("input_tokens"))
				current.record.OutputTokens = int(integer("output_tokens"))
				var modelExpected bool
				_ = json.Unmarshal(row["model_identity_expected"], &modelExpected)
				evidenceValid := stagedBridgeSameJSON(row["response_json"], true) && stagedBridgeSameJSON(row["local_input_tokens"], binding.InputTokens) && stagedBridgeSameJSON(row["provider_minus_local_input_tokens"], 0) && text("returned_model_sha256") == sha([]byte(current.record.ReturnedModel)) && stagedBridgeHex(text("response_sha256"), 32) && integer("response_bytes") > 0
				for _, flag := range []string{"transport_error", "response_read_error", "cancelled"} {
					if value, present := row[flag]; present && !stagedBridgeSameJSON(value, false) {
						evidenceValid = false
					}
				}
				if total, present := row["total_tokens"]; present && !stagedBridgeSameJSON(total, current.record.InputTokens+current.record.OutputTokens) {
					evidenceValid = false
				}
				if !evidenceValid || integer("http_status") != 200 || text("usage_status") != "reported" || !modelExpected || (current.record.ReturnedModel != "deepseek-flash" && current.record.ReturnedModel != "deepseek-v4.1-flash-1017") || current.record.FinishReason != "stop" || current.record.InputTokens != binding.InputTokens || current.record.OutputTokens < 1 || current.record.OutputTokens > 65536 || !stagedBridgeHex(current.record.ResponseContentSHA256, 32) {
					current.uncertain = true
					current.record.Rejection = "incomplete_or_unknown_observation"
				}
			case "logical_end":
				var failed bool
				if json.Unmarshal(row["complete_error"], &failed) != nil || !stagedBridgeSameJSON(row["complete_error"], false) || !current.start || current.end || !current.response || failed || integer("wire_attempts") != 1 {
					current.uncertain = true
					current.record.Rejection = "incomplete_logical_outcome"
				}
				current.end = true
			default:
				return result, fmt.Errorf("bridge: unknown ledger event")
			}
		}
		for _, id := range order {
			current := calls[id]
			current.record.Complete = current.start && current.request && current.response && current.end && !current.uncertain
			if !current.record.Complete {
				uncertain = true
				if current.record.Rejection == "" {
					current.record.Rejection = "orphan_or_incomplete_call"
				}
			}
			key := current.record.Metadata.Stage + "/" + current.record.Metadata.WorkID + "/" + current.record.Metadata.ParentCatalogueRoot
			attemptKey := fmt.Sprintf("%s/%d", key, current.record.Metadata.Attempt)
			if seenWork[attemptKey] || (current.record.Metadata.Attempt == 2 && previous[key] != 1) {
				uncertain = true
				current.record.Complete = false
				current.record.Rejection = "duplicate_or_unbound_attempt"
			}
			seenWork[attemptKey] = true
			if current.record.Complete {
				previous[key] = current.record.Metadata.Attempt
			}
			result.Records = append(result.Records, current.record)
		}
	}
	if uncertain {
		return result, fmt.Errorf("bridge: UNCERTAIN_LEDGER_STOP")
	}
	return result, nil
}

// AuthenticateObserved对照实际账本认证保存响应，ReceiptID和纯校验seal不代表语义接受。
// No HTTP, token count, ledger append or stage acceptance occurs here.
func (b *stagedWireBridge) AuthenticateObserved(ctx context.Context, expected stagedBridgeObservedExpectation) (llm.CompleteResponse, error) {
	bad := func() (llm.CompleteResponse, error) {
		return llm.CompleteResponse{}, fmt.Errorf("bridge: observed response authentication unavailable; STOP")
	}
	inventory, e := b.WholeInventory(ctx)
	if e != nil {
		return bad()
	}
	logical, e := stagedBridgeLogicalSHA(expected.Request)
	if e != nil {
		return bad()
	}
	endpoint, wire, e := b.client.RenderRequest(expected.Request)
	if e != nil || endpoint != b.identity.Admission.Endpoint || sha(wire) != expected.Binding.RequestSHA256 || expected.Binding.Identity != b.identity.Admission || expected.ResponseSHA256 == "" || stagedBridgeHash(expected.SavedResponse) != expected.ResponseSHA256 {
		return bad()
	}
	members := append([]string(nil), expected.Members...)
	if expected.Metadata.Stage != "mapping" {
		if b.resolve == nil {
			return bad()
		}
		resolved, e := b.resolve(ctx, expected.Metadata.Stage, expected.Metadata.WorkID, expected.Metadata.ParentCatalogueRoot, expected.Metadata.Attempt, logical)
		if e != nil {
			return bad()
		}
		if len(members) > 0 {
			a := append([]string(nil), members...)
			c := append([]string(nil), resolved...)
			sort.Strings(a)
			sort.Strings(c)
			if !reflect.DeepEqual(a, c) {
				return bad()
			}
		}
		members = append([]string(nil), resolved...)
	}
	docs, rows, e := b.directDocuments(members)
	if e != nil || !reflect.DeepEqual(docs, expected.Metadata.UnitSourceDocIDs) || stagedBridgeSourcesSHA(rows) != expected.Metadata.SourceBindingsSHA256 {
		return bad()
	}
	sort.Strings(members)
	var found *stagedBridgeObservedRecord
	for i := range inventory.Records {
		record := &inventory.Records[i]
		if record.ReceiptID != expected.ReceiptID {
			continue
		}
		if found != nil {
			return bad()
		}
		found = record
	}
	if found == nil || !found.Complete || !reflect.DeepEqual(found.Metadata, expected.Metadata) || found.Binding != expected.Binding || found.LogicalRequestSHA256 != logical || found.MembersSHA256 != stagedBridgeHash(members) || found.ResponseContentSHA256 != sha([]byte(expected.SavedResponse.Text)) || found.FinishReason != expected.SavedResponse.FinishReason || found.InputTokens != expected.SavedResponse.InputTokens || found.OutputTokens != expected.SavedResponse.OutputTokens {
		return bad()
	}
	return expected.SavedResponse, nil
}

func (b *stagedWireBridge) admit(*callInfo, wireRequest, []byte, tokenCount) error {
	return fmt.Errorf("bridge: actual request context required")
}
func (b *stagedWireBridge) admitRequest(request *http.Request, info *callInfo, parsed wireRequest, wire []byte, count tokenCount) error {
	p := info.staged
	cap, ok := request.Context().Value(stagedBridgeDispatchKey{}).(*stagedBridgeDispatch)
	if p == nil || p.owner != b || !ok || cap != p.cap || cap.prepared != p || cap.seal != p.seal || p.integrity() != nil || p.logicalID.Load() != info.ID || info.Attempts.Load() != 1 {
		return fmt.Errorf("bridge: missing/forged/replayed private dispatch context")
	}
	m := p.snapshot.Metadata
	single, original := "", ""
	if len(p.snapshot.Documents) == 1 {
		single = p.snapshot.Documents[0].DocumentID
		original = p.snapshot.Documents[0].OriginalSHA256
	}
	if info.Stage != m.Stage || info.Mode != stagedBridgeMode || info.Phase != m.Stage || info.Document != single || info.SourceSHA != original || !reflect.DeepEqual(info.Documents, m.UnitSourceDocIDs) || info.ParentSHA != "" || request.URL.String() != p.snapshot.Endpoint || request.Method != p.snapshot.Method || request.ContentLength != int64(len(wire)) || request.Header.Get("Content-Type") != "application/json" || !bytes.Equal(wire, p.wire) || sha(wire) != p.binding.RequestSHA256 || count != p.snapshot.Count {
		return fmt.Errorf("bridge: actual wire or authenticated call metadata drift")
	}
	actual, e := counterSeedIdentity(b.counter)
	if e != nil || actual != b.counterIdentity || parsed.MaxTokens != 65536 || parsed.Model != b.identity.Admission.Model || p.recheckAuthority(request.Context()) != nil || b.failure() != nil {
		return fmt.Errorf("bridge: runtime/profile/source authority changed")
	}
	if !p.forwarded.CompareAndSwap(false, true) {
		return fmt.Errorf("bridge: second actual wire blocked")
	}
	return nil
}

func (b *stagedWireBridge) observe(info *callInfo, raw []byte, outcome map[string]any, persistErr error) {
	p := info.staged
	if p == nil || p.owner != b {
		return
	}
	response, e := stagedBridgeResponseEvidence(raw, p.snapshot.Count.Input)
	if persistErr != nil || outcome["http_status"] != 200 || outcome["transport_error"] == true || outcome["response_read_error"] == true || !p.forwarded.Load() {
		e = fmt.Errorf("bridge: incomplete observed response or ledger")
	}
	var receipt string
	if persistErr == nil {
		receipt = "observed:" + stagedBridgeHash(outcome)
	}
	p.mu.Lock()
	p.observed = true
	p.receipt = receipt
	if response != nil {
		p.response = *response
	}
	p.observationErr = e
	p.mu.Unlock()
	if e != nil {
		b.stop(e)
		if p.cancel != nil {
			p.cancel()
		}
	}
}
func (p *stagedBridgePrepared) complete(response *llm.CompleteResponse, err error) error {
	p.mu.Lock()
	observed, receipt, actual, observedErr := p.observed, p.receipt, p.response, p.observationErr
	p.mu.Unlock()
	if err != nil || !observed || receipt == "" || observedErr != nil || response == nil || !reflect.DeepEqual(*response, actual) || p.logicalID.Load() == 0 || !p.forwarded.Load() {
		p.owner.stop(fmt.Errorf("bridge: logical completion lacks exact durable observation"))
		return fmt.Errorf("bridge: logical completion lacks exact durable observation")
	}
	p.owner.mu.Lock()
	state := p.owner.attempts[p.workKey()]
	state.Complete = true
	p.owner.attempts[p.workKey()] = state
	p.owner.mu.Unlock()
	return nil
}

// 只检查提供商响应证据，不解释阶段业务schema；schema重试仍受调用方的独立策略控制。
func stagedBridgeResponseEvidence(raw []byte, input int) (*llm.CompleteResponse, error) {
	if !utf8.Valid(raw) || stagedBridgeUniqueJSON(raw) != nil {
		return nil, fmt.Errorf("bridge: ambiguous provider response")
	}
	var envelope struct {
		Model   string `json:"model"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			Input  *int64 `json:"prompt_tokens"`
			Output *int64 `json:"completion_tokens"`
			Total  *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, fmt.Errorf("bridge: invalid provider JSON")
	}
	response := &llm.CompleteResponse{}
	if len(envelope.Choices) > 0 {
		response.FinishReason = envelope.Choices[0].Finish
		if envelope.Choices[0].Message.Content != nil {
			response.Text = *envelope.Choices[0].Message.Content
		}
	}
	u := envelope.Usage
	if u != nil {
		if u.Input != nil {
			response.InputTokens = int(*u.Input)
		}
		if u.Output != nil {
			response.OutputTokens = int(*u.Output)
		}
	}
	if (envelope.Model != "deepseek-flash" && envelope.Model != "deepseek-v4.1-flash-1017") || len(envelope.Choices) != 1 || envelope.Choices[0].Finish != "stop" || envelope.Choices[0].Message.Content == nil || u == nil || u.Input == nil || u.Output == nil || *u.Input != int64(input) || *u.Input <= 0 || *u.Output <= 0 || *u.Output > 65536 || (u.Total != nil && *u.Total != *u.Input+*u.Output) {
		return response, fmt.Errorf("bridge: incomplete/non-stop/model/usage evidence")
	}
	return response, nil
}

func stagedBridgeUniqueJSON(raw []byte) error {
	return stagedBridgeCheckJSON(raw, true)
}
func stagedBridgeSameJSON(raw json.RawMessage, expected any) bool {
	decode := func(raw []byte) (any, error) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var value any
		if e := d.Decode(&value); e != nil {
			return nil, e
		}
		if _, e := d.Token(); e != io.EOF {
			return nil, fmt.Errorf("trailing JSON")
		}
		return value, nil
	}
	want, e := json.Marshal(expected)
	if e != nil {
		return false
	}
	a, e := decode(raw)
	if e != nil {
		return false
	}
	b, e := decode(want)
	return e == nil && reflect.DeepEqual(a, b)
}
func stagedBridgeCheckJSON(raw []byte, provider bool) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	critical := map[string]bool{"model": true, "choices": true, "finish_reason": true, "message": true, "content": true, "usage": true, "prompt_tokens": true, "completion_tokens": true, "total_tokens": true}
	var walk func() error
	walk = func() error {
		token, e := d.Token()
		if e != nil {
			return e
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				token, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := token.(string)
				if !ok || seen[key] || (provider && critical[strings.ToLower(key)] && key != strings.ToLower(key)) {
					return fmt.Errorf("ambiguous key")
				}
				seen[key] = true
				if e := walk(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		case json.Delim('['):
			for d.More() {
				if e := walk(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		return nil
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing provider JSON")
	}
	return nil
}
