package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
)

// Only fake transports and a deliberately fake constant counter run in these tests.
// The real CLI independently pins the known original/diagnostic/source helper before entry.
type indexedFixtureCounter struct{ calls atomic.Int64 }

func (*indexedFixtureCounter) seededIdentity() seededCounterIdentity {
	return knownIndexedPlan().Counter
}
func (c *indexedFixtureCounter) Count(context.Context, []byte) (tokenCount, error) {
	c.calls.Add(1)
	i := c.seededIdentity()
	return tokenCount{Input: 100, Reserved: 65536, Context: 1000000, CounterID: i.CounterID, RuntimeSHA: i.RuntimeSHA}, nil
}

func indexedFixture(t *testing.T) (indexedInputs, string) {
	t.Helper()
	source := "Alpha is a source-only fixture.\n\n# Caption"
	spec := knownIndexedPlan()
	spec.OriginalSHA = sha([]byte(source))
	spec.OriginalBytes = len(source)
	helper, err := annotation.BuildSourceIndexedPlan(annotation.SourceIndexedInput{Skill: spec.Skill, Path: spec.Path, Original: source, ExpectedOriginalSHA256: spec.OriginalSHA})
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := []byte("opaque fixture diagnostic artifact; never a model input")
	spec.DiagnosticsSHA = sha(diagnostics)
	in := indexedInputs{spec: spec, original: source, diagnostics: diagnostics, helper: helper}
	response := map[string]any{"items": []any{map[string]any{"id": "i1", "tag": "entity", "name": "Alpha", "content": "Alpha fixture", "detail": "Alpha is a source-only fixture.", "confidence": 0.9, "evidence": []any{map[string]any{"paragraph": "p1", "quote": "Alpha"}}, "identity": map[string]any{"mode": "literal", "paragraph": "p1", "quote": "Alpha"}}}, "coverage": []any{map[string]any{"paragraph": "p1", "disposition": "covered", "items": []string{"i1"}, "reason": ""}, map[string]any{"paragraph": "p2", "disposition": "no_annotation", "items": []string{}, "reason": "Synthetic test heading; not a completeness claim"}}}
	b, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return in, string(b)
}

func indexedFakeResponse(t *testing.T, content string, input, output int) *http.Response {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": "fixture-response", "model": "deepseek-flash", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": content}}}, "usage": map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}
}

func indexedFixtureAttempt(t *testing.T, generation, verdict string, input, output int) (string, int, error) {
	t.Helper()
	t.Setenv("CAIRN_LLM_API_KEY", "synthetic-test-key")
	in, _ := indexedFixture(t)
	counter := &indexedFixtureCounter{}
	out := filepath.Join(t.TempDir(), "attempt")
	if err := indexedPreflight(context.Background(), in, out, "fixture-plan", counter); err != nil {
		t.Fatal(err)
	}
	calls := 0
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	http.DefaultTransport = seededRenderTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "opaque fixture diagnostic") || strings.Contains(string(raw), "synthetic-test-key") {
			t.Fatal("private diagnostics/auth value in model body")
		}
		if calls == 1 {
			return indexedFakeResponse(t, generation, input, output), nil
		}
		if calls == 2 {
			return indexedFakeResponse(t, verdict, 100, 5), nil
		}
		t.Fatal("third paid wire")
		return nil, fmt.Errorf("unexpected wire")
	})
	err := indexedRun(context.Background(), in, out, "fixture-plan", counter)
	return out, calls, err
}

func indexedReadAudit(t *testing.T, out string) map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(out, "indexed-replay-audit-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("audit files%v err%v", files, err)
	}
	var v map[string]any
	raw, err := os.ReadFile(files[0])
	if err != nil || json.Unmarshal(raw, &v) != nil {
		t.Fatal("audit unavailable")
	}
	return v
}

