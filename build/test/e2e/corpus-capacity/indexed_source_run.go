package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	buildmeta "github.com/xcosmosbox/cairn/build/internal"
	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

const indexedMode = "original-indexed-generation-validation/v1"
const indexedProvisional = "PROVISIONAL_GATEB_PASS_REQUIRES_ROOT_REVIEW"

// indexedPlan 授权唯一已认证原文，不把标注前的原文伪装成 member 来源。
// A separately pinned source-only experiment; historical candidates/feedback are not inputs.
type indexedPlan struct {
	Format         string                `json:"format"`
	Mode           string                `json:"mode"`
	SourceRole     string                `json:"source_role"`
	Document       string                `json:"document_id"`
	Skill          string                `json:"skill"`
	Path           string                `json:"path"`
	SourceURL      string                `json:"source_url"`
	OriginalSHA    string                `json:"original_sha256"`
	BodySHA        string                `json:"body_sha256"`
	CorpusSHA      string                `json:"corpus_sha256"`
	OriginalBytes  int                   `json:"original_bytes"`
	BodyStart      int                   `json:"body_start_byte"`
	BodyBytes      int                   `json:"body_bytes"`
	HelperCommit   string                `json:"helper_commit"`
	HelperPlanSHA  string                `json:"helper_plan_sha256"`
	IndexSHA       string                `json:"source_index_sha256"`
	PrepCommit     string                `json:"prep_archive_commit"`
	DiagnosticsSHA string                `json:"diagnostics_sha256"`
	Counter        seededCounterIdentity `json:"counter_identity"`
	MaxTokens      int                   `json:"max_tokens"`
	MaxWires       int                   `json:"max_actual_wires"`
	PerCallWires   int                   `json:"max_wires_per_call"`
	RequestSeconds int                   `json:"request_timeout_seconds"`
	RunSeconds     int                   `json:"run_timeout_seconds"`
	Concurrency    int                   `json:"logical_concurrency"`
}

func knownIndexedPlan() indexedPlan {
	return indexedPlan{
		Format: "cairn-original-indexed-plan/v1", Mode: indexedMode, SourceRole: "annotation_input_original",
		Document: seededDocument, Skill: "multihop-public-originals", Path: "references/" + seededDocument + ".md",
		SourceURL:   "https://www.wired.com/story/best-october-prime-day-deals-2023-5/",
		OriginalSHA: seededOriginalSHA, BodySHA: "4e0c6e6644b39e5c4be056773abb9a7a1c2399b6a98c65ca940094afe7059bbb",
		CorpusSHA: "20b61b5ab84de84a927420c5d265b7ec8d859ae49980699958a787ade9e4d28f", OriginalBytes: 71332, BodyStart: 213, BodyBytes: 71119,
		HelperCommit: "f746581e53ffae858d79c4f409af0bd33c1d68bb", HelperPlanSHA: "ef21256ca81d9d8eeadd13bbb14a3fe25fe6ea8d36320859e5cc73cad446a212", IndexSHA: "f7fe38fa39f56491949052112f5d75c06cd71210336cc991218455df928160a8",
		PrepCommit: "bf6b55a072734987605c72dcaca6e63e8a2259f9", DiagnosticsSHA: "ba19cf4a5e342756cac4b387cd8e0b299a74bea3b61b60a27cd2e38dbc884862",
		Counter:   seededCounterIdentity{PythonSHA: "fa67443527ed9647f760d807e2a38f26340757123e643c4639cf273ed15d5ea7", AdapterSHA: "6ae23dc721898eaad4f950f45e39bcd0716821bfc6ab1c5a6cec31e24ae622cc", RuntimeSHA: "014e3ce5e3227119f5ef3b4ac1e8f9dc3e044926cc3914aa0966edece419a6ec", CounterID: "deepseek-recipe@935817c66f5d6e64838f419692545f06787e485f/v41"},
		MaxTokens: 65536, MaxWires: 2, PerCallWires: 1, RequestSeconds: 600, RunSeconds: 1800, Concurrency: 1,
	}
}

type indexedInputs struct {
	spec        indexedPlan
	original    string
	diagnostics []byte // Opaque review artifact; never passed to a model builder.
	helper      *annotation.SourceIndexedPlan
}

func indexedBuildInputs(spec indexedPlan, original string, diagnostics []byte) (indexedInputs, error) {
	if spec != knownIndexedPlan() || !utf8.ValidString(original) || len(original) != spec.OriginalBytes || sha([]byte(original)) != spec.OriginalSHA || len(original[spec.BodyStart:]) != spec.BodyBytes || sha([]byte(original[spec.BodyStart:])) != spec.BodySHA || sha([]byte(spec.SourceURL)) != spec.Document || sha(diagnostics) != spec.DiagnosticsSHA {
		return indexedInputs{}, fmt.Errorf("indexed: independently pinned original/diagnostic identity required")
	}
	helper, err := annotation.BuildSourceIndexedPlan(annotation.SourceIndexedInput{Skill: spec.Skill, Path: spec.Path, Original: original, ExpectedOriginalSHA256: spec.OriginalSHA})
	if err != nil || helper.Identity().IndexSHA256 != spec.IndexSHA || helper.Identity().PlanSHA256 != spec.HelperPlanSHA {
		return indexedInputs{}, fmt.Errorf("indexed: accepted source-only helper identity changed")
	}
	return indexedInputs{spec: spec, original: original, diagnostics: append([]byte(nil), diagnostics...), helper: helper}, nil
}

func loadIndexedInputs(originalPath, diagnosticsPath, manifest, pin string) (indexedInputs, error) {
	raw, err := os.ReadFile(manifest)
	var spec indexedPlan
	if err != nil || pin == "" || sha(raw) != pin || !utf8.Valid(raw) || stagedBridgeUniqueJSON(raw) != nil || decodeStrict(raw, &spec) != nil || !stagedBridgeSameJSON(raw, knownIndexedPlan()) {
		return indexedInputs{}, fmt.Errorf("indexed: exact independent experiment manifest required")
	}
	original, err := os.ReadFile(originalPath)
	if err != nil {
		return indexedInputs{}, fmt.Errorf("indexed: original unavailable")
	}
	diagnostics, err := os.ReadFile(diagnosticsPath)
	if err != nil {
		return indexedInputs{}, fmt.Errorf("indexed: opaque private diagnostic freeze unavailable")
	}
	return indexedBuildInputs(spec, string(original), diagnostics)
}

