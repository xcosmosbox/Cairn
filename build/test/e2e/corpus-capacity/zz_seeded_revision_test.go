package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

func seedFixture() seededInputs {
	original := "Original authoritative text: Widget exists.\n"
	return seededInputs{Original: original, SourceSHA: sha([]byte(original)), Document: "fixed-source", Skill: "fixture", Path: "references/fixed.md", Candidate: &dktypes.AnnotatedDocument{Skill: "fixture", FilePath: "references/fixed.md", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagEntity, Name: "Widget", Content: "Widget exists.", Confidence: 1}}}, ArchivedRejectionSHA: "provenance-not-typed-feedback"}
}

const seededRevisedJSON = `{"items":[{"tag":"entity","name":"Widget","content":"Widget exists.","detail":"Original authoritative Widget exists.","confidence":1}]}`

type seedScriptClient struct {
	replies  []string
	phases   []string
	requests []llm.CompleteRequest
}

func (*seedScriptClient) ProviderName() string { return "synthetic-offline" }
func (c *seedScriptClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	b, ok := ctx.Value(seededBindingKey{}).(seededCallBinding)
	if !ok {
		return nil, fmt.Errorf("missing explicit binding")
	}
	var info callInfo
	if err := b.bind(req, &info); err != nil {
		return nil, err
	}
	c.phases = append(c.phases, b.Phase)
	c.requests = append(c.requests, req)
	if len(c.phases) > len(c.replies) {
		return nil, fmt.Errorf("unexpected fourth call")
	}
	return &llm.CompleteResponse{Text: c.replies[len(c.phases)-1], InputTokens: 100, OutputTokens: 2, FinishReason: "stop"}, nil
}

// 历史 wrapped reason 不是新的 explicit-false 证据；只有当前资格门的类型化意见可修订。
// Old wrapped prose must never become typed feedback; qualify the fixed seed first.
func TestSeededQualificationControlsAllLaterCalls(t *testing.T) {
	tests := []struct {
		name    string
		replies []string
		want    string
		calls   int
		failed  bool
	}{
		{"stochastic pass", []string{`{"idempotent":true}`}, "QUALIFICATION_PASS_no_revision_effect", 1, false},
		{"missing explicit false", []string{`{"reason":"historical-looking prose"}`}, "FAIL_qualification_untyped_or_execution", 1, true},
		{"empty critique", []string{`{"idempotent":false,"reason":" "}`}, "FAIL_qualification_untyped_or_execution", 1, true},
		{"malformed qualification", []string{`broken`}, "FAIL_qualification_untyped_or_execution", 1, true},
		{"qualified replacement", []string{`{"idempotent":false,"reason":"Current specific fallible omission"}`, seededRevisedJSON, `{"idempotent":true}`}, "SEEDED_REVISION_GATE_PASS_not_capacity_or_causal_effect", 3, false},
		{"empty replacement", []string{`{"idempotent":false,"reason":"Current specific omission"}`, `{"items":[]}`}, "FAIL_nonempty_format_gate", 2, true},
		{"final rejection", []string{`{"idempotent":false,"reason":"Current specific omission"}`, seededRevisedJSON, `{"idempotent":false,"reason":"Still incomplete"}`}, "FAIL_final_gate", 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &seedScriptClient{replies: tt.replies}
			e := &seededExecutor{in: seedFixture(), next: c}
			status, err := e.execute(context.Background())
			if status != tt.want || (err != nil) != tt.failed || len(c.phases) != tt.calls {
				t.Fatalf("status=%s err=%v phases=%v", status, err, c.phases)
			}
			if len(c.phases) > 1 {
				var input struct {
					Original string `json:"original"`
					Critique string `json:"critique"`
				}
				if json.Unmarshal([]byte(c.requests[1].User), &input) != nil || input.Original != e.in.Original || input.Critique != "Current specific fallible omission" && tt.name == "qualified replacement" {
					t.Fatal("revision lost current critique or full original")
				}
				if strings.Contains(c.requests[1].User, e.in.ArchivedRejectionSHA) {
					t.Fatal("old provenance became feedback")
				}
			}
		})
	}
}

