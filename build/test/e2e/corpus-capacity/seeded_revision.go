package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	buildmeta "github.com/xcosmosbox/cairn/build/internal"
	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

const seededMode = "fixed-r3-qualification-revision-validation/v1"
const seededDocument = "6f388affbc74a3719f00167cd84e7a04981655e63c7acdab04d0c82d57ebb92b"
const seededOriginalSHA = "96c295b30c38e1cf2421ca4615ce375a7012a68b3f81ec05947fa4a03a519309"
const seededCandidateSHA = "7b7a3d15e711f0857d3899c981e79b17eb7f47a5a7b694ec59d1e983e63de34a"
const seededRejectionSHA = "c2d503378f06c25940c2e698dd00b78763fafeb582d7de9a00734e73a018b735"
const seededArchive = "1a44fa27db4221778da3986efa5f4414c6f27611"
const seededMaxTokens = 65536

// seededPlan 只描述新实验的固定输入，不把旧错误字符串冒充新类型化判决。
// A new plan pins historical bytes; old wrapped error prose is provenance only.
type seededPlan struct {
	Format               string `json:"format"`
	Mode                 string `json:"mode"`
	ArchiveCommit        string `json:"archive_commit"`
	Document             string `json:"document_id"`
	OriginalSHA          string `json:"original_sha256"`
	CandidateSHA         string `json:"candidate_sha256"`
	ArchivedRejectionSHA string `json:"archived_rejection_sha256"`
	MaxTokens            int    `json:"max_tokens"`
	RequestSeconds       int    `json:"request_timeout_seconds"`
	RunSeconds           int    `json:"run_timeout_seconds"`
	MaxWires             int    `json:"max_actual_wires"`
	PerCallWires         int    `json:"max_wires_per_call"`
}

type seededInputs struct {
	Original                         string
	Candidate                        *dktypes.AnnotatedDocument
	SourceSHA, Document, Skill, Path string
	ArchivedRejectionSHA             string
}

// seededCounterIdentity 绑定计数器实际配置；计数值相等不能掩盖运行时替换。
// Bind executable, adapter and runtime pins, not merely an equal token count.
type seededCounterIdentity struct {
	PythonSHA  string `json:"python_sha256"`
	AdapterSHA string `json:"adapter_sha256"`
	RuntimeSHA string `json:"runtime_receipt_sha256"`
	CounterID  string `json:"counter_id"`
}

func (c externalCounter) seededIdentity() seededCounterIdentity {
	return seededCounterIdentity{c.PythonSHA, c.AdapterSHA, c.ReceiptSHA, "deepseek-recipe@935817c66f5d6e64838f419692545f06787e485f/v41"}
}
func counterSeedIdentity(counter tokenCounter) (seededCounterIdentity, error) {
	c, ok := counter.(interface{ seededIdentity() seededCounterIdentity })
	if !ok {
		return seededCounterIdentity{}, fmt.Errorf("seeded: explicit counter identity required")
	}
	i := c.seededIdentity()
	if i.PythonSHA == "" || i.AdapterSHA == "" || i.RuntimeSHA == "" || i.CounterID == "" {
		return i, fmt.Errorf("seeded: incomplete counter configuration")
	}
	return i, nil
}
func (i seededCounterIdentity) matches(count tokenCount) bool {
	return count.RuntimeSHA == i.RuntimeSHA && count.CounterID == i.CounterID && count.Reserved == seededMaxTokens && count.Context == 1000000 && count.Input > 0 && count.Input <= count.Context-count.Reserved
}

