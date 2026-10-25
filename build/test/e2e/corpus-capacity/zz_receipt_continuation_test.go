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

	buildmeta "github.com/xcosmosbox/cairn/build/internal"
	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

func receiptJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}
func receiptPut(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// 真实生产客户端只接内存 HTTP；构造完整负父回执，而不是把旧错误字符串强转类型。
// The fixture executes the existing three-phase client offline and independently anchors its files.
func receiptParentFixture(t *testing.T) (string, receiptPlan, receiptParent) {
	t.Helper()
	t.Setenv("CAIRN_LLM_API_KEY", "synthetic-offline-only")
	in := seedFixture()
	run := t.TempDir()
	counter := seedTestCounter{identity: seedTestIdentity()}
	planRaw := []byte("fixture-parent-plan\n")
	planSHA := sha(planRaw)
	if err := seedPreflight(context.Background(), in, run, planSHA, counter); err != nil {
		t.Fatal(err)
	}
	texts := []string{`{"idempotent":false,"reason":"Parent qualification fallible reason"}`, seededRevisedJSON, `{"idempotent":false,"reason":"Parent final fallible reason referencing references/foreign.md"}`}
	sent := 0
	previous := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) {
		if sent >= len(texts) {
			t.Fatal("extra fixture wire")
		}
		response := seedHTTPResponse(texts[sent])
		sent++
		return response, nil
	})
	err := seededRun(context.Background(), in, run, planSHA, counter)
	http.DefaultTransport = previous
	if err == nil || sent != 3 {
		t.Fatalf("parent fixture err=%v wires=%d", err, sent)
	}
	root := t.TempDir()
	entries, err := os.ReadDir(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range entries {
		raw, e := os.ReadFile(filepath.Join(run, f.Name()))
		if e != nil {
			t.Fatal(e)
		}
		receiptPut(t, filepath.Join(root, "run", f.Name()), raw)
	}
	receiptPut(t, filepath.Join(root, "evidence", "plan.json"), planRaw)
	receiptPut(t, filepath.Join(root, "fixed-inputs", "original", in.Path), []byte(in.Original))
	seedRaw := receiptJSON(t, in.Candidate)
	receiptPut(t, filepath.Join(root, "fixed-inputs", "stages", "02_gate_a", in.Skill, "references_"+in.Document+"_r3.json"), seedRaw)
	// The historical opt-in runner stamps its real fixed-R3 constant. This synthetic
	// parent has a different independently anchored seed, so its fixture metadata must agree.
	receiptChangeJSON(t, filepath.Join(root, "run/seed-run-start.json"), func(v map[string]any) { v["candidate_sha256"] = sha(seedRaw) })
	read := func(n string) []byte {
		raw, e := os.ReadFile(filepath.Join(root, n))
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	var start struct {
		Binary string `json:"binary_sha256"`
		Driver string `json:"driver_source_digest"`
	}
	if json.Unmarshal(read("run/seed-run-start.json"), &start) != nil {
		t.Fatal("start")
	}
	p := knownReceiptPlan()
	p.ParentArchiveCommit = "synthetic-independently-anchored-parent"
	p.Document, p.Skill, p.Path = in.Document, in.Skill, in.Path
	p.OriginalSHA, p.OriginalBytes, p.SeedItems, p.ParentItems = in.SourceSHA, len(in.Original), len(in.Candidate.Items), 1
	p.SeedCandidateSHA, p.CandidateSHA, p.FinalResponseSHA = sha(seedRaw), sha(read("run/format-gate.private.json")), sha(read("run/validation-response.private.json"))
	p.FeedbackSHA = sha([]byte("Parent final fallible reason referencing references/foreign.md"))
	p.ParentStartSHA, p.ParentFinishSHA, p.ParentLedgerSHA = sha(read("run/seed-run-start.json")), sha(read("run/seed-run-finish.json")), sha(read("run/requests.jsonl"))
	p.ParentBinarySHA, p.ParentDriverDigest, p.ProductionDigest = start.Binary, start.Driver, buildmeta.SourceDigest("annotation/", "llm/")
	p.ParentPlanSHA, p.ParentPreflightSHA, p.Counter = planSHA, sha(read("run/seed-preflight.json")), counter.identity
	receiptReanchor(t, root, &p)
	parent, err := verifyReceiptParent(root, p)
	if err != nil {
		t.Fatal(err)
	}
	return root, p, parent
}

func receiptReanchor(t *testing.T, root string, p *receiptPlan) {
	t.Helper()
	var files []map[string]any
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "manifest.json" {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		files = append(files, map[string]any{"path": rel, "bytes": len(raw), "sha256": sha(raw)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := receiptJSON(t, map[string]any{"files": files})
	receiptPut(t, filepath.Join(root, "manifest.json"), raw)
	p.ParentManifestSHA = sha(raw)
}

func receiptChangeJSON(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("decode fixture")
	}
	mutate(v)
	receiptPut(t, path, receiptJSON(t, v))
}

func receiptChangeLedger(t *testing.T, root string, p *receiptPlan, mutate func([]map[string]any) []map[string]any) {
	t.Helper()
	path := filepath.Join(root, "run", "requests.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			t.Fatal("ledger")
		}
		rows = append(rows, row)
	}
	var result []byte
	for _, row := range mutate(rows) {
		one, _ := json.Marshal(row)
		result = append(result, append(one, '\n')...)
	}
	receiptPut(t, path, result)
	p.ParentLedgerSHA = sha(result)
}

func TestReceiptParentRejectsMissingOrChangedMembersAndSelfAssertedPlan(t *testing.T) {
	root, pins, _ := receiptParentFixture(t)
	for _, name := range []string{"run/requests.jsonl", "run/seed-run-start.json", "run/seed-run-finish.json", "run/qualification-request-capture.json", "run/qualification-response.private.json", "run/qualification-typed-rejection.private.json", "run/revision-request-capture.json", "run/revision-response.private.json", "run/revision.private.json", "run/format-gate.private.json", "run/validation-request-capture.json", "run/validation-response.private.json", "run/seed-preflight.json", "evidence/plan.json", "fixed-inputs/original/references/fixed.md"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err = verifyReceiptParent(root, pins); err == nil {
				t.Fatal("missing parent member admitted")
			}
			receiptPut(t, path, append(append([]byte{}, original...), ' '))
			if _, err = verifyReceiptParent(root, pins); err == nil {
				t.Fatal("changed parent member admitted")
			}
			receiptPut(t, path, original)
		})
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	raw := receiptJSON(t, pins)
	receiptPut(t, planPath, raw)
	if _, err := loadReceiptParent(root, planPath, sha(raw)); err == nil {
		t.Fatal("self-consistent foreign manifest bypassed independent known-parent pins")
	}
	wrong := pins
	wrong.ParentManifestSHA = "rewritten-anchor"
	if _, err := verifyReceiptParent(root, wrong); err == nil {
		t.Fatal("manifest pin ignored")
	}
	path := filepath.Join(root, "run", "qualification-response.private.json")
	original, _ := os.ReadFile(path)
	outside := filepath.Join(t.TempDir(), "outside")
	receiptPut(t, outside, original)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyReceiptParent(root, pins); err == nil {
		t.Fatal("parent symlink accepted")
	}
}

// 即使测试重签外层散列，也必须由实际生产解析/顺序/usage 校验拒绝坏父证据。
// Reanchoring synthetic fixtures exercises semantic integrity beyond a hash-only check.
func TestReceiptParentRejectsReanchoredBadTraceAndOutcomes(t *testing.T) {
	for _, field := range []string{"phase", "stage", "source", "mode", "document", "retry", "runtime", "delta", "usage", "model", "missing-outcome", "capture", "passing-parent", "manual-candidate", "missing-flag", "null-flag", "blank-reason", "true-verdict", "saved-usage"} {
		t.Run(field, func(t *testing.T) {
			root, p, _ := receiptParentFixture(t)
			if field == "passing-parent" {
				receiptChangeJSON(t, filepath.Join(root, "run/seed-run-finish.json"), func(v map[string]any) { v["status"] = "SEEDED_REVISION_GATE_PASS_not_capacity_or_causal_effect" })
				raw, _ := os.ReadFile(filepath.Join(root, "run/seed-run-finish.json"))
				p.ParentFinishSHA = sha(raw)
			} else if field == "manual-candidate" {
				receiptChangeJSON(t, filepath.Join(root, "run/format-gate.private.json"), func(v map[string]any) { v["items"].([]any)[0].(map[string]any)["name"] = "Manually substituted" })
				raw, _ := os.ReadFile(filepath.Join(root, "run/format-gate.private.json"))
				p.CandidateSHA = sha(raw)
			} else if field == "capture" {
				receiptChangeJSON(t, filepath.Join(root, "run/revision-request-capture.json"), func(v map[string]any) { v["request_sha256"] = "self-consistent-but-not-production" })
				receiptChangeLedger(t, root, &p, func(rows []map[string]any) []map[string]any {
					for _, x := range rows {
						if x["event"] == "request" && x["phase"] == "revision" {
							x["request_sha256"] = "self-consistent-but-not-production"
						}
					}
					return rows
				})
			} else if field == "missing-flag" || field == "null-flag" || field == "blank-reason" || field == "true-verdict" {
				text := map[string]string{"missing-flag": `{"reason":"Parent final fallible reason referencing references/foreign.md"}`, "null-flag": `{"idempotent":null,"reason":"Parent final fallible reason referencing references/foreign.md"}`, "blank-reason": `{"idempotent":false,"reason":" "}`, "true-verdict": `{"idempotent":true}`}[field]
				receiptChangeJSON(t, filepath.Join(root, "run/validation-response.private.json"), func(v map[string]any) { v["response"].(map[string]any)["text"] = text })
				raw, _ := os.ReadFile(filepath.Join(root, "run/validation-response.private.json"))
				p.FinalResponseSHA = sha(raw)
				receiptChangeLedger(t, root, &p, func(rows []map[string]any) []map[string]any {
					for _, x := range rows {
						if x["event"] == "response" && x["phase"] == "validation" {
							x["final_content_sha256"] = sha([]byte(text))
						}
					}
					return rows
				})
			} else if field == "saved-usage" {
				receiptChangeJSON(t, filepath.Join(root, "run/validation-response.private.json"), func(v map[string]any) { v["response"].(map[string]any)["input_tokens"] = 101 })
				raw, _ := os.ReadFile(filepath.Join(root, "run/validation-response.private.json"))
				p.FinalResponseSHA = sha(raw)
			} else {
				receiptChangeLedger(t, root, &p, func(rows []map[string]any) []map[string]any {
					for i, x := range rows {
						if field == "missing-outcome" && x["event"] == "response" {
							return append(rows[:i], rows[i+1:]...)
						}
						if x["event"] == "request" {
							switch field {
							case "phase":
								x["phase"] = "validation"
							case "stage":
								x["stage"] = "semantic_gate"
							case "source":
								x["original_sha256"] = "other"
							case "mode":
								x["operation_mode"] = receiptMode
							case "document":
								x["document_id"] = "other"
							case "retry":
								x["provider_attempt"] = 2
							case "runtime":
								x["token_count"].(map[string]any)["runtime_receipt_sha256"] = "other"
							}
						}
						if x["event"] == "response" {
							switch field {
							case "delta":
								x["provider_minus_local_input_tokens"] = 1
							case "usage":
								x["usage_status"] = "unknown"
							case "model":
								x["returned_model"] = "unexpected"
							}
						}
					}
					return rows
				})
			}
			receiptReanchor(t, root, &p)
			if _, err := verifyReceiptParent(root, p); err == nil {
				t.Fatal("bad reanchored parent admitted")
			}
		})
	}
}

type receiptScriptClient struct {
	replies  []string
	phases   []string
	requests []llm.CompleteRequest
}

func (*receiptScriptClient) ProviderName() string { return "offline-receipt-script" }
func (c *receiptScriptClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	b, ok := ctx.Value(seededBindingKey{}).(receiptCallBinding)
	if !ok {
		return nil, fmt.Errorf("missing receipt binding")
	}
	info := &callInfo{}
	if err := b.bind(req, info); err != nil {
		return nil, err
	}
	c.phases = append(c.phases, b.Phase)
	c.requests = append(c.requests, req)
	if len(c.phases) > len(c.replies) {
		return nil, fmt.Errorf("extra live phase")
	}
	return &llm.CompleteResponse{Text: c.replies[len(c.phases)-1], InputTokens: 100, OutputTokens: 2, FinishReason: "stop"}, nil
}

func TestReceiptUsesExactParentAndOnlyTwoPhases(t *testing.T) {
	_, _, parent := receiptParentFixture(t)
	for _, tt := range []struct {
		name    string
		replies []string
		status  string
		calls   int
	}{
		{"pass", []string{seededRevisedJSON, `{"idempotent":true}`}, "RECEIPT_REVISION_GATE_PASS_not_capacity_or_causal_effect", 2},
		{"negative", []string{seededRevisedJSON, `{"idempotent":false,"reason":"Still incomplete"}`}, "FAIL_final_gate", 2},
		{"untyped", []string{seededRevisedJSON, `{"reason":"Not explicit"}`}, "FAIL_final_gate_untyped_or_execution", 2},
		{"empty", []string{`{"items":[]}`}, "FAIL_nonempty_format_gate", 1},
		{"malformed", []string{`bad`}, "FAIL_revision", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &receiptScriptClient{replies: tt.replies}
			e := &receiptExecutor{parent: parent, next: c}
			status, err := e.execute(context.Background())
			if status != tt.status || len(c.phases) != tt.calls || (err == nil) != (tt.name == "pass") {
				t.Fatalf("%s %v %v", status, err, c.phases)
			}
			if c.phases[0] != "revision" || tt.calls == 2 && c.phases[1] != "validation" {
				t.Fatal("qualification or wrong phase")
			}
			var data struct {
				Original  string          `json:"original"`
				Critique  string          `json:"critique"`
				Candidate json.RawMessage `json:"candidate"`
			}
			if json.Unmarshal([]byte(c.requests[0].User), &data) != nil || data.Original != parent.in.Original || data.Critique != parent.feedback.Reason || len(data.Candidate) == 0 {
				t.Fatal("source/candidate/unedited feedback lost")
			}
		})
	}
	changed := parent
	changed.feedback = &annotation.SemanticRejection{Reason: "Manual diagnosis instead of observed reason"}
	c := &receiptScriptClient{}
	if _, err := (&receiptExecutor{parent: changed, next: c}).execute(context.Background()); err == nil || len(c.phases) != 0 {
		t.Fatal("manual feedback admitted")
	}
}