func TestSeededSourceBindingDoesNotGuessFromCritique(t *testing.T) {
	in := seedFixture()
	req, err := captureSeedRevision(in, &annotation.SemanticRejection{Reason: "Critique mentions references/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.md but cannot change ownership"})
	if err != nil {
		t.Fatal(err)
	}
	binding := seededCallBinding{Phase: "revision", Document: in.Document, SourceSHA: in.SourceSHA, Original: in.Original, SystemSHA: sha([]byte(req.System)), UserSHA: sha([]byte(req.User))}
	var info callInfo
	if err = binding.bind(req, &info); err != nil {
		t.Fatal(err)
	}
	if info.Document != in.Document || len(info.Documents) != 1 || info.Phase != "revision" {
		t.Fatal("explicit source binding lost")
	}
	binding.Original = "substituted original"
	if binding.bind(req, &callInfo{}) == nil {
		t.Fatal("mismatched source admitted")
	}
	bad := seedFixture()
	bad.Candidate.FilePath = "references/other.md"
	if bad.validate() == nil {
		t.Fatal("candidate/source mismatch admitted")
	}
}

func seedHTTPResponse(text string) *http.Response {
	raw, _ := json.Marshal(map[string]any{"model": "deepseek-flash", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": text}}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 2, "total_tokens": 102}})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}
}

// 新增驱动仍走同一真实客户端/traceTransport；429 不能利用 provider 重试多花一次 wire。
// The production client's retry loop cannot dispatch another actual seeded wire.
func TestSeededWireFailureCancelsProviderRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &memoryLog{}
	ledger := &durableLedger{file: m, cancel: cancel}
	in := seedFixture()
	sent := 0
	observer := &traceTransport{ledger: ledger, counter: fittingCounter(), seedPolicy: &seededWirePolicy{Document: in.Document, SourceSHA: in.SourceSHA, Counter: seedTestIdentity()}, next: transportFunc(func(*http.Request) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"synthetic rate limit"}`))}, nil
	})}
	previous := http.DefaultTransport
	http.DefaultTransport = observer
	defer func() { http.DefaultTransport = previous }()
	c, err := llm.NewOpenAICompatClient(llm.Config{Endpoint: "https://api.deepseek.com/chat/completions", APIKeyEnv: "CAIRN_SEED_TEST_UNUSED", Model: "deepseek-flash", MaxTokens: seededMaxTokens, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	bounded := &boundedClient{next: c, sem: make(chan struct{}, 1), ledger: ledger}
	e := &seededExecutor{in: in, next: bounded}
	status, err := e.execute(ctx)
	if err == nil || status != "FAIL_qualification_untyped_or_execution" || sent != 1 || observer.sent.Load() != 1 || ctx.Err() == nil {
		t.Fatalf("status=%s err=%v sent=%d", status, err, sent)
	}
	counts, _ := auditTraceBytes(m.Bytes())
	if counts["admitted_attempts"] != 1 || counts["attempt_outcomes"] != 1 || counts["logical_calls"] != 1 {
		t.Fatal("failed attempt denominator missing")
	}
}

func TestSeededPolicyRejectsWrongPhaseRetryAndOverrides(t *testing.T) {
	in := seedFixture()
	raw := []byte(`{"model":"deepseek-flash","max_tokens":65536,"messages":[],"thinking":{"type":"enabled"},"response_format":{"type":"json_object"}}`)
	var req wireRequest
	_ = json.Unmarshal(raw, &req)
	p := &seededWirePolicy{Document: in.Document, SourceSHA: in.SourceSHA, Counter: seedTestIdentity()}
	info := &callInfo{Mode: seededMode, Phase: "revision", Document: in.Document, Documents: []string{in.Document}, SourceSHA: in.SourceSHA}
	info.Attempts.Store(1)
	if p.admit(info, req, raw, tokenCount{Input: 100, Reserved: seededMaxTokens, Context: 1000000, CounterID: "test-counter", RuntimeSHA: "test-runtime"}) == nil {
		t.Fatal("revision admitted before qualification")
	}
	info.Phase = "qualification"
	info.Attempts.Store(2)
	if p.admit(info, req, raw, tokenCount{Input: 100, Reserved: seededMaxTokens, Context: 1000000, CounterID: "test-counter", RuntimeSHA: "test-runtime"}) == nil {
		t.Fatal("wire retry admitted")
	}
	info.Attempts.Store(1)
	override := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"temperature":0}`)...)
	if p.admit(info, req, override, tokenCount{Input: 100, Reserved: seededMaxTokens, Context: 1000000, CounterID: "test-counter", RuntimeSHA: "test-runtime"}) == nil {
		t.Fatal("sampling override admitted")
	}
	for _, phase := range []string{"qualification", "revision", "validation"} {
		info.Phase = phase
		if err := p.admit(info, req, raw, tokenCount{Input: 100, Reserved: seededMaxTokens, Context: 1000000, CounterID: "test-counter", RuntimeSHA: "test-runtime"}); err != nil {
			t.Fatal(err)
		}
	}
	if p.admit(info, req, raw, tokenCount{Input: 100, Reserved: seededMaxTokens, Context: 1000000, CounterID: "test-counter", RuntimeSHA: "test-runtime"}) == nil {
		t.Fatal("fourth wire admitted")
	}
}

