package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

type bridgeTestCounter struct {
	identity seededCounterIdentity
	input    atomic.Int64
	calls    atomic.Int64
	mutate   func(context.Context, []byte)
}

func (c *bridgeTestCounter) seededIdentity() seededCounterIdentity { return c.identity }
func (c *bridgeTestCounter) Count(ctx context.Context, wire []byte) (tokenCount, error) {
	c.calls.Add(1)
	if c.mutate != nil {
		c.mutate(ctx, wire)
	}
	var r wireRequest
	if json.Unmarshal(wire, &r) != nil {
		return tokenCount{}, errors.New("fake counter invalid JSON")
	}
	return tokenCount{Input: int(c.input.Load()), Reserved: r.MaxTokens, Context: 1000000, CounterID: c.identity.CounterID, RuntimeSHA: c.identity.RuntimeSHA}, nil
}

type bridgeTestFile struct {
	*os.File
	syncs  int
	failAt int
}

func (f *bridgeTestFile) Sync() error {
	f.syncs++
	if f.failAt > 0 && f.syncs >= f.failAt {
		return errors.New("injected sync failure")
	}
	return f.File.Sync()
}

type bridgeFixture struct {
	b        *stagedWireBridge
	config   stagedBridgeConfig
	file     *os.File
	writer   *bridgeTestFile
	counter  *bridgeTestCounter
	call     incremental.StagedMappingCall
	sends    atomic.Int64
	mu       sync.Mutex
	actual   []byte
	run      func(*http.Request) (*http.Response, error)
	registry map[string][]string
}

func bridgePin(value string) string { return sha([]byte(value)) }
func bridgeProvider(content string, input, output int) *http.Response {
	raw, _ := json.Marshal(map[string]any{"id": "synthetic-response-id", "model": "deepseek-flash", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": content}}}, "usage": map[string]int{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}
}
func bridgeFixtureConfig(t *testing.T, segment string, history []stagedBridgeLedgerSegment) *bridgeFixture {
	t.Helper()
	f := &bridgeFixture{registry: map[string][]string{}}
	root := t.TempDir()
	ledger, file, e := openLedger(filepath.Join(root, "active.jsonl"), nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { file.Close() })
	f.file = file
	f.writer = &bridgeTestFile{File: file}
	ledger.file = f.writer
	f.counter = &bridgeTestCounter{identity: seededCounterIdentity{PythonSHA: bridgePin("python"), AdapterSHA: bridgePin("adapter"), RuntimeSHA: bridgePin("official-runtime-fixture"), CounterID: stagedBridgeCounter}}
	f.counter.input.Store(10)
	docs := []stagedBridgeDocument{{bridgePin("doc-a"), bridgePin("original-a")}, {bridgePin("doc-b"), bridgePin("original-b")}}
	sort.Slice(docs, func(i, j int) bool { return docs[i].DocumentID < docs[j].DocumentID })
	members := map[string][]string{"unit-shared": {docs[0].DocumentID, docs[1].DocumentID}, "unit-single": {docs[1].DocumentID}}
	clientConfig := llm.Config{Endpoint: "https://api.deepseek.com/chat/completions", Model: "deepseek-flash", APIKeyEnv: "CAIRN_BRIDGE_TEST_UNUSED", MaxTokens: 65536, MaxRetries: 1}
	renderer, e := llm.NewOpenAICompatClient(clientConfig)
	if e != nil {
		t.Fatal(e)
	}
	pins, e := stagedBridgeProfile(renderer)
	if e != nil {
		t.Fatal(e)
	}
	pins.CounterID, pins.RuntimeSHA256 = f.counter.identity.CounterID, f.counter.identity.RuntimeSHA
	identity := incremental.StagedMappingIdentity{CodeSourceCommit: strings.Repeat("1", 40), CorpusSHA256: bridgePin("corpus"), RosterSHA256: bridgePin("roster"), AcceptedSetSHA256: bridgePin("accepted"), InputSHA256: bridgePin("input"), MethodSHA256: bridgePin("method"), ExpectedDocuments: len(docs), Admission: pins}
	f.config = stagedBridgeConfig{Client: clientConfig, Identity: identity, Expected: identity, Documents: docs, MemberDocuments: members, SourceBindingsSHA256: stagedBridgeSourcesSHA(docs), MemberDocumentsSHA256: stagedBridgeMembersSHA(members), DriverSHA256: stagedBridgeDriverSHA(), Counter: f.counter, CounterIdentity: f.counter.identity, Ledger: ledger, LedgerSegmentID: bridgePin(segment), History: history, HistorySHA256: stagedBridgeHistorySHA(history)}
	f.config.BaseTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		f.sends.Add(1)
		wire, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		f.mu.Lock()
		f.actual = append([]byte(nil), wire...)
		f.mu.Unlock()
		if f.run != nil {
			return f.run(r)
		}
		return bridgeProvider("private output", 10, 20), nil
	})
	f.config.ResolveMembers = func(_ context.Context, stage, work, parent string, attempt int, logical string) ([]string, error) {
		key := fmt.Sprintf("%s/%s/%s/%d/%s", stage, work, parent, attempt, logical)
		f.mu.Lock()
		defer f.mu.Unlock()
		members, ok := f.registry[key]
		if !ok {
			return nil, errors.New("unregistered actual leaf")
		}
		return append([]string(nil), members...), nil
	}
	f.call = incremental.StagedMappingCall{BatchID: bridgePin("work"), CatalogueSHA256: bridgePin("catalogue-context"), Attempt: 1, Units: []string{"unit-shared"}, Request: llm.CompleteRequest{System: "private system; return JSON", User: "private prompt material", MaxTokens: 65536}}
	return f
}
func newBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	f := bridgeFixtureConfig(t, "segment-one", nil)
	var e error
	f.b, e = newStagedWireBridge(f.config)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *bridgeFixture) prepare(t *testing.T, call incremental.StagedMappingCall) *stagedBridgePrepared {
	t.Helper()
	p, e := f.b.Prepare(context.Background(), call)
	if e != nil {
		t.Fatal(e)
	}
	return p.(*stagedBridgePrepared)
}
func (f *bridgeFixture) rows(t *testing.T) []map[string]any {
	t.Helper()
	raw, e := os.ReadFile(f.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	var rows []map[string]any
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if json.Unmarshal(line, &row) != nil {
			t.Fatal("invalid test ledger")
		}
		rows = append(rows, row)
	}
	return rows
}
func (f *bridgeFixture) register(t *testing.T, stage string, call incremental.StagedMappingCall, members []string) {
	t.Helper()
	logical, e := stagedBridgeLogicalSHA(call.Request)
	if e != nil {
		t.Fatal(e)
	}
	key := fmt.Sprintf("%s/%s/%s/%d/%s", stage, call.BatchID, call.CatalogueSHA256, call.Attempt, logical)
	f.mu.Lock()
	f.registry[key] = append([]string(nil), members...)
	f.mu.Unlock()
}