func receiptFakeRun(t *testing.T, parent receiptParent, final string) (string, error, int) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fresh")
	counter := seedTestCounter{identity: parent.plan.Counter}
	if err := receiptPreflight(context.Background(), parent, out, "fixture-receipt-plan", counter); err != nil {
		t.Fatal(err)
	}
	texts := []string{seededRevisedJSON, final}
	sent := 0
	previous := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) {
		if sent >= 2 {
			t.Fatal("third actual wire")
		}
		response := seedHTTPResponse(texts[sent])
		sent++
		return response, nil
	})
	err := receiptRun(context.Background(), parent, out, "fixture-receipt-plan", counter)
	http.DefaultTransport = previous
	return out, err, sent
}

func TestReceiptRunAuditAndProvisionalCheckpoint(t *testing.T) {
	_, _, parent := receiptParentFixture(t)
	out, err, sent := receiptFakeRun(t, parent, `{"idempotent":true}`)
	if err != nil || sent != 2 {
		t.Fatalf("pass %v wires%d", err, sent)
	}
	var cp map[string]any
	if readJSON(filepath.Join(out, "accepted-checkpoint.json"), &cp) != nil || cp["acceptance_state"] != "provisional_until_receipt_audit_pass" {
		t.Fatal("checkpoint not explicitly provisional")
	}
	for _, field := range []string{"checkpoint", "accepted", "cleaned", "revision", "response", "counter", "parent"} {
		t.Run(field, func(t *testing.T) {
			fresh, err, _ := receiptFakeRun(t, parent, `{"idempotent":true}`)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "checkpoint":
				receiptChangeJSON(t, filepath.Join(fresh, "accepted-checkpoint.json"), func(v map[string]any) { v["parent_proof_sha256"] = "other" })
			case "accepted":
				receiptChangeJSON(t, filepath.Join(fresh, "accepted-cleaned.private.json"), func(v map[string]any) { v["items"].([]any)[0].(map[string]any)["name"] = "Tampered" })
			case "cleaned", "revision":
				name := "format-gate.private.json"
				if field == "revision" {
					name = "revision.private.json"
				}
				receiptChangeJSON(t, filepath.Join(fresh, name), func(v map[string]any) { v["items"].([]any)[0].(map[string]any)["name"] = "Tampered" })
			case "response":
				receiptChangeJSON(t, filepath.Join(fresh, "validation-response.private.json"), func(v map[string]any) {
					v["response"].(map[string]any)["text"] = `{"idempotent":true,"reason":"rewritten"}`
				})
			case "counter", "parent":
				raw, _ := os.ReadFile(filepath.Join(fresh, "requests.jsonl"))
				from, to := `"runtime_receipt_sha256":"test-runtime"`, `"runtime_receipt_sha256":"other"`
				if field == "parent" {
					from, to = parent.proofSHA, "other-parent"
				}
				changed := strings.ReplaceAll(string(raw), from, to)
				if changed == string(raw) {
					t.Fatal("tamper fixture did not change ledger")
				}
				receiptPut(t, filepath.Join(fresh, "requests.jsonl"), []byte(changed))
			}
			if receiptAudit(fresh, parent, "fixture-receipt-plan") == nil {
				t.Fatal("tampered accepted packet reusable")
			}
		})
	}
	negative, err, sent := receiptFakeRun(t, parent, `{"idempotent":false,"reason":"Still incomplete"}`)
	if err == nil || sent != 2 {
		t.Fatal("negative accepted")
	}
	if _, err := os.Stat(filepath.Join(negative, "accepted-checkpoint.json")); !os.IsNotExist(err) {
		t.Fatal("negative checkpoint exists")
	}
	names, _ := filepath.Glob(filepath.Join(negative, "receipt-audit-*.json"))
	var audit map[string]any
	if len(names) != 1 || readJSON(names[0], &audit) != nil || audit["integrity_pass"] != true || audit["accepted"] != false {
		t.Fatal("complete typed negative confused with bad integrity")
	}
	receiptChangeJSON(t, filepath.Join(negative, "validation-typed-rejection.private.json"), func(v map[string]any) { v["reason"] = "Manual rewrite" })
	if receiptAudit(negative, parent, "fixture-receipt-plan") == nil {
		t.Fatal("changed negative reason accepted")
	}
	names, _ = filepath.Glob(filepath.Join(negative, "receipt-audit-*.json"))
	var latest map[string]any
	if readJSON(names[len(names)-1], &latest) != nil || latest["integrity_pass"] != false {
		t.Fatal("negative reason drift not detected")
	}
}