func TestIndexedDriverTwoWiresProvisionalOnly(t *testing.T) {
	_, generation := indexedFixture(t)
	out, calls, err := indexedFixtureAttempt(t, generation, `{"idempotent":true}`, 100, 20)
	if err != nil || calls != 2 {
		t.Fatalf("calls%d err%v", calls, err)
	}
	audit := indexedReadAudit(t, out)
	if audit["integrity_pass"] != true || audit["provisional"] != true || audit["accepted"] != false {
		t.Fatalf("audit%v", audit)
	}
	for _, phase := range []string{"generation", "validation"} {
		for _, suffix := range []string{"-request.private.json", "-wire.private.json", "-http-response.private.json", "-response.private.json"} {
			if _, err := os.Stat(filepath.Join(out, phase+suffix)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range []string{"accepted-checkpoint.private.json", "accepted-checkpoint.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatal("machine pass became accepted checkpoint")
		}
	}
}

func TestIndexedDriverTypedNegativeIsHealthyStop(t *testing.T) {
	_, generation := indexedFixture(t)
	out, calls, err := indexedFixtureAttempt(t, generation, `{"idempotent":false,"reason":"fixture omission"}`, 100, 20)
	if err == nil || calls != 2 {
		t.Fatalf("calls%d err%v", calls, err)
	}
	audit := indexedReadAudit(t, out)
	if audit["integrity_pass"] != true || audit["provisional"] != false || audit["accepted"] != false || audit["gate_b"] != "false" {
		t.Fatalf("audit%v", audit)
	}
	if _, err := os.Stat(filepath.Join(out, "provisional-candidate.private.json")); !os.IsNotExist(err) {
		t.Fatal("negative candidate promoted")
	}
}

func TestIndexedDriverStopsBeforeGateBOnStrictOrUsageFailure(t *testing.T) {
	_, valid := indexedFixture(t)
	for _, tc := range []struct {
		name, content string
		input, output int
		integrity     bool
	}{
		{"strict-empty", `{"items":[],"coverage":[]}`, 100, 20, true},
		{"bad-output", valid, 100, 0, false},
		{"oversized-output", valid, 100, 65537, false},
		{"bad-input", valid, 99, 20, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, calls, err := indexedFixtureAttempt(t, tc.content, `{"idempotent":true}`, tc.input, tc.output)
			if err == nil || calls != 1 {
				t.Fatalf("calls%d err%v", calls, err)
			}
			audit := indexedReadAudit(t, out)
			if audit["integrity_pass"] != tc.integrity || audit["provisional"] != false {
				t.Fatalf("audit%v", audit)
			}
			if _, err := os.Stat(filepath.Join(out, "validation-wire.private.json")); !os.IsNotExist(err) {
				t.Fatal("spent or prepared second stage")
			}
		})
	}
}

func TestIndexedDriverKnownManifestAndInputFence(t *testing.T) {
	t.Parallel()
	p := knownIndexedPlan()
	if p.MaxWires != 2 || p.PerCallWires != 1 || p.SourceRole != "annotation_input_original" {
		t.Fatal("profile weakened")
	}
	if _, err := indexedBuildInputs(p, "not the independently pinned original", nil); err == nil {
		t.Fatal("untrusted caller original accepted")
	}
	dir := t.TempDir()
	raw, _ := json.Marshal(p)
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", sha([]byte("other"))} {
		if _, err := loadIndexedInputs("unread", "unread", path, bad); err == nil {
			t.Fatal("manifest pin bypass")
		}
	}
}

