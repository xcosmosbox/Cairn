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

const receiptMode = "parent163-revision-validation/v1"

// receiptPlan 是独立批准的有限父回执，不把失败候选伪装成已接受 checkpoint。
// The manifest admits one known complete negative parent, never an arbitrary caller assertion.
type receiptPlan struct {
	Format              string                `json:"format"`
	Mode                string                `json:"mode"`
	ParentArchiveCommit string                `json:"parent_archive_commit"`
	ParentManifestSHA   string                `json:"parent_manifest_sha256"`
	ParentLedgerSHA     string                `json:"parent_ledger_sha256"`
	ParentStartSHA      string                `json:"parent_start_sha256"`
	ParentFinishSHA     string                `json:"parent_finish_sha256"`
	ParentBinarySHA     string                `json:"parent_binary_sha256"`
	ParentDriverDigest  string                `json:"parent_driver_source_digest"`
	ProductionDigest    string                `json:"production_source_digest"`
	ParentPlanSHA       string                `json:"parent_plan_sha256"`
	ParentPreflightSHA  string                `json:"parent_preflight_sha256"`
	Document            string                `json:"document_id"`
	Skill               string                `json:"skill"`
	Path                string                `json:"path"`
	OriginalSHA         string                `json:"original_sha256"`
	SeedCandidateSHA    string                `json:"seed_candidate_sha256"`
	CandidateSHA        string                `json:"parent_candidate_sha256"`
	FinalResponseSHA    string                `json:"parent_final_response_sha256"`
	FeedbackSHA         string                `json:"parent_typed_reason_sha256"`
	OriginalBytes       int                   `json:"original_bytes"`
	SeedItems           int                   `json:"seed_items"`
	ParentItems         int                   `json:"parent_items"`
	Counter             seededCounterIdentity `json:"counter_identity"`
	MaxTokens           int                   `json:"max_tokens"`
	RequestSeconds      int                   `json:"request_timeout_seconds"`
	RunSeconds          int                   `json:"run_timeout_seconds"`
	MaxWires            int                   `json:"max_actual_wires"`
	PerCallWires        int                   `json:"max_wires_per_call"`
}

func knownReceiptPlan() receiptPlan {
	return receiptPlan{
		Format: "cairn-parent-receipt-plan/v1", Mode: receiptMode,
		ParentArchiveCommit: "2da71d37dfda6db29a25445283f205b90d873334",
		ParentManifestSHA:   "e70f01201c281d5d2d19c1fb9603bca812abbcee9afc7c124e0a0aac2f59fef3",
		ParentLedgerSHA:     "d5a06026a0c51bea166c0bf3dbb8dc445ee8cf71714c0dd62a90b4d13e7ff51d",
		ParentStartSHA:      "6ec87ab685af3dff3192ff4066104cae6bad85675ff6f586b4904f9116c88b31",
		ParentFinishSHA:     "74a88d5024f5a6e59496d0100b2e62266d50d85bbaa2fdbd79e8735a1ea0215f",
		ParentBinarySHA:     "b2b9d86dca7d3f68538a401d21ad55d381ec5b270cc23a24ec21a3919b0afa86",
		ParentDriverDigest:  "sha256:332a841147ae76e89fa2faccb33dfe48a81386fd206171843b8beb384c3d5666",
		ProductionDigest:    "sha256:ee7c8905ac0e0736a45245cd1082e1f46c7d60faf15422f8ffc54ac6282bcdf9",
		ParentPlanSHA:       "a1103d1d30d6a98586c069d4e3db1a4f7832b1da4b44db3a144ceedb1f49bf4b",
		ParentPreflightSHA:  "b3584fd38d8e3e013696c38032d57ba189843e8d5e1298ee1bd2103ad2625930",
		Document:            seededDocument, Skill: "multihop-public", Path: "references/" + seededDocument + ".md",
		OriginalSHA: seededOriginalSHA, SeedCandidateSHA: seededCandidateSHA,
		CandidateSHA:     "01abf76117282c668a413d890f561733323565ba0687ed7d2ef63226be543d5c",
		FinalResponseSHA: "09cf12e276f2d6e3ec77572b46837fefb5bea9261b1b511670cc0cb274b912ad",
		FeedbackSHA:      "4188a394eb8ec982eac02deaa8deafeeb5a60ef2412bfec721c726f633d57746",
		OriginalBytes:    71332, SeedItems: 57, ParentItems: 163,
		Counter:   seededCounterIdentity{PythonSHA: "fa67443527ed9647f760d807e2a38f26340757123e643c4639cf273ed15d5ea7", AdapterSHA: "6ae23dc721898eaad4f950f45e39bcd0716821bfc6ab1c5a6cec31e24ae622cc", RuntimeSHA: "014e3ce5e3227119f5ef3b4ac1e8f9dc3e044926cc3914aa0966edece419a6ec", CounterID: "deepseek-recipe@935817c66f5d6e64838f419692545f06787e485f/v41"},
		MaxTokens: seededMaxTokens, RequestSeconds: 600, RunSeconds: 1800, MaxWires: 2, PerCallWires: 1,
	}
}

type receiptParent struct {
	in       seededInputs
	feedback *annotation.SemanticRejection
	plan     receiptPlan
	proofSHA string
}

func loadReceiptParent(root, manifest, pin string) (receiptParent, error) {
	raw, err := os.ReadFile(manifest)
	var plan receiptPlan
	if err != nil || pin == "" || sha(raw) != pin || decodeStrict(raw, &plan) != nil || plan != knownReceiptPlan() {
		return receiptParent{}, fmt.Errorf("receipt: independent known-parent manifest pin required")
	}
	return verifyReceiptParent(root, plan)
}

// receiptRead 不接受逃逸路径或符号链接；清单本身还必须匹配独立批准的散列。
// Resolve only regular files below the verified parent directory.
func receiptRead(root, name string) ([]byte, error) {
	if filepath.IsAbs(name) || filepath.Clean(name) != name || name == "." || strings.HasPrefix(name, "..") {
		return nil, fmt.Errorf("receipt: invalid parent member")
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("receipt: parent root unavailable")
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, fmt.Errorf("receipt: parent member unavailable")
	}
	real, err := filepath.EvalSymlinks(path)
	rel, relErr := filepath.Rel(base, real)
	if err != nil || relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("receipt: parent member escapes root")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("receipt: parent member unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return nil, fmt.Errorf("receipt: parent member exceeds bound")
	}
	return raw, nil
}

