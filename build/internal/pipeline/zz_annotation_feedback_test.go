package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type feedbackClient func(llm.CompleteRequest) (*llm.CompleteResponse, error)

func (f feedbackClient) Complete(_ context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return f(r)
}
func (feedbackClient) ProviderName() string { return "feedback-test" }

func newFeedbackRunner(t *testing.T, client llm.Client, retries int) func(context.Context, string, string, string) (*dktypes.AnnotatedDocument, string) {
	t.Helper()
	o, err := NewOrchestrator(Options{Client: client, MaxTokens: 71, MaxRetries: retries})
	if err != nil {
		t.Fatal(err)
	}
	return o.processDocument
}

func feedbackAnnotation(name, detail string) string {
	wire, _ := json.Marshal(map[string]any{"items": []map[string]any{
		{"tag": "entity", "name": "`" + name + "`", "content": "lower confidence", "detail": "old detail", "confidence": .1},
		{"tag": "entity", "name": name, "content": "kept " + name, "detail": detail, "confidence": 1.5},
	}})
	return string(wire)
}

func feedbackIsGate(r llm.CompleteRequest) bool {
	return strings.Contains(r.System, "你是知识标注的语义核验专家")
}

// 旧行为把每轮都变成同一个原文请求；最新有效候选/意见必须跨暂时失败保留，新的语义拒绝才替换。
// Exercise the actual three-round state machine and both gates, not a mirrored feedback helper.
func TestAnnotationFeedbackLatestAndTransientFailures(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"annotate-transport", "annotate-parse", "gate-a", "gate-b-transport", "gate-b-parse", "gate-b-missing", "gate-b-null", "gate-b-blank", "new-semantic", "reject-all"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			original := strings.Repeat("full authoritative source with dates and conditions\n", 128) + "SOURCE-END"
			annotations, gates := 0, 0
			var revisionWires []string
			client := feedbackClient(func(r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if r.MaxTokens != 71 {
					t.Fatalf("token cap changed: %d", r.MaxTokens)
				}
				if feedbackIsGate(r) {
					gates++
					if !strings.Contains(r.User, original) {
						t.Fatal("Gate B received substituted original")
					}
					if annotations == 1 {
						return &llm.CompleteResponse{Text: `{"idempotent":false,"reason":"first specific critique"}`}, nil
					}
					if annotations == 2 {
						switch failure {
						case "gate-b-transport":
							return nil, errors.New("transient gate transport")
						case "gate-b-parse":
							return &llm.CompleteResponse{Text: "invalid JSON"}, nil
						case "gate-b-missing":
							return &llm.CompleteResponse{Text: `{"reason":"unverified new critique"}`}, nil
						case "gate-b-null":
							return &llm.CompleteResponse{Text: `{"idempotent":null,"reason":"unverified new critique"}`}, nil
						case "gate-b-blank":
							return &llm.CompleteResponse{Text: `{"idempotent":false,"reason":" "}`}, nil
						case "new-semantic", "reject-all":
							return &llm.CompleteResponse{Text: `{"idempotent":false,"reason":"second specific critique"}`}, nil
						}
					}
					if failure == "reject-all" {
						return &llm.CompleteResponse{Text: `{"idempotent":false,"reason":"third specific critique"}`}, nil
					}
					return &llm.CompleteResponse{Text: `{"idempotent":true,"reason":"faithful"}`}, nil
				}
				annotations++
				if annotations > 3 {
					t.Fatal("outer cap exceeded")
				}
				if annotations > 1 {
					revisionWires = append(revisionWires, r.User)
					var input struct {
						Original  string
						Candidate []map[string]any
						Critique  string
					}
					if err := json.Unmarshal([]byte(r.User), &input); err != nil {
						t.Fatalf("no revision feedback: %v", err)
					}
					wantName, wantReason := "first unit", "first specific critique"
					if annotations == 3 && (failure == "new-semantic" || failure == "reject-all") {
						wantName, wantReason = "second unit", "second specific critique"
					}
					if input.Original != original || input.Critique != wantReason || len(input.Candidate) != 1 {
						t.Fatalf("wrong source/critique/candidate: %+v", input)
					}
					candidate := input.Candidate[0]
					if candidate["name"] != wantName || candidate["content"] != "kept "+wantName || candidate["confidence"] != float64(1) || len(candidate) != 5 {
						t.Fatalf("not latest Gate A-cleaned candidate: %#v", candidate)
					}
					if input.Critique == "second specific critique" && strings.Contains(r.User, "first unit") {
						t.Fatal("historical candidate union")
					}
				} else if !strings.Contains(r.User, original) {
					t.Fatal("first source missing")
				}
				if annotations == 2 {
					if failure == "annotate-transport" {
						return nil, errors.New("transient annotation transport")
					}
					if failure == "annotate-parse" {
						return &llm.CompleteResponse{Text: "invalid JSON"}, nil
					}
					if failure == "gate-a" {
						return &llm.CompleteResponse{Text: feedbackAnnotation("second unit", "")}, nil
					}
					return &llm.CompleteResponse{Text: feedbackAnnotation("second unit", "complete second detail")}, nil
				}
				name := "first unit"
				if annotations == 3 {
					name = "replacement unit"
				}
				return &llm.CompleteResponse{Text: feedbackAnnotation(name, "complete source detail")}, nil
			})
			run := newFeedbackRunner(t, client, 1)
			doc, reason := run(context.Background(), "skill", "ref.md", original)
			if annotations != 3 {
				t.Fatalf("annotations=%d", annotations)
			}
			wantGates := 3
			if failure == "annotate-transport" || failure == "annotate-parse" || failure == "gate-a" {
				wantGates = 2
			}
			if gates != wantGates {
				t.Fatalf("gate calls=%d want=%d", gates, wantGates)
			}
			if failure == "reject-all" {
				if doc != nil || !strings.Contains(reason, "third specific critique") {
					t.Fatalf("exhaustion: doc=%+v reason=%s", doc, reason)
				}
			} else if reason != "" || doc == nil || len(doc.Items) != 1 || doc.Items[0].Name != "replacement unit" || doc.Items[0].ID != "" {
				t.Fatalf("not full replacement: %+v reason=%s", doc, reason)
			}
			if failure != "new-semantic" && failure != "reject-all" && !reflect.DeepEqual(revisionWires[0], revisionWires[1]) {
				t.Fatal("transient failure replaced valid feedback")
			}
		})
	}
}