func TestSeededDurableFailurePreventsAnyWire(t *testing.T) {
	in := seedFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &memoryLog{failAt: 2}
	sent := 0
	observer := &traceTransport{ledger: &durableLedger{file: m, cancel: cancel}, counter: fittingCounter(), seedPolicy: &seededWirePolicy{Document: in.Document, SourceSHA: in.SourceSHA, Counter: seedTestIdentity()}, next: transportFunc(func(*http.Request) (*http.Response, error) {
		sent++
		return seedHTTPResponse(`{"idempotent":true}`), nil
	})}
	previous := http.DefaultTransport
	http.DefaultTransport = observer
	defer func() { http.DefaultTransport = previous }()
	c, _ := llm.NewOpenAICompatClient(llm.Config{Endpoint: "https://api.deepseek.com/chat/completions", APIKeyEnv: "CAIRN_SEED_TEST_UNUSED", Model: "deepseek-flash", MaxTokens: seededMaxTokens, MaxRetries: 1})
	e := &seededExecutor{in: in, next: &boundedClient{next: c, sem: make(chan struct{}, 1), ledger: observer.ledger}}
	_, err := e.execute(ctx)
	if err == nil || sent != 0 || observer.ledger.failure() == nil {
		t.Fatal("unsynced admission sent a provider request")
	}
}