func loadSeededInputs(root, planPath, pin string) (seededInputs, seededPlan, error) {
	var in seededInputs
	var p seededPlan
	raw, err := os.ReadFile(planPath)
	if err != nil || pin == "" || sha(raw) != pin {
		return in, p, fmt.Errorf("seeded: plan identity mismatch")
	}
	if decodeStrict(raw, &p) != nil || p.Format != "cairn-seeded-revision-plan/v1" || p.Mode != seededMode || p.ArchiveCommit != seededArchive || p.Document != seededDocument || p.OriginalSHA != seededOriginalSHA || p.CandidateSHA != seededCandidateSHA || p.ArchivedRejectionSHA != seededRejectionSHA || p.MaxTokens != seededMaxTokens || p.RequestSeconds != 600 || p.RunSeconds != 1800 || p.MaxWires != 3 || p.PerCallWires != 1 {
		return in, p, fmt.Errorf("seeded: fixed R3 profile mismatch")
	}
	rel := "references/" + seededDocument + ".md"
	paths := []struct {
		path, hash string
		size       int
	}{
		{"original/" + rel, seededOriginalSHA, 71332},
		{"stages/02_gate_a/multihop-public/references_" + seededDocument + "_r3.json", seededCandidateSHA, 27992},
		{"stages/03_gate_b/multihop-public/references_" + seededDocument + "_r3_fail.json", seededRejectionSHA, 656},
	}
	var blobs [][]byte
	for _, item := range paths {
		b, e := os.ReadFile(filepath.Join(root, item.path))
		if e != nil || len(b) != item.size || sha(b) != item.hash {
			return in, p, fmt.Errorf("seeded: historical input identity mismatch")
		}
		blobs = append(blobs, b)
	}
	var candidate dktypes.AnnotatedDocument
	if decodeStrict(blobs[1], &candidate) != nil {
		return in, p, fmt.Errorf("seeded: candidate schema mismatch")
	}
	var old struct {
		Passed bool   `json:"passed"`
		Reason string `json:"reason"`
	}
	if decodeStrict(blobs[2], &old) != nil || old.Passed || strings.TrimSpace(old.Reason) == "" {
		return in, p, fmt.Errorf("seeded: archived rejection provenance invalid")
	}
	in = seededInputs{Original: string(blobs[0]), Candidate: &candidate, SourceSHA: p.OriginalSHA, Document: p.Document, Skill: "multihop-public", Path: rel, ArchivedRejectionSHA: p.ArchivedRejectionSHA}
	if err := in.validate(); err != nil || len(candidate.Items) != 57 {
		return seededInputs{}, p, fmt.Errorf("seeded: source/candidate binding invalid")
	}
	// Candidate and original spans are unknown; no graph UUID/ID is introduced.
	for _, item := range candidate.Items {
		if item.ID != "" || item.SourceSpan != nil {
			return seededInputs{}, p, fmt.Errorf("seeded: unexpected identity or original span")
		}
	}
	return in, p, nil
}

func (in seededInputs) validate() error {
	if in.Original == "" || sha([]byte(in.Original)) != in.SourceSHA || in.Document == "" || in.Candidate == nil || in.Candidate.Skill != in.Skill || in.Candidate.FilePath != in.Path || len(in.Candidate.Items) == 0 {
		return fmt.Errorf("seeded: ambiguous or mismatched source")
	}
	return nil
}

type seededBindingKey struct{}
type seededCallBinding struct {
	Phase, Document, SourceSHA, Original string
	SystemSHA, UserSHA                   string
}

func (b seededCallBinding) bind(req llm.CompleteRequest, info *callInfo) error {
	if b.Document == "" || b.SourceSHA == "" || sha([]byte(b.Original)) != b.SourceSHA || sha([]byte(req.System)) != b.SystemSHA || sha([]byte(req.User)) != b.UserSHA || req.MaxTokens != seededMaxTokens {
		return fmt.Errorf("seeded: request/source binding mismatch")
	}
	switch b.Phase {
	case "revision":
		var input struct {
			Original  string          `json:"original"`
			Candidate json.RawMessage `json:"candidate"`
			Critique  string          `json:"critique"`
		}
		if decodeStrict([]byte(req.User), &input) != nil || input.Original != b.Original || sha([]byte(input.Original)) != b.SourceSHA || len(input.Candidate) == 0 || strings.TrimSpace(input.Critique) == "" {
			return fmt.Errorf("seeded: decoded revision original mismatch")
		}
		info.Stage = "annotation"
	case "qualification", "validation":
		prefix := "# 语义幂等核验任务\n\n## 原始文档 / original document\n"
		rest := strings.TrimPrefix(req.User, prefix)
		if rest == req.User || !strings.HasPrefix(rest, b.Original) || sha([]byte(rest[:len(b.Original)])) != b.SourceSHA {
			return fmt.Errorf("seeded: gate original mismatch")
		}
		info.Stage = "semantic_gate"
	default:
		return fmt.Errorf("seeded: unknown phase")
	}
	info.Mode, info.Phase, info.SourceSHA = seededMode, b.Phase, b.SourceSHA
	info.Document, info.Documents = b.Document, []string{b.Document}
	return nil
}

func (info *callInfo) addSeedIdentity(row map[string]any) {
	if info.Mode != "" {
		row["operation_mode"], row["phase"], row["original_sha256"] = info.Mode, info.Phase, info.SourceSHA
	}
}

// seededWirePolicy 是现有 traceTransport 的可选入场条件，不替代其网络和持久化路径。
// Optional admission on the existing transport bounds phases and forbids wire retries.
type seededWirePolicy struct {
	mu                  sync.Mutex
	Document, SourceSHA string
	Counter             seededCounterIdentity
	admitted            int
}

