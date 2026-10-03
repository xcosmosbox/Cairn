package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// usageTransport observes every HTTP attempt, including retries and rejected
// length responses. It persists no credentials, prompts or generated content.
type usageTransport struct {
	next                                                                http.RoundTripper
	file                                                                *os.File
	mu                                                                  sync.Mutex
	calls                                                               atomic.Int64
	responses, missingUsage, inputTokens, outputTokens, lengthResponses int
	writeErr                                                            error
}

func (t *usageTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	call := t.calls.Add(1)
	start := time.Now()
	row := map[string]any{"http_attempt": call, "started_at": start.UTC().Format(time.RFC3339Nano)}
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err == nil {
			raw, readErr := io.ReadAll(body)
			body.Close()
			if readErr == nil {
				row["request_sha256"] = sha256Hex(raw)
				var metadata struct {
					Model     string `json:"model"`
					MaxTokens int    `json:"max_tokens"`
				}
				if json.Unmarshal(raw, &metadata) == nil {
					row["model"] = metadata.Model
					row["request_max_tokens"] = metadata.MaxTokens
				}
			}
		}
	}
	response, err := t.next.RoundTrip(request)
	if err != nil {
		row["transport_error"] = true
		row["seconds"] = time.Since(start).Seconds()
		t.record(row, nil)
		return response, err
	}
	raw, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	// Preserve the original response semantics, including a read failure.
	response.Body = &replayedBody{Reader: bytes.NewReader(raw), readErr: readErr}
	row["http_status"] = response.StatusCode
	row["seconds"] = time.Since(start).Seconds()
	row["response_sha256"] = sha256Hex(raw)
	row["response_bytes"] = len(raw)
	if readErr != nil {
		row["response_read_error"] = true
	}
	var parsed struct {
		Usage *struct {
			Input  int `json:"prompt_tokens"`
			Output int `json:"completion_tokens"`
			Total  int `json:"total_tokens"`
		} `json:"usage"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	var usage *tokenUsage
	if json.Unmarshal(raw, &parsed) == nil {
		if len(parsed.Choices) > 0 {
			row["finish_reason"] = parsed.Choices[0].FinishReason
		}
		if parsed.Usage != nil {
			usage = &tokenUsage{input: parsed.Usage.Input, output: parsed.Usage.Output}
			row["input_tokens"], row["output_tokens"], row["total_tokens"] = parsed.Usage.Input, parsed.Usage.Output, parsed.Usage.Total
		}
	}
	t.record(row, usage)
	return response, nil
}

type tokenUsage struct{ input, output int }

func (t *usageTransport) record(row map[string]any, usage *tokenUsage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := row["http_status"]; ok {
		t.responses++
	}
	if usage == nil {
		t.missingUsage++
	} else {
		t.inputTokens += usage.input
		t.outputTokens += usage.output
	}
	if row["finish_reason"] == "length" {
		t.lengthResponses++
	}
	if err := json.NewEncoder(t.file).Encode(row); err != nil && t.writeErr == nil {
		t.writeErr = err
	}
	if err := t.file.Sync(); err != nil && t.writeErr == nil {
		t.writeErr = err
	}
}

func (t *usageTransport) summary() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]any{"http_attempts": t.calls.Load(), "http_responses": t.responses,
		"input_tokens": t.inputTokens, "output_tokens": t.outputTokens, "length_responses": t.lengthResponses,
		"attempts_without_usage": t.missingUsage, "usage_complete_for_responses": t.missingUsage == 0,
		"ledger_write_ok": t.writeErr == nil, "scope": "fresh HTTP requests only; includes length responses; historical annotation usage excluded"}
}

type replayedBody struct {
	*bytes.Reader
	readErr error
}

func (r *replayedBody) Read(buffer []byte) (int, error) {
	n, err := r.Reader.Read(buffer)
	if err == io.EOF && r.readErr != nil {
		return n, r.readErr
	}
	return n, err
}
func (*replayedBody) Close() error { return nil }