func indexedClientConfig() llm.Config {
	return llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_LLM_API_KEY", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: 65536, Timeout: "600s", MaxRetries: 1}
}

// indexedRender 调用与 doComplete 相同的生产序列化器；不经伪造 Complete/HTTP 捕获。
// RenderRequest does no credential lookup, token count, admission or HTTP.
func indexedRender(req llm.CompleteRequest) ([]byte, error) {
	client, err := llm.NewOpenAICompatClient(indexedClientConfig())
	if err != nil {
		return nil, err
	}
	endpoint, raw, err := client.RenderRequest(req)
	var wire wireRequest
	var fields map[string]json.RawMessage
	if err != nil || endpoint != dkconfig.DeepSeekEndpoint || json.Unmarshal(raw, &wire) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 5 || wire.Model != dkconfig.DeepSeekFlashModel || wire.MaxTokens != 65536 || wire.Thinking.Type != "enabled" || wire.ResponseFormat.Type != "json_object" || len(wire.Messages) != 2 || wire.Messages[0].Role != "system" || wire.Messages[1].Role != "user" || revisionUUIDInRequest(req) {
		return nil, fmt.Errorf("indexed: exact production profile or full rendered UUID guard")
	}
	return raw, nil
}

func revisionUUIDInRequest(req llm.CompleteRequest) bool {
	return sourceUUIDPatternBridge.MatchString(req.System) || sourceUUIDPatternBridge.MatchString(req.User)
}

func indexedProductionDigest() string { return buildmeta.SourceDigest("annotation/", "llm/") }
func indexedBinarySHA() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	return sha(b), err
}

type indexedWireReceipt struct {
	RequestSHA string     `json:"request_sha256"`
	Count      tokenCount `json:"token_count"`
}

type indexedPreflightRecord struct {
	Format         string                           `json:"format"`
	Mode           string                           `json:"mode"`
	SourceRole     string                           `json:"source_role"`
	PlanSHA        string                           `json:"plan_sha256"`
	OriginalSHA    string                           `json:"original_sha256"`
	DiagnosticsSHA string                           `json:"diagnostics_sha256"`
	Helper         annotation.SourceIndexedIdentity `json:"helper_identity"`
	Binary         string                           `json:"binary_sha256"`
	Driver         string                           `json:"driver_source_digest"`
	Production     string                           `json:"production_source_digest"`
	Counter        seededCounterIdentity            `json:"counter_identity"`
	Generation     indexedWireReceipt               `json:"generation"`
	ModelCalls     int                              `json:"model_calls"`
	FutureGate     string                           `json:"future_gate_wire"`
}

func indexedPreflight(ctx context.Context, in indexedInputs, out, planSHA string, counter tokenCounter) error {
	identity, err := counterSeedIdentity(counter)
	if err != nil || identity != in.spec.Counter {
		return fmt.Errorf("indexed: reviewed official runtime required")
	}
	req, err := in.helper.Request()
	if err != nil {
		return err
	}
	wire, err := indexedRender(req)
	if err != nil {
		return err
	}
	count, err := counter.Count(ctx, wire)
	if err != nil || !identity.matches(count) {
		return fmt.Errorf("indexed: first actual request count/context unavailable")
	}
	binary, err := indexedBinarySHA()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("indexed: fresh exclusive output directory required")
	}
	pre := indexedPreflightRecord{Format: "cairn-indexed-preflight/v1", Mode: indexedMode, SourceRole: in.spec.SourceRole, PlanSHA: planSHA, OriginalSHA: in.spec.OriginalSHA, DiagnosticsSHA: in.spec.DiagnosticsSHA, Helper: in.helper.Identity(), Binary: binary, Driver: driverSourceDigest(), Production: indexedProductionDigest(), Counter: identity, Generation: indexedWireReceipt{sha(wire), count}, FutureGate: "unknown until actual strict candidate; counted and guarded before its sole admission"}
	for name, value := range map[string]any{"indexed-preflight.json": pre, "source-index.private.json": in.helper.Blocks(), "generation-preflight-request.private.json": req} {
		if err := writeNew(filepath.Join(out, name), value); err != nil {
			return err
		}
	}
	for name, raw := range map[string][]byte{"original.private.md": []byte(in.original), "diagnostics-abc.private.json": in.diagnostics, "generation-preflight-wire.private.json": wire} {
		if err := writeBytesNew(filepath.Join(out, name), raw); err != nil {
			return err
		}
	}
	return nil
}

func readIndexedPreflight(out string, in indexedInputs, planSHA string) (indexedPreflightRecord, []byte, error) {
	var pre indexedPreflightRecord
	raw, err := receiptRead(out, "indexed-preflight.json")
	binary, berr := indexedBinarySHA()
	if err != nil || berr != nil || decodeStrict(raw, &pre) != nil || !stagedBridgeSameJSON(raw, pre) || pre.Format != "cairn-indexed-preflight/v1" || pre.Mode != indexedMode || pre.SourceRole != in.spec.SourceRole || pre.PlanSHA != planSHA || pre.OriginalSHA != in.spec.OriginalSHA || pre.DiagnosticsSHA != in.spec.DiagnosticsSHA || pre.Helper != in.helper.Identity() || pre.Binary != binary || pre.Driver != driverSourceDigest() || pre.Production != indexedProductionDigest() || pre.Counter != in.spec.Counter || pre.ModelCalls != 0 || !pre.Counter.matches(pre.Generation.Count) {
		return pre, raw, fmt.Errorf("indexed: preflight source/binary/runtime/recipe mismatch")
	}
	req, _ := in.helper.Request()
	wire, err := indexedRender(req)
	if err != nil || sha(wire) != pre.Generation.RequestSHA {
		return pre, raw, fmt.Errorf("indexed: first production wire changed")
	}
	for name, want := range map[string][]byte{"original.private.md": []byte(in.original), "diagnostics-abc.private.json": in.diagnostics, "generation-preflight-wire.private.json": wire} {
		b, e := receiptRead(out, name)
		if e != nil || !bytes.Equal(b, want) {
			return pre, raw, fmt.Errorf("indexed: preflight private input changed")
		}
	}
	for name, want := range map[string]any{"generation-preflight-request.private.json": req, "source-index.private.json": in.helper.Blocks()} {
		b, e := receiptRead(out, name)
		if e != nil || !stagedBridgeSameJSON(b, want) {
			return pre, raw, fmt.Errorf("indexed: private recipe/index changed")
		}
	}
	return pre, raw, nil
}