func TestReceiptRejectsPreflightDriftAndMissingKeyBeforeWire(t *testing.T) {
	_, _, parent := receiptParentFixture(t)
	for _, field := range []string{"runtime", "python", "adapter", "counter", "binary", "parent", "missing-key"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("CAIRN_LLM_API_KEY", "synthetic-offline-only")
			out := filepath.Join(t.TempDir(), "fresh")
			c := seedTestCounter{identity: parent.plan.Counter}
			if err := receiptPreflight(context.Background(), parent, out, "plan", c); err != nil {
				t.Fatal(err)
			}
			if receiptPreflight(context.Background(), parent, out, "plan", c) == nil {
				t.Fatal("preflight reused output")
			}
			changed := parent
			switch field {
			case "runtime":
				c.identity.RuntimeSHA = "other"
			case "python":
				c.identity.PythonSHA = "other"
			case "adapter":
				c.identity.AdapterSHA = "other"
			case "counter":
				c.identity.CounterID = "other"
			case "binary":
				receiptChangeJSON(t, filepath.Join(out, "receipt-preflight.json"), func(v map[string]any) { v["binary_sha256"] = "other" })
			case "parent":
				changed.proofSHA = "other"
			case "missing-key":
				t.Setenv("CAIRN_LLM_API_KEY", "")
			}
			sent := 0
			previous := http.DefaultTransport
			http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) {
				sent++
				return seedHTTPResponse(`{"idempotent":true}`), nil
			})
			err := receiptRun(context.Background(), changed, out, "plan", c)
			http.DefaultTransport = previous
			if err == nil || sent != 0 {
				t.Fatalf("drift reached wire %v %d", err, sent)
			}
		})
	}
}