// 首次 Gate A/无法核验不是语义意见，下一轮仍须使用历史 Annotate wire。
func TestAnnotationNoSemanticFeedbackFromOrdinaryFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"gate-a", "gate-b-transport", "gate-b-parse", "gate-b-missing", "gate-b-null", "gate-b-blank"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			calls, gates := 0, 0
			var first llm.CompleteRequest
			client := feedbackClient(func(r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if feedbackIsGate(r) {
					gates++
					if calls == 1 {
						switch failure {
						case "gate-b-transport":
							return nil, errors.New("transport")
						case "gate-b-parse":
							return &llm.CompleteResponse{Text: "invalid"}, nil
						case "gate-b-missing":
							return &llm.CompleteResponse{Text: `{"reason":"not verified"}`}, nil
						case "gate-b-null":
							return &llm.CompleteResponse{Text: `{"idempotent":null,"reason":"not verified"}`}, nil
						case "gate-b-blank":
							return &llm.CompleteResponse{Text: `{"idempotent":false,"reason":" "}`}, nil
						}
					}
					return &llm.CompleteResponse{Text: `{"idempotent":true}`}, nil
				}
				calls++
				if calls == 1 {
					first = r
				} else if !reflect.DeepEqual(first, r) {
					t.Fatal("ordinary error was fed back as semantic data")
				}
				detail := "full detail"
				if calls == 1 && failure == "gate-a" {
					detail = ""
				}
				return &llm.CompleteResponse{Text: feedbackAnnotation("same unit", detail)}, nil
			})
			doc, reason := newFeedbackRunner(t, client, 1)(context.Background(), "s", "p", "original")
			wantGates := 2
			if failure == "gate-a" {
				wantGates = 1
			}
			if doc == nil || reason != "" || calls != 2 || gates != wantGates {
				t.Fatalf("doc=%+v reason=%s calls=%d gates=%d", doc, reason, calls, gates)
			}
		})
	}
}

