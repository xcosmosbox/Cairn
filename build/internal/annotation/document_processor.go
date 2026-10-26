package annotation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// DocumentOutcome 只表示本篇标注范围；完整产品状态由集成调用者汇总。
// DocumentOutcome describes annotation only, never product-level completeness.
type DocumentOutcome string

const (
	DocumentComplete    DocumentOutcome = "complete"
	DocumentPartial     DocumentOutcome = "partial"
	DocumentUnavailable DocumentOutcome = "unavailable"
	DocumentCancelled   DocumentOutcome = "cancelled"
)

// DocumentAttempt 记录实际失败步骤；CandidateID 标识该次生成/核验的精确候选。
// DocumentAttempt records actual work, including rejected or unavailable candidates.
type DocumentAttempt struct {
	Number      int    `json:"number"`
	Step        string `json:"step"`
	CandidateID string `json:"candidate_id,omitempty"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason,omitempty"`
}

// DocumentResult 的 Document 仅在 complete 时可供下游使用，其余是最后候选证据。
// Document is authoritative only when Outcome is DocumentComplete.
type DocumentResult struct {
	Skill       string                     `json:"skill"`
	FilePath    string                     `json:"file_path"`
	Document    *dktypes.AnnotatedDocument `json:"document,omitempty"`
	CandidateID string                     `json:"candidate_id,omitempty"`
	Outcome     DocumentOutcome            `json:"outcome"`
	Reason      string                     `json:"reason,omitempty"`
	Attempts    []DocumentAttempt          `json:"attempts"`
	Exhausted   bool                       `json:"exhausted"`
}

// DocumentProcessor 对一篇原文进行有限线性修订，绝不嵌套组件重试。
// One shared extra-recovery allowance bounds all failed steps in this document.
type DocumentProcessor struct {
	annotator  *LLMAnnotator
	gate       *SemanticGate
	maxRetries int
}

// NewDocumentProcessor 的 maxRetries>0 为共享额外恢复次数，0 默认3，负值禁用。
// Initial generation/assessment do not consume recovery; at most 2+2N model calls occur.
// Provider HTTP retries remain separate; this helper never adds a nested retry loop.
func NewDocumentProcessor(client llm.Client, maxTokens, maxRetries, maxNameRunes int) *DocumentProcessor {
	return &DocumentProcessor{annotator: NewLLMAnnotator(client, maxTokens, -1), gate: NewSemanticGate(client, maxTokens, -1), maxRetries: recoveryAllowance(maxRetries)}
}

func (p *DocumentProcessor) Process(ctx context.Context, skill, path, original string) DocumentResult {
	result := DocumentResult{Skill: skill, FilePath: path, Outcome: DocumentUnavailable, Attempts: []DocumentAttempt{}}
	finish := func(err error, exhausted bool) DocumentResult {
		result.Reason = err.Error()
		result.Exhausted = exhausted
		var defect *SemanticFeedback
		switch {
		case callerCancelled(ctx, err):
			result.Outcome = DocumentCancelled
		case errors.As(err, &defect):
			result.Outcome = DocumentPartial
		default:
			result.Outcome = DocumentUnavailable
		}
		return result
	}
	if err := ctx.Err(); err != nil {
		return finish(err, false)
	}
	if p == nil || p.annotator == nil || p.gate == nil {
		return finish(permanentError(fmt.Errorf("annotation: nil document processor")), false)
	}
	if err := validateInput(p.annotator.client, p.annotator.maxTokens, original); err != nil {
		return finish(err, false)
	}
	if strings.TrimSpace(original) == "" {
		result.Document = &dktypes.AnnotatedDocument{Skill: skill, FilePath: path}
		result.CandidateID = candidateIdentity(result.Document)
		result.Outcome = DocumentComplete
		return result
	}
	remaining := p.maxRetries
	step, feedback := "annotate", ""
	record := func(id string, err error) {
		outcome, reason := "generated", ""
		if step == "assess" {
			outcome = "complete"
		}
		if err != nil {
			reason = err.Error()
			outcome = "unavailable"
			var classified *processingError
			var defect *SemanticFeedback
			switch {
			case callerCancelled(ctx, err):
				outcome = "cancelled"
			case errors.As(err, &defect):
				outcome = "rejected"
			case errors.As(err, &classified):
				outcome = classified.kind
			}
		}
		result.Attempts = append(result.Attempts, DocumentAttempt{Number: len(result.Attempts) + 1, Step: step, CandidateID: id, Outcome: outcome, Reason: reason})
	}
	for {
		if err := ctx.Err(); err != nil {
			return finish(err, false)
		}
		var err error
		id := result.CandidateID
		if step == "assess" {
			err = p.gate.assessOnce(ctx, original, result.Document)
		} else {
			var candidate *dktypes.AnnotatedDocument
			if step == "revise" {
				candidate = result.Document
			}
			var doc *dktypes.AnnotatedDocument
			doc, id, err = p.annotator.generateOnce(ctx, skill, path, original, candidate, feedback)
			if err == nil {
				result.Document = doc
				result.CandidateID = id
			}
		}
		record(id, err)
		if err == nil {
			if step == "assess" {
				result.Outcome = DocumentComplete
				result.Reason = ""
				return result
			}
			step = "assess"
			continue
		}
		if terminalError(err) {
			return finish(err, false)
		}
		if remaining == 0 {
			return finish(err, true)
		}
		remaining--
		var defect *SemanticFeedback
		if errors.As(err, &defect) {
			feedback = defect.Error()
			step = "revise"
		}
		// Invalid assessment repeats this exact candidate; generation errors repeat only
		// that generation/revision. No successful sibling or previous step is replayed.
	}
}