// Prepare不能用fake Complete捕获wire；真实现有客户端必须转发同一renderer产生的实际Body。
func TestStagedBridgePrepareIsPureAndDispatchForwardsExactActualBody(t *testing.T) {
	t.Parallel()
	f := newBridgeFixture(t)
	p := f.prepare(t, f.call)
	wire := p.Wire()
	binding := p.Binding()
	if f.sends.Load() != 0 || len(f.rows(t)) != 0 || f.writer.syncs != 0 {
		t.Fatal("Prepare emitted HTTP/ledger/admission")
	}
	wire[0] = '!'
	if p.Wire()[0] == '!' {
		t.Fatal("wire accessor aliases prepared buffer")
	}
	result, e := p.Dispatch(context.Background(), binding)
	if e != nil {
		t.Fatal(e)
	}
	if f.sends.Load() != 1 || result.Binding != binding || !strings.HasPrefix(result.ReceiptID, "observed:") || result.Response.Text != "private output" || result.Response.InputTokens != 10 || result.Response.OutputTokens != 20 {
		t.Fatal("exact observed completion missing")
	}
	if !bytes.Equal(f.actual, p.Wire()) {
		t.Fatal("actual forwarded Body differs")
	}
	rows := f.rows(t)
	if len(rows) != 4 || rows[0]["event"] != "logical_start" || rows[1]["event"] != "request" || rows[2]["event"] != "response" || rows[3]["event"] != "logical_end" {
		t.Fatal("existing ledger path not reused")
	}
	if rows[1]["original_sha256"] != "" || rows[1]["source_bindings_sha256"] != p.snapshot.Metadata.SourceBindingsSHA256 || len(rows[1]["unit_source_document_ids"].([]any)) != 2 || rows[1]["parent_catalogue_sha256"] != f.call.CatalogueSHA256 {
		t.Fatal("direct source set confused with catalogue history or fake single original")
	}
	raw, _ := os.ReadFile(f.file.Name())
	for _, private := range []string{"private prompt material", "private system", "private output", "Bearer"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("private body/auth in metadata: %s", private)
		}
	}
	if _, e := p.Dispatch(context.Background(), binding); e == nil || f.sends.Load() != 1 {
		t.Fatal("one-use handle replayed")
	}
}

func TestStagedBridgePreflightsBothAttemptsAndDeduplicatesDispatchAcrossHandles(t *testing.T) {
	t.Parallel()
	f := newBridgeFixture(t)
	one := f.prepare(t, f.call)
	duplicate := f.prepare(t, f.call)
	secondCall := f.call
	secondCall.Attempt = 2
	secondCall.Request.System += " fixed sanitized repair"
	early := f.prepare(t, secondCall)
	if len(f.rows(t)) != 0 || f.sends.Load() != 0 {
		t.Fatal("two-attempt preflight emitted events")
	}
	if _, e := early.Dispatch(context.Background(), early.Binding()); e == nil || f.sends.Load() != 0 {
		t.Fatal("attempt2 dispatched without predecessor")
	}
	if _, e := one.Dispatch(context.Background(), one.Binding()); e != nil {
		t.Fatal(e)
	}
	if _, e := duplicate.Dispatch(context.Background(), duplicate.Binding()); e == nil || f.sends.Load() != 1 {
		t.Fatal("duplicate Prepare repaid same work/attempt")
	}
	if f.sends.Load() != 1 {
		t.Fatal("complete response automatically triggered attempt2")
	}
	// A trusted controller separately classifies its complete response as schema-invalid before this call.
	if inv, e := f.b.WholeInventory(context.Background()); e != nil {
		t.Fatal("inventory after successful first dispatch", e, inv.Records)
	}
	second := f.prepare(t, secondCall)
	if _, e := second.Dispatch(context.Background(), second.Binding()); e != nil || f.sends.Load() != 2 {
		t.Fatal("controller-authorized bounded schema attempt2 failed", e)
	}
}