type indexedBinding struct {
	owner   *indexedWirePolicy
	phase   string
	request llm.CompleteRequest
	wire    []byte
	count   tokenCount
}

func (b *indexedBinding) bind(req llm.CompleteRequest, info *callInfo) error {
	if b == nil || b.owner == nil || !reflect.DeepEqual(req, b.request) || b.owner.in == nil {
		return fmt.Errorf("indexed: missing or changed private source binding")
	}
	p := b.owner
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active != b || p.err != nil || p.boundInfo != nil || info.ID < 1 || indexedBindingDigest(b) != p.preparedSeal {
		return fmt.Errorf("indexed: private logical call already bound or invalid")
	}
	info.Mode, info.Phase = indexedMode, b.phase
	info.Stage = "annotation"
	if b.phase == "validation" {
		info.Stage = "semantic_gate"
	} else if b.phase != "generation" {
		return fmt.Errorf("indexed: unknown phase")
	}
	info.Document, info.Documents, info.SourceSHA = b.owner.in.spec.Document, []string{b.owner.in.spec.Document}, b.owner.in.spec.OriginalSHA
	info.ParentSHA = ""
	p.boundInfo, p.boundIdentity = info, indexedIdentityOf(info)
	return nil
}

type indexedObserved struct {
	binding  *indexedBinding
	response *llm.CompleteResponse
	observed bool
	info     *callInfo
	identity indexedCallIdentity
}

type indexedCallIdentity struct {
	ID                                           int64
	Stage, Mode, Phase, Document, Source, Parent string
	Documents                                    []string
}

func indexedIdentityOf(info *callInfo) indexedCallIdentity {
	return indexedCallIdentity{info.ID, info.Stage, info.Mode, info.Phase, info.Document, info.SourceSHA, info.ParentSHA, append([]string(nil), info.Documents...)}
}

func (row indexedObserved) identityIntact() bool {
	return row.info != nil && row.info.Attempts.Load() == 1 && reflect.DeepEqual(indexedIdentityOf(row.info), row.identity)
}

type indexedWirePolicy struct {
	mu            sync.Mutex
	in            *indexedInputs
	pre           indexedPreflightRecord
	counter       tokenCounter
	out           string
	cancel        context.CancelFunc
	err           error
	active        *indexedBinding
	rows          []indexedObserved
	validation    *llm.CompleteRequest
	boundInfo     *callInfo
	boundIdentity indexedCallIdentity
	preparedSeal  string
}

// 独立保存在 policy 内的期望值不能随返回的 binding 一起被改写。
// The context object's copied wire/count is not its own authority at dispatch.
func indexedBindingDigest(b *indexedBinding) string {
	raw, err := json.Marshal(struct {
		Phase   string
		Request llm.CompleteRequest
		WireSHA string
		Count   tokenCount
	}{b.phase, b.request, sha(b.wire), b.count})
	if err != nil {
		return ""
	}
	return sha(raw)
}

func (p *indexedWirePolicy) stop(err error) {
	if p.err == nil {
		p.err = err
	}
	if p.cancel != nil {
		p.cancel()
	}
}

func (p *indexedWirePolicy) prepare(ctx context.Context, phase string, req llm.CompleteRequest) (*indexedBinding, error) {
	wire, err := indexedRender(req)
	if err != nil {
		return nil, err
	}
	count, err := p.counter.Count(ctx, append([]byte(nil), wire...))
	identity, ierr := counterSeedIdentity(p.counter)
	if err != nil || ierr != nil || identity != p.pre.Counter || !identity.matches(count) {
		return nil, fmt.Errorf("indexed: complete actual wire/runtime cannot be counted")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil || p.active != nil || len(p.rows) >= 2 || (len(p.rows) == 0 && phase != "generation") || (len(p.rows) == 1 && (phase != "validation" || !p.rows[0].observed || !p.rows[0].identityIntact() || p.rows[0].response == nil || p.validation == nil || !reflect.DeepEqual(req, *p.validation))) {
		return nil, fmt.Errorf("indexed: wrong phase, prior failure or two-call budget")
	}
	if len(p.rows) == 0 && (sha(wire) != p.pre.Generation.RequestSHA || count != p.pre.Generation.Count) {
		return nil, fmt.Errorf("indexed: actual first wire/count differs from frozen preflight")
	}
	b := &indexedBinding{owner: p, phase: phase, request: stagedBridgeCopyRequest(req), wire: append([]byte(nil), wire...), count: count}
	p.active = b
	p.preparedSeal = indexedBindingDigest(b)
	p.boundInfo, p.boundIdentity = nil, indexedCallIdentity{}
	return b, nil
}

// 只从已经观察且持久化的 generation 响应重建候选，才能授权动态 GateB 原文请求。
// Caller-supplied documents cannot fabricate a post-generation source/candidate binding.
func (p *indexedWirePolicy) authorizeValidation() (*annotation.SourceIndexedCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil || p.active != nil || len(p.rows) != 1 || !p.rows[0].observed || !p.rows[0].identityIntact() || p.rows[0].response == nil || p.validation != nil {
		return nil, fmt.Errorf("indexed: generation observation not complete")
	}
	candidate, err := p.in.helper.Validate(p.rows[0].response)
	if err != nil {
		return nil, err
	}
	request, err := captureSeedGate(seededInputs{Original: p.in.original}, candidate.Document())
	if err != nil {
		return nil, err
	}
	if _, err := indexedRender(request); err != nil {
		return nil, err
	}
	p.validation = &request
	return candidate, nil
}

func (p *indexedWirePolicy) admit(*callInfo, wireRequest, []byte, tokenCount) error {
	return fmt.Errorf("indexed: actual forwarded request context required")
}

func (p *indexedWirePolicy) admitRequest(request *http.Request, info *callInfo, parsed wireRequest, wire []byte, count tokenCount) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := request.Context().Value(seededBindingKey{}).(*indexedBinding)
	actual, err := counterSeedIdentity(p.counter)
	wantStage := "annotation"
	if b != nil && b.phase == "validation" {
		wantStage = "semantic_gate"
	}
	if !ok || b == nil || b != p.active || b.owner != p || indexedBindingDigest(b) != p.preparedSeal || p.err != nil || len(p.rows) >= 2 || err != nil || actual != p.pre.Counter || info != p.boundInfo || !reflect.DeepEqual(indexedIdentityOf(info), p.boundIdentity) || info.ID < 1 || info.Stage != wantStage || info.Mode != indexedMode || info.Phase != b.phase || info.Document != p.in.spec.Document || !reflect.DeepEqual(info.Documents, []string{p.in.spec.Document}) || info.SourceSHA != p.in.spec.OriginalSHA || info.ParentSHA != "" || info.Attempts.Load() != 1 || request.URL.String() != dkconfig.DeepSeekEndpoint || request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" || request.ContentLength != int64(len(wire)) || !bytes.Equal(wire, b.wire) || count != b.count || parsed.Model != dkconfig.DeepSeekFlashModel || parsed.MaxTokens != 65536 || (len(p.rows) == 1 && !p.rows[0].identityIntact()) {
		p.stop(fmt.Errorf("indexed: actual request/source/profile/runtime binding changed"))
		return p.err
	}
	if (len(p.rows) == 0 && b.phase != "generation") || (len(p.rows) == 1 && b.phase != "validation") {
		p.stop(fmt.Errorf("indexed: duplicate actual wire or phase"))
		return p.err
	}
	p.rows = append(p.rows, indexedObserved{binding: b, info: info, identity: indexedIdentityOf(info)})
	return nil
}