func receiptDocument(raw []byte, in seededInputs, count int) (*dktypes.AnnotatedDocument, error) {
	var d dktypes.AnnotatedDocument
	if decodeStrict(raw, &d) != nil || d.Skill != in.Skill || d.FilePath != in.Path || len(d.Items) != count {
		return nil, fmt.Errorf("receipt: parent candidate binding")
	}
	for _, item := range d.Items {
		if item.ID != "" || item.SourceSpan != nil {
			return nil, fmt.Errorf("receipt: parent introduces graph identity or span")
		}
	}
	return &d, nil
}

// verifyReceiptParent 将完整负结论与完整性分开：不要求接受审计 PASS，也不以其 FAIL 证明可信。
// Reproduce all three production calls and obtain the actual final typed rejection offline.
func verifyReceiptParent(root string, p receiptPlan) (receiptParent, error) {
	bad := func(what string) (receiptParent, error) { return receiptParent{}, fmt.Errorf("receipt: %s", what) }
	manifest, err := receiptRead(root, "manifest.json")
	if err != nil || sha(manifest) != p.ParentManifestSHA {
		return bad("parent archive manifest mismatch")
	}
	var inventory struct {
		Files []struct {
			Path  string `json:"path"`
			Bytes int    `json:"bytes"`
			SHA   string `json:"sha256"`
		} `json:"files"`
	}
	if json.Unmarshal(manifest, &inventory) != nil || len(inventory.Files) == 0 {
		return bad("parent archive inventory")
	}
	seen := map[string]bool{}
	verified := map[string][]byte{}
	for _, f := range inventory.Files {
		data, e := receiptRead(root, f.Path)
		if e != nil || seen[f.Path] || len(data) != f.Bytes || sha(data) != f.SHA {
			return bad("parent member hash mismatch")
		}
		seen[f.Path] = true
		verified[f.Path] = data
	}
	read := func(name string) []byte {
		return verified[name]
	}
	original := read("fixed-inputs/original/" + p.Path)
	seed := read("fixed-inputs/stages/02_gate_a/" + p.Skill + "/references_" + p.Document + "_r3.json")
	if len(original) != p.OriginalBytes || sha(original) != p.OriginalSHA || sha(seed) != p.SeedCandidateSHA {
		return bad("original or fixed parent seed mismatch")
	}
	in := seededInputs{Original: string(original), SourceSHA: p.OriginalSHA, Document: p.Document, Skill: p.Skill, Path: p.Path}
	in.Candidate, err = receiptDocument(seed, in, p.SeedItems)
	if err != nil || in.validate() != nil {
		return bad("parent source/candidate mismatch")
	}
	startRaw, finishRaw := read("run/seed-run-start.json"), read("run/seed-run-finish.json")
	if sha(startRaw) != p.ParentStartSHA || sha(finishRaw) != p.ParentFinishSHA {
		return bad("parent start/finish pin mismatch")
	}
	var start struct {
		Mode        string                `json:"mode"`
		Counter     seededCounterIdentity `json:"counter_identity"`
		Binary      string                `json:"binary_sha256"`
		Driver      string                `json:"driver_source_digest"`
		Production  string                `json:"production_source_digest"`
		Plan        string                `json:"plan_sha256"`
		Preflight   string                `json:"preflight_sha256"`
		Source      string                `json:"original_sha256"`
		Seed        string                `json:"candidate_sha256"`
		Max         int                   `json:"max_actual_wires"`
		PerCall     int                   `json:"max_wires_per_call"`
		Inner       int                   `json:"annotation_and_gate_inner_attempts"`
		Retry       int                   `json:"provider_wire_retries_admitted"`
		Outer       int                   `json:"outer_retries"`
		Concurrency int                   `json:"logical_concurrency"`
		Tokens      int                   `json:"max_tokens"`
		Request     int                   `json:"request_timeout_seconds"`
		Run         int                   `json:"run_timeout_seconds"`
		Model       string                `json:"requested_model"`
		Endpoint    string                `json:"endpoint"`
		Thinking    string                `json:"thinking"`
		Response    string                `json:"response_format"`
	}
	if json.Unmarshal(startRaw, &start) != nil || start.Mode != seededMode || start.Counter != p.Counter || start.Binary != p.ParentBinarySHA || start.Driver != p.ParentDriverDigest || start.Production != p.ProductionDigest || start.Plan != p.ParentPlanSHA || start.Preflight != p.ParentPreflightSHA || start.Source != p.OriginalSHA || start.Seed != p.SeedCandidateSHA || start.Max != 3 || start.PerCall != 1 || start.Inner != 1 || start.Retry != 0 || start.Outer != 0 || start.Concurrency != 1 || start.Tokens != seededMaxTokens || start.Request != 600 || start.Run != 1800 || start.Model != "deepseek-flash" || start.Endpoint != "https://api.deepseek.com/chat/completions" || start.Thinking != "enabled" || start.Response != "json_object" {
		return bad("parent method/counter identity mismatch")
	}
	if sha(read("evidence/plan.json")) != p.ParentPlanSHA || sha(read("run/seed-preflight.json")) != p.ParentPreflightSHA || buildmeta.SourceDigest("annotation/", "llm/") != p.ProductionDigest {
		return bad("parent plan/preflight/production mismatch")
	}
	var finish receiptFinish
	if json.Unmarshal(finishRaw, &finish) != nil || finish.Mode != seededMode || finish.Status != "FAIL_final_gate" || !finish.ExecutionError || !finish.LedgerOK || finish.Calls != 3 || finish.Wires != 3 || finish.Responses != 3 || finish.MissingUsage != 0 || strings.Join(finish.Phases, "/") != "qualification/revision/validation" {
		return bad("parent is not the complete admitted negative outcome")
	}
	ledger := read("run/requests.jsonl")
	if sha(ledger) != p.ParentLedgerSHA {
		return bad("parent ledger pin mismatch")
	}
	counts, failures := inspectReceiptTraceWithReader(ledger, seededMode, "", in, p.Counter, []string{"qualification", "revision", "validation"}, func(name string) ([]byte, error) {
		data := read("run/" + name)
		if data == nil {
			return nil, fmt.Errorf("receipt: parent snapshot member missing")
		}
		return data, nil
	})
	if len(failures) != 0 || counts["input_tokens"] != finish.Input || counts["output_tokens"] != finish.Output {
		return bad("parent trace/response/counter integrity")
	}
	replay := &parentReceiptReplayClient{verified: verified}
	binder := &seededExecutor{in: in}
	req, err := captureSeedGate(in, in.Candidate)
	if err != nil {
		return bad("parent qualification capture")
	}
	err = annotation.NewSemanticGate(replay, seededMaxTokens, 1).Check(binder.withPhase(context.Background(), "qualification", req), in.Original, in.Candidate)
	var first *annotation.SemanticRejection
	if !errors.As(err, &first) || strings.TrimSpace(first.Reason) == "" {
		return bad("parent qualification is not current typed false")
	}
	var savedFirst struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(read("run/qualification-typed-rejection.private.json"), &savedFirst) != nil || savedFirst.Reason != first.Reason {
		return bad("parent qualification feedback substitution")
	}
	req, err = captureSeedRevision(in, first)
	if err != nil {
		return bad("parent revision capture")
	}
	revised, err := annotation.NewLLMAnnotator(replay, seededMaxTokens, 1).Revise(binder.withPhase(context.Background(), "revision", req), in.Skill, in.Path, in.Original, in.Candidate, first)
	if err != nil {
		return bad("parent production revision replay")
	}
	revisedRaw, _ := json.MarshalIndent(revised, "", "  ")
	if string(append(revisedRaw, '\n')) != string(read("run/revision.private.json")) {
		return bad("parent parsed revision mismatch")
	}
	cleaned, err := annotation.NewFormatGate(0).Check(revised)
	cleanRaw, _ := json.MarshalIndent(cleaned, "", "  ")
	actualClean := read("run/format-gate.private.json")
	if err != nil || cleaned == nil || len(cleaned.Items) != p.ParentItems || sha(actualClean) != p.CandidateSHA || string(append(cleanRaw, '\n')) != string(actualClean) {
		return bad("parent nonempty format replay mismatch")
	}
	if _, err = receiptDocument(actualClean, in, p.ParentItems); err != nil {
		return bad("parent cleaned identity mismatch")
	}
	req, err = captureSeedGate(in, cleaned)
	if err != nil {
		return bad("parent final capture")
	}
	err = annotation.NewSemanticGate(replay, seededMaxTokens, 1).Check(binder.withPhase(context.Background(), "validation", req), in.Original, cleaned)
	var final *annotation.SemanticRejection
	if !errors.As(err, &final) || strings.TrimSpace(final.Reason) == "" || sha([]byte(final.Reason)) != p.FeedbackSHA || sha(read("run/validation-response.private.json")) != p.FinalResponseSHA || strings.Join(replay.phases, "/") != "qualification/revision/validation" {
		return bad("parent final typed rejection mismatch")
	}
	in.Candidate = cleaned
	proof := struct{ Mode, Manifest, Ledger, Candidate, Response, Feedback string }{receiptMode, p.ParentManifestSHA, p.ParentLedgerSHA, p.CandidateSHA, p.FinalResponseSHA, p.FeedbackSHA}
	proofRaw, _ := json.Marshal(proof)
	return receiptParent{in: in, feedback: final, plan: p, proofSHA: sha(proofRaw)}, nil
}