func (p *seededWirePolicy) admit(info *callInfo, req wireRequest, raw []byte, count tokenCount) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	phases := []string{"qualification", "revision", "validation"}
	if !p.Counter.matches(count) || p.admitted >= len(phases) || info.Phase != phases[p.admitted] || info.Mode != seededMode || info.Document != p.Document || len(info.Documents) != 1 || info.Documents[0] != p.Document || info.SourceSHA != p.SourceSHA || info.Attempts.Load() != 1 || req.MaxTokens != seededMaxTokens || req.Model != "deepseek-flash" || req.Thinking.Type != "enabled" || req.ResponseFormat.Type != "json_object" {
		return fmt.Errorf("seeded: phase/profile/wire budget")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return fmt.Errorf("seeded: unexpected wire overrides")
	}
	for _, key := range []string{"model", "messages", "max_tokens", "response_format", "thinking"} {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("seeded: missing profile field")
		}
	}
	p.admitted++
	return nil
}

type seededCapture struct {
	request llm.CompleteRequest
	calls   int
}

func (*seededCapture) ProviderName() string { return "offline-production-builder-capture" }
func (c *seededCapture) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.calls++
	c.request = req
	return nil, errors.New("offline capture; no provider")
}

func captureSeedGate(in seededInputs, candidate *dktypes.AnnotatedDocument) (llm.CompleteRequest, error) {
	c := &seededCapture{}
	_ = annotation.NewSemanticGate(c, seededMaxTokens, 1).Check(context.Background(), in.Original, candidate)
	if c.calls != 1 {
		return llm.CompleteRequest{}, fmt.Errorf("seeded: gate builder must emit exactly one call")
	}
	return c.request, nil
}
func captureSeedRevision(in seededInputs, feedback *annotation.SemanticRejection) (llm.CompleteRequest, error) {
	c := &seededCapture{}
	_, _ = annotation.NewLLMAnnotator(c, seededMaxTokens, 1).Revise(context.Background(), in.Skill, in.Path, in.Original, in.Candidate, feedback)
	if c.calls != 1 {
		return llm.CompleteRequest{}, fmt.Errorf("seeded: revision builder must emit exactly one call")
	}
	return c.request, nil
}

type seededExecutor struct {
	in            seededInputs
	next          llm.Client
	out           string
	phases        []string
	accepted      *dktypes.AnnotatedDocument
	acceptedPhase string
}

func (e *seededExecutor) withPhase(ctx context.Context, phase string, expected llm.CompleteRequest) context.Context {
	return context.WithValue(ctx, seededBindingKey{}, seededCallBinding{Phase: phase, Document: e.in.Document, SourceSHA: e.in.SourceSHA, Original: e.in.Original, SystemSHA: sha([]byte(expected.System)), UserSHA: sha([]byte(expected.User))})
}

// seedRecordingClient 保留解析所用 content 和失败状态，供离线重放实际类型化资格结论。
// Retain final response content privately so audit can replay the actual typed gate result.
type seedRecordingClient struct {
	next   llm.Client
	out    string
	cancel context.CancelFunc
}

func (c *seedRecordingClient) ProviderName() string { return c.next.ProviderName() }
func (c *seedRecordingClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	b, ok := ctx.Value(seededBindingKey{}).(seededCallBinding)
	if !ok {
		return nil, fmt.Errorf("seeded: recording phase missing")
	}
	var info callInfo
	if err := b.bind(req, &info); err != nil {
		return nil, err
	}
	raw, err := renderSeedWire(ctx, req)
	if err != nil {
		return nil, err
	}
	metadata := seededRequestMetadata(req, raw, tokenCount{})
	metadata["phase"], metadata["original_sha256"] = b.Phase, b.SourceSHA
	if err = writeNew(filepath.Join(c.out, b.Phase+"-request-capture.json"), metadata); err != nil {
		c.cancel()
		return nil, fmt.Errorf("seeded: request capture persistence failed")
	}
	response, completeErr := c.next.Complete(ctx, req)
	if err = writeNew(filepath.Join(c.out, b.Phase+"-response.private.json"), map[string]any{"response": response, "complete_error": completeErr != nil}); err != nil {
		c.cancel()
		return response, fmt.Errorf("seeded: response persistence failed")
	}
	return response, completeErr
}
func (e *seededExecutor) save(name string, v any) error {
	if e.out == "" {
		return nil
	}
	return writeNew(filepath.Join(e.out, name), v)
}

func (e *seededExecutor) accept(doc *dktypes.AnnotatedDocument, phase string) error {
	e.accepted, e.acceptedPhase = doc, phase
	if e.out == "" {
		return nil
	}
	if err := e.save("accepted-cleaned.private.json", doc); err != nil {
		return err
	}
	accepted, err := os.ReadFile(filepath.Join(e.out, "accepted-cleaned.private.json"))
	if err != nil {
		return err
	}
	runStart, err := os.ReadFile(filepath.Join(e.out, "seed-run-start.json"))
	if err != nil {
		return err
	}
	return e.save("accepted-checkpoint.json", map[string]any{"format": "cairn-seeded-accepted-checkpoint/v1", "mode": seededMode, "acceptance_state": "provisional_until_seed_audit_pass", "accepted_by_phase": phase, "document_id": e.in.Document, "skill": e.in.Skill, "path": e.in.Path, "original_sha256": e.in.SourceSHA, "accepted_cleaned_sha256": sha(accepted), "run_identity_receipt_sha256": sha(runStart), "run_identity_receipt": "seed-run-start.json", "actual_pass_receipt": phase + "-response.private.json", "assign_ids": "not performed; separate accepted checkpoint adapter required", "original_spans": "unknown", "historical63_outcome": "FAIL; unchanged"})
}