func (p *indexedWirePolicy) observe(info *callInfo, raw []byte, outcome map[string]any, persistErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.rows) == 0 || p.active == nil || p.rows[len(p.rows)-1].binding != p.active {
		p.stop(fmt.Errorf("indexed: unbound actual response"))
		return
	}
	row := &p.rows[len(p.rows)-1]
	if row.observed {
		p.stop(fmt.Errorf("indexed: duplicate actual outcome"))
		return
	}
	// 在判断内容/usage 前保留实际帧，不记录认证头或凭据。
	// Preserve the actual response body before rejecting uncertain or invalid evidence.
	saveErr := writeBytesNew(filepath.Join(p.out, row.identity.Phase+"-http-response.private.json"), raw)
	response, parseErr := indexedResponseEvidence(raw, row.binding.count.Input)
	row.observed, row.response = true, response
	if persistErr != nil || saveErr != nil || parseErr != nil || indexedBindingDigest(row.binding) != p.preparedSeal || !row.identityIntact() || row.info != info || outcome["http_status"] != 200 || outcome["transport_error"] == true || outcome["response_read_error"] == true {
		p.stop(fmt.Errorf("indexed: actual response/usage/ledger evidence incomplete"))
	}
}

type indexedPhaseKey struct{}
type indexedSavedResponse struct {
	Response      *llm.CompleteResponse `json:"response"`
	CompleteError bool                  `json:"complete_error"`
}
type indexedRecordingClient struct {
	next   llm.Client
	policy *indexedWirePolicy
}

func (c *indexedRecordingClient) ProviderName() string { return c.next.ProviderName() }
func (c *indexedRecordingClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	phase, _ := ctx.Value(indexedPhaseKey{}).(string)
	binding, err := c.policy.prepare(ctx, phase, req)
	if err != nil {
		c.policy.cancel()
		return nil, err
	}
	out := c.policy.out
	metadata := seededRequestMetadata(req, binding.wire, binding.count)
	metadata["phase"], metadata["source_role"], metadata["original_sha256"], metadata["helper_identity"] = phase, c.policy.in.spec.SourceRole, c.policy.in.spec.OriginalSHA, c.policy.in.helper.Identity()
	for name, value := range map[string]any{phase + "-request.private.json": req, phase + "-request-capture.json": metadata} {
		if err := writeNew(filepath.Join(out, name), value); err != nil {
			c.policy.cancel()
			return nil, fmt.Errorf("indexed: private request persistence failed")
		}
	}
	if err := writeBytesNew(filepath.Join(out, phase+"-wire.private.json"), binding.wire); err != nil {
		c.policy.cancel()
		return nil, err
	}
	response, completeErr := c.next.Complete(context.WithValue(ctx, seededBindingKey{}, binding), req)
	c.policy.mu.Lock()
	if completeErr == nil {
		if c.policy.err != nil || len(c.policy.rows) == 0 || c.policy.rows[len(c.policy.rows)-1].binding != binding || !c.policy.rows[len(c.policy.rows)-1].observed || !c.policy.rows[len(c.policy.rows)-1].identityIntact() || response == nil || !reflect.DeepEqual(response, c.policy.rows[len(c.policy.rows)-1].response) {
			completeErr = fmt.Errorf("indexed: completion differs from durable actual response")
		}
	}
	if completeErr != nil {
		c.policy.stop(fmt.Errorf("indexed: logical request incomplete"))
	} else {
		c.policy.active = nil
	}
	c.policy.mu.Unlock()
	if err := writeNew(filepath.Join(out, phase+"-response.private.json"), indexedSavedResponse{response, completeErr != nil}); err != nil {
		c.policy.cancel()
		return response, fmt.Errorf("indexed: parsed response persistence failed")
	}
	return response, completeErr
}

type indexedExecution struct {
	Status string
	Phases []string
	Items  int
	GateB  string
}