// 所有重放只消费刚才散列验证过的同一内存快照，避免第二次文件读取改变父证据。
// Parent replay consumes the verified snapshot, with no second read or provider path.
type parentReceiptReplayClient struct {
	verified map[string][]byte
	phases   []string
}

func (*parentReceiptReplayClient) ProviderName() string { return "offline-verified-parent-snapshot" }
func (c *parentReceiptReplayClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	b, ok := ctx.Value(seededBindingKey{}).(seededCallBinding)
	if !ok {
		return nil, fmt.Errorf("receipt: parent replay binding missing")
	}
	if err := b.bind(req, &callInfo{}); err != nil {
		return nil, err
	}
	var saved struct {
		Response      *llm.CompleteResponse `json:"response"`
		CompleteError bool                  `json:"complete_error"`
	}
	if decodeStrict(c.verified["run/"+b.Phase+"-response.private.json"], &saved) != nil || saved.Response == nil || saved.CompleteError {
		return nil, fmt.Errorf("receipt: parent replay response unavailable")
	}
	var capture map[string]json.RawMessage
	if json.Unmarshal(c.verified["run/"+b.Phase+"-request-capture.json"], &capture) != nil {
		return nil, fmt.Errorf("receipt: parent replay capture unavailable")
	}
	wire, err := renderSeedWire(ctx, req)
	if err != nil {
		return nil, err
	}
	for key, want := range map[string]string{"phase": b.Phase, "original_sha256": b.SourceSHA, "system_sha256": sha([]byte(req.System)), "user_sha256": sha([]byte(req.User)), "request_sha256": sha(wire)} {
		var got string
		if json.Unmarshal(capture[key], &got) != nil || got != want {
			return nil, fmt.Errorf("receipt: parent production capture mismatch")
		}
	}
	c.phases = append(c.phases, b.Phase)
	return saved.Response, nil
}

type receiptFinish struct {
	Mode           string   `json:"mode"`
	Status         string   `json:"status"`
	Phases         []string `json:"phases"`
	ExecutionError bool     `json:"execution_error"`
	LedgerOK       bool     `json:"ledger_ok"`
	Calls          int      `json:"logical_calls"`
	Wires          int      `json:"actual_wires"`
	Responses      int      `json:"http_responses"`
	MissingUsage   int      `json:"responses_without_usage"`
	Input          int      `json:"reported_input_tokens"`
	Output         int      `json:"reported_output_tokens"`
}

type receiptTraceRow struct {
	Stage      string     `json:"stage"`
	Event      string     `json:"event"`
	Mode       string     `json:"operation_mode"`
	Phase      string     `json:"phase"`
	Source     string     `json:"original_sha256"`
	Parent     string     `json:"parent_proof_sha256"`
	Document   string     `json:"document_id"`
	Documents  []string   `json:"document_ids"`
	Call       int64      `json:"call_id"`
	Provider   int        `json:"provider_attempt"`
	Status     int        `json:"http_status"`
	Finish     string     `json:"finish_reason"`
	Delta      *int       `json:"provider_minus_local_input_tokens"`
	Model      string     `json:"returned_model"`
	ModelOK    bool       `json:"model_identity_expected"`
	Usage      string     `json:"usage_status"`
	RequestSHA string     `json:"request_sha256"`
	SystemSHA  string     `json:"system_sha256"`
	UserSHA    string     `json:"user_sha256"`
	ContentSHA string     `json:"final_content_sha256"`
	Input      int        `json:"input_tokens"`
	Output     int        `json:"output_tokens"`
	LocalInput int        `json:"local_input_tokens"`
	Count      tokenCount `json:"token_count"`
}

// inspectReceiptTrace 对计费分母和真正被解析的 content 绑定，不能用成功字符串替代证据。
// Reconcile captures, observed content, usage and explicit source/parent identity for every call.
func inspectReceiptTrace(out string, raw []byte, mode, parent string, in seededInputs, counter seededCounterIdentity, phases []string) (map[string]int, []string) {
	return inspectReceiptTraceWithReader(raw, mode, parent, in, counter, phases, func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(out, name)) })
}