func TestReceiptWireCapsPersistenceAndBadModel(t *testing.T) {
	_, _, parent := receiptParentFixture(t)
	for _, responseFailure := range []string{"ledger", "rate-limit"} {
		t.Run(responseFailure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := &memoryLog{}
			if responseFailure == "ledger" {
				m.failAt = 2
			}
			ledger := &durableLedger{file: m, cancel: cancel}
			sent := 0
			first, err := captureSeedRevision(parent.in, parent.feedback)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := renderSeedWire(ctx, first)
			if err != nil {
				t.Fatal(err)
			}
			count, _ := fittingCounter().Count(ctx, wire)
			observer := &traceTransport{ledger: ledger, counter: fittingCounter(), seedPolicy: &receiptWirePolicy{Document: parent.in.Document, SourceSHA: parent.in.SourceSHA, ParentSHA: parent.proofSHA, Counter: parent.plan.Counter, FirstWireSHA: sha(wire), FirstCount: count}, next: transportFunc(func(*http.Request) (*http.Response, error) {
				sent++
				return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"synthetic"}`))}, nil
			})}
			previous := http.DefaultTransport
			http.DefaultTransport = observer
			defer func() { http.DefaultTransport = previous }()
			client, _ := llm.NewOpenAICompatClient(llm.Config{Endpoint: "https://api.deepseek.com/chat/completions", APIKeyEnv: "CAIRN_TEST_UNUSED", Model: "deepseek-flash", MaxTokens: seededMaxTokens, MaxRetries: 1})
			e := &receiptExecutor{parent: parent, next: &boundedClient{next: client, sem: make(chan struct{}, 1), ledger: ledger}}
			_, err = e.execute(ctx)
			want := 1
			if responseFailure == "ledger" {
				want = 0
			}
			if err == nil || sent != want || ctx.Err() == nil {
				t.Fatalf("failure=%s wires%d err%v", responseFailure, sent, err)
			}
		})
	}
	out := filepath.Join(t.TempDir(), "fresh")
	c := seedTestCounter{identity: parent.plan.Counter}
	if err := receiptPreflight(context.Background(), parent, out, "plan", c); err != nil {
		t.Fatal(err)
	}
	sent := 0
	previous := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) {
		sent++
		if sent == 1 {
			return seedHTTPResponse(seededRevisedJSON), nil
		}
		response := seedHTTPResponse(`{"idempotent":true}`)
		raw, _ := io.ReadAll(response.Body)
		response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(raw), `"deepseek-flash"`, `"unexpected-model"`)))
		return response, nil
	})
	err := receiptRun(context.Background(), parent, out, "plan", c)
	http.DefaultTransport = previous
	if err == nil || sent != 2 {
		t.Fatal("unexpected model accepted or retried")
	}
	var checkpoint map[string]any
	if readJSON(filepath.Join(out, "accepted-checkpoint.json"), &checkpoint) != nil || checkpoint["acceptance_state"] != "provisional_until_receipt_audit_pass" {
		t.Fatal("parsed true hid pending audit")
	}
	if receiptAudit(out, parent, "plan") == nil {
		t.Fatal("bad-model checkpoint reusable")
	}
}

func TestReceiptPolicyRejectsQualificationRetryOverridesAndThirdWire(t *testing.T) {
	_, _, parent := receiptParentFixture(t)
	raw := []byte(`{"model":"deepseek-flash","max_tokens":65536,"messages":[],"thinking":{"type":"enabled"},"response_format":{"type":"json_object"}}`)
	var req wireRequest
	_ = json.Unmarshal(raw, &req)
	count, _ := fittingCounter().Count(context.Background(), raw)
	p := &receiptWirePolicy{Document: parent.in.Document, SourceSHA: parent.in.SourceSHA, ParentSHA: parent.proofSHA, Counter: parent.plan.Counter, FirstWireSHA: sha(raw), FirstCount: count}
	info := &callInfo{Mode: receiptMode, Phase: "qualification", SourceSHA: parent.in.SourceSHA, Document: parent.in.Document, Documents: []string{parent.in.Document}, ParentSHA: parent.proofSHA}
	info.Attempts.Store(1)
	if p.admit(info, req, raw, count) == nil {
		t.Fatal("qualification admitted")
	}
	info.Phase = "validation"
	if p.admit(info, req, raw, count) == nil {
		t.Fatal("wrong order admitted")
	}
	info.Phase = "revision"
	info.Attempts.Store(2)
	if p.admit(info, req, raw, count) == nil {
		t.Fatal("retry admitted")
	}
	info.Attempts.Store(1)
	info.ParentSHA = "other"
	if p.admit(info, req, raw, count) == nil {
		t.Fatal("foreign parent admitted")
	}
	info.ParentSHA = parent.proofSHA
	if p.admit(info, req, append(append([]byte{}, raw...), ' '), count) == nil {
		t.Fatal("first actual bytes drifted from frozen preflight")
	}
	override := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"temperature":0}`)...)
	if p.admit(info, req, override, count) == nil {
		t.Fatal("profile override admitted")
	}
	for _, phase := range []string{"revision", "validation"} {
		info.Phase = phase
		if err := p.admit(info, req, raw, count); err != nil {
			t.Fatal(err)
		}
	}
	if p.admit(info, req, raw, count) == nil {
		t.Fatal("third wire admitted")
	}
}

type receiptDriftingCounter struct {
	seedTestCounter
	calls int
}

func (c *receiptDriftingCounter) Count(ctx context.Context, raw []byte) (tokenCount, error) {
	n, err := c.seedTestCounter.Count(ctx, raw)
	c.calls++
	if c.calls >= 3 {
		n.Input++
	}
	return n, err
}

// 首次预检及 run 重计都通过后，真实发送前的相同身份计数漂移仍应为零 wire。
// The transport must reject a last-moment count drift, not merely fail the post-paid audit.
func TestReceiptFirstTransportAdmissionMatchesFrozenCount(t *testing.T) {
	_, _, parent := receiptParentFixture(t)
	c := &receiptDriftingCounter{seedTestCounter: seedTestCounter{identity: parent.plan.Counter}}
	out := filepath.Join(t.TempDir(), "fresh")
	if err := receiptPreflight(context.Background(), parent, out, "plan", c); err != nil {
		t.Fatal(err)
	}
	sent := 0
	previous := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) { sent++; return seedHTTPResponse(seededRevisedJSON), nil })
	err := receiptRun(context.Background(), parent, out, "plan", c)
	http.DefaultTransport = previous
	if err == nil || sent != 0 || c.calls != 3 {
		t.Fatalf("changed count dispatched or retried: sent%d calls%d err%v", sent, c.calls, err)
	}
	counts, _ := auditTrace(filepath.Join(out, "requests.jsonl"))
	if counts["admitted_attempts"] != 0 || counts["rejected_attempts"] != 1 {
		t.Fatal("rejected drift not retained exactly once")
	}
}