func TestStagedBridgeAuthenticatedAllDirectSourcesAndGenericLeafAuthority(t *testing.T) {
	t.Parallel()
	f := newBridgeFixture(t)
	f.register(t, "description", f.call, []string{"unit-shared"})
	docs, rows, e := f.b.directDocuments([]string{"unit-shared"})
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []struct {
		docs []string
		root string
	}{{docs[:1], stagedBridgeSourcesSHA(rows[:1])}, {docs, bridgePin("wrong")}, {[]string{docs[1], docs[0]}, stagedBridgeSourcesSHA(rows)}} {
		if _, e := f.b.PrepareRequest(context.Background(), f.call.Request, "description", f.call.BatchID, f.call.CatalogueSHA256, 1, bad.docs, bad.root); e == nil {
			t.Fatal("caller self-asserted partial/wrong direct docs accepted")
		}
	}
	if _, e := f.b.PrepareRequest(context.Background(), f.call.Request, "description", bridgePin("wrong work"), f.call.CatalogueSHA256, 1, docs, stagedBridgeSourcesSHA(rows)); e == nil {
		t.Fatal("unregistered work accepted")
	}
	p, e := f.b.PrepareRequest(context.Background(), f.call.Request, "description", f.call.BatchID, f.call.CatalogueSHA256, 1, docs, stagedBridgeSourcesSHA(rows))
	if e != nil {
		t.Fatal(e)
	}
	f.config.MemberDocuments["unit-shared"][0] = bridgePin("caller mutated map")
	f.config.Documents[0].OriginalSHA256 = bridgePin("caller mutated original")
	if _, e := p.Dispatch(context.Background(), p.Binding()); e != nil {
		t.Fatal("constructor did not copy authoritative inputs", e)
	}
	single := newBridgeFixture(t)
	call := single.call
	call.Units = []string{"unit-single"}
	sp := single.prepare(t, call)
	if _, e := sp.Dispatch(context.Background(), sp.Binding()); e != nil {
		t.Fatal(e)
	}
	if single.rows(t)[1]["original_sha256"] != sp.snapshot.Documents[0].OriginalSHA256 {
		t.Fatal("single actual original omitted")
	}
}

func TestStagedBridgeActualBodyMustMatchReplayAndApprovedLength(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"actual-body", "getbody", "length", "endpoint", "method", "missing-cap"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newBridgeFixture(t)
			p := f.prepare(t, f.call)
			p.used.Store(true)
			p.cap = &stagedBridgeDispatch{p, p.seal}
			ctx := context.WithValue(context.Background(), stagedBridgeDispatchKey{}, p.cap)
			info := &callInfo{ID: 1}
			if e := p.bind(p.request, info); e != nil {
				t.Fatal(e)
			}
			ctx = context.WithValue(ctx, callKey{}, info)
			request, e := http.NewRequestWithContext(ctx, "POST", p.snapshot.Endpoint, bytes.NewReader(p.Wire()))
			if e != nil {
				t.Fatal(e)
			}
			request.Header.Set("Content-Type", "application/json")
			switch kind {
			case "actual-body":
				request.Body = io.NopCloser(strings.NewReader(`{"changed":"actual"}`))
			case "getbody":
				request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("changed replay")), nil }
			case "length":
				request.ContentLength--
			case "endpoint":
				request.URL.Host = "other.example"
			case "method":
				request.Method = "GET"
			case "missing-cap":
				request = request.WithContext(context.WithValue(context.Background(), callKey{}, info))
			}
			if _, e := f.b.observer.RoundTrip(request); e == nil || f.sends.Load() != 0 {
				t.Fatal("changed actual request forwarded")
			}
		})
	}
}

