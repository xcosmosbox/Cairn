package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rawTraceRoundTrip func(*http.Request) (*http.Response, error)

func (f rawTraceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type rawTraceBrokenBody struct{ done bool }

func (b *rawTraceBrokenBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, errors.New("body interrupted")
	}
	b.done = true
	return copy(p, "partial"), nil
}
func (*rawTraceBrokenBody) Close() error { return nil }

// Missing usage, a local transport failure and unavailable persistence previously
// latched shared admission. Each later independent request must still be observed.
func TestRawTraceRetainsActualBodiesNullUsageAndLaterOutcomes(t *testing.T) {
	t.Setenv("CAIRN_LLM_API_KEY", "secret-trace-value")
	dir := t.TempDir()
	var calls int
	payloads := []string{`{"model":"deepseek-flash","choices":[],"echo":"secret-trace-value"}`, ``, `{"usage":{"prompt_tokens":3,"completion_tokens":7}}`}
	tr := NewRawTraceTransport(dir, rawTraceRoundTrip(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Contains(raw, []byte("actual request")) {
			t.Fatal("actual request changed")
		}
		calls++
		if calls == 2 {
			return nil, errors.New("provider secret-trace-value unavailable")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payloads[calls-1]))}, nil
	}))
	// Force only the evidence sink to fail; observation and provider work continue.
	if err := tr.file.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		raw := `{"messages":[{"content":"actual request references/` + strings.Repeat("a", 64) + `.md"}]}`
		req, _ := http.NewRequest("POST", "https://example.invalid/model", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer secret-trace-value")
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("unrelated replay")), nil }
		response, err := tr.RoundTrip(req)
		if i == 1 {
			if err == nil {
				t.Fatal("missing error")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(response.Body)
		if string(got) != payloads[i] {
			t.Fatal("provider response changed")
		}
	}
	if calls != 3 {
		t.Fatal("independent calls stopped")
	}
	if err := tr.Close(); err == nil {
		t.Fatal("sink failure must remain explicit")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "observed-outcomes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-trace-value")) {
		t.Fatal("credential leaked")
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 || string(rows[1]["input_tokens"]) != "null" || string(rows[5]["input_tokens"]) != "3" {
		t.Fatal("lost outcomes or fabricated usage")
	}
	var body []byte
	if err := json.Unmarshal(rows[1]["response_body"], &body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("[REDACTED]")) || string(rows[1]["response_sha256"]) != `"`+traceHash([]byte(payloads[0]))+`"` {
		t.Fatal("raw observation digest/redaction lost")
	}
	if string(rows[0]["request_sha256"]) != string(rows[1]["request_sha256"]) {
		t.Fatal("response parent changed")
	}
}

func TestRawTracePreservesResponseReadFailure(t *testing.T) {
	tr := NewRawTraceTransport(t.TempDir(), rawTraceRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: &rawTraceBrokenBody{}}, nil
	}))
	defer tr.Close()
	req, _ := http.NewRequest("POST", "https://example.invalid", strings.NewReader("request"))
	response, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	if string(raw) != "partial" || err == nil || err.Error() != "body interrupted" {
		t.Fatalf("read=%q err=%v", raw, err)
	}
}