func inspectReceiptTraceWithReader(raw []byte, mode, parent string, in seededInputs, counter seededCounterIdentity, phases []string, read func(string) ([]byte, error)) (map[string]int, []string) {
	counts, failures := auditTraceBytes(raw)
	want := len(phases)
	if counts["logical_calls"] != want || counts["admitted_attempts"] != want || counts["attempt_outcomes"] != want || counts["attempts_with_unknown_usage"] != 0 || counts["rejected_attempts"] != 0 {
		failures = append(failures, "exact call/outcome denominator")
	}
	starts, responses := 0, 0
	byCall := map[int64]string{}
	inputs := map[int64]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row receiptTraceRow
		if json.Unmarshal([]byte(line), &row) != nil {
			failures = append(failures, "malformed trace row")
			continue
		}
		if row.Mode != mode || row.Source != in.SourceSHA || row.Parent != parent {
			failures = append(failures, "mode/source/parent binding")
		}
		wantStage := "semantic_gate"
		if row.Phase == "revision" {
			wantStage = "annotation"
		}
		if row.Stage != wantStage {
			failures = append(failures, "phase/stage binding")
		}
		if row.Event == "logical_start" {
			if starts >= want || row.Phase != phases[starts] || row.Document != in.Document || len(row.Documents) != 1 || row.Documents[0] != in.Document || byCall[row.Call] != "" {
				failures = append(failures, "phase/document order")
			}
			byCall[row.Call] = row.Phase
			starts++
		}
		if byCall[row.Call] == "" || byCall[row.Call] != row.Phase {
			failures = append(failures, "per-call phase")
		}
		if row.Event == "request" {
			if !counter.matches(row.Count) || row.Provider != 1 || row.Document != in.Document || len(row.Documents) != 1 || row.Documents[0] != in.Document {
				failures = append(failures, "counter/document/retry")
			}
			inputs[row.Call] = row.Count.Input
			var capture map[string]json.RawMessage
			captureRaw, captureErr := read(row.Phase + "-request-capture.json")
			if captureErr != nil || json.Unmarshal(captureRaw, &capture) != nil {
				failures = append(failures, "capture missing")
				continue
			}
			for key, want := range map[string]string{"phase": row.Phase, "original_sha256": in.SourceSHA, "request_sha256": row.RequestSHA, "system_sha256": row.SystemSHA, "user_sha256": row.UserSHA} {
				var got string
				if json.Unmarshal(capture[key], &got) != nil || got == "" || got != want {
					failures = append(failures, "capture/observed wire mismatch")
				}
			}
			var gotParent string
			if parent != "" && (json.Unmarshal(capture["parent_proof_sha256"], &gotParent) != nil || gotParent != parent) {
				failures = append(failures, "capture parent mismatch")
			}
		}
		if row.Event == "response" {
			responses++
			if row.Provider != 1 || row.Status != 200 || row.Finish != "stop" || row.Delta == nil || *row.Delta != 0 || row.Usage != "reported" || !row.ModelOK || (row.Model != "deepseek-flash" && row.Model != "deepseek-v4.1-flash-1017") || row.Input != inputs[row.Call] || row.LocalInput != row.Input || row.Output < 0 {
				failures = append(failures, "observed provider/usage/counter outcome")
			}
			var saved struct {
				Response      *llm.CompleteResponse `json:"response"`
				CompleteError bool                  `json:"complete_error"`
			}
			savedRaw, savedErr := read(row.Phase + "-response.private.json")
			if savedErr != nil || json.Unmarshal(savedRaw, &saved) != nil || saved.Response == nil || saved.CompleteError || row.ContentSHA == "" || sha([]byte(saved.Response.Text)) != row.ContentSHA || saved.Response.InputTokens != row.Input || saved.Response.OutputTokens != row.Output || saved.Response.FinishReason != row.Finish {
				failures = append(failures, "saved parsed response/observed content mismatch")
			}
			counts["input_tokens"] += row.Input
			counts["output_tokens"] += row.Output
		}
	}
	if starts != want || responses != want {
		failures = append(failures, "incomplete phase inventory")
	}
	return counts, failures
}

func (p receiptParent) validate() error {
	raw, _ := json.MarshalIndent(p.in.Candidate, "", "  ")
	if p.in.validate() != nil || p.feedback == nil || strings.TrimSpace(p.feedback.Reason) == "" || sha([]byte(p.feedback.Reason)) != p.plan.FeedbackSHA || sha(append(raw, '\n')) != p.plan.CandidateSHA || len(p.in.Candidate.Items) != p.plan.ParentItems || p.in.SourceSHA != p.plan.OriginalSHA || p.in.Document != p.plan.Document || p.in.Skill != p.plan.Skill || p.in.Path != p.plan.Path || p.proofSHA == "" {
		return fmt.Errorf("receipt: validated parent changed")
	}
	return nil
}

type receiptCallBinding struct {
	seededCallBinding
	ParentSHA, FeedbackSHA string
}

func (b receiptCallBinding) bind(req llm.CompleteRequest, info *callInfo) error {
	if b.ParentSHA == "" || b.FeedbackSHA == "" || (b.Phase != "revision" && b.Phase != "validation") {
		return fmt.Errorf("receipt: phase/parent binding missing")
	}
	if err := b.seededCallBinding.bind(req, info); err != nil {
		return err
	}
	if b.Phase == "revision" {
		var input struct {
			Critique string `json:"critique"`
		}
		if json.Unmarshal([]byte(req.User), &input) != nil || sha([]byte(input.Critique)) != b.FeedbackSHA {
			return fmt.Errorf("receipt: parent critique changed")
		}
	}
	info.Mode, info.ParentSHA = receiptMode, b.ParentSHA
	return nil
}

// receiptWirePolicy 不扩大旧三阶段策略；该模式固定只允许两条 wire。
// A separate closed two-phase policy preserves the old seeded policy unchanged.
type receiptWirePolicy struct {
	mu                             sync.Mutex
	Document, SourceSHA, ParentSHA string
	Counter                        seededCounterIdentity
	admitted                       int
}

func (p *receiptWirePolicy) admit(info *callInfo, req wireRequest, raw []byte, count tokenCount) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	phases := []string{"revision", "validation"}
	if !p.Counter.matches(count) || p.admitted >= 2 || info.Phase != phases[p.admitted] || info.Mode != receiptMode || info.ParentSHA != p.ParentSHA || p.ParentSHA == "" || info.Document != p.Document || len(info.Documents) != 1 || info.Documents[0] != p.Document || info.SourceSHA != p.SourceSHA || info.Attempts.Load() != 1 || req.MaxTokens != seededMaxTokens || req.Model != "deepseek-flash" || req.Thinking.Type != "enabled" || req.ResponseFormat.Type != "json_object" {
		return fmt.Errorf("receipt: phase/profile/two-wire budget")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return fmt.Errorf("receipt: unexpected wire overrides")
	}
	for _, key := range []string{"model", "messages", "max_tokens", "response_format", "thinking"} {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("receipt: missing profile field")
		}
	}
	p.admitted++
	return nil
}