func TestStagedBridgePrivateBindingAndActualContextDrift(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"wire", "work", "stage", "parent", "attempt", "units", "docs", "original", "code", "method", "runtime", "profile", "segment", "source-root", "driver"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			f := newBridgeFixture(t)
			p := f.prepare(t, f.call)
			binding := p.Binding()
			switch field {
			case "wire":
				p.wire = append(p.wire, ' ')
			case "work":
				p.snapshot.Metadata.WorkID = bridgePin("changed")
			case "stage":
				p.snapshot.Metadata.Stage = "relation"
			case "parent":
				p.snapshot.Metadata.ParentCatalogueRoot = bridgePin("changed")
			case "attempt":
				p.snapshot.Metadata.Attempt = 2
			case "units":
				p.snapshot.Members = []string{"unit-single"}
			case "docs":
				p.snapshot.Metadata.UnitSourceDocIDs = p.snapshot.Metadata.UnitSourceDocIDs[:1]
			case "original":
				p.snapshot.Documents[0].OriginalSHA256 = bridgePin("changed")
			case "code":
				p.snapshot.Identity.CodeSourceCommit = strings.Repeat("2", 40)
			case "method":
				p.snapshot.Identity.MethodSHA256 = bridgePin("changed")
			case "runtime":
				p.snapshot.Count.RuntimeSHA = bridgePin("changed")
			case "profile":
				p.binding.Identity.ProfileSHA256 = bridgePin("changed")
			case "segment":
				p.snapshot.SegmentID = bridgePin("changed")
			case "source-root":
				p.snapshot.AllSourcesSHA256 = bridgePin("changed")
			case "driver":
				p.snapshot.DriverSHA256 = bridgePin("changed")
			}
			if _, e := p.Dispatch(context.Background(), binding); e == nil || f.sends.Load() != 0 || len(f.rows(t)) != 0 {
				t.Fatal("private binding drift not rejected before events/HTTP")
			}
		})
	}
	for _, field := range []string{"stage", "doc", "source", "call", "phase", "mode", "parent"} {
		t.Run("actual-context/"+field, func(t *testing.T) {
			t.Parallel()
			f := newBridgeFixture(t)
			p := f.prepare(t, f.call)
			f.counter.mutate = func(ctx context.Context, _ []byte) {
				info, _ := ctx.Value(callKey{}).(*callInfo)
				if info == nil {
					return
				}
				switch field {
				case "stage":
					info.Stage = "relation"
				case "doc":
					info.Documents = info.Documents[:1]
				case "source":
					info.SourceSHA = bridgePin("changed")
				case "call":
					info.ID++
				case "phase":
					info.Phase = "changed"
				case "mode":
					info.Mode = "changed"
				case "parent":
					info.ParentSHA = bridgePin("changed")
				}
			}
			if _, e := p.Dispatch(context.Background(), p.Binding()); e == nil || f.sends.Load() != 0 {
				t.Fatal("actual private context drift forwarded")
			}
		})
	}
}

func TestStagedBridgeUnknownOutcomeNeverPermitsAnotherActualWire(t *testing.T) {
	for _, kind := range []string{"429", "transport", "input-mismatch", "zero-output", "oversized-output", "nonstop", "unknown-model", "missing-usage", "duplicate-usage", "persistence"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newBridgeFixture(t)
			f.run = func(*http.Request) (*http.Response, error) {
				switch kind {
				case "429":
					r := bridgeProvider("private output", 10, 20)
					r.StatusCode = 429
					return r, nil
				case "transport":
					return nil, errors.New("synthetic network error")
				case "input-mismatch":
					return bridgeProvider("private output", 11, 20), nil
				case "zero-output":
					return bridgeProvider("private output", 10, 0), nil
				case "oversized-output":
					return bridgeProvider("private output", 10, 65537), nil
				case "nonstop":
					r := bridgeProvider("private output", 10, 20)
					raw, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(strings.NewReader(strings.Replace(string(raw), `"stop"`, `"length"`, 1)))
					return r, nil
				case "unknown-model":
					r := bridgeProvider("private output", 10, 20)
					raw, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(strings.NewReader(strings.Replace(string(raw), `"deepseek-flash"`, `"unknown-model"`, 1)))
					return r, nil
				case "missing-usage":
					return response(`{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"content":"private output"}}]}`), nil
				case "duplicate-usage":
					return response(`{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"content":"private output"}}],"usage":{"prompt_tokens":10,"completion_tokens":20},"usage":{"prompt_tokens":10,"completion_tokens":20}}`), nil
				}
				return bridgeProvider("private output", 10, 20), nil
			}
			if kind == "persistence" {
				f.writer.failAt = 3
			}
			first := f.prepare(t, f.call)
			secondCall := f.call
			secondCall.Attempt = 2
			second := f.prepare(t, secondCall)
			result, e := first.Dispatch(context.Background(), first.Binding())
			if e == nil || f.sends.Load() != 1 {
				t.Fatal("bad outcome accepted or retried", e)
			}
			if kind == "persistence" && result.ReceiptID != "" {
				t.Fatal("failed sync invented durable observed receipt")
			}
			if _, e := second.Dispatch(context.Background(), second.Binding()); e == nil || f.sends.Load() != 1 {
				t.Fatal("unknown outcome allowed schema attempt2")
			}
		})
	}
}