func TestAnnotationOuterAndInnerCapsUnchanged(t *testing.T) {
	t.Parallel()
	for _, gateFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(gateFailure), func(t *testing.T) {
			t.Parallel()
			annotations, gates := 0, 0
			client := feedbackClient(func(r llm.CompleteRequest) (*llm.CompleteResponse, error) {
				if feedbackIsGate(r) {
					gates++
					return nil, errors.New("unverifiable")
				}
				annotations++
				if gateFailure {
					return &llm.CompleteResponse{Text: feedbackAnnotation("unit", "detail")}, nil
				}
				return nil, errors.New("annotation unavailable")
			})
			doc, reason := newFeedbackRunner(t, client, 2)(context.Background(), "s", "p", "source")
			if doc != nil || reason == "" {
				t.Fatal("exhaustion did not skip")
			}
			if gateFailure {
				if annotations != 3 || gates != 6 {
					t.Fatalf("annotations=%d gates=%d", annotations, gates)
				}
			} else if annotations != 6 || gates != 0 {
				t.Fatalf("annotations=%d gates=%d", annotations, gates)
			}
		})
	}
}

// 共享标注器可并发使用，但每篇文档的反馈、候选和预算必须完全隔离。
func TestAnnotationFeedbackConcurrentDocumentIsolation(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	annotations := map[string]int{}
	gates := map[string]int{}
	client := feedbackClient(func(r llm.CompleteRequest) (*llm.CompleteResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		key := "alpha-only"
		other := "beta-only"
		if strings.Contains(r.User, other) {
			key, other = other, key
		}
		if strings.Contains(r.User, other) {
			return nil, errors.New("cross-document contamination")
		}
		if feedbackIsGate(r) {
			gates[key]++
			if gates[key] == 1 {
				return &llm.CompleteResponse{Text: `{"idempotent":false,"reason":"` + key + ` critique"}`}, nil
			}
			return &llm.CompleteResponse{Text: `{"idempotent":true}`}, nil
		}
		annotations[key]++
		if annotations[key] == 2 {
			var input struct {
				Original, Critique string
				Candidate          []map[string]any
			}
			if err := json.Unmarshal([]byte(r.User), &input); err != nil {
				return nil, err
			}
			if input.Original != key || input.Critique != key+" critique" || len(input.Candidate) != 1 || input.Candidate[0]["name"] != key {
				return nil, errors.New("feedback state crossed documents")
			}
		} else if annotations[key] > 2 {
			return nil, errors.New("unexpected retry")
		}
		return &llm.CompleteResponse{Text: feedbackAnnotation(key, "detail")}, nil
	})
	run := newFeedbackRunner(t, client, 1)
	var wg sync.WaitGroup
	for _, key := range []string{"alpha-only", "beta-only"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			doc, reason := run(context.Background(), key, key+".md", key)
			if doc == nil || reason != "" || doc.Skill != key || doc.FilePath != key+".md" || len(doc.Items) != 1 || doc.Items[0].Name != key {
				t.Errorf("isolation failed: %+v reason=%s", doc, reason)
			}
		}(key)
	}
	wg.Wait()
	if annotations["alpha-only"] != 2 || annotations["beta-only"] != 2 || gates["alpha-only"] != 2 || gates["beta-only"] != 2 {
		t.Fatalf("annotations=%v gates=%v", annotations, gates)
	}
}