type receiptRecordingClient struct {
	next   llm.Client
	out    string
	cancel context.CancelFunc
}

func (c *receiptRecordingClient) ProviderName() string { return c.next.ProviderName() }
func (c *receiptRecordingClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	b, ok := ctx.Value(seededBindingKey{}).(receiptCallBinding)
	if !ok {
		return nil, fmt.Errorf("receipt: recording binding missing")
	}
	if err := b.bind(req, &callInfo{}); err != nil {
		return nil, err
	}
	wire, err := renderSeedWire(ctx, req)
	if err != nil {
		return nil, err
	}
	metadata := seededRequestMetadata(req, wire, tokenCount{})
	metadata["phase"], metadata["original_sha256"], metadata["parent_proof_sha256"] = b.Phase, b.SourceSHA, b.ParentSHA
	if err = writeNew(filepath.Join(c.out, b.Phase+"-request-capture.json"), metadata); err != nil {
		c.cancel()
		return nil, fmt.Errorf("receipt: capture persistence failed")
	}
	response, completeErr := c.next.Complete(ctx, req)
	if err = writeNew(filepath.Join(c.out, b.Phase+"-response.private.json"), map[string]any{"response": response, "complete_error": completeErr != nil}); err != nil {
		c.cancel()
		return response, fmt.Errorf("receipt: response persistence failed")
	}
	return response, completeErr
}

type receiptReplayClient struct{ seedReplayClient }

func (c *receiptReplayClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	b, ok := ctx.Value(seededBindingKey{}).(receiptCallBinding)
	if !ok {
		return nil, fmt.Errorf("receipt: replay binding missing")
	}
	if err := b.bind(req, &callInfo{}); err != nil {
		return nil, err
	}
	// 只复用原有生产序列化/私有响应重放；这里不进入有界客户端或真实 transport。
	// The existing replay client is offline; parent identity is checked separately in the trace.
	return c.seedReplayClient.Complete(context.WithValue(ctx, seededBindingKey{}, b.seededCallBinding), req)
}

type receiptExecutor struct {
	revised        *dktypes.AnnotatedDocument
	cleaned        *dktypes.AnnotatedDocument
	parent         receiptParent
	next           llm.Client
	out            string
	phases         []string
	accepted       *dktypes.AnnotatedDocument
	finalRejection *annotation.SemanticRejection
}

func (e *receiptExecutor) withPhase(ctx context.Context, phase string, req llm.CompleteRequest) context.Context {
	b := receiptCallBinding{seededCallBinding: seededCallBinding{Phase: phase, Document: e.parent.in.Document, SourceSHA: e.parent.in.SourceSHA, Original: e.parent.in.Original, SystemSHA: sha([]byte(req.System)), UserSHA: sha([]byte(req.User))}, ParentSHA: e.parent.proofSHA, FeedbackSHA: e.parent.plan.FeedbackSHA}
	return context.WithValue(ctx, seededBindingKey{}, b)
}
func (e *receiptExecutor) save(name string, value any) error {
	if e.out == "" {
		return nil
	}
	return writeNew(filepath.Join(e.out, name), value)
}

// execute 只修订一次；原文优先级由未修改的生产 builder/parser 执行，不注入人工诊断。
// Exactly revision -> nonempty format -> validation; no qualification or automatic continuation.
func (e *receiptExecutor) execute(ctx context.Context) (string, error) {
	if err := e.parent.validate(); err != nil {
		return "FAIL_parent", err
	}
	in := e.parent.in
	req, err := captureSeedRevision(in, e.parent.feedback)
	if err != nil {
		return "FAIL_capture", err
	}
	e.phases = append(e.phases, "revision")
	revised, err := annotation.NewLLMAnnotator(e.next, seededMaxTokens, 1).Revise(e.withPhase(ctx, "revision", req), in.Skill, in.Path, in.Original, in.Candidate, e.parent.feedback)
	if err != nil {
		return "FAIL_revision", fmt.Errorf("receipt: revision failed")
	}
	e.revised = revised
	if err = e.save("revision.private.json", revised); err != nil {
		return "FAIL_persistence", err
	}
	cleaned, err := annotation.NewFormatGate(0).Check(revised)
	if err != nil || cleaned == nil || len(cleaned.Items) == 0 {
		return "FAIL_nonempty_format_gate", fmt.Errorf("receipt: empty or invalid replacement")
	}
	e.cleaned = cleaned
	if err = e.save("format-gate.private.json", cleaned); err != nil {
		return "FAIL_persistence", err
	}
	req, err = captureSeedGate(in, cleaned)
	if err != nil {
		return "FAIL_capture", err
	}
	e.phases = append(e.phases, "validation")
	err = annotation.NewSemanticGate(e.next, seededMaxTokens, 1).Check(e.withPhase(ctx, "validation", req), in.Original, cleaned)
	if err != nil {
		if errors.As(err, &e.finalRejection) && strings.TrimSpace(e.finalRejection.Reason) != "" {
			if saveErr := e.save("validation-typed-rejection.private.json", map[string]any{"reason": e.finalRejection.Reason}); saveErr != nil {
				return "FAIL_persistence", saveErr
			}
			return "FAIL_final_gate", fmt.Errorf("receipt: final typed semantic rejection")
		}
		return "FAIL_final_gate_untyped_or_execution", fmt.Errorf("receipt: final gate did not return an accepted result")
	}
	e.accepted = cleaned
	if e.out != "" {
		if err = e.save("accepted-cleaned.private.json", cleaned); err != nil {
			return "FAIL_persistence", err
		}
		accepted, aerr := os.ReadFile(filepath.Join(e.out, "accepted-cleaned.private.json"))
		start, serr := os.ReadFile(filepath.Join(e.out, "receipt-run-start.json"))
		if aerr != nil || serr != nil {
			return "FAIL_persistence", fmt.Errorf("receipt: checkpoint inputs unavailable")
		}
		if err = e.save("accepted-checkpoint.json", map[string]any{"format": "cairn-receipt-accepted-checkpoint/v1", "mode": receiptMode, "acceptance_state": "provisional_until_receipt_audit_pass", "parent_proof_sha256": e.parent.proofSHA, "original_sha256": in.SourceSHA, "accepted_cleaned_sha256": sha(accepted), "run_identity_receipt_sha256": sha(start), "actual_pass_receipt": "validation-response.private.json", "assign_ids": "not performed; separate accepted checkpoint adapter required", "original_spans": "unknown"}); err != nil {
			return "FAIL_persistence", err
		}
	}
	return "RECEIPT_REVISION_GATE_PASS_not_capacity_or_causal_effect", nil
}