func bridgeHistory(t *testing.T, f *bridgeFixture) stagedBridgeLedgerSegment {
	t.Helper()
	raw, e := os.ReadFile(f.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	reader, e := os.Open(f.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { reader.Close() })
	return stagedBridgeLedgerSegment{ID: f.config.LedgerSegmentID, File: reader, Bytes: int64(len(raw)), SHA256: sha(raw)}
}

func TestStagedBridgeRealFileRestartAuthenticatesAndNeverRepays(t *testing.T) {
	t.Parallel()
	first := newBridgeFixture(t)
	first.run = func(*http.Request) (*http.Response, error) { return bridgeProvider("{schema-invalid}", 10, 20), nil }
	p := first.prepare(t, first.call)
	r, e := p.Dispatch(context.Background(), p.Binding())
	if e != nil {
		t.Fatal(e)
	}
	expected := stagedBridgeObservedExpectation{Request: first.call.Request, Metadata: p.snapshot.Metadata, Binding: r.Binding, ReceiptID: r.ReceiptID, SavedResponse: r.Response, ResponseSHA256: stagedBridgeHash(r.Response), Members: first.call.Units}
	old := bridgeHistory(t, first)
	oldBytes, _ := os.ReadFile(old.File.Name())
	if e := first.file.Close(); e != nil {
		t.Fatal(e)
	}
	// A fresh bridge, fresh empty file and reset call counter model a real restart.
	second := bridgeFixtureConfig(t, "segment-two", []stagedBridgeLedgerSegment{old})
	second.b, e = newStagedWireBridge(second.config)
	if e != nil {
		t.Fatal(e)
	}
	beforeCount := second.counter.calls.Load()
	if _, e := second.b.WholeInventory(context.Background()); e != nil {
		t.Fatal("restart inventory", e)
	}
	got, e := second.b.AuthenticateObserved(context.Background(), expected)
	if e != nil || !reflect.DeepEqual(got, r.Response) || second.counter.calls.Load() != beforeCount || second.sends.Load() != 0 || len(second.rows(t)) != 0 {
		t.Fatal("historical complete response not purely authenticated", e)
	}
	duplicate := second.prepare(t, second.call)
	if _, e := duplicate.Dispatch(context.Background(), duplicate.Binding()); e == nil || second.sends.Load() != 0 {
		t.Fatal("restart repaid observed attempt1")
	}
	call2 := second.call
	call2.Attempt = 2
	p2 := second.prepare(t, call2)
	if _, e := p2.Dispatch(context.Background(), p2.Binding()); e != nil || second.sends.Load() != 1 {
		t.Fatal("authenticated complete schema-invalid predecessor cannot advance", e)
	}
	inv, e := second.b.WholeInventory(context.Background())
	if e != nil || len(inv.Records) != 2 || inv.Records[0].CallID != 1 || inv.Records[1].CallID != 1 || inv.Records[0].SegmentID == inv.Records[1].SegmentID || !inv.Records[1].Complete {
		t.Fatal("segment identity lost or inventory omitted a call", e, inv.Records)
	}
	inv.Records[0].Metadata.UnitSourceDocIDs[0] = "mutated-copy"
	if _, e := second.b.AuthenticateObserved(context.Background(), expected); e != nil {
		t.Fatal("inventory exposed mutable authority", e)
	}
	after, _ := os.ReadFile(old.File.Name())
	if !bytes.Equal(after, oldBytes) {
		t.Fatal("old ledger was appended or overwritten")
	}
	for _, field := range []string{"stage", "work", "parent", "attempt", "request", "docs", "source", "receipt", "binding", "text", "usage", "stop", "members"} {
		t.Run(field, func(t *testing.T) {
			bad := expected
			bad.Metadata.UnitSourceDocIDs = append([]string(nil), expected.Metadata.UnitSourceDocIDs...)
			switch field {
			case "stage":
				bad.Metadata.Stage = "description"
			case "work":
				bad.Metadata.WorkID = bridgePin("another-work")
			case "parent":
				bad.Metadata.ParentCatalogueRoot = bridgePin("another-parent")
			case "attempt":
				bad.Metadata.Attempt = 2
			case "request":
				bad.Request.User += " changed"
			case "docs":
				bad.Metadata.UnitSourceDocIDs = bad.Metadata.UnitSourceDocIDs[:1]
			case "source":
				bad.Metadata.SourceBindingsSHA256 = bridgePin("wrong")
			case "receipt":
				bad.ReceiptID = "observed:" + bridgePin("wrong")
			case "binding":
				bad.Binding.HandleID = bridgePin("wrong")
			case "text":
				bad.SavedResponse.Text = "different"
			case "usage":
				bad.SavedResponse.InputTokens++
			case "stop":
				bad.SavedResponse.FinishReason = "length"
			case "members":
				bad.Members = []string{"unit-single"}
			}
			bad.ResponseSHA256 = stagedBridgeHash(bad.SavedResponse)
			if _, e := second.b.AuthenticateObserved(context.Background(), bad); e == nil {
				t.Fatal("unauthenticated saved result accepted")
			}
		})
	}
}

func TestStagedBridgeWholeHistoryStopsOrphansUnknownDuplicatesAndIncompleteTails(t *testing.T) {
	t.Parallel()
	first := newBridgeFixture(t)
	p := first.prepare(t, first.call)
	if _, e := p.Dispatch(context.Background(), p.Binding()); e != nil {
		t.Fatal(e)
	}
	rows := first.rows(t)
	for _, kind := range []string{"intent-only", "admitted-no-response", "no-logical-end", "orphan-response", "unknown-outcome", "duplicate-work", "partial-json", "wrong-root", "wrong-source", "wrong-count"} {
		t.Run(kind, func(t *testing.T) {
			raw, _ := json.Marshal(rows)
			var copyRows []map[string]any
			_ = json.Unmarshal(raw, &copyRows)
			switch kind {
			case "intent-only":
				copyRows = copyRows[:1]
			case "admitted-no-response":
				copyRows = copyRows[:2]
			case "no-logical-end":
				copyRows = copyRows[:3]
			case "orphan-response":
				copyRows = copyRows[2:]
			case "unknown-outcome":
				copyRows[2]["finish_reason"] = "length"
			case "wrong-root":
				copyRows[0]["history_root_sha256"] = bridgePin("wrong")
			case "wrong-source":
				copyRows[1]["source_bindings_sha256"] = bridgePin("wrong")
			case "wrong-count":
				copyRows[1]["token_count"].(map[string]any)["input_tokens"] = 11
			case "duplicate-work":
				for _, r := range rows {
					b, _ := json.Marshal(r)
					var c map[string]any
					_ = json.Unmarshal(b, &c)
					c["call_id"] = 2
					copyRows = append(copyRows, c)
				}
			}
			var buf bytes.Buffer
			for _, r := range copyRows {
				b, _ := json.Marshal(r)
				buf.Write(b)
				buf.WriteByte('\n')
			}
			if kind == "partial-json" {
				buf.WriteString(`{"event":`)
			}
			name := filepath.Join(t.TempDir(), "history.jsonl")
			if e := os.WriteFile(name, buf.Bytes(), 0600); e != nil {
				t.Fatal(e)
			}
			reader, e := os.Open(name)
			if e != nil {
				t.Fatal(e)
			}
			defer reader.Close()
			history := stagedBridgeLedgerSegment{ID: first.config.LedgerSegmentID, File: reader, Bytes: int64(buf.Len()), SHA256: sha(buf.Bytes())}
			f := bridgeFixtureConfig(t, "new-active", []stagedBridgeLedgerSegment{history})
			f.b, e = newStagedWireBridge(f.config)
			if e != nil {
				t.Fatal("diagnostic bridge unavailable", e)
			}
			if _, e := f.b.WholeInventory(context.Background()); e == nil {
				t.Fatal("incomplete/ambiguous full history accepted")
			}
			if _, e := f.b.Prepare(context.Background(), f.call); e == nil || f.sends.Load() != 0 || len(f.rows(t)) != 0 {
				t.Fatal("uncertain history allowed new attempt")
			}
		})
	}
}

func TestStagedBridgeHistoricalPinsAndFreshActiveSegmentCannotBeReused(t *testing.T) {
	t.Parallel()
	first := newBridgeFixture(t)
	p := first.prepare(t, first.call)
	if _, e := p.Dispatch(context.Background(), p.Binding()); e != nil {
		t.Fatal(e)
	}
	history := bridgeHistory(t, first)
	for _, kind := range []string{"reused-id", "wrong-history-sha", "wrong-bytes", "wrong-file-sha", "nonempty-active", "duplicate-history-id", "active-alias"} {
		t.Run(kind, func(t *testing.T) {
			f := bridgeFixtureConfig(t, "fresh-active", []stagedBridgeLedgerSegment{history})
			switch kind {
			case "reused-id":
				f.config.LedgerSegmentID = history.ID
			case "wrong-history-sha":
				f.config.HistorySHA256 = bridgePin("wrong")
			case "wrong-bytes":
				f.config.History[0].Bytes++
				f.config.HistorySHA256 = stagedBridgeHistorySHA(f.config.History)
			case "wrong-file-sha":
				f.config.History[0].SHA256 = bridgePin("wrong")
				f.config.HistorySHA256 = stagedBridgeHistorySHA(f.config.History)
			case "nonempty-active":
				_, _ = f.file.Write([]byte("occupied"))
			case "duplicate-history-id":
				f.config.History = append(f.config.History, history)
				f.config.HistorySHA256 = stagedBridgeHistorySHA(f.config.History)
			case "active-alias":
				f.config.History[0].File = f.file
				f.config.HistorySHA256 = stagedBridgeHistorySHA(f.config.History)
			}
			b, e := newStagedWireBridge(f.config)
			if e == nil {
				if _, e = b.Prepare(context.Background(), f.call); e == nil {
					t.Fatal("unbound/reused segment allowed dispatch preparation")
				}
			}
			if f.sends.Load() != 0 {
				t.Fatal("constructor sent HTTP")
			}
		})
	}
	second := bridgeFixtureConfig(t, "another-active", []stagedBridgeLedgerSegment{history})
	var e error
	second.b, e = newStagedWireBridge(second.config)
	if e != nil {
		t.Fatal(e)
	}
	call := second.call
	call.BatchID = bridgePin("new-work")
	prepared := second.prepare(t, call)
	if e := os.WriteFile(history.File.Name(), []byte("changed after preflight\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := prepared.Dispatch(context.Background(), prepared.Binding()); e == nil || second.sends.Load() != 0 || len(second.rows(t)) != 0 {
		t.Fatal("changed historical file forwarded new wire")
	}
}

func TestStagedBridgeRejectsContradictoryIdentityOnEveryLedgerEvent(t *testing.T) {
	t.Parallel()
	f := newBridgeFixture(t)
	p := f.prepare(t, f.call)
	r, e := p.Dispatch(context.Background(), p.Binding())
	if e != nil {
		t.Fatal(e)
	}
	expect := stagedBridgeObservedExpectation{Request: f.call.Request, Metadata: p.snapshot.Metadata, Binding: r.Binding, ReceiptID: r.ReceiptID, SavedResponse: r.Response, ResponseSHA256: stagedBridgeHash(r.Response), Members: f.call.Units}
	original, e := os.ReadFile(f.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	for _, event := range []int{0, 1, 2, 3} {
		for _, field := range []string{"phase", "document_ids", "document_id", "method_sha256", "code_source_commit", "corpus_sha256", "roster_sha256", "accepted_set_sha256", "input_sha256", "handle_id", "identity-case", "binding-case", "counter-case", "parent-proof", "requested_model", "max_tokens", "thinking", "response_format"} {
			t.Run(fmt.Sprintf("event%d/%s", event, field), func(t *testing.T) {
				lines := bytes.Split(bytes.TrimSuffix(original, []byte{'\n'}), []byte{'\n'})
				var row map[string]any
				_ = json.Unmarshal(lines[event], &row)
				switch field {
				case "parent-proof":
					row["parent_proof_sha256"] = nil
				case "phase":
					row[field] = "relation"
				case "document_ids":
					row[field] = []string{f.config.Documents[0].DocumentID}
				case "document_id":
					row[field] = f.config.Documents[0].DocumentID
				case "identity-case":
					nested := row["staged_identity"].(map[string]any)
					nested["methodsha256"] = nested["MethodSHA256"]
					delete(nested, "MethodSHA256")
				case "binding-case":
					nested := row["wire_binding"].(map[string]any)
					nested["inputtokens"] = nested["InputTokens"]
					delete(nested, "InputTokens")
				case "counter-case":
					nested := row["counter_configuration"].(map[string]any)
					for key, value := range nested {
						nested[strings.ToUpper(key)] = value
						delete(nested, key)
						break
					}
				default:
					row[field] = bridgePin("changed-alias")
				}
				lines[event], _ = json.Marshal(row)
				modified := append(bytes.Join(lines, []byte{'\n'}), '\n')
				if e := os.WriteFile(f.file.Name(), modified, 0600); e != nil {
					t.Fatal(e)
				}
				if _, e := f.b.WholeInventory(context.Background()); e == nil {
					t.Fatal("contradictory recorded identity accepted")
				}
				if _, e := f.b.AuthenticateObserved(context.Background(), expect); e == nil {
					t.Fatal("unchanged response receipt authenticated contradictory whole ledger")
				}
			})
		}
	}
	if e := os.WriteFile(f.file.Name(), original, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.AuthenticateObserved(context.Background(), expect); e != nil {
		t.Fatal("restored authentic baseline rejected", e)
	}
}

func TestStagedBridgeDoesNotReturnSuccessWhenTransportMutatesRecordedAttribution(t *testing.T) {
	t.Parallel()
	f := newBridgeFixture(t)
	f.run = func(r *http.Request) (*http.Response, error) {
		r.Context().Value(callKey{}).(*callInfo).Phase = "relation"
		return bridgeProvider("private output", 10, 20), nil
	}
	p := f.prepare(t, f.call)
	if _, e := p.Dispatch(context.Background(), p.Binding()); e == nil || f.sends.Load() != 1 {
		t.Fatal("contradictory completed event chain returned as success")
	}
	if _, e := f.b.Prepare(context.Background(), f.call); e == nil {
		t.Fatal("contradictory actual observation did not stop future attempts")
	}
}

func TestStagedBridgeRehashedReceiptCannotHideContradictoryObservedOutcome(t *testing.T) {
	t.Parallel()
	f := newBridgeFixture(t)
	p := f.prepare(t, f.call)
	r, e := p.Dispatch(context.Background(), p.Binding())
	if e != nil {
		t.Fatal(e)
	}
	expect := stagedBridgeObservedExpectation{Request: f.call.Request, Metadata: p.snapshot.Metadata, Binding: r.Binding, ReceiptID: r.ReceiptID, SavedResponse: r.Response, ResponseSHA256: stagedBridgeHash(r.Response), Members: f.call.Units}
	original, e := os.ReadFile(f.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"local_input_tokens", "provider_minus_local_input_tokens", "total_tokens", "response_json", "transport_error", "response_read_error", "cancelled", "returned_model_sha256", "response_sha256", "response_bytes", "null-delta", "null-complete-error"} {
		t.Run(field, func(t *testing.T) {
			lines := bytes.Split(bytes.TrimSuffix(original, []byte{'\n'}), []byte{'\n'})
			event := 2
			if field == "null-complete-error" {
				event = 3
			}
			var row map[string]any
			_ = json.Unmarshal(lines[event], &row)
			switch field {
			case "response_json":
				row[field] = false
			case "transport_error", "response_read_error", "cancelled":
				row[field] = true
			case "returned_model_sha256", "response_sha256":
				row[field] = "invalid"
			case "response_bytes":
				row[field] = 0
			case "null-delta":
				row["provider_minus_local_input_tokens"] = nil
			case "null-complete-error":
				row["complete_error"] = nil
			default:
				row[field] = 999
			}
			lines[event], _ = json.Marshal(row)
			modified := append(bytes.Join(lines, []byte{'\n'}), '\n')
			if e := os.WriteFile(f.file.Name(), modified, 0600); e != nil {
				t.Fatal(e)
			}
			bad := expect
			bad.ReceiptID = "observed:" + sha(lines[2])
			if _, e := f.b.WholeInventory(context.Background()); e == nil {
				t.Fatal("contradictory outcome became COMPLETE")
			}
			if _, e := f.b.AuthenticateObserved(context.Background(), bad); e == nil {
				t.Fatal("rehashing receipt hid contradictory outcome")
			}
		})
	}
	if e := os.WriteFile(f.file.Name(), original, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.AuthenticateObserved(context.Background(), expect); e != nil {
		t.Fatal("authentic baseline lost", e)
	}
}

func TestStagedBridgeUniqueWireAndHandleIdentityWithinWholeHistory(t *testing.T) {
	t.Parallel()
	f := newBridgeFixture(t)
	first := f.prepare(t, f.call)
	if _, e := first.Dispatch(context.Background(), first.Binding()); e != nil {
		t.Fatal(e)
	}
	other := f.call
	other.BatchID = bridgePin("different-work")
	second := f.prepare(t, other)
	if _, e := second.Dispatch(context.Background(), second.Binding()); e != nil {
		t.Fatal(e)
	}
	original, e := os.ReadFile(f.file.Name())
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"duplicate-wire", "duplicate-handle"} {
		t.Run(kind, func(t *testing.T) {
			lines := bytes.Split(bytes.TrimSuffix(original, []byte{'\n'}), []byte{'\n'})
			for i := 4; i < len(lines); i++ {
				var row map[string]any
				_ = json.Unmarshal(lines[i], &row)
				if kind == "duplicate-wire" && (i == 5 || i == 6) {
					row["http_attempt"] = 1
				}
				if kind == "duplicate-handle" {
					row["handle_id"] = first.Binding().HandleID
					row["wire_binding"].(map[string]any)["HandleID"] = first.Binding().HandleID
				}
				lines[i], _ = json.Marshal(row)
			}
			if e := os.WriteFile(f.file.Name(), append(bytes.Join(lines, []byte{'\n'}), '\n'), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := f.b.WholeInventory(context.Background()); e == nil {
				t.Fatal("two logical calls share one wire/handle identity")
			}
		})
	}
	if e := os.WriteFile(f.file.Name(), original, 0600); e != nil {
		t.Fatal(e)
	}
	if inventory, e := f.b.WholeInventory(context.Background()); e != nil || len(inventory.Records) != 2 {
		t.Fatal("distinct actual identities rejected", e)
	}
}

// staged bridge has concurrency=1; queued dispatch/inventory wait cancellably.
// Old OpenAI client/default concurrency is unchanged. An in-flight call is never a crash tail.
func TestStagedBridgeSerialEntryWaitsForInFlightCallBeforeInventory(t *testing.T) {
	t.Parallel()
	f := newBridgeFixture(t)
	first := f.prepare(t, f.call)
	call2 := f.call
	call2.BatchID = bridgePin("second-work")
	second := f.prepare(t, call2)
	call3 := f.call
	call3.BatchID = bridgePin("cancelled-work")
	cancelled := f.prepare(t, call3)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.run = func(*http.Request) (*http.Response, error) {
		once.Do(func() { close(started); <-release })
		return bridgeProvider("private output", 10, 20), nil
	}
	firstDone := make(chan error, 1)
	go func() { _, e := first.Dispatch(context.Background(), first.Binding()); firstDone <- e }()
	<-started
	secondDone := make(chan error, 1)
	go func() { _, e := second.Dispatch(context.Background(), second.Binding()); secondDone <- e }()
	inventoryDone := make(chan error, 1)
	go func() { _, e := f.b.WholeInventory(context.Background()); inventoryDone <- e }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := cancelled.Dispatch(ctx, cancelled.Binding()); !errors.Is(e, context.Canceled) {
		t.Fatal("queued cancellation ignored", e)
	}
	select {
	case e := <-secondDone:
		t.Fatal("second dispatch did not wait", e)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case e := <-inventoryDone:
		t.Fatal("public inventory scanned legal in-flight call", e)
	default:
	}
	if f.sends.Load() != 1 || len(f.rows(t)) != 2 {
		t.Fatal("queued operation emitted events or a wire")
	}
	close(release)
	if e := <-firstDone; e != nil {
		t.Fatal(e)
	}
	if e := <-secondDone; e != nil {
		t.Fatal(e)
	}
	if e := <-inventoryDone; e != nil {
		t.Fatal(e)
	}
	inv, e := f.b.WholeInventory(context.Background())
	if e != nil || len(inv.Records) != 2 || f.sends.Load() != 2 {
		t.Fatal("serialized calls lost complete outcomes", e)
	}
}
