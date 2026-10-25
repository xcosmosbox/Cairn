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