func executeIndexed(ctx context.Context, in indexedInputs, client llm.Client, out string, policy *indexedWirePolicy) (indexedExecution, error) {
	result := indexedExecution{Status: "FAIL_generation", Phases: []string{"generation"}, GateB: "not_called"}
	request, err := in.helper.Request()
	if err != nil {
		return result, err
	}
	response, err := client.Complete(context.WithValue(ctx, indexedPhaseKey{}, "generation"), request)
	if err != nil {
		return result, err
	}
	result.Status = "FAIL_indexed_validation"
	// Both the execution and the policy rebuild the strict candidate from observed text;
	// the private audit remains distinct from semantic acceptance.
	candidate, err := in.helper.Validate(response)
	if err != nil {
		return result, err
	}
	if policy != nil {
		bound, err := policy.authorizeValidation()
		if err != nil || !reflect.DeepEqual(candidate.Audit(), boundAudit(bound)) {
			return result, fmt.Errorf("indexed: observed strict candidate changed")
		}
	}
	document := candidate.Document()
	result.Items = len(document.Items)
	for name, value := range map[string]any{"indexed-audit.private.json": candidate.Audit(), "format-gate.private.json": document} {
		if err := writeNew(filepath.Join(out, name), value); err != nil {
			return result, err
		}
	}
	result.Status, result.Phases, result.GateB = "FAIL_final_gate", append(result.Phases, "validation"), "unknown"
	err = annotation.NewSemanticGate(client, 65536, 1).Check(context.WithValue(ctx, indexedPhaseKey{}, "validation"), in.original, document)
	if err != nil {
		var rejection *annotation.SemanticRejection
		if errors.As(err, &rejection) {
			result.GateB = "false"
			if saveErr := writeNew(filepath.Join(out, "validation-typed-rejection.private.json"), rejection); saveErr != nil {
				return result, saveErr
			}
		}
		return result, err
	}
	result.Status, result.GateB = indexedProvisional, "true"
	// A machine true is only a provisional artifact; never name it an accepted checkpoint.
	err = writeNew(filepath.Join(out, "provisional-candidate.private.json"), map[string]any{"format": "cairn-indexed-provisional-candidate/v1", "state": indexedProvisional, "helper_identity": in.helper.Identity(), "document": document, "accepted": false, "assign_ids": "not performed", "root_review": "required: ALL no_annotation rows, independent A12/B2/C3 fidelity review and immutable archive; separate checkpoint adapter"})
	return result, err
}

func boundAudit(candidate *annotation.SourceIndexedCandidate) annotation.SourceIndexedAudit {
	if candidate == nil {
		return annotation.SourceIndexedAudit{}
	}
	return candidate.Audit()
}

type indexedStart struct {
	Format         string                           `json:"format"`
	Mode           string                           `json:"mode"`
	SourceRole     string                           `json:"source_role"`
	PlanSHA        string                           `json:"plan_sha256"`
	PreflightSHA   string                           `json:"preflight_sha256"`
	OriginalSHA    string                           `json:"original_sha256"`
	DiagnosticsSHA string                           `json:"diagnostics_sha256"`
	Helper         annotation.SourceIndexedIdentity `json:"helper_identity"`
	Binary         string                           `json:"binary_sha256"`
	Driver         string                           `json:"driver_source_digest"`
	Production     string                           `json:"production_source_digest"`
	Counter        seededCounterIdentity            `json:"counter_identity"`
	StartedAt      string                           `json:"started_at"`
	MaxWires       int                              `json:"max_actual_wires"`
	PerCallWires   int                              `json:"max_wires_per_call"`
}
type indexedFinish struct {
	Format         string   `json:"format"`
	Mode           string   `json:"mode"`
	SourceRole     string   `json:"source_role"`
	Status         string   `json:"status"`
	GateB          string   `json:"gate_b"`
	FinishedAt     string   `json:"finished_at"`
	Phases         []string `json:"phases"`
	Calls          int      `json:"logical_calls"`
	Wires          int      `json:"actual_wires"`
	Responses      int      `json:"http_responses"`
	MissingUsage   int      `json:"responses_without_usage"`
	Items          int      `json:"items"`
	InputTokens    int64    `json:"reported_input_tokens"`
	OutputTokens   int64    `json:"reported_output_tokens"`
	ExecutionError bool     `json:"execution_error"`
	LedgerOK       bool     `json:"ledger_ok"`
}