func TestSeededOfflineCaptureAndPrivateReplayAudit(t *testing.T) {
	t.Setenv("CAIRN_LLM_API_KEY", "synthetic-offline-only")
	in := seedFixture()
	out := t.TempDir()
	counter := seedTestCounter{identity: seedTestIdentity()}
	if err := seedPreflight(context.Background(), in, out, "synthetic-plan", counter); err != nil {
		t.Fatal(err)
	}
	replies := []string{`{"idempotent":false,"reason":"New current typed reason"}`, seededRevisedJSON, `{"idempotent":true}`}
	sent := 0
	previous := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) {
		if sent >= len(replies) {
			t.Fatal("extra actual wire")
		}
		text := replies[sent]
		sent++
		return seedHTTPResponse(text), nil
	})
	defer func() { http.DefaultTransport = previous }()
	if err := seededRun(context.Background(), in, out, "synthetic-plan", counter); err != nil {
		t.Fatal(err)
	}
	if sent != 3 {
		t.Fatalf("actual fake wires=%d", sent)
	}
	ledgerPath := filepath.Join(out, "requests.jsonl")
	originalLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.ReplaceAll(string(originalLedger), `"runtime_receipt_sha256":"test-runtime"`, `"runtime_receipt_sha256":"unreviewed-B"`))
	if string(changed) == string(originalLedger) {
		t.Fatal("counter tamper fixture did not change a request")
	}
	if err = os.WriteFile(ledgerPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if seedAudit(out, in) == nil {
		t.Fatal("ledger counter drift accepted against freeze")
	}
	if err = os.WriteFile(ledgerPath, originalLedger, 0600); err != nil {
		t.Fatal(err)
	}
	// A rewritten final verdict must no longer match the observed final-content hash.
	path := filepath.Join(out, "validation-response.private.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), `\"idempotent\":true`, `\"idempotent\":true,\"reason\":\"tampered\"`))
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if seedAudit(out, in) == nil {
		t.Fatal("tampered private verdict accepted")
	}
}

func TestSeededPlanRejectsAlternativeHistoricalInputs(t *testing.T) {
	dir := t.TempDir()
	plan := seededPlan{Format: "cairn-seeded-revision-plan/v1", Mode: seededMode, ArchiveCommit: seededArchive, Document: seededDocument, OriginalSHA: seededOriginalSHA, CandidateSHA: "alternative-R2", ArchivedRejectionSHA: seededRejectionSHA, MaxTokens: seededMaxTokens, RequestSeconds: 600, RunSeconds: 1800, MaxWires: 3, PerCallWires: 1}
	raw, _ := json.Marshal(plan)
	p := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSeededInputs(dir, p, sha(raw)); err == nil {
		t.Fatal("alternative seed candidate admitted")
	}
}

func seedTestIdentity() seededCounterIdentity {
	return seededCounterIdentity{PythonSHA: "test-python", AdapterSHA: "test-adapter", RuntimeSHA: "test-runtime", CounterID: "test-counter"}
}

type seedTestCounter struct{ identity seededCounterIdentity }

func (c seedTestCounter) seededIdentity() seededCounterIdentity { return c.identity }
func (c seedTestCounter) Count(ctx context.Context, raw []byte) (tokenCount, error) {
	n, e := fittingCounter().Count(ctx, raw)
	n.RuntimeSHA = c.identity.RuntimeSHA
	n.CounterID = c.identity.CounterID
	return n, e
}

// Reviewer probe on the original head accepted A-preflight/B-run with one fake wire.
// The frozen counter configuration must reject that drift before any wire now.
func TestSeededCounterFreezeAndKeyRejectBeforeDispatch(t *testing.T) {
	for _, field := range []string{"runtime", "counter", "python", "adapter", "missing-key"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("CAIRN_LLM_API_KEY", "synthetic-offline-only")
			in := seedFixture()
			out := t.TempDir()
			a := seedTestCounter{identity: seedTestIdentity()}
			if err := seedPreflight(context.Background(), in, out, "plan", a); err != nil {
				t.Fatal(err)
			}
			b := a
			switch field {
			case "runtime":
				b.identity.RuntimeSHA = "unreviewed-B"
			case "counter":
				b.identity.CounterID = "unreviewed-B"
			case "python":
				b.identity.PythonSHA = "unreviewed-B"
			case "adapter":
				b.identity.AdapterSHA = "unreviewed-B"
			case "missing-key":
				t.Setenv("CAIRN_LLM_API_KEY", "")
			}
			sent := 0
			previous := http.DefaultTransport
			http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) {
				sent++
				return seedHTTPResponse(`{"idempotent":true}`), nil
			})
			defer func() { http.DefaultTransport = previous }()
			if seededRun(context.Background(), in, out, "plan", b) == nil || sent != 0 {
				t.Fatalf("drift admitted actual wires=%d", sent)
			}
		})
	}
}

// A parser's true verdict can coexist with a bad wire/model identity. The
// provisional checkpoint must remain unusable when the complete audit fails.
func TestSeededParsedPassCannotAcceptBadWireCheckpoint(t *testing.T) {
	t.Setenv("CAIRN_LLM_API_KEY", "synthetic-offline-only")
	in := seedFixture()
	out := t.TempDir()
	counter := seedTestCounter{identity: seedTestIdentity()}
	if err := seedPreflight(context.Background(), in, out, "plan", counter); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	sent := 0
	http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"unexpected-model","choices":[{"finish_reason":"stop","message":{"content":"{\"idempotent\":true}"}}],"usage":{"prompt_tokens":100,"completion_tokens":2}}`))}, nil
	})
	defer func() { http.DefaultTransport = previous }()
	if seededRun(context.Background(), in, out, "plan", counter) == nil || sent != 1 {
		t.Fatal("bad wire parser pass accepted")
	}
	var checkpoint map[string]any
	if readJSON(filepath.Join(out, "accepted-checkpoint.json"), &checkpoint) != nil || checkpoint["acceptance_state"] != "provisional_until_seed_audit_pass" {
		t.Fatal("checkpoint hides pending audit")
	}
	if seedAudit(out, in) == nil {
		t.Fatal("bad wire provisional checkpoint reusable")
	}
}