func recoveryAllowance(n int) int {
	if n == 0 {
		return DefaultAnnotateMaxRetries
	}
	if n < 0 {
		return 0
	}
	return n
}
func textIdentity(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
func candidateIdentity(doc *dktypes.AnnotatedDocument) string {
	b, _ := json.Marshal(doc)
	return textIdentity(string(b))
}

type processingError struct {
	kind string
	err  error
}

func (e *processingError) Error() string { return e.err.Error() }
func (e *processingError) Unwrap() error { return e.err }
func permanentError(err error) error     { return &processingError{kind: "permanent", err: err} }
func invalidError(err error) error       { return &processingError{kind: "invalid", err: err} }
func unavailableError(err error) error   { return &processingError{kind: "unavailable", err: err} }
func terminalError(err error) bool {
	var e *processingError
	if errors.As(err, &e) {
		return e.kind == "permanent"
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
func callerCancelled(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var e *processingError
	return !errors.As(err, &e) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}
func validateInput(client llm.Client, maxTokens int, source string) error {
	if client == nil || (reflect.ValueOf(client).Kind() == reflect.Ptr && reflect.ValueOf(client).IsNil()) {
		return permanentError(fmt.Errorf("annotation: nil llm client"))
	}
	if maxTokens < 0 {
		return permanentError(fmt.Errorf("annotation: negative maxTokens"))
	}
	if !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return permanentError(fmt.Errorf("annotation: unsupported source encoding or NUL"))
	}
	return nil
}

var providerStatus = regexp.MustCompile(`^(?:openai_compatible|anthropic): status ([0-9]{3})(?::|$)`)

func modelError(err error) error {
	var declared interface{ Permanent() bool }
	if errors.As(err, &declared) && declared.Permanent() {
		return permanentError(err)
	}
	// Current providers expose plain errors. Inspect the status before its body:
	// a 503 body mentioning API keys or max_tokens is still service unavailability.
	msg := err.Error()
	if match := providerStatus.FindStringSubmatch(msg); len(match) > 0 {
		status, _ := strconv.Atoi(match[1])
		switch status {
		case 400, 401, 403, 404, 405, 413, 422:
			return permanentError(err)
		}
		return unavailableError(err)
	}
	for _, prefix := range []string{
		"openai_compatible: nil renderer", "openai_compatible: max_tokens ",
		"openai_compatible: DeepSeek accepts at most 16 stop sequences",
		"openai_compatible: marshal request:", "openai_compatible: build request:",
		"anthropic: marshal request:", "anthropic: build request:",
		"openai_compatible: missing API key", "anthropic: missing API key",
	} {
		if strings.HasPrefix(msg, prefix) {
			return permanentError(err)
		}
	}
	if msg == "missing API key" {
		return permanentError(err)
	}
	return unavailableError(err)
}

// decodeUniqueObject 拒绝重复键，否则后一个 supported/omissions 会抹掉先前反证。
// Duplicate assessment/candidate fields must never erase negative evidence.
func decodeUniqueObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid object key")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate JSON field %q", key)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON data")
	}
	return fields, nil
}