// execute 不调用编排器，也不会把一次新实验附加成旧 #63 的第四轮。
// Qualification gates revision; no pipeline continuation or extra candidate is admitted.
func (e *seededExecutor) execute(ctx context.Context) (string, error) {
	if err := e.in.validate(); err != nil {
		return "FAIL_input", err
	}
	expected, err := captureSeedGate(e.in, e.in.Candidate)
	if err != nil {
		return "FAIL_capture", err
	}
	e.phases = append(e.phases, "qualification")
	err = annotation.NewSemanticGate(e.next, seededMaxTokens, 1).Check(e.withPhase(ctx, "qualification", expected), e.in.Original, e.in.Candidate)
	if err == nil {
		if err = e.accept(e.in.Candidate, "qualification"); err != nil {
			return "FAIL_persistence", err
		}
		return "QUALIFICATION_PASS_no_revision_effect", nil
	}
	var feedback *annotation.SemanticRejection
	if !errors.As(err, &feedback) || strings.TrimSpace(feedback.Reason) == "" {
		return "FAIL_qualification_untyped_or_execution", fmt.Errorf("seeded: qualification did not yield typed feedback")
	}
	if err = e.save("qualification-typed-rejection.private.json", map[string]any{"reason": feedback.Reason}); err != nil {
		return "FAIL_persistence", err
	}
	expected, err = captureSeedRevision(e.in, feedback)
	if err != nil {
		return "FAIL_capture", err
	}
	e.phases = append(e.phases, "revision")
	revised, err := annotation.NewLLMAnnotator(e.next, seededMaxTokens, 1).Revise(e.withPhase(ctx, "revision", expected), e.in.Skill, e.in.Path, e.in.Original, e.in.Candidate, feedback)
	if err != nil {
		return "FAIL_revision", fmt.Errorf("seeded: revision failed")
	}
	if err = e.save("revision.private.json", revised); err != nil {
		return "FAIL_persistence", err
	}
	cleaned, err := annotation.NewFormatGate(0).Check(revised)
	if err != nil || cleaned == nil || len(cleaned.Items) == 0 {
		return "FAIL_nonempty_format_gate", fmt.Errorf("seeded: revised candidate is empty or fails format gate")
	}
	if err = e.save("format-gate.private.json", cleaned); err != nil {
		return "FAIL_persistence", err
	}
	expected, err = captureSeedGate(e.in, cleaned)
	if err != nil {
		return "FAIL_capture", err
	}
	e.phases = append(e.phases, "validation")
	if err = annotation.NewSemanticGate(e.next, seededMaxTokens, 1).Check(e.withPhase(ctx, "validation", expected), e.in.Original, cleaned); err != nil {
		return "FAIL_final_gate", fmt.Errorf("seeded: final semantic gate rejected or failed")
	}
	if err = e.accept(cleaned, "validation"); err != nil {
		return "FAIL_persistence", err
	}
	return "SEEDED_REVISION_GATE_PASS_not_capacity_or_causal_effect", nil
}