// Reviewer probes on b009 accepted zero/over-reserve usage and spent validation
// after invalid revision input. The actual response must persist, then stop immediately.
func TestReceiptImpossibleUsageStopsBeforeAnotherWire(t *testing.T) {
	_, _, parent := receiptParentFixture(t)
	for _, tt := range []struct {
		name, field string
		value       any
		missing     bool
	}{
		{"zero input", "prompt_tokens", 0, false}, {"mismatched input", "prompt_tokens", 101, false},
		{"negative input", "prompt_tokens", -1, false}, {"missing input", "prompt_tokens", nil, true},
		{"zero output", "completion_tokens", 0, false}, {"excess output", "completion_tokens", seededMaxTokens + 1, false},
		{"negative output", "completion_tokens", -1, false}, {"missing output", "completion_tokens", nil, true},
	} {
		for _, phase := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s-phase%d", tt.name, phase), func(t *testing.T) {
				out := filepath.Join(t.TempDir(), "fresh")
				counter := seedTestCounter{identity: parent.plan.Counter}
				if err := receiptPreflight(context.Background(), parent, out, "plan", counter); err != nil {
					t.Fatal(err)
				}
				sent := 0
				previous := http.DefaultTransport
				http.DefaultTransport = transportFunc(func(*http.Request) (*http.Response, error) {
					index := sent
					sent++
					text := seededRevisedJSON
					if index == 1 {
						text = `{"idempotent":true}`
					}
					response := seedHTTPResponse(text)
					if index == phase {
						raw, _ := io.ReadAll(response.Body)
						var body map[string]any
						_ = json.Unmarshal(raw, &body)
						usage := body["usage"].(map[string]any)
						if tt.missing {
							delete(usage, tt.field)
						} else {
							usage[tt.field] = tt.value
						}
						delete(usage, "total_tokens")
						changed, _ := json.Marshal(body)
						response.Body = io.NopCloser(strings.NewReader(string(changed)))
					}
					return response, nil
				})
				err := receiptRun(context.Background(), parent, out, "plan", counter)
				http.DefaultTransport = previous
				if err == nil || sent != phase+1 {
					t.Fatalf("bad usage accepted or spent later wire: err%v sent%d", err, sent)
				}
				name := "revision"
				if phase == 1 {
					name = "validation"
				}
				var saved struct {
					Response      *llm.CompleteResponse `json:"response"`
					CompleteError bool                  `json:"complete_error"`
				}
				if readJSON(filepath.Join(out, name+"-response.private.json"), &saved) != nil || saved.Response == nil || !saved.CompleteError {
					t.Fatal("actual bad-usage response/error lost")
				}
				if _, err := os.Stat(filepath.Join(out, "accepted-checkpoint.json")); !os.IsNotExist(err) {
					t.Fatal("bad usage created accepted checkpoint")
				}
			})
		}
	}
}