type receiptPreflightRecord struct {
	Format         string                `json:"format"`
	Mode           string                `json:"mode"`
	Parent         string                `json:"parent_proof_sha256"`
	Plan           string                `json:"plan_sha256"`
	ParentManifest string                `json:"parent_manifest_sha256"`
	ParentLedger   string                `json:"parent_ledger_sha256"`
	Source         string                `json:"original_sha256"`
	Candidate      string                `json:"candidate_sha256"`
	Feedback       string                `json:"feedback_sha256"`
	Binary         string                `json:"binary_sha256"`
	Driver         string                `json:"driver_source_digest"`
	Production     string                `json:"production_source_digest"`
	Counter        seededCounterIdentity `json:"counter_identity"`
	ModelCalls     int                   `json:"model_calls"`
	Revision       struct {
		RequestSHA string     `json:"request_sha256"`
		Count      tokenCount `json:"token_count"`
	} `json:"revision"`
}

func receiptPreflight(ctx context.Context, parent receiptParent, out, planSHA string, counter tokenCounter) error {
	if err := parent.validate(); err != nil {
		return err
	}
	identity, err := counterSeedIdentity(counter)
	if err != nil || identity != parent.plan.Counter {
		return fmt.Errorf("receipt: reviewed parent counter required")
	}
	req, err := captureSeedRevision(parent.in, parent.feedback)
	if err != nil {
		return err
	}
	probe := (&receiptExecutor{parent: parent}).withPhase(ctx, "revision", req).Value(seededBindingKey{}).(receiptCallBinding)
	if err = probe.bind(req, &callInfo{}); err != nil {
		return err
	}
	wire, err := renderSeedWire(ctx, req)
	if err != nil {
		return err
	}
	count, err := counter.Count(ctx, wire)
	if err != nil || !identity.matches(count) {
		return fmt.Errorf("receipt: actual revision preflight context guard")
	}
	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return err
	}
	if err = os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("receipt: fresh exclusive output required")
	}
	return writeNew(filepath.Join(out, "receipt-preflight.json"), map[string]any{"format": "cairn-receipt-preflight/v1", "mode": receiptMode, "model_calls": 0, "parent_proof_sha256": parent.proofSHA, "parent_manifest_sha256": parent.plan.ParentManifestSHA, "parent_ledger_sha256": parent.plan.ParentLedgerSHA, "candidate_sha256": parent.plan.CandidateSHA, "feedback_sha256": parent.plan.FeedbackSHA, "original_sha256": parent.in.SourceSHA, "counter_identity": identity, "plan_sha256": planSHA, "binary_sha256": sha(binary), "driver_source_digest": driverSourceDigest(), "production_source_digest": buildmeta.SourceDigest("annotation/", "llm/"), "revision": seededRequestMetadata(req, wire, count), "max_actual_wires": 2, "max_wires_per_call": 1, "qualification_calls": 0, "final_gate_wire": "unknown until the nonempty replacement; actual per-wire guard mandatory", "original_spans": "unknown"})
}

func readReceiptPreflight(out string, parent receiptParent, planSHA string, identity seededCounterIdentity) (receiptPreflightRecord, []byte, error) {
	var pre receiptPreflightRecord
	raw, err := os.ReadFile(filepath.Join(out, "receipt-preflight.json"))
	binary, berr := os.ReadFile(os.Args[0])
	if err != nil || berr != nil || json.Unmarshal(raw, &pre) != nil || pre.Format != "cairn-receipt-preflight/v1" || pre.Mode != receiptMode || pre.Parent != parent.proofSHA || pre.Plan != planSHA || pre.ParentManifest != parent.plan.ParentManifestSHA || pre.ParentLedger != parent.plan.ParentLedgerSHA || pre.Source != parent.in.SourceSHA || pre.Candidate != parent.plan.CandidateSHA || pre.Feedback != parent.plan.FeedbackSHA || pre.Binary != sha(binary) || pre.Driver != driverSourceDigest() || pre.Production != buildmeta.SourceDigest("annotation/", "llm/") || pre.Counter != identity || identity != parent.plan.Counter || pre.ModelCalls != 0 {
		return pre, raw, fmt.Errorf("receipt: reviewed preflight identity changed")
	}
	return pre, raw, nil
}