// Regression probes preserved from independent review of27123/2985; no provider.
func TestIndexedDriverRegressionAuditRejectsContradictoryObservedLedger(t *testing.T) {
	in, generation := indexedFixture(t)
	out, calls, err := indexedFixtureAttempt(t, generation, `{"idempotent":true}`, 100, 20)
	if err != nil || calls != 2 {
		t.Fatal("baseline", calls, err)
	}
	original, err := os.ReadFile(filepath.Join(out, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"response_json", "transport_error", "response_read_error", "total_tokens", "null-complete-error", "requested_model", "max_tokens", "response-docs"} {
		t.Run(field, func(t *testing.T) {
			lines := bytes.Split(bytes.TrimSuffix(original, []byte{'\n'}), []byte{'\n'})
			pos := 2
			if field == "null-complete-error" {
				pos = 3
			}
			if field == "requested_model" || field == "max_tokens" {
				pos = 1
			}
			var row map[string]any
			_ = json.Unmarshal(lines[pos], &row)
			switch field {
			case "response_json":
				row[field] = false
			case "transport_error", "response_read_error":
				row[field] = true
			case "total_tokens":
				row[field] = 999
			case "null-complete-error":
				row["complete_error"] = nil
			case "requested_model":
				row[field] = "other-model"
			case "max_tokens":
				row[field] = 17
			case "response-docs":
				row["document_ids"] = []string{"wrong-document"}
			}
			lines[pos], _ = json.Marshal(row)
			if e := os.WriteFile(filepath.Join(out, "requests.jsonl"), append(bytes.Join(lines, []byte{'\n'}), '\n'), 0600); e != nil {
				t.Fatal(e)
			}
			if e := indexedAudit(out, in, "fixture-plan", &indexedFixtureCounter{}); e == nil {
				t.Fatal("contradictory ledger accepted as replay-verified provisional")
			}
		})
	}
}

func TestIndexedDriverRegressionPostForwardIdentityDriftStopsBeforeGateB(t *testing.T) {
	for _, field := range []string{"stage", "call", "document", "documents", "documents-inplace", "source", "phase"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("CAIRN_LLM_API_KEY", "synthetic-review-key")
			in, generation := indexedFixture(t)
			counter := &indexedFixtureCounter{}
			out := filepath.Join(t.TempDir(), "attempt")
			if e := indexedPreflight(context.Background(), in, out, "fixture-plan", counter); e != nil {
				t.Fatal(e)
			}
			calls := 0
			old := http.DefaultTransport
			defer func() { http.DefaultTransport = old }()
			http.DefaultTransport = seededRenderTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				_, _ = io.ReadAll(r.Body)
				if calls == 1 {
					info := r.Context().Value(callKey{}).(*callInfo)
					switch field {
					case "stage":
						info.Stage = "semantic_gate"
					case "call":
						info.ID = 99
					case "document":
						info.Document = "wrong-document"
					case "documents":
						info.Documents = []string{"wrong-document"}
					case "documents-inplace":
						info.Documents[0] = "wrong-document"
					case "source":
						info.SourceSHA = sha([]byte("wrong"))
					case "phase":
						info.Phase = "validation"
					}
					return indexedFakeResponse(t, generation, 100, 20), nil
				}
				return indexedFakeResponse(t, `{"idempotent":true}`, 100, 5), nil
			})
			err := indexedRun(context.Background(), in, out, "fixture-plan", counter)
			if err == nil || calls != 1 {
				t.Fatal("identity drift paid GateB or returned success", field, calls, err)
			}
			if _, err := os.Stat(filepath.Join(out, "generation-http-response.private.json")); err != nil {
				t.Fatal("actual first response lost on identity drift", err)
			}
		})
	}
}