// Resigning matching response/ledger/finish totals cannot turn impossible usage
// into accepted proof, even when the production text parser still returns true.
func TestReceiptOfflineAuditRejectsCoherentOutOfBudgetUsage(t *testing.T) {
	_, _, parent := receiptParentFixture(t)
	for _, output := range []int{0, seededMaxTokens + 1} {
		t.Run(fmt.Sprint(output), func(t *testing.T) {
			out, err, _ := receiptFakeRun(t, parent, `{"idempotent":true}`)
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"revision", "validation"} {
				receiptChangeJSON(t, filepath.Join(out, phase+"-response.private.json"), func(v map[string]any) { v["response"].(map[string]any)["output_tokens"] = output })
			}
			path := filepath.Join(out, "requests.jsonl")
			raw, _ := os.ReadFile(path)
			var changed []byte
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var row map[string]any
				_ = json.Unmarshal([]byte(line), &row)
				if row["event"] == "response" {
					row["output_tokens"] = output
					row["total_tokens"] = 100 + output
				}
				one, _ := json.Marshal(row)
				changed = append(changed, append(one, '\n')...)
			}
			receiptPut(t, path, changed)
			receiptChangeJSON(t, filepath.Join(out, "receipt-run-finish.json"), func(v map[string]any) { v["reported_output_tokens"] = 2 * output })
			if receiptAudit(out, parent, "fixture-receipt-plan") == nil {
				t.Fatal("impossible coherent usage accepted")
			}
			names, _ := filepath.Glob(filepath.Join(out, "receipt-audit-*.json"))
			var last map[string]any
			if readJSON(names[len(names)-1], &last) != nil || last["integrity_pass"] != false || last["accepted"] != false {
				t.Fatal("bad-usage audit did not reject integrity/acceptance")
			}
		})
	}
}