func receiptRun(ctx context.Context, parent receiptParent, out, planSHA string, counter tokenCounter) error {
	if strings.TrimSpace(os.Getenv("CAIRN_LLM_API_KEY")) == "" {
		return fmt.Errorf("receipt: nonempty API key required before dispatch")
	}
	if err := parent.validate(); err != nil {
		return err
	}
	identity, err := counterSeedIdentity(counter)
	if err != nil {
		return err
	}
	pre, rawPre, err := readReceiptPreflight(out, parent, planSHA, identity)
	if err != nil {
		return err
	}
	req, err := captureSeedRevision(parent.in, parent.feedback)
	if err != nil {
		return err
	}
	wire, err := renderSeedWire(ctx, req)
	if err != nil {
		return err
	}
	count, err := counter.Count(ctx, wire)
	if err != nil || !identity.matches(count) || count != pre.Revision.Count || sha(wire) != pre.Revision.RequestSHA {
		return fmt.Errorf("receipt: revision wire/counter differs from preflight")
	}
	ctx, cancel := context.WithTimeout(ctx, 1800*time.Second)
	defer cancel()
	if err = writeNew(filepath.Join(out, "receipt-run-start.json"), map[string]any{"format": "cairn-receipt-run/v1", "mode": receiptMode, "parent_proof_sha256": parent.proofSHA, "parent_manifest_sha256": parent.plan.ParentManifestSHA, "parent_ledger_sha256": parent.plan.ParentLedgerSHA, "candidate_sha256": parent.plan.CandidateSHA, "feedback_sha256": parent.plan.FeedbackSHA, "original_sha256": parent.in.SourceSHA, "counter_identity": identity, "plan_sha256": planSHA, "preflight_sha256": sha(rawPre), "binary_sha256": pre.Binary, "driver_source_digest": pre.Driver, "production_source_digest": pre.Production, "started_at": now(), "max_actual_wires": 2, "max_wires_per_call": 1, "qualification_calls": 0, "annotation_and_gate_inner_attempts": 1, "provider_wire_retries_admitted": 0, "outer_retries": 0, "logical_concurrency": 1, "max_tokens": seededMaxTokens, "request_timeout_seconds": 600, "run_timeout_seconds": 1800, "requested_model": "deepseek-flash", "endpoint": "https://api.deepseek.com/chat/completions", "response_format": "json_object", "thinking": "enabled", "original_spans": "unknown", "historical63_outcome": "FAIL; unchanged", "qa_gold_holdout_full609": false}); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(out, "receipt-private.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
	observer := &traceTransport{next: previous, ledger: ledger, counter: counter, seedPolicy: &receiptWirePolicy{Document: parent.in.Document, SourceSHA: parent.in.SourceSHA, ParentSHA: parent.proofSHA, Counter: identity}}
	http.DefaultTransport = observer
	defer func() { http.DefaultTransport = previous }()
	client, err := llm.NewOpenAICompatClient(llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_LLM_API_KEY", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: seededMaxTokens, Timeout: "600s", MaxRetries: 1})
	if err != nil {
		return err
	}
	bounded := &boundedClient{next: client, sem: make(chan struct{}, 1), ledger: ledger}
	recorded := &receiptRecordingClient{next: bounded, out: out, cancel: cancel}
	executor := &receiptExecutor{parent: parent, next: recorded, out: out}
	status, runErr := executor.execute(ctx)
	receiptErr := writeNew(filepath.Join(out, "receipt-run-finish.json"), map[string]any{"format": "cairn-receipt-finish/v1", "mode": receiptMode, "parent_proof_sha256": parent.proofSHA, "finished_at": now(), "status": status, "execution_error": runErr != nil, "phases": executor.phases, "logical_calls": bounded.sequence.Load(), "actual_wires": observer.sent.Load(), "http_responses": observer.responses.Load(), "reported_input_tokens": observer.input.Load(), "reported_output_tokens": observer.output.Load(), "responses_without_usage": observer.missingUsage.Load(), "ledger_ok": ledger.failure() == nil, "original_spans": "unknown"})
	auditErr := receiptAudit(out, parent, planSHA)
	if runErr != nil || receiptErr != nil || auditErr != nil {
		return fmt.Errorf("receipt: bounded attempt not accepted; retained receipts")
	}
	return nil
}

// receiptAudit 保留有效负结果：完整性可为真，接受仍为假，不把预期的语义拒绝当作坏账本。
// Integrity and candidate acceptance are separate; a healthy negative run never creates acceptance.
func receiptAudit(out string, parent receiptParent, planSHA string) error {
	var failures []string
	pre, preRaw, err := readReceiptPreflight(out, parent, planSHA, parent.plan.Counter)
	if err != nil {
		failures = append(failures, "reviewed preflight identity mismatch")
	}
	startRaw, startErr := os.ReadFile(filepath.Join(out, "receipt-run-start.json"))
	var start struct {
		Mode          string                `json:"mode"`
		Parent        string                `json:"parent_proof_sha256"`
		Manifest      string                `json:"parent_manifest_sha256"`
		Ledger        string                `json:"parent_ledger_sha256"`
		Plan          string                `json:"plan_sha256"`
		Preflight     string                `json:"preflight_sha256"`
		Counter       seededCounterIdentity `json:"counter_identity"`
		Binary        string                `json:"binary_sha256"`
		Driver        string                `json:"driver_source_digest"`
		Production    string                `json:"production_source_digest"`
		Source        string                `json:"original_sha256"`
		Candidate     string                `json:"candidate_sha256"`
		Feedback      string                `json:"feedback_sha256"`
		Max           int                   `json:"max_actual_wires"`
		Per           int                   `json:"max_wires_per_call"`
		Qualification int                   `json:"qualification_calls"`
		Inner         int                   `json:"annotation_and_gate_inner_attempts"`
		Retry         int                   `json:"provider_wire_retries_admitted"`
		Outer         int                   `json:"outer_retries"`
		Concurrency   int                   `json:"logical_concurrency"`
		Tokens        int                   `json:"max_tokens"`
		Request       int                   `json:"request_timeout_seconds"`
		Run           int                   `json:"run_timeout_seconds"`
		Model         string                `json:"requested_model"`
		Endpoint      string                `json:"endpoint"`
		Thinking      string                `json:"thinking"`
		Response      string                `json:"response_format"`
	}
	if startErr != nil || json.Unmarshal(startRaw, &start) != nil || start.Mode != receiptMode || start.Parent != parent.proofSHA || start.Plan != planSHA || start.Preflight != sha(preRaw) || start.Counter != parent.plan.Counter || start.Counter != pre.Counter || start.Binary != pre.Binary || start.Driver != pre.Driver || start.Production != pre.Production || start.Source != parent.in.SourceSHA || start.Candidate != parent.plan.CandidateSHA || start.Feedback != parent.plan.FeedbackSHA || start.Max != 2 || start.Per != 1 || start.Qualification != 0 || start.Inner != 1 || start.Retry != 0 || start.Outer != 0 || start.Concurrency != 1 || start.Tokens != seededMaxTokens || start.Request != 600 || start.Run != 1800 || start.Model != "deepseek-flash" || start.Endpoint != "https://api.deepseek.com/chat/completions" || start.Thinking != "enabled" || start.Response != "json_object" {
		failures = append(failures, "run identity/profile mismatch")
	}
	if start.Manifest != parent.plan.ParentManifestSHA || start.Ledger != parent.plan.ParentLedgerSHA {
		failures = append(failures, "start parent manifest/ledger mismatch")
	}
	finishRaw, finishErr := os.ReadFile(filepath.Join(out, "receipt-run-finish.json"))
	var finish struct {
		receiptFinish
		Parent string `json:"parent_proof_sha256"`
	}
	if finishErr != nil || json.Unmarshal(finishRaw, &finish) != nil || finish.Mode != receiptMode || finish.Parent != parent.proofSHA || !finish.LedgerOK || finish.Calls != 2 || finish.Wires != 2 || finish.Responses != 2 || finish.MissingUsage != 0 || strings.Join(finish.Phases, "/") != "revision/validation" {
		failures = append(failures, "incomplete finish/phase inventory")
	}
	acceptedStatus := finish.Status == "RECEIPT_REVISION_GATE_PASS_not_capacity_or_causal_effect"
	negativeStatus := finish.Status == "FAIL_final_gate"
	if !acceptedStatus && !negativeStatus || acceptedStatus && finish.ExecutionError || negativeStatus && !finish.ExecutionError {
		failures = append(failures, "not a complete passing or typed-negative outcome")
	}
	ledger, readErr := os.ReadFile(filepath.Join(out, "requests.jsonl"))
	counts, traceFailures := inspectReceiptTrace(out, ledger, receiptMode, parent.proofSHA, parent.in, parent.plan.Counter, []string{"revision", "validation"})
	if readErr != nil {
		failures = append(failures, "ledger unavailable")
	}
	failures = append(failures, traceFailures...)
	if counts["input_tokens"] != finish.Input || counts["output_tokens"] != finish.Output {
		failures = append(failures, "finish usage total mismatch")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(ledger)), "\n") {
		var row receiptTraceRow
		if json.Unmarshal([]byte(line), &row) == nil && row.Event == "request" && row.Phase == "revision" && (row.Count != pre.Revision.Count || row.RequestSHA != pre.Revision.RequestSHA) {
			failures = append(failures, "first actual wire/counter differs from preflight")
		}
	}
	replay := &receiptReplayClient{seedReplayClient{out: out, files: map[string]string{}}}
	replayed := &receiptExecutor{parent: parent, next: replay}
	status, replayErr := replayed.execute(context.Background())
	if status != finish.Status || strings.Join(replay.phases, "/") != "revision/validation" || acceptedStatus && replayErr != nil || negativeStatus && (replayErr == nil || replayed.finalRejection == nil) {
		failures = append(failures, "production parser/capture outcome replay mismatch")
	}
	for name, doc := range map[string]*dktypes.AnnotatedDocument{"revision.private.json": replayed.revised, "format-gate.private.json": replayed.cleaned} {
		raw, readErr := os.ReadFile(filepath.Join(out, name))
		expected, _ := json.MarshalIndent(doc, "", "  ")
		if readErr != nil || doc == nil || string(append(expected, '\n')) != string(raw) {
			failures = append(failures, "saved revised/cleaned document differs from production replay")
		}
	}
	if negativeStatus {
		var saved struct {
			Reason string `json:"reason"`
		}
		if readJSON(filepath.Join(out, "validation-typed-rejection.private.json"), &saved) != nil || replayed.finalRejection == nil || saved.Reason != replayed.finalRejection.Reason {
			failures = append(failures, "saved typed rejection differs from production replay")
		}
	}
	// 冻结的第一条实际请求必须与免费预检相同；最终请求由重放候选独立重建。
	// The first actual wire is the preflight wire; the final one is rebuilt from the replacement.
	var capture struct {
		Request string `json:"request_sha256"`
	}
	if readJSON(filepath.Join(out, "revision-request-capture.json"), &capture) != nil || capture.Request != pre.Revision.RequestSHA {
		failures = append(failures, "first wire differs from preflight")
	}
	var checkpoint struct {
		Format   string `json:"format"`
		Mode     string `json:"mode"`
		State    string `json:"acceptance_state"`
		Parent   string `json:"parent_proof_sha256"`
		Source   string `json:"original_sha256"`
		Accepted string `json:"accepted_cleaned_sha256"`
		Start    string `json:"run_identity_receipt_sha256"`
		Pass     string `json:"actual_pass_receipt"`
	}
	acceptedRaw, acceptedErr := os.ReadFile(filepath.Join(out, "accepted-cleaned.private.json"))
	checkpointRaw, checkpointErr := os.ReadFile(filepath.Join(out, "accepted-checkpoint.json"))
	if acceptedStatus {
		replayedRaw, _ := json.MarshalIndent(replayed.accepted, "", "  ")
		if acceptedErr != nil || checkpointErr != nil || json.Unmarshal(checkpointRaw, &checkpoint) != nil || checkpoint.Format != "cairn-receipt-accepted-checkpoint/v1" || checkpoint.Mode != receiptMode || checkpoint.State != "provisional_until_receipt_audit_pass" || checkpoint.Parent != parent.proofSHA || checkpoint.Source != parent.in.SourceSHA || checkpoint.Accepted != sha(acceptedRaw) || checkpoint.Start != sha(startRaw) || checkpoint.Pass != "validation-response.private.json" || replayed.accepted == nil || string(append(replayedRaw, '\n')) != string(acceptedRaw) {
			failures = append(failures, "provisional checkpoint/accepted candidate mismatch")
		}
	} else if acceptedErr == nil || checkpointErr == nil || replayed.accepted != nil {
		failures = append(failures, "negative outcome contains accepted artifacts")
	}
	integrity := len(failures) == 0
	accepted := integrity && acceptedStatus
	err = writeNew(filepath.Join(out, "receipt-audit-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json"), map[string]any{"format": "cairn-receipt-audit/v1", "mode": receiptMode, "status": finish.Status, "integrity_pass": integrity, "accepted": accepted, "pass": accepted, "errors": failures, "parent_proof_sha256": parent.proofSHA, "parent_manifest_sha256": parent.plan.ParentManifestSHA, "plan_sha256": planSHA, "preflight_sha256": sha(preRaw), "start_sha256": sha(startRaw), "finish_sha256": sha(finishRaw), "ledger_sha256": sha(ledger), "ledger_bytes": len(ledger), "counts": counts, "private_replay_file_sha256": replay.files, "accepted_checkpoint_sha256": sha(checkpointRaw), "accepted_cleaned_sha256": sha(acceptedRaw), "original_spans": "unknown", "assign_ids": "not performed", "quality_capacity_causal_effect": "not established"})
	if err != nil {
		return err
	}
	if !accepted {
		return fmt.Errorf("receipt: no completely audited accepted candidate")
	}
	return nil
}

type receiptCLI struct {
	mode, root, manifest, pin, out         string
	python, pythonSHA, runtime, runtimeSHA string
	allowPaid                              bool
}

func runReceiptCLI(c receiptCLI) error {
	if c.mode != "receipt-preflight" && c.mode != "receipt-run" && c.mode != "receipt-audit" {
		return fmt.Errorf("receipt: unknown mode")
	}
	if c.mode == "receipt-run" && !c.allowPaid || c.mode != "receipt-run" && c.allowPaid {
		return fmt.Errorf("receipt: mode/admission mismatch")
	}
	parent, err := loadReceiptParent(c.root, c.manifest, c.pin)
	if err != nil {
		return err
	}
	if c.mode == "receipt-audit" {
		return receiptAudit(c.out, parent, c.pin)
	}
	adapter := filepath.Join(rootPath(), "e2e/benchmarks/multihop_public/capacity/token_count.py")
	raw, err := os.ReadFile(adapter)
	if err != nil {
		return err
	}
	counter := externalCounter{Python: c.python, PythonSHA: c.pythonSHA, Adapter: adapter, AdapterSHA: sha(raw), Receipt: c.runtime, ReceiptSHA: c.runtimeSHA}
	if c.mode == "receipt-preflight" {
		return receiptPreflight(context.Background(), parent, c.out, c.pin, counter)
	}
	return receiptRun(context.Background(), parent, c.out, c.pin, counter)
}