// renderSeedWire 通过真实客户端序列化，但拦在内存 transport，绝不读取模型密钥或联网。
// Capture the production client's actual wire serialization using an in-memory transport.
func renderSeedWire(ctx context.Context, req llm.CompleteRequest) ([]byte, error) {
	previous := http.DefaultTransport
	var raw []byte
	calls := 0
	http.DefaultTransport = seededRenderTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		b, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		defer b.Close()
		raw, err = io.ReadAll(b)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`))}, nil
	})
	defer func() { http.DefaultTransport = previous }()
	c, err := llm.NewOpenAICompatClient(llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_OFFLINE_CAPTURE_UNUSED", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: seededMaxTokens, Timeout: "600s", MaxRetries: 1})
	if err == nil {
		_, err = c.Complete(ctx, req)
	}
	if err != nil || calls != 1 {
		return nil, fmt.Errorf("seeded: offline wire capture failed")
	}
	return raw, nil
}

type seededRenderTransport func(*http.Request) (*http.Response, error)

func (f seededRenderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func seededRequestMetadata(req llm.CompleteRequest, raw []byte, count tokenCount) map[string]any {
	result := map[string]any{"system_sha256": sha([]byte(req.System)), "user_sha256": sha([]byte(req.User)), "request_sha256": sha(raw), "request_bytes": len(raw), "token_count": count}
	var envelope struct {
		Original  string          `json:"original"`
		Candidate json.RawMessage `json:"candidate"`
		Critique  string          `json:"critique"`
	}
	if decodeStrict([]byte(req.User), &envelope) == nil && envelope.Original != "" {
		result["decoded_original_sha256"], result["projected_candidate_json_sha256"], result["decoded_critique_sha256"] = sha([]byte(envelope.Original)), sha(envelope.Candidate), sha([]byte(envelope.Critique))
	}
	return result
}

func seedPreflight(ctx context.Context, in seededInputs, out, planSHA string, counter tokenCounter) error {
	identity, err := counterSeedIdentity(counter)
	if err != nil {
		return err
	}
	req, err := captureSeedGate(in, in.Candidate)
	if err != nil {
		return err
	}
	raw, err := renderSeedWire(ctx, req)
	if err != nil {
		return err
	}
	count, err := counter.Count(ctx, raw)
	if err != nil {
		return err
	}
	if !identity.matches(count) {
		return fmt.Errorf("seeded: qualification preflight context guard")
	}
	// Synthetic critique exercises the production revision builder for free; it is not future live feedback.
	synthetic := &annotation.SemanticRejection{Reason: "OFFLINE SYNTHETIC typed rejection; not archived/provider feedback"}
	rev, err := captureSeedRevision(in, synthetic)
	if err != nil {
		return err
	}
	var info callInfo
	binding := seededCallBinding{Phase: "revision", Document: in.Document, SourceSHA: in.SourceSHA, Original: in.Original, SystemSHA: sha([]byte(rev.System)), UserSHA: sha([]byte(rev.User))}
	if err = binding.bind(rev, &info); err != nil {
		return err
	}
	revRaw, err := renderSeedWire(ctx, rev)
	if err != nil {
		return err
	}
	revCount, err := counter.Count(ctx, revRaw)
	if err != nil {
		return err
	}
	if !identity.matches(revCount) {
		return fmt.Errorf("seeded: synthetic revision preflight context guard")
	}
	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		return err
	}
	return writeNew(filepath.Join(out, "seed-preflight.json"), map[string]any{"format": "cairn-seeded-preflight/v1", "mode": seededMode, "model_calls": 0, "counter_identity": identity, "plan_sha256": planSHA, "original_sha256": in.SourceSHA, "candidate_sha256": seededCandidateSHA, "archived_rejection_sha256": in.ArchivedRejectionSHA, "archived_reason_use": "provenance_only; not typed feedback", "binary_sha256": sha(binary), "driver_source_digest": driverSourceDigest(), "production_source_digest": buildmeta.SourceDigest("annotation/", "llm/"), "qualification": seededRequestMetadata(req, raw, count), "revision_builder_synthetic_critique_only": seededRequestMetadata(rev, revRaw, revCount), "actual_revision_and_final_gate_wire": "unknown until new typed qualification and new candidate; per-wire guard mandatory", "max_actual_wires": 3, "original_spans": "unknown; no new stored span claim"})
}

func seededRun(ctx context.Context, in seededInputs, out, planSHA string, counter tokenCounter) error {
	if strings.TrimSpace(os.Getenv("CAIRN_LLM_API_KEY")) == "" {
		return fmt.Errorf("seeded: nonempty API key required before dispatch")
	}
	identity, identityErr := counterSeedIdentity(counter)
	if identityErr != nil {
		return identityErr
	}
	preflight, err := os.ReadFile(filepath.Join(out, "seed-preflight.json"))
	if err != nil {
		return fmt.Errorf("seeded: reviewed preflight required")
	}
	var pre map[string]json.RawMessage
	if json.Unmarshal(preflight, &pre) != nil {
		return fmt.Errorf("seeded: preflight invalid")
	}
	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		return err
	}
	for key, want := range map[string]string{"plan_sha256": planSHA, "binary_sha256": sha(binary), "driver_source_digest": driverSourceDigest(), "production_source_digest": buildmeta.SourceDigest("annotation/", "llm/")} {
		var got string
		if json.Unmarshal(pre[key], &got) != nil || got != want {
			return fmt.Errorf("seeded: preflight identity changed")
		}
	}
	var expectedCounter seededCounterIdentity
	if decodeStrict(pre["counter_identity"], &expectedCounter) != nil || identity != expectedCounter {
		return fmt.Errorf("seeded: preflight counter configuration changed")
	}
	qualification, err := captureSeedGate(in, in.Candidate)
	if err != nil {
		return err
	}
	qualificationWire, err := renderSeedWire(ctx, qualification)
	if err != nil {
		return err
	}
	currentCount, err := counter.Count(ctx, qualificationWire)
	if err != nil {
		return err
	}
	var expectedQualification struct {
		RequestSHA string     `json:"request_sha256"`
		Count      tokenCount `json:"token_count"`
	}
	if json.Unmarshal(pre["qualification"], &expectedQualification) != nil || expectedQualification.RequestSHA != sha(qualificationWire) || currentCount != expectedQualification.Count || !identity.matches(currentCount) {
		return fmt.Errorf("seeded: preflight wire/counter mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, 1800*time.Second)
	defer cancel()
	if err = writeNew(filepath.Join(out, "seed-run-start.json"), map[string]any{"format": "cairn-seeded-run/v1", "mode": seededMode, "counter_identity": identity, "requested_model": "deepseek-flash", "endpoint": "https://api.deepseek.com/chat/completions", "response_format": "json_object", "thinking": "enabled", "started_at": now(), "plan_sha256": planSHA, "preflight_sha256": sha(preflight), "binary_sha256": sha(binary), "driver_source_digest": driverSourceDigest(), "production_source_digest": buildmeta.SourceDigest("annotation/", "llm/"), "original_sha256": in.SourceSHA, "candidate_sha256": seededCandidateSHA, "archived_rejection_sha256": in.ArchivedRejectionSHA, "max_actual_wires": 3, "max_wires_per_call": 1, "annotation_and_gate_inner_attempts": 1, "provider_configured_retries": 1, "provider_wire_retries_admitted": 0, "outer_retries": 0, "max_tokens": seededMaxTokens, "request_timeout_seconds": 600, "run_timeout_seconds": 1800, "logical_concurrency": 1, "historical63_outcome": "FAIL; unchanged", "qa_gold_holdout_full609": false}); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(out, "seed-private.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	previousLog := log.Writer()
	log.SetOutput(logFile)
	defer log.SetOutput(previousLog)
	ledger, file, err := openLedger(filepath.Join(out, "requests.jsonl"), cancel)
	if err != nil {
		return err
	}
	defer file.Close()
	previous := http.DefaultTransport
	observer := &traceTransport{next: previous, ledger: ledger, counter: counter, seedPolicy: &seededWirePolicy{Document: in.Document, SourceSHA: in.SourceSHA, Counter: identity}}
	http.DefaultTransport = observer
	defer func() { http.DefaultTransport = previous }()
	client, err := llm.NewOpenAICompatClient(llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_LLM_API_KEY", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: seededMaxTokens, Timeout: "600s", MaxRetries: 1})
	if err != nil {
		return err
	}
	bounded := &boundedClient{next: client, sem: make(chan struct{}, 1), ledger: ledger}
	recorded := &seedRecordingClient{next: bounded, out: out, cancel: cancel}
	executor := &seededExecutor{in: in, next: recorded, out: out}
	status, runErr := executor.execute(ctx)
	receiptErr := writeNew(filepath.Join(out, "seed-run-finish.json"), map[string]any{"format": "cairn-seeded-finish/v1", "mode": seededMode, "finished_at": now(), "status": status, "execution_error": runErr != nil, "phases": executor.phases, "logical_calls": bounded.sequence.Load(), "actual_wires": observer.sent.Load(), "http_responses": observer.responses.Load(), "reported_input_tokens": observer.input.Load(), "reported_output_tokens": observer.output.Load(), "responses_without_usage": observer.missingUsage.Load(), "ledger_ok": ledger.failure() == nil, "original_spans": "unknown", "historical63_outcome": "FAIL; unchanged"})
	auditErr := seedAudit(out, in)
	if runErr != nil || receiptErr != nil || auditErr != nil {
		return fmt.Errorf("seeded: bounded attempt failed; retained receipts")
	}
	return nil
}

type seedReplayClient struct {
	out    string
	phases []string
	files  map[string]string
}

func (*seedReplayClient) ProviderName() string { return "offline-private-response-replay" }
func (c *seedReplayClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	b, ok := ctx.Value(seededBindingKey{}).(seededCallBinding)
	if !ok {
		return nil, fmt.Errorf("seeded: replay binding missing")
	}
	var info callInfo
	if err := b.bind(req, &info); err != nil {
		return nil, err
	}
	path := filepath.Join(c.out, b.Phase+"-response.private.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("seeded: replay response unavailable")
	}
	var saved struct {
		Response      *llm.CompleteResponse `json:"response"`
		CompleteError bool                  `json:"complete_error"`
	}
	if decodeStrict(raw, &saved) != nil || saved.CompleteError || saved.Response == nil {
		return nil, fmt.Errorf("seeded: replay execution failure")
	}
	capturePath := filepath.Join(c.out, b.Phase+"-request-capture.json")
	captureRaw, err := os.ReadFile(capturePath)
	if err != nil {
		return nil, fmt.Errorf("seeded: request capture unavailable")
	}
	var capture map[string]json.RawMessage
	if json.Unmarshal(captureRaw, &capture) != nil {
		return nil, fmt.Errorf("seeded: request capture invalid")
	}
	wire, err := renderSeedWire(ctx, req)
	if err != nil {
		return nil, err
	}
	for key, want := range map[string]string{"phase": b.Phase, "original_sha256": b.SourceSHA, "system_sha256": sha([]byte(req.System)), "user_sha256": sha([]byte(req.User)), "request_sha256": sha(wire)} {
		var got string
		if json.Unmarshal(capture[key], &got) != nil || got != want {
			return nil, fmt.Errorf("seeded: replay request differs from captured production request")
		}
	}
	c.phases = append(c.phases, b.Phase)
	c.files[filepath.Base(path)] = sha(raw)
	c.files[filepath.Base(capturePath)] = sha(captureRaw)
	return saved.Response, nil
}

func seedAudit(out string, in seededInputs) error {
	raw, err := os.ReadFile(filepath.Join(out, "requests.jsonl"))
	counts, failures := auditTraceBytes(raw)
	if err != nil {
		failures = append(failures, "ledger unavailable")
	}
	var start struct {
		Mode         string                `json:"mode"`
		PlanSHA      string                `json:"plan_sha256"`
		PreflightSHA string                `json:"preflight_sha256"`
		Counter      seededCounterIdentity `json:"counter_identity"`
		MaxWires     int                   `json:"max_actual_wires"`
		PerCall      int                   `json:"max_wires_per_call"`
		OriginalSHA  string                `json:"original_sha256"`
	}
	var reviewed struct {
		Counter seededCounterIdentity `json:"counter_identity"`
		Mode    string                `json:"mode"`
		PlanSHA string                `json:"plan_sha256"`
	}
	preBytes, preErr := os.ReadFile(filepath.Join(out, "seed-preflight.json"))
	if readJSON(filepath.Join(out, "seed-run-start.json"), &start) != nil || start.Mode != seededMode || start.MaxWires != 3 || start.PerCall != 1 || start.OriginalSHA != in.SourceSHA || preErr != nil || sha(preBytes) != start.PreflightSHA || json.Unmarshal(preBytes, &reviewed) != nil || reviewed.Mode != seededMode || reviewed.PlanSHA != start.PlanSHA || reviewed.Counter != start.Counter || start.Counter.RuntimeSHA == "" || start.Counter.PythonSHA == "" || start.Counter.AdapterSHA == "" || start.Counter.CounterID == "" {
		failures = append(failures, "reviewed counter/start/preflight identity mismatch")
	}
	var finish struct {
		Mode           string   `json:"mode"`
		Status         string   `json:"status"`
		Phases         []string `json:"phases"`
		ExecutionError bool     `json:"execution_error"`
		LedgerOK       bool     `json:"ledger_ok"`
	}
	if readJSON(filepath.Join(out, "seed-run-finish.json"), &finish) != nil || finish.Mode != seededMode || !finish.LedgerOK || finish.ExecutionError {
		failures = append(failures, "finish receipt missing or failed")
	}
	want := 3
	if finish.Status == "QUALIFICATION_PASS_no_revision_effect" {
		want = 1
	} else if finish.Status != "SEEDED_REVISION_GATE_PASS_not_capacity_or_causal_effect" {
		failures = append(failures, "seeded outcome not accepted")
	}
	if counts["logical_calls"] != want || counts["admitted_attempts"] != want || counts["attempt_outcomes"] != want || counts["attempts_with_unknown_usage"] != 0 || counts["rejected_attempts"] != 0 {
		failures = append(failures, "seeded exact call/usage denominator mismatch")
	}
	phases := []string{"qualification", "revision", "validation"}
	starts := 0
	responses := 0
	phaseByCall := map[int64]string{}
	if strings.Join(finish.Phases, "/") != strings.Join(phases[:want], "/") {
		failures = append(failures, "finish phase inventory mismatch")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row struct {
			Event      string     `json:"event"`
			Mode       string     `json:"operation_mode"`
			Phase      string     `json:"phase"`
			Source     string     `json:"original_sha256"`
			Document   string     `json:"document_id"`
			Provider   int        `json:"provider_attempt"`
			Status     int        `json:"http_status"`
			Finish     string     `json:"finish_reason"`
			Delta      *int       `json:"provider_minus_local_input_tokens"`
			Call       int64      `json:"call_id"`
			RequestSHA string     `json:"request_sha256"`
			SystemSHA  string     `json:"system_sha256"`
			UserSHA    string     `json:"user_sha256"`
			ContentSHA string     `json:"final_content_sha256"`
			Input      int        `json:"input_tokens"`
			Output     int        `json:"output_tokens"`
			Count      tokenCount `json:"token_count"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		if row.Mode != seededMode || row.Source != in.SourceSHA {
			failures = append(failures, "seeded source/mode ledger binding mismatch")
		}
		if row.Event == "logical_start" {
			if starts >= want || row.Phase != phases[starts] || row.Document != in.Document {
				failures = append(failures, "seeded phase/document order mismatch")
			}
			starts++
			phaseByCall[row.Call] = row.Phase
		}
		if phaseByCall[row.Call] != row.Phase {
			failures = append(failures, "per-call phase binding mismatch")
		}
		if row.Event == "request" {
			if !start.Counter.matches(row.Count) {
				failures = append(failures, "request counter differs from reviewed freeze")
			}
			var captured map[string]json.RawMessage
			if readJSON(filepath.Join(out, row.Phase+"-request-capture.json"), &captured) != nil {
				failures = append(failures, "request capture missing")
			} else {
				for key, want := range map[string]string{"request_sha256": row.RequestSHA, "system_sha256": row.SystemSHA, "user_sha256": row.UserSHA} {
					var got string
					if json.Unmarshal(captured[key], &got) != nil || got == "" || got != want {
						failures = append(failures, "captured/actual wire identity mismatch")
					}
				}
			}
			if row.Provider != 1 {
				failures = append(failures, "provider retry attempted")
			}
		}
		if row.Event == "response" {
			responses++
			if row.Provider != 1 || row.Status != 200 || row.Finish != "stop" || row.Delta == nil || *row.Delta != 0 {
				failures = append(failures, "seeded provider outcome/counter mismatch")
			}
			var saved struct {
				Response      *llm.CompleteResponse `json:"response"`
				CompleteError bool                  `json:"complete_error"`
			}
			if readJSON(filepath.Join(out, row.Phase+"-response.private.json"), &saved) != nil || saved.Response == nil || saved.CompleteError || row.ContentSHA == "" || sha([]byte(saved.Response.Text)) != row.ContentSHA || saved.Response.InputTokens != row.Input || saved.Response.OutputTokens != row.Output || saved.Response.FinishReason != row.Finish {
				failures = append(failures, "private parsed response differs from observed wire content")
			}
		}
	}
	if starts != want || responses != want {
		failures = append(failures, "seeded phase evidence incomplete")
	}
	// 直接重放私有 content，通过当前生产解析器重新证明 explicit false 与后续门结论。
	// Replay private content through production parsers; a finish string alone is not typed evidence.
	replay := &seedReplayClient{out: out, files: map[string]string{}}
	replayed := &seededExecutor{in: in, next: replay}
	replayStatus, replayErr := replayed.execute(context.Background())
	if replayErr != nil || replayStatus != finish.Status || len(replay.phases) != want {
		failures = append(failures, "private typed-response replay mismatch")
	}
	var accepted dktypes.AnnotatedDocument
	var checkpoint struct {
		Mode        string `json:"mode"`
		Phase       string `json:"accepted_by_phase"`
		OriginalSHA string `json:"original_sha256"`
		AcceptedSHA string `json:"accepted_cleaned_sha256"`
		RunSHA      string `json:"run_identity_receipt_sha256"`
	}
	acceptedRaw, acceptedErr := os.ReadFile(filepath.Join(out, "accepted-cleaned.private.json"))
	startRaw, startErr := os.ReadFile(filepath.Join(out, "seed-run-start.json"))
	if acceptedErr != nil || startErr != nil || json.Unmarshal(acceptedRaw, &accepted) != nil || replayed.accepted == nil || readJSON(filepath.Join(out, "accepted-checkpoint.json"), &checkpoint) != nil || checkpoint.Mode != seededMode || checkpoint.Phase != replayed.acceptedPhase || checkpoint.OriginalSHA != in.SourceSHA || checkpoint.AcceptedSHA != sha(acceptedRaw) || checkpoint.RunSHA != sha(startRaw) {
		failures = append(failures, "accepted checkpoint identity missing or mismatched")
	} else {
		actualJSON, _ := json.Marshal(accepted)
		replayJSON, _ := json.Marshal(replayed.accepted)
		if string(actualJSON) != string(replayJSON) {
			failures = append(failures, "accepted cleaned document differs from replay")
		}
	}
	receiptErr := writeNew(filepath.Join(out, "seed-audit-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json"), map[string]any{"format": "cairn-seeded-audit/v1", "mode": seededMode, "status": finish.Status, "pass": len(failures) == 0, "errors": failures, "counts": counts, "ledger_sha256": sha(raw), "ledger_bytes": len(raw), "original_sha256": in.SourceSHA, "candidate_sha256": seededCandidateSHA, "archived_rejection_sha256": in.ArchivedRejectionSHA, "private_replay_file_sha256": replay.files, "historical63_outcome": "FAIL; unchanged", "quality_capacity_causal_effect": "not established"})
	if receiptErr != nil {
		return receiptErr
	}
	if len(failures) > 0 {
		return fmt.Errorf("seeded: audit failed")
	}
	return nil
}
