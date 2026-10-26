package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RawTraceTransport 只记录实际生产请求/响应；未知用量和记录失败不控制执行。
// This observer never retries, admits or cancels model or mutation work.
type RawTraceTransport struct {
	next     http.RoundTripper
	dir      string
	file     *os.File
	sequence atomic.Int64
	mu       sync.Mutex
	rows     []json.RawMessage
	sinkErr  error
}

// NewRawTraceTransport 目录/文件失败只告警；调用者仍能获得观察器。
// No headers or endpoint URLs are persisted. A nil next uses DefaultTransport.
func NewRawTraceTransport(dir string, next http.RoundTripper) *RawTraceTransport {
	if next == nil {
		next = http.DefaultTransport
	}
	t := &RawTraceTransport{next: next, dir: dir}
	err := os.MkdirAll(dir, 0700)
	if err == nil {
		t.file, err = os.OpenFile(filepath.Join(dir, "requests.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
	if err != nil {
		t.sinkErr = err
		log.Printf("WARN raw model trace unavailable; observed outcomes remain in memory")
	}
	return t
}

func (t *RawTraceTransport) append(row map[string]any) {
	raw, err := json.Marshal(row)
	if err != nil {
		log.Printf("WARN raw model outcome serialization failed")
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows = append(t.rows, json.RawMessage(raw))
	if t.file == nil {
		return
	}
	n, err := t.file.Write(append(raw, '\n'))
	if err == nil && n != len(raw)+1 {
		err = io.ErrShortWrite
	}
	if err != nil {
		t.sinkErr = err
		log.Printf("WARN raw model trace write failed; retaining observed outcomes in memory")
	}
}

// Close 排出已观察到的结果，不重放调用；返回值只说明该记录落点不可用。
// Sink errors are evidence warnings, not construction failure or retry signals.
func (t *RawTraceTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file != nil {
		if err := t.file.Sync(); err != nil {
			t.sinkErr = err
		}
		if err := t.file.Close(); err != nil {
			t.sinkErr = err
		}
		t.file = nil
	}
	if t.sinkErr != nil {
		raw, err := json.Marshal(t.rows)
		if err == nil {
			var f *os.File
			f, err = os.OpenFile(filepath.Join(t.dir, "observed-outcomes.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				_, err = f.Write(raw)
				if e := f.Close(); err == nil {
					err = e
				}
			}
		}
		if err != nil {
			log.Printf("WARN raw observed outcomes could not be drained")
		}
	}
	return t.sinkErr
}

func traceHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func traceRedact(raw []byte, secrets []string) []byte {
	for _, secret := range secrets {
		if secret != "" {
			raw = bytes.ReplaceAll(raw, []byte(secret), []byte("[REDACTED]"))
		}
	}
	return raw
}

var traceDocuments = regexp.MustCompile(`references/([a-f0-9]{64})\.md`)

func traceDocumentIDs(raw []byte) []string {
	set := map[string]bool{}
	for _, m := range traceDocuments.FindAllSubmatch(raw, -1) {
		set[string(m[1])] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (t *RawTraceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	id := t.sequence.Add(1)
	start := time.Now()
	// 用实际流而非GetBody副本绑定父请求；读取后交还同样的字节。
	// The bytes forwarded to the provider identify the parent of this result.
	var raw []byte
	var err error
	if request.Body != nil {
		raw, err = io.ReadAll(request.Body)
		request.Body.Close()
	}
	secrets := []string{strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "), request.Header.Get("X-Api-Key"), request.Header.Get("Api-Key"), os.Getenv("CAIRN_LLM_API_KEY")}
	clean := traceRedact(raw, secrets)
	parent := traceHash(raw)
	row := map[string]any{"event": "request", "attempt_id": id, "time_utc": start.UTC().Format(time.RFC3339Nano), "document_ids": traceDocumentIDs(raw), "request_sha256": parent, "request_body": clean, "body_redacted": !bytes.Equal(raw, clean)}
	if err != nil {
		row["error"] = string(traceRedact([]byte(err.Error()), secrets))
		t.append(row)
		return nil, fmt.Errorf("raw trace: request body unavailable")
	}
	t.append(row)
	request.Body = io.NopCloser(bytes.NewReader(raw))
	request.ContentLength = int64(len(raw))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	response, networkErr := t.next.RoundTrip(request)
	outcome := map[string]any{"event": "response", "attempt_id": id, "request_sha256": parent, "document_ids": traceDocumentIDs(raw), "time_utc": time.Now().UTC().Format(time.RFC3339Nano), "seconds": time.Since(start).Seconds(), "usage_status": "unknown", "input_tokens": nil, "output_tokens": nil, "total_tokens": nil}
	if networkErr != nil || response == nil {
		if networkErr == nil {
			networkErr = errors.New("missing HTTP response")
		}
		outcome["outcome"] = "transport_error"
		outcome["error"] = string(traceRedact([]byte(networkErr.Error()), secrets))
		t.append(outcome)
		return response, networkErr
	}
	var responseRaw []byte
	var readErr error
	if response.Body == nil {
		readErr = errors.New("missing response body")
	} else {
		responseRaw, readErr = io.ReadAll(response.Body)
		response.Body.Close()
	}
	response.Body = &rawTraceBody{Reader: bytes.NewReader(responseRaw), readErr: readErr}
	clean = traceRedact(responseRaw, secrets)
	outcome["http_status"] = response.StatusCode
	outcome["response_sha256"] = traceHash(responseRaw)
	outcome["response_body"] = clean
	outcome["body_redacted"] = !bytes.Equal(responseRaw, clean)
	outcome["outcome"] = "observed_response"
	if readErr != nil {
		outcome["outcome"] = "response_read_error"
		outcome["error"] = string(traceRedact([]byte(readErr.Error()), secrets))
	}
	var parsed struct {
		Model string `json:"model"`
		Usage *struct {
			Input  *int64 `json:"prompt_tokens"`
			Output *int64 `json:"completion_tokens"`
			Total  *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(responseRaw, &parsed) != nil {
		outcome["response_json"] = false
	} else {
		outcome["response_json"] = true
		outcome["returned_model"] = string(traceRedact([]byte(parsed.Model), secrets))
		if u := parsed.Usage; u != nil {
			if u.Input != nil && *u.Input >= 0 {
				outcome["input_tokens"] = *u.Input
			}
			if u.Output != nil && *u.Output >= 0 {
				outcome["output_tokens"] = *u.Output
			}
			if u.Total != nil && *u.Total >= 0 {
				outcome["total_tokens"] = *u.Total
			}
			if outcome["input_tokens"] != nil && outcome["output_tokens"] != nil {
				outcome["usage_status"] = "reported"
			}
		}
	}
	if outcome["usage_status"] == "unknown" {
		outcome["usage_warning"] = "missing or partial provider usage; no inferred zero"
	}
	t.append(outcome)
	return response, nil
}

type rawTraceBody struct {
	*bytes.Reader
	readErr error
}

func (r *rawTraceBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && r.readErr != nil {
		return n, r.readErr
	}
	return n, err
}
func (*rawTraceBody) Close() error { return nil }