func indexedRun(ctx context.Context, in indexedInputs, out, planSHA string, counter tokenCounter) error {
	if strings.TrimSpace(os.Getenv("CAIRN_LLM_API_KEY")) == "" {
		return fmt.Errorf("indexed: nonempty authentication key required before run")
	}
	pre, rawPre, err := readIndexedPreflight(out, in, planSHA)
	if err != nil {
		return err
	}
	identity, err := counterSeedIdentity(counter)
	req, _ := in.helper.Request()
	wire, renderErr := indexedRender(req)
	count, countErr := counter.Count(ctx, wire)
	if err != nil || renderErr != nil || countErr != nil || identity != pre.Counter || count != pre.Generation.Count || sha(wire) != pre.Generation.RequestSHA {
		return fmt.Errorf("indexed: first request/runtime drift before run")
	}
	ctx, cancel := context.WithTimeout(ctx, 1800*time.Second)
	defer cancel()
	start := indexedStart{Format: "cairn-indexed-run/v1", Mode: indexedMode, SourceRole: in.spec.SourceRole, PlanSHA: planSHA, PreflightSHA: sha(rawPre), OriginalSHA: in.spec.OriginalSHA, DiagnosticsSHA: in.spec.DiagnosticsSHA, Helper: in.helper.Identity(), Binary: pre.Binary, Driver: pre.Driver, Production: pre.Production, Counter: identity, StartedAt: now(), MaxWires: 2, PerCallWires: 1}
	if err := writeNew(filepath.Join(out, "indexed-run-start.json"), start); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(out, "indexed-private.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	oldLog := log.Writer()
	log.SetOutput(logFile)
	defer log.SetOutput(oldLog)
	ledger, file, err := openLedger(filepath.Join(out, "requests.jsonl"), cancel)
	if err != nil {
		return err
	}
	defer file.Close()
	policy := &indexedWirePolicy{in: &in, pre: pre, counter: counter, out: out, cancel: cancel}
	observer := &traceTransport{next: http.DefaultTransport, ledger: ledger, counter: counter, seedPolicy: policy}
	client, err := llm.NewOpenAICompatClientWithTransport(indexedClientConfig(), observer)
	if err != nil {
		return err
	}
	bounded := &boundedClient{next: client, sem: make(chan struct{}, 1), ledger: ledger}
	recorded := &indexedRecordingClient{next: bounded, policy: policy}
	result, runErr := executeIndexed(ctx, in, recorded, out, policy)
	finish := indexedFinish{Format: "cairn-indexed-finish/v1", Mode: indexedMode, SourceRole: in.spec.SourceRole, Status: result.Status, GateB: result.GateB, FinishedAt: now(), Phases: result.Phases, Calls: int(bounded.sequence.Load()), Wires: int(observer.sent.Load()), Responses: int(observer.responses.Load()), MissingUsage: int(observer.missingUsage.Load()), Items: result.Items, InputTokens: observer.input.Load(), OutputTokens: observer.output.Load(), ExecutionError: runErr != nil, LedgerOK: ledger.failure() == nil}
	finishErr := writeNew(filepath.Join(out, "indexed-run-finish.json"), finish)
	auditErr := indexedAudit(out, in, planSHA, counter)
	if runErr != nil || finishErr != nil || auditErr != nil {
		return fmt.Errorf("indexed: attempt is not a provisional verified candidate; retained receipts")
	}
	return nil
}

type indexedTraceRow struct {
	receiptTraceRow
	Attempt       int64  `json:"http_attempt"`
	Admitted      bool   `json:"admitted"`
	CompleteError bool   `json:"complete_error"`
	WireAttempts  int    `json:"wire_attempts"`
	RawSHA        string `json:"response_sha256"`
	fields        map[string]json.RawMessage
}

func indexedEvidenceFields(fields map[string]json.RawMessage, expected map[string]any) bool {
	for key := range fields {
		for canonical := range expected {
			if strings.EqualFold(key, canonical) && key != canonical {
				return false
			}
		}
	}
	for key, want := range expected {
		raw, ok := fields[key]
		if !ok || !stagedBridgeSameJSON(raw, want) {
			return false
		}
	}
	return true
}

// 共享生产响应证据校验后，再要求关键键精确拼写，防止 Go 大小写兼容掩盖歧义。
// Live and offline validation share these exact provider-field rules.
func indexedResponseEvidence(raw []byte, input int) (*llm.CompleteResponse, error) {
	response, err := stagedBridgeResponseEvidence(raw, input)
	if err != nil {
		return response, err
	}
	bad := func() (*llm.CompleteResponse, error) {
		return response, fmt.Errorf("indexed: ambiguous critical provider fields")
	}
	var top, choice, message, usage map[string]json.RawMessage
	var choices []json.RawMessage
	if json.Unmarshal(raw, &top) != nil || json.Unmarshal(top["choices"], &choices) != nil || len(choices) != 1 || json.Unmarshal(choices[0], &choice) != nil || json.Unmarshal(choice["message"], &message) != nil || json.Unmarshal(top["usage"], &usage) != nil {
		return bad()
	}
	for _, group := range []struct {
		fields map[string]json.RawMessage
		keys   []string
	}{{top, []string{"model", "choices", "usage"}}, {choice, []string{"finish_reason", "message"}}, {message, []string{"content"}}, {usage, []string{"prompt_tokens", "completion_tokens", "total_tokens"}}} {
		for key := range group.fields {
			for _, canonical := range group.keys {
				if strings.EqualFold(key, canonical) && key != canonical {
					return bad()
				}
			}
		}
	}
	var model string
	if json.Unmarshal(top["model"], &model) != nil || (model != "deepseek-flash" && model != "deepseek-v4.1-flash-1017") || !indexedEvidenceFields(choice, map[string]any{"finish_reason": "stop"}) || !indexedEvidenceFields(message, map[string]any{"content": response.Text}) || !indexedEvidenceFields(usage, map[string]any{"prompt_tokens": response.InputTokens, "completion_tokens": response.OutputTokens}) {
		return bad()
	}
	return response, nil
}

// 重放客户端只读一次私有快照，所有请求仍由真实 builder/renderer 重建并重新计数。
// This never dispatches; it binds every production request to actual raw/parsed outcomes.
type indexedReplayClient struct {
	in       indexedInputs
	pre      indexedPreflightRecord
	files    map[string][]byte
	rows     []indexedTraceRow
	counter  tokenCounter
	position int
	failures []string
}

func (*indexedReplayClient) ProviderName() string { return "offline-indexed-observed-response-replay" }
func (c *indexedReplayClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	bad := func() (*llm.CompleteResponse, error) {
		c.failures = append(c.failures, "actual request/response replay binding")
		return nil, fmt.Errorf("indexed: replay lacks complete bound observed evidence")
	}
	phase, _ := ctx.Value(indexedPhaseKey{}).(string)
	if c.position >= len(c.rows)/4 || (c.position == 0 && phase != "generation") || (c.position == 1 && phase != "validation") {
		return bad()
	}
	block := c.rows[c.position*4 : c.position*4+4]
	requestRow, responseRow := block[1], block[2]
	c.position++
	wire, err := indexedRender(req)
	if err != nil || !bytes.Equal(c.files[phase+"-wire.private.json"], wire) || !stagedBridgeSameJSON(c.files[phase+"-request.private.json"], req) {
		return bad()
	}
	identity, err := counterSeedIdentity(c.counter)
	count, countErr := c.counter.Count(ctx, append([]byte(nil), wire...))
	if err != nil || countErr != nil || identity != c.pre.Counter || !identity.matches(count) || requestRow.Count != count || requestRow.RequestSHA != sha(wire) || requestRow.SystemSHA != sha([]byte(req.System)) || requestRow.UserSHA != sha([]byte(req.User)) || (phase == "generation" && (count != c.pre.Generation.Count || sha(wire) != c.pre.Generation.RequestSHA)) {
		return bad()
	}
	if !indexedEvidenceFields(requestRow.fields, map[string]any{"admitted": true, "provider_attempt": 1, "http_attempt": requestRow.Call, "request_sha256": sha(wire), "request_bytes": len(wire), "requested_model": "deepseek-flash", "max_tokens": 65536, "thinking": map[string]string{"type": "enabled"}, "response_format": map[string]string{"type": "json_object"}, "token_count": count, "system_sha256": sha([]byte(req.System)), "system_bytes": len(req.System), "system_codepoints": utf8.RuneCountInString(req.System), "user_sha256": sha([]byte(req.User)), "user_bytes": len(req.User), "user_codepoints": utf8.RuneCountInString(req.User)}) {
		return bad()
	}
	metadata := seededRequestMetadata(req, wire, count)
	metadata["phase"], metadata["source_role"], metadata["original_sha256"], metadata["helper_identity"] = phase, c.in.spec.SourceRole, c.in.spec.OriginalSHA, c.in.helper.Identity()
	if !stagedBridgeSameJSON(c.files[phase+"-request-capture.json"], metadata) {
		return bad()
	}
	raw := c.files[phase+"-http-response.private.json"]
	actual, err := indexedResponseEvidence(raw, count.Input)
	var saved indexedSavedResponse
	if err != nil || decodeStrict(c.files[phase+"-response.private.json"], &saved) != nil || saved.CompleteError || saved.Response == nil || !reflect.DeepEqual(saved.Response, actual) || responseRow.RawSHA != sha(raw) || responseRow.ContentSHA != sha([]byte(actual.Text)) || responseRow.Status != 200 || responseRow.Finish != "stop" || !responseRow.ModelOK || (responseRow.Model != "deepseek-flash" && responseRow.Model != "deepseek-v4.1-flash-1017") || responseRow.Input != actual.InputTokens || responseRow.Output != actual.OutputTokens || responseRow.LocalInput != count.Input || responseRow.Delta == nil || *responseRow.Delta != 0 || responseRow.Usage != "reported" {
		return bad()
	}
	if !stagedBridgeSameJSON(c.files[phase+"-response.private.json"], indexedSavedResponse{actual, false}) {
		return bad()
	}
	var envelope struct {
		Model string                     `json:"model"`
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !indexedEvidenceFields(responseRow.fields, map[string]any{"http_attempt": responseRow.Call, "provider_attempt": 1, "http_status": 200, "response_json": true, "response_sha256": sha(raw), "response_bytes": len(raw), "returned_model": envelope.Model, "returned_model_sha256": sha([]byte(envelope.Model)), "model_identity_expected": true, "final_content_sha256": sha([]byte(actual.Text)), "finish_reason": "stop", "usage_status": "reported", "input_tokens": actual.InputTokens, "output_tokens": actual.OutputTokens, "local_input_tokens": count.Input, "provider_minus_local_input_tokens": 0}) {
		return bad()
	}
	for _, flag := range []string{"transport_error", "response_read_error", "cancelled"} {
		if value, ok := responseRow.fields[flag]; ok && !stagedBridgeSameJSON(value, false) {
			return bad()
		}
	}
	total, hasTotal := envelope.Usage["total_tokens"]
	ledgerTotal, hasLedgerTotal := responseRow.fields["total_tokens"]
	if hasTotal && !bytes.Equal(bytes.TrimSpace(total), []byte("null")) {
		if !hasLedgerTotal || !stagedBridgeSameJSON(ledgerTotal, actual.InputTokens+actual.OutputTokens) {
			return bad()
		}
	} else if hasLedgerTotal {
		return bad()
	}
	return saved.Response, nil
}

func indexedAudit(out string, in indexedInputs, planSHA string, counter tokenCounter) error {
	var failures []string
	pre, preRaw, err := readIndexedPreflight(out, in, planSHA)
	if err != nil {
		failures = append(failures, "preflight/source/binary identity")
	}
	files := map[string][]byte{}
	read := func(name string) []byte {
		if raw, ok := files[name]; ok {
			return raw
		}
		raw, e := receiptRead(out, name)
		if e != nil {
			failures = append(failures, "missing private artifact: "+name)
		}
		files[name] = raw
		return raw
	}
	var start indexedStart
	if decodeStrict(read("indexed-run-start.json"), &start) != nil || !stagedBridgeSameJSON(read("indexed-run-start.json"), start) || start.Format != "cairn-indexed-run/v1" || start.Mode != indexedMode || start.SourceRole != in.spec.SourceRole || start.PlanSHA != planSHA || start.PreflightSHA != sha(preRaw) || start.OriginalSHA != in.spec.OriginalSHA || start.DiagnosticsSHA != in.spec.DiagnosticsSHA || start.Helper != in.helper.Identity() || start.Binary != pre.Binary || start.Driver != pre.Driver || start.Production != pre.Production || start.Counter != pre.Counter || start.MaxWires != 2 || start.PerCallWires != 1 || start.StartedAt == "" {
		failures = append(failures, "run start binding")
	}
	var finish indexedFinish
	if decodeStrict(read("indexed-run-finish.json"), &finish) != nil || !stagedBridgeSameJSON(read("indexed-run-finish.json"), finish) || finish.Format != "cairn-indexed-finish/v1" || finish.Mode != indexedMode || finish.SourceRole != in.spec.SourceRole || !finish.LedgerOK || finish.MissingUsage != 0 || finish.FinishedAt == "" {
		failures = append(failures, "run finish binding")
	}
	ledger := read("requests.jsonl")
	counts, traceFailures := auditTraceBytes(ledger)
	failures = append(failures, traceFailures...)
	var rows []indexedTraceRow
	scanner := bufio.NewScanner(bytes.NewReader(ledger))
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var row indexedTraceRow
		if stagedBridgeUniqueJSON(scanner.Bytes()) != nil || json.Unmarshal(scanner.Bytes(), &row) != nil {
			failures = append(failures, "malformed/ambiguous ledger row")
		}
		if json.Unmarshal(scanner.Bytes(), &row.fields) != nil {
			failures = append(failures, "ledger object required")
		}
		rows = append(rows, row)
	}
	if scanner.Err() != nil || (len(rows) != 4 && len(rows) != 8) {
		failures = append(failures, "exact finite four-event call inventory")
	}
	calls := len(rows) / 4
	if counts["logical_calls"] != calls || counts["admitted_attempts"] != calls || counts["attempt_outcomes"] != calls || counts["rejected_attempts"] != 0 || finish.Calls != calls || finish.Wires != calls || finish.Responses != calls || len(finish.Phases) != calls {
		failures = append(failures, "logical/actual/response denominator")
	}
	var totalIn, totalOut int64
	for i, row := range rows {
		call := i/4 + 1
		phase, stage := "generation", "annotation"
		if call == 2 {
			phase, stage = "validation", "semantic_gate"
		}
		if call > 2 || row.Event != []string{"logical_start", "request", "response", "logical_end"}[i%4] || row.Call != int64(call) || row.Mode != indexedMode || row.Phase != phase || row.Stage != stage || row.Source != in.spec.OriginalSHA || row.Parent != "" {
			failures = append(failures, "exact source/phase/event order")
		}
		if !indexedEvidenceFields(row.fields, map[string]any{"event": []string{"logical_start", "request", "response", "logical_end"}[i%4], "call_id": call, "operation_mode": indexedMode, "phase": phase, "stage": stage, "original_sha256": in.spec.OriginalSHA}) {
			failures = append(failures, "exact typed ledger identity fields")
		}
		if parent, ok := row.fields["parent_proof_sha256"]; ok && !stagedBridgeSameJSON(parent, "") {
			failures = append(failures, "no historical parent in source-only mode")
		}
		for key := range row.fields {
			for _, canonical := range []string{"parent_proof_sha256", "document_id", "document_ids"} {
				if strings.EqualFold(key, canonical) && key != canonical {
					failures = append(failures, "case-fold identity alias")
				}
			}
		}
		if i%4 == 0 || i%4 == 1 {
			if row.Document != in.spec.Document || !reflect.DeepEqual(row.Documents, []string{in.spec.Document}) {
				failures = append(failures, "single original document binding")
			}
			if !indexedEvidenceFields(row.fields, map[string]any{"document_id": in.spec.Document, "document_ids": []string{in.spec.Document}}) {
				failures = append(failures, "exact typed original document fields")
			}
		} else {
			for key, want := range map[string]any{"document_id": in.spec.Document, "document_ids": []string{in.spec.Document}} {
				if value, ok := row.fields[key]; ok && !stagedBridgeSameJSON(value, want) {
					failures = append(failures, "contradictory optional outcome document")
				}
			}
		}
		if i%4 == 1 && (!row.Admitted || row.Attempt != int64(call) || row.Provider != 1 || !pre.Counter.matches(row.Count)) {
			failures = append(failures, "one actual wire and verified counter per call")
		}
		if i%4 == 2 {
			if row.Attempt != int64(call) || row.Provider != 1 {
				failures = append(failures, "outcome identity")
			}
			totalIn += int64(row.Input)
			totalOut += int64(row.Output)
		}
		if i%4 == 3 && (row.CompleteError || row.WireAttempts != 1) {
			failures = append(failures, "logical completion")
		}
		if i%4 == 3 && !indexedEvidenceFields(row.fields, map[string]any{"complete_error": false, "wire_attempts": 1}) {
			failures = append(failures, "exact boolean logical completion required")
		}
	}
	if finish.InputTokens != totalIn || finish.OutputTokens != totalOut {
		failures = append(failures, "reported total usage")
	}
	for i := 0; i < calls && i < 2; i++ {
		phase := []string{"generation", "validation"}[i]
		for _, suffix := range []string{"-request.private.json", "-wire.private.json", "-request-capture.json", "-http-response.private.json", "-response.private.json"} {
			read(phase + suffix)
		}
	}
	tmp, tmpErr := os.MkdirTemp("", "cairn-indexed-replay-")
	if tmpErr != nil {
		return tmpErr
	}
	defer os.RemoveAll(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	replay := &indexedReplayClient{in: in, pre: pre, files: files, rows: rows, counter: counter}
	result, replayErr := executeIndexed(ctx, in, replay, tmp, nil)
	failures = append(failures, replay.failures...)
	if result.Status != finish.Status || result.GateB != finish.GateB || result.Items != finish.Items || !reflect.DeepEqual(result.Phases, finish.Phases) || (replayErr != nil) != finish.ExecutionError || replay.position != calls {
		failures = append(failures, "complete production schema/semantic replay differs from finish")
	}
	// Only deterministic production artifacts are compared; replay timestamps never substitute evidence.
	entries, _ := os.ReadDir(tmp)
	for _, entry := range entries {
		want, e := os.ReadFile(filepath.Join(tmp, entry.Name()))
		if e != nil || !stagedBridgeSameJSON(read(entry.Name()), json.RawMessage(want)) {
			failures = append(failures, "actual strict/gate/provisional artifact differs: "+entry.Name())
		}
	}
	if finish.Status != indexedProvisional {
		if _, e := os.Lstat(filepath.Join(out, "provisional-candidate.private.json")); e == nil {
			failures = append(failures, "negative/incomplete run has a provisional candidate")
		}
	}
	for _, name := range []string{"accepted-checkpoint.json", "accepted-checkpoint.private.json", "accepted.private.json"} {
		if _, e := os.Lstat(filepath.Join(out, name)); e == nil {
			failures = append(failures, "indexed mode never creates accepted checkpoints")
		}
	}
	provisional := len(failures) == 0 && result.Status == indexedProvisional && result.GateB == "true"
	hashes := map[string]string{}
	for name, raw := range files {
		hashes[name] = sha(raw)
	}
	report := map[string]any{"format": "cairn-indexed-replay-audit/v1", "mode": indexedMode, "source_role": in.spec.SourceRole, "original_sha256": in.spec.OriginalSHA, "helper_identity": in.helper.Identity(), "plan_sha256": planSHA, "preflight_sha256": sha(preRaw), "ledger_sha256": sha(ledger), "private_artifact_sha256": hashes, "counts": counts, "integrity_pass": len(failures) == 0, "provisional": provisional, "accepted": false, "status": finish.Status, "gate_b": result.GateB, "errors": failures, "assign_ids": "not performed", "root_review": "required: ALL no_annotation rows + separate A12/B2/C3 diagnostics/fidelity/archive review; separate checkpoint adapter", "quality_capacity_causality_full609": "not established"}
	if err := writeNew(filepath.Join(out, "indexed-replay-audit-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json"), report); err != nil {
		return err
	}
	if !provisional {
		return fmt.Errorf("indexed: no replay-verified provisional candidate; retained audit")
	}
	return nil
}

type indexedCLI struct {
	mode, original, diagnostics, manifest, pin, out string
	python, pythonSHA, runtime, runtimeSHA          string
	allowPaid                                       bool
}

func runIndexedCLI(c indexedCLI) error {
	if c.mode != "indexed-preflight" && c.mode != "indexed-run" && c.mode != "indexed-audit" {
		return fmt.Errorf("indexed: unknown mode")
	}
	if (c.mode == "indexed-run") != c.allowPaid {
		return fmt.Errorf("indexed: mode/explicit admission mismatch")
	}
	in, err := loadIndexedInputs(c.original, c.diagnostics, c.manifest, c.pin)
	if err != nil {
		return err
	}
	adapter := filepath.Join(rootPath(), "e2e/benchmarks/multihop_public/capacity/token_count.py")
	adapterBytes, err := os.ReadFile(adapter)
	if err != nil {
		return err
	}
	counter := externalCounter{Python: c.python, PythonSHA: c.pythonSHA, Adapter: adapter, AdapterSHA: sha(adapterBytes), Receipt: c.runtime, ReceiptSHA: c.runtimeSHA}
	switch c.mode {
	case "indexed-preflight":
		return indexedPreflight(context.Background(), in, c.out, c.pin, counter)
	case "indexed-audit":
		return indexedAudit(c.out, in, c.pin, counter)
	default:
		return indexedRun(context.Background(), in, c.out, c.pin, counter)
	}
}