func TestIndexedDriverRegressionExactEvidenceAliasesAndNulls(t *testing.T) {
	in, generation := indexedFixture(t)
	out, calls, err := indexedFixtureAttempt(t, generation, `{"idempotent":true}`, 100, 20)
	if err != nil || calls != 2 {
		t.Fatal("baseline", calls, err)
	}
	ledgerPath := filepath.Join(out, "requests.jsonl")
	original, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	savedPath := filepath.Join(out, "generation-response.private.json")
	savedOriginal, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"cancelled", "null-error-flag", "response-json-alias", "mode-alias", "model-alias", "max-alias", "count-alias", "saved-error-null", "optional-error-alias", "optional-total-alias"} {
		t.Run(kind, func(t *testing.T) {
			if err := os.WriteFile(ledgerPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(savedPath, savedOriginal, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "saved-error-null" {
				var saved map[string]any
				_ = json.Unmarshal(savedOriginal, &saved)
				saved["complete_error"] = nil
				b, _ := json.Marshal(saved)
				if err := os.WriteFile(savedPath, b, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				lines := bytes.Split(bytes.TrimSuffix(original, []byte{'\n'}), []byte{'\n'})
				pos := 2
				if kind == "model-alias" || kind == "max-alias" || kind == "count-alias" {
					pos = 1
				}
				var row map[string]any
				_ = json.Unmarshal(lines[pos], &row)
				switch kind {
				case "cancelled":
					row["cancelled"] = true
				case "null-error-flag":
					row["transport_error"] = nil
				case "response-json-alias":
					row["Response_Json"] = false
				case "mode-alias":
					row["OPERATION_MODE"] = row["operation_mode"]
					delete(row, "operation_mode")
				case "model-alias":
					row["Requested_Model"] = "other"
				case "max-alias":
					row["MAX_TOKENS"] = 17
				case "count-alias":
					row["token_count"].(map[string]any)["Input_Tokens"] = 101
				case "optional-error-alias":
					row["Response_Read_Error"] = true
				case "optional-total-alias":
					row["Total_Tokens"] = 999
				}
				lines[pos], _ = json.Marshal(row)
				if err := os.WriteFile(ledgerPath, append(bytes.Join(lines, []byte{'\n'}), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := indexedAudit(out, in, "fixture-plan", &indexedFixtureCounter{}); err == nil {
				t.Fatal("contradictory exact evidence accepted", kind)
			}
		})
	}
}

// Regression probes preserved from independent review of27123/2985; no provider.
func TestIndexedDriverRegressionPreparedBindingCannotCoherentlyReplaceFrozenFirstWire(t *testing.T) {
	for _, kind := range []string{"wire", "count"} {
		t.Run(kind, func(t *testing.T) {
			in, _ := indexedFixture(t)
			counter := &indexedFixtureCounter{}
			out := filepath.Join(t.TempDir(), "attempt")
			if e := indexedPreflight(context.Background(), in, out, "fixture-plan", counter); e != nil {
				t.Fatal(e)
			}
			pre, _, e := readIndexedPreflight(out, in, "fixture-plan")
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy := &indexedWirePolicy{in: &in, pre: pre, counter: counter, out: out, cancel: cancel}
			request, _ := in.helper.Request()
			binding, e := policy.prepare(ctx, "generation", request)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "wire" {
				request.System += " replaced after preparation"
				binding.request = request
				binding.wire, e = indexedRender(request)
				if e != nil {
					t.Fatal(e)
				}
			} else {
				binding.count.Input++
			}
			info := &callInfo{ID: 1}
			if e := binding.bind(request, info); e != nil {
				return
			}
			info.Attempts.Store(1)
			req, e := http.NewRequestWithContext(context.WithValue(ctx, seededBindingKey{}, binding), "POST", indexedClientConfig().Endpoint, bytes.NewReader(binding.wire))
			if e != nil {
				t.Fatal(e)
			}
			req.Header.Set("Content-Type", "application/json")
			var parsed wireRequest
			if json.Unmarshal(binding.wire, &parsed) != nil {
				t.Fatal("badfixture")
			}
			if e := policy.admitRequest(req, info, parsed, binding.wire, binding.count); e == nil {
				t.Fatal("coherently changed prepared capability admitted despite frozen preflight", kind)
			}
		})
	}
}

// Original last-wins evidence proofs retained from independent review.
func TestIndexedDriverRegressionDuplicateRequiredErrorFieldsAreNotLastWinsEvidence(t *testing.T) {
	in, generation := indexedFixture(t)
	out, calls, e := indexedFixtureAttempt(t, generation, `{"idempotent":true}`, 100, 20)
	if e != nil || calls != 2 {
		t.Fatal("baseline", calls, e)
	}
	for _, entry := range []struct{ name, key string }{{"indexed-run-finish.json", "execution_error"}, {"generation-response.private.json", "complete_error"}} {
		t.Run(entry.name, func(t *testing.T) {
			name := filepath.Join(out, entry.name)
			raw, e := os.ReadFile(name)
			if e != nil {
				t.Fatal(e)
			}
			modified := append([]byte("{\""+entry.key+"\":true,"), raw[1:]...)
			if e := os.WriteFile(name, modified, 0600); e != nil {
				t.Fatal(e)
			}
			e = indexedAudit(out, in, "fixture-plan", &indexedFixtureCounter{})
			if restore := os.WriteFile(name, raw, 0600); restore != nil {
				t.Fatal(restore)
			}
			if e == nil {
				t.Fatal("duplicate explicit true then false accepted as known complete evidence", entry.key)
			}
		})
	}
}
