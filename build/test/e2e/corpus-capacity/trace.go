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
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

type syncWriter interface {
	Write([]byte) (int, error)
	Sync() error
}

// durableLedger serializes admission and sticky failures. Once persistence fails,
// the shared context is cancelled; already admitted calls may finish, none are hidden.
type durableLedger struct {
	mu     sync.Mutex
	file   syncWriter
	cancel context.CancelFunc
	err    error
	rows   int
}

func (l *durableLedger) append(row any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	raw, err := json.Marshal(row)
	if err == nil {
		var n int
		n, err = l.file.Write(append(raw, '\n'))
		if err == nil && n != len(raw)+1 {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = l.file.Sync()
	}
	if err != nil {
		l.err = err
		if l.cancel != nil {
			l.cancel()
		}
		return err
	}
	l.rows++
	return nil
}
func (l *durableLedger) failure() error { l.mu.Lock(); defer l.mu.Unlock(); return l.err }

type tokenCount struct {
	Input      int    `json:"input_tokens"`
	Reserved   int    `json:"reserved_output_tokens"`
	Context    int    `json:"context_limit"`
	CounterID  string `json:"counter_id"`
	RuntimeSHA string `json:"runtime_receipt_sha256"`
}
type tokenCounter interface {
	Count(context.Context, []byte) (tokenCount, error)
}

type callKey struct{}
type callInfo struct {
	ID        int64
	Stage     string
	Document  string
	Documents []string
	Attempts  atomic.Int64
	Mode      string
	Phase     string
	SourceSHA string
}

type boundedClient struct {
	next     llm.Client
	sem      chan struct{}
	sequence atomic.Int64
	active   atomic.Int64
	peak     atomic.Int64
	ledger   *durableLedger
}

func (c *boundedClient) ProviderName() string { return c.next.ProviderName() }
func (c *boundedClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := c.ledger.failure(); err != nil {
		return nil, fmt.Errorf("capacity: ledger unavailable")
	}
	n := c.active.Add(1)
	defer c.active.Add(-1)
	for old := c.peak.Load(); n > old; old = c.peak.Load() {
		if c.peak.CompareAndSwap(old, n) {
			break
		}
	}
	info := &callInfo{ID: c.sequence.Add(1)}
	if binding, ok := ctx.Value(seededBindingKey{}).(seededCallBinding); ok {
		// 修订 JSON 中的路径可能只出现在不可信意见中；只用已验证的原文绑定。
		// Seeded calls use explicit original-bound identity, never prompt regex guesses.
		if err := binding.bind(req, info); err != nil {
			return nil, err
		}
	} else {
		info.Stage, info.Document, info.Documents = stage(req.System), documentIdentity(req.User), documentIdentities(req.User)
	}
	start := map[string]any{"event": "logical_start", "call_id": info.ID, "stage": info.Stage, "document_id": info.Document, "document_ids": info.Documents, "time_utc": now()}
	info.addSeedIdentity(start)
	if err := c.ledger.append(start); err != nil {
		return nil, fmt.Errorf("capacity: logical receipt failed")
	}
	response, err := c.next.Complete(context.WithValue(ctx, callKey{}, info), req)
	end := map[string]any{"event": "logical_end", "call_id": info.ID, "stage": info.Stage, "wire_attempts": info.Attempts.Load(), "complete_error": err != nil, "time_utc": now()}
	info.addSeedIdentity(end)
	if e := c.ledger.append(end); e != nil {
		return response, fmt.Errorf("capacity: completion receipt failed")
	}
	return response, err
}

// Classification observes unchanged prompts. An unfamiliar prompt is explicit;
// the system SHA remains authoritative if a later implementation adds a stage.
func stage(system string) string {
	for _, s := range []struct{ prefix, name string }{
		{"你是知识结构化标注专家。", "annotation"}, {"你是知识标注的语义核验专家。", "semantic_gate"},
		{"你是领域知识融合专家。", "extract"}, {"你是领域知识图谱的修正助手。", "repair"},
		{"你是一个文件命名助手。", "file_slug"}, {"你是跨子域关系的原文验证专家。", "cross_relation_verify"},
		{"你是知识融合重写专家。", "describe"}, {"你是领域知识关系识别专家。", "relate"},
	} {
		if strings.HasPrefix(system, s.prefix) {
			return s.name
		}
	}
	if strings.Contains(system, "已有 domain/subdomain/融合节点）、") {
		return "backfill"
	}
	if strings.HasPrefix(system, "你是领域知识融合的补全助手。") {
		return "coverage"
	}
	if strings.Contains(system, "description") && strings.Contains(system, "融合") {
		return "describe"
	}
	if strings.Contains(system, "关系") && strings.Contains(system, "subdomain") {
		return "relate"
	}
	return "unknown"
}

func knownStageName(name string) bool {
	switch name {
	case "annotation", "semantic_gate", "extract", "repair", "file_slug", "describe", "relate", "backfill", "coverage", "cross_relation_verify":
		return true
	}
	return false
}

var documentRE = regexp.MustCompile(`references/([a-f0-9]{64})\.md`)

func documentIdentity(user string) string {
	v := documentRE.FindStringSubmatch(user)
	if len(v) > 1 {
		return v[1]
	}
	return "not_exposed_by_prompt"
}

// Multi-source verifier calls must not be attributed only to the first matching path.
func documentIdentities(user string) []string {
	seen := map[string]bool{}
	for _, match := range documentRE.FindAllStringSubmatch(user, -1) {
		seen[match[1]] = true
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

type wireRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens      int `json:"max_tokens"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
	Thinking struct {
		Type string `json:"type"`
	} `json:"thinking"`
}

type traceTransport struct {
	next           http.RoundTripper
	ledger         *durableLedger
	counter        tokenCounter
	sequence       atomic.Int64
	sent           atomic.Int64
	responses      atomic.Int64
	missingUsage   atomic.Int64
	input          atomic.Int64
	output         atomic.Int64
	identityFailed atomic.Bool
	seedPolicy     *seededWirePolicy // nil preserves the historical capacity profile.
}

func (t *traceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	start := time.Now()
	id := t.sequence.Add(1)
	info, ok := request.Context().Value(callKey{}).(*callInfo)
	if !ok {
		return nil, fmt.Errorf("capacity: missing logical call identity")
	}
	attempt := info.Attempts.Add(1)
	row := map[string]any{"event": "request", "http_attempt": id, "call_id": info.ID, "provider_attempt": attempt, "stage": info.Stage, "document_id": info.Document, "document_ids": info.Documents, "time_utc": now(), "admitted": false}
	info.addSeedIdentity(row)
	reject := func(class string) (*http.Response, error) {
		row["rejection"] = class
		_ = t.ledger.append(row)
		return nil, fmt.Errorf("capacity: request rejected (%s)", class)
	}
	if t.identityFailed.Load() {
		return reject("previous_model_identity_failure")
	}
	if request.URL.String() != "https://api.deepseek.com/chat/completions" || request.Method != "POST" {
		return reject("endpoint_or_method")
	}
	if request.GetBody == nil {
		return reject("request_not_replayable")
	}
	body, err := request.GetBody()
	if err != nil {
		return reject("request_body")
	}
	raw, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return reject("request_body")
	}
	row["request_sha256"], row["request_bytes"] = sha(raw), len(raw)
	var parsed wireRequest
	if json.Unmarshal(raw, &parsed) != nil || len(parsed.Messages) != 2 || parsed.Model != "deepseek-flash" || parsed.MaxTokens < 1 || parsed.MaxTokens > 393216 {
		return reject("request_contract")
	}
	row["requested_model"], row["max_tokens"] = parsed.Model, parsed.MaxTokens
	row["thinking"], row["response_format"] = parsed.Thinking, parsed.ResponseFormat
	for i, m := range parsed.Messages {
		if (i == 0 && m.Role != "system") || (i == 1 && m.Role != "user") {
			return reject("message_roles")
		}
		prefix := m.Role + "_"
		row[prefix+"bytes"], row[prefix+"codepoints"], row[prefix+"sha256"] = len(m.Content), utf8.RuneCountInString(m.Content), sha([]byte(m.Content))
	}
	if t.counter == nil {
		return reject("tokenizer_unavailable")
	}
	count, err := t.counter.Count(request.Context(), raw)
	if err != nil {
		return reject("tokenizer_unverified")
	}
	row["token_count"] = count
	if count.Input < 1 || count.Reserved != parsed.MaxTokens || count.Context != 1000000 || count.CounterID == "" || count.RuntimeSHA == "" || count.Input > count.Context-count.Reserved {
		return reject("context_reservation")
	}
	if request.Context().Err() != nil {
		return reject("cancelled")
	}
	if t.seedPolicy != nil {
		if err := t.seedPolicy.admit(info, parsed, raw, count); err != nil {
			return reject("seeded_phase_profile_or_wire_budget")
		}
	}
	row["admitted"] = true
	if err = t.ledger.append(row); err != nil {
		return nil, fmt.Errorf("capacity: predispatch receipt failed")
	}
	// The durable request event defines admission. A concurrently failed call may
	// cancel it afterwards; it remains in the ledger and is drained conservatively.
	t.sent.Add(1)
	response, networkErr := t.next.RoundTrip(request)
	outcome := map[string]any{"event": "response", "http_attempt": id, "call_id": info.ID, "provider_attempt": attempt, "stage": info.Stage, "time_utc": now(), "seconds": time.Since(start).Seconds(), "usage_status": "unknown"}
	info.addSeedIdentity(outcome)
	if networkErr != nil || response == nil {
		outcome["transport_error"] = true
		if request.Context().Err() != nil {
			outcome["cancelled"] = true
		}
		_ = t.ledger.append(outcome)
		if t.seedPolicy != nil && t.ledger.cancel != nil {
			t.ledger.cancel()
		}
		if networkErr == nil {
			networkErr = errors.New("capacity: nil HTTP response")
		}
		return response, networkErr
	}
	t.responses.Add(1)
	rawResponse, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	response.Body = &replayedBody{Reader: bytes.NewReader(rawResponse), readErr: readErr}
	outcome["seconds"] = time.Since(start).Seconds()
	outcome["http_status"], outcome["response_sha256"], outcome["response_bytes"] = response.StatusCode, sha(rawResponse), len(rawResponse)
	if readErr != nil {
		outcome["response_read_error"] = true
	}
	if date, e := http.ParseTime(response.Header.Get("Date")); e == nil {
		outcome["provider_date"] = date.UTC().Format(time.RFC3339)
	}
	for _, key := range []string{"X-Request-Id", "X-Deepseek-Request-Id"} {
		if value := response.Header.Get(key); value != "" {
			outcome[strings.ToLower(key)+"_sha256"] = sha([]byte(value))
		}
	}
	var parsedResponse struct {
		ID      string      `json:"id"`
		Model   string      `json:"model"`
		Created json.Number `json:"created"`
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			Input  *int64 `json:"prompt_tokens"`
			Output *int64 `json:"completion_tokens"`
			Total  *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(rawResponse, &parsedResponse) != nil {
		outcome["response_json"] = false
	} else {
		outcome["response_json"] = true
		outcome["returned_model"] = "unknown"
		if parsedResponse.Model == "deepseek-flash" || parsedResponse.Model == "deepseek-v4.1-flash-1017" {
			outcome["returned_model"] = parsedResponse.Model
		}
		if parsedResponse.Model != "" {
			outcome["returned_model_sha256"] = sha([]byte(parsedResponse.Model))
		}
		outcome["model_identity_expected"] = outcome["returned_model"] != "unknown"
		if response.StatusCode == 200 && outcome["model_identity_expected"] == false {
			t.identityFailed.Store(true)
			if t.ledger.cancel != nil {
				t.ledger.cancel()
			}
		}
		if parsedResponse.ID != "" {
			outcome["response_id_sha256"] = sha([]byte(parsedResponse.ID))
			if uuidRE.MatchString(parsedResponse.ID) {
				outcome["response_id"] = parsedResponse.ID
			}
		}
		if len(parsedResponse.Choices) > 0 {
			if t.seedPolicy != nil {
				outcome["final_content_sha256"] = sha([]byte(parsedResponse.Choices[0].Message.Content))
			}
			switch finish := parsedResponse.Choices[0].Finish; finish {
			case "stop", "length", "content_filter", "tool_calls", "insufficient_system_resource":
				outcome["finish_reason"] = finish
			default:
				outcome["finish_reason"] = "unknown"
			}
		}
		u := parsedResponse.Usage
		if u != nil && u.Input != nil && u.Output != nil && *u.Input >= 0 && *u.Output >= 0 {
			outcome["usage_status"] = "reported"
			outcome["input_tokens"], outcome["output_tokens"] = *u.Input, *u.Output
			if u.Total != nil && *u.Total >= 0 {
				outcome["total_tokens"] = *u.Total
			}
			t.input.Add(*u.Input)
			t.output.Add(*u.Output)
			outcome["local_input_tokens"] = count.Input
			outcome["provider_minus_local_input_tokens"] = *u.Input - int64(count.Input)
		}
	}
	if outcome["usage_status"] == "unknown" {
		t.missingUsage.Add(1)
	}
	// 单次资格/修订/验证都没有付费重试；异常先取消，再保留原响应供原客户端解析。
	// A failed seeded outcome cancels before the production client's retry loop.
	if t.seedPolicy != nil && (readErr != nil || response.StatusCode != 200 || outcome["finish_reason"] != "stop" || outcome["usage_status"] != "reported" || outcome["model_identity_expected"] != true) && t.ledger.cancel != nil {
		t.ledger.cancel()
	}
	// Returning the original response preserves production parsing and retry policy.
	// A failed outcome write cancels the shared run; audit cannot call it complete.
	_ = t.ledger.append(outcome)
	return response, nil
}

var uuidRE = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

type replayedBody struct {
	*bytes.Reader
	readErr error
}

func (r *replayedBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && r.readErr != nil {
		return n, r.readErr
	}
	return n, err
}
func (*replayedBody) Close() error { return nil }

func openLedger(path string, cancel context.CancelFunc) (*durableLedger, *os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, nil, err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return nil, nil, err
	}
	dir, err := os.Open(filepathDir(path))
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return &durableLedger{file: f, cancel: cancel}, f, nil
}
func filepathDir(path string) string {
	if p := strings.LastIndex(path, string(os.PathSeparator)); p >= 0 {
		return path[:p]
	}
	return "."
}
