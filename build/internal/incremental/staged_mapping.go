package incremental

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const stagedMaxOutput = 65536
const stagedVisibleWork = 32768
const stagedOfficialCounterID = "deepseek-recipe@935817c66f5d6e64838f419692545f06787e485f/v41"

// ErrStagedMappingUncertain 表示已有派发意图但缺少完整回执；不能自动再次付费。
// An indeterminate dispatch requires reconciliation, never an automatic retry.
var ErrStagedMappingUncertain = errors.New("staged mapping: UNCERTAIN_CHECKPOINT_STOP")

// ErrStagedMappingOverflow 仅允许在派发前分批，不能用于已派发调用的回退。
// Only a pre-dispatch overflow can cause an ordered split.
var ErrStagedMappingOverflow = errors.New("staged mapping: predispatch overflow")

// 只有完整且身份已验证的响应能形成此分类；来源/基础设施错误不能冒充 schema 失败。
// This private sentinel is evidence of a schema failure, never a generic replay error.
var errStagedResponseSchema = errors.New("staged mapping: complete response schema invalid")

// StagedMappingAdmissionIdentity 由调用方独立冻结，不能由待验证的回执自签。
// Pins identify the future serializer/counter bridge; this package provides no live bridge.
type StagedMappingAdmissionIdentity struct {
	Endpoint, Model, ProfileSHA256, SerializerSHA256, CounterID, RuntimeSHA256 string
}

// StagedMappingIdentity 区分代码版本、语料和已验收标注，避免混用来源哈希。
// Canonical digest recipes are implemented by StagedMappingInputDigests.
type StagedMappingIdentity struct {
	CodeSourceCommit, CorpusSHA256, RosterSHA256, AcceptedSetSHA256, InputSHA256, MethodSHA256 string
	ExpectedDocuments                                                                          int
	Admission                                                                                  StagedMappingAdmissionIdentity
}

// StagedMappingDocumentRoot 是独立认证的完整名册条目；摘要相等不构成真实性证明。
// The pipeline must authenticate these roots before invoking this offline adapter.
type StagedMappingDocumentRoot struct {
	DocID, Skill, FilePath, OriginalSHA256, AcceptedCheckpointSHA256 string
}

// StagedMappingDocument 保留完整标注，包括每个共享 member 的不同来源内容。
// Optional empty Detail is preserved, not fabricated for a later integration gate.
type StagedMappingDocument struct {
	Root       StagedMappingDocumentRoot
	Annotation dktypes.AnnotatedDocument
}

type StagedMappingInput struct {
	Identity  StagedMappingIdentity
	Roster    []StagedMappingDocumentRoot
	Documents []StagedMappingDocument
}

// StagedMappingCall 冻结一次逻辑尝试的父目录和单元；重试不能换父目录。
// Request contains aliases and source annotations, never persisted node UUIDs.
type StagedMappingCall struct {
	BatchID, CatalogueSHA256 string
	Attempt                  int
	Units                    []string
	Request                  llm.CompleteRequest
}

// StagedMappingWireBinding 将不可变句柄绑定到真实请求和官方计数回执。
// Dispatch must compare this exact binding at the actual production transport boundary.
type StagedMappingWireBinding struct {
	HandleID, RequestSHA256                         string
	Identity                                        StagedMappingAdmissionIdentity
	InputTokens, ReservedOutputTokens, ContextLimit int
}

type StagedMappingDispatchResult struct {
	Binding   StagedMappingWireBinding
	ReceiptID string
	Response  llm.CompleteResponse
}

// StagedMappingPreparedCall 的 Prepare 不得访问提供商；Wire 必须来自生产序列化器。
// The adapter copies Wire/Binding and supplies the expected binding to one Dispatch.
type StagedMappingPreparedCall interface {
	Wire() []byte
	Binding() StagedMappingWireBinding
	Dispatch(context.Context, StagedMappingWireBinding) (StagedMappingDispatchResult, error)
}

// StagedMappingAdmitter 是必需的注入边界；本离线改动不实现 HTTP 或复制序列化器。
// A fake proves orchestration only; a separately reviewed bridge must enforce exact wire identity.
type StagedMappingAdmitter interface {
	Identity() StagedMappingAdmissionIdentity
	Prepare(context.Context, StagedMappingCall) (StagedMappingPreparedCall, error)
}

type StagedMappingOptions struct {
	StageDir string
	Resume   bool
	Admitter StagedMappingAdmitter
	fault    func(string) error // 包内崩溃注入点；不构成产品配置。 Test-only crash points.
}

// StagedMappingBirth 永久保存首次派生的 members 和父层身份，后续融合不重新派生。
// Current membership belongs to extract.Result; birth membership is immutable.
type StagedMappingBirth struct {
	UUID, DomainID, SubdomainID string
	Members                     []string
}

type StagedMappingReceipt struct {
	Identity                           StagedMappingIdentity
	CatalogueSHA256, JournalHeadSHA256 string
	AcceptedBatches                    int
}

type StagedMappingResult struct {
	Extraction *extract.Result
	Documents  []StagedMappingDocument
	Sources    []storage.NodeSource
	Births     []StagedMappingBirth
	Receipt    StagedMappingReceipt
}

type stagedIncidence struct {
	DocID, OriginalSHA256, AcceptedCheckpointSHA256, Skill, FilePath string
	Item                                                             dktypes.AnnotatedItem
}

type stagedUnit struct {
	ID      string
	Sources []stagedIncidence
}

type stagedCatalogue struct {
	Nodes   []dktypes.Node
	Births  []StagedMappingBirth
	Sources []storage.NodeSource
	Done    []string
}

func stagedHash(v any) string         { b, _ := json.Marshal(v); return stagedBytesHash(b) }
func stagedBytesHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func stagedHex(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == n && strings.ToLower(s) == s
}
func stagedNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return r.IsNil()
	}
	return false
}

// StagedMappingInputDigests 只计算规范身份，不授予输入真实性。
// Roster and documents are sorted by DocID; items by ID; all annotation fields are retained.
func StagedMappingInputDigests(in StagedMappingInput) (roster, accepted, input string, err error) {
	if !stagedValidUTF8(reflect.ValueOf(in)) {
		return "", "", "", fmt.Errorf("staged mapping: invalid UTF-8")
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return "", "", "", e
	}
	var c StagedMappingInput
	if e = json.Unmarshal(raw, &c); e != nil {
		return "", "", "", e
	}
	sort.Slice(c.Roster, func(i, j int) bool { return c.Roster[i].DocID < c.Roster[j].DocID })
	sort.Slice(c.Documents, func(i, j int) bool { return c.Documents[i].Root.DocID < c.Documents[j].Root.DocID })
	for i := range c.Documents {
		sort.Slice(c.Documents[i].Annotation.Items, func(a, b int) bool {
			return c.Documents[i].Annotation.Items[a].ID < c.Documents[i].Annotation.Items[b].ID
		})
	}
	roots := make([]StagedMappingDocumentRoot, 0, len(c.Documents))
	for _, d := range c.Documents {
		roots = append(roots, d.Root)
	}
	return stagedHash(c.Roster), stagedHash(roots), stagedHash(c.Documents), nil
}

func stagedCanonicalInput(in StagedMappingInput) (StagedMappingInput, []stagedUnit, error) {
	if !stagedValidUTF8(reflect.ValueOf(in)) {
		return in, nil, fmt.Errorf("staged mapping: invalid UTF-8")
	}
	b, e := json.Marshal(in)
	if e != nil {
		return in, nil, fmt.Errorf("staged mapping: invalid input encoding")
	}
	var c StagedMappingInput
	_ = json.Unmarshal(b, &c)
	i := c.Identity
	if !stagedHex(i.CodeSourceCommit, 20) || !stagedHex(i.CorpusSHA256, 32) || !stagedHex(i.MethodSHA256, 32) || i.ExpectedDocuments < 1 || len(c.Roster) != i.ExpectedDocuments || len(c.Documents) != i.ExpectedDocuments {
		return c, nil, fmt.Errorf("staged mapping: incomplete roster or missing independent roots")
	}
	for _, s := range []string{i.RosterSHA256, i.AcceptedSetSHA256, i.InputSHA256, i.Admission.ProfileSHA256, i.Admission.SerializerSHA256, i.Admission.RuntimeSHA256} {
		if !stagedHex(s, 32) {
			return c, nil, fmt.Errorf("staged mapping: invalid identity pin")
		}
	}
	if i.Admission.Endpoint != "https://api.deepseek.com/chat/completions" || i.Admission.Model != "deepseek-flash" || i.Admission.CounterID != stagedOfficialCounterID {
		return c, nil, fmt.Errorf("staged mapping: invalid admission profile")
	}
	r, a, h, e := StagedMappingInputDigests(c)
	if e != nil || r != i.RosterSHA256 || a != i.AcceptedSetSHA256 || h != i.InputSHA256 {
		return c, nil, fmt.Errorf("staged mapping: input root mismatch")
	}
	sort.Slice(c.Roster, func(a, b int) bool { return c.Roster[a].DocID < c.Roster[b].DocID })
	sort.Slice(c.Documents, func(a, b int) bool { return c.Documents[a].Root.DocID < c.Documents[b].Root.DocID })
	byID := map[string]*stagedUnit{}
	var ids []string
	paths := map[string]bool{}
	for n := range c.Documents {
		d := &c.Documents[n]
		root := c.Roster[n]
		if root != d.Root || root.DocID == "" || (n > 0 && c.Roster[n-1].DocID == root.DocID) || root.Skill == "" || (root.FilePath == "" || root.FilePath == "." || root.FilePath == "..") || path.Clean(root.FilePath) != root.FilePath || path.IsAbs(root.FilePath) || strings.HasPrefix(root.FilePath, "../") || strings.Contains(root.FilePath, "\\") || paths[root.FilePath] || !stagedHex(root.OriginalSHA256, 32) || !stagedHex(root.AcceptedCheckpointSHA256, 32) || d.Annotation.Skill != root.Skill || d.Annotation.FilePath != root.FilePath || len(d.Annotation.Items) == 0 {
			return c, nil, fmt.Errorf("staged mapping: invalid complete document binding")
		}
		paths[root.FilePath] = true
		sort.Slice(d.Annotation.Items, func(a, b int) bool { return d.Annotation.Items[a].ID < d.Annotation.Items[b].ID })
		for k, it := range d.Annotation.Items {
			if it.ID == "" || (k > 0 && d.Annotation.Items[k-1].ID == it.ID) || (it.Tag != dktypes.TagEntity && it.Tag != dktypes.TagConcept) || strings.TrimSpace(it.Name) == "" || strings.TrimSpace(it.Content) == "" || math.IsNaN(it.Confidence) || it.Confidence < 0 || it.Confidence > 1 || !utf8.ValidString(it.Detail) {
				return c, nil, fmt.Errorf("staged mapping: invalid source incidence")
			}
			if it.SourceSpan != nil && (it.SourceSpan.StartLine < 1 || it.SourceSpan.EndLine < it.SourceSpan.StartLine) {
				return c, nil, fmt.Errorf("staged mapping: invalid source span")
			}
			u := byID[it.ID]
			if u == nil {
				u = &stagedUnit{ID: it.ID}
				byID[it.ID] = u
				ids = append(ids, it.ID)
			} else if u.Sources[0].Item.Tag != it.Tag {
				return c, nil, fmt.Errorf("staged mapping: shared member tag mismatch")
			}
			u.Sources = append(u.Sources, stagedIncidence{root.DocID, root.OriginalSHA256, root.AcceptedCheckpointSHA256, root.Skill, root.FilePath, it})
		}
	}
	// 首次出现顺序与 #75 的规范 doc/member 计划一致。 First canonical incidence determines unit order.
	units := make([]stagedUnit, 0, len(ids))
	for _, id := range ids {
		units = append(units, *byID[id])
	}
	return c, units, nil
}

// RunStagedMapping 只建立适配器自有阶段库；未验收或不确定调用不会成为可返回结果。
// It does not invoke incremental.Run, reflow, rebalance, provider construction or publication.
func RunStagedMapping(ctx context.Context, input StagedMappingInput, opts StagedMappingOptions) (*StagedMappingResult, error) {
	in, units, err := stagedCanonicalInput(input)
	if err != nil {
		return nil, err
	}
	if stagedNil(opts.Admitter) || opts.Admitter.Identity() != in.Identity.Admission {
		return nil, fmt.Errorf("staged mapping: required independently bound admitter unavailable")
	}
	s, err := openStagedSession(ctx, in, opts)
	if err != nil {
		return nil, err
	}
	defer s.close()
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if s.attempt != nil && s.phase == "intent" {
			return nil, ErrStagedMappingUncertain
		}
		if s.attempt != nil && s.phase == "response" {
			if err = s.finishResponse(ctx, units); err != nil {
				return nil, err
			}
			continue
		}
		var batch []stagedUnit
		attempt := 1
		feedback := ""
		if s.attempt != nil && s.phase == "invalid" {
			if s.attempt.Call.Attempt >= 2 {
				return nil, fmt.Errorf("staged mapping: schema attempts exhausted")
			}
			batch, err = stagedPendingBatch(s.state, units, s.attempt.Call.Units)
			if err != nil {
				return nil, err
			}
			attempt = 2
			feedback = "输出结构校验失败，请仅按 schema 修正。"
		} else {
			if s.attempt != nil {
				if err = s.append(stagedEvent{Kind: "abandoned"}); err != nil {
					return nil, err
				}
				s.attempt = nil
				s.phase = ""
			}
			done := map[string]bool{}
			for _, id := range s.state.Done {
				done[id] = true
			}
			for _, u := range units {
				if !done[u.ID] {
					batch = append(batch, u)
				}
			}
			if len(batch) == 0 {
				return s.result(in), nil
			}
			if len(batch) > 32 {
				batch = batch[:32]
			}
		}
		for {
			plan, e := s.preparePlan(ctx, batch, attempt, feedback)
			if e != nil {
				if errors.Is(e, ErrStagedMappingOverflow) && attempt == 1 && len(batch) > 1 {
					batch = batch[:len(batch)/2]
					continue
				}
				return nil, e
			}
			// 外部回调只能持有副本，不能修改已冻结的 unit 列表或请求切片。
			// A bridge retaining its arguments must not mutate the call journaled below.
			callbackCall := plan.Call
			callbackCall.Units = append([]string(nil), plan.Call.Units...)
			callbackCall.Request.StopSequences = append([]string(nil), plan.Call.Request.StopSequences...)
			handle, e := opts.Admitter.Prepare(ctx, callbackCall)
			if e != nil {
				if errors.Is(e, ErrStagedMappingOverflow) && attempt == 1 && len(batch) > 1 {
					batch = batch[:len(batch)/2]
					continue
				}
				return nil, e
			}
			if stagedNil(handle) {
				return nil, fmt.Errorf("staged mapping: nil prepared handle")
			}
			wire := append([]byte(nil), handle.Wire()...)
			binding := handle.Binding()
			if e = stagedValidateWire(plan.Call, wire, binding, in.Identity.Admission); e != nil {
				if errors.Is(e, ErrStagedMappingOverflow) && attempt == 1 && len(batch) > 1 {
					batch = batch[:len(batch)/2]
					continue
				}
				return nil, e
			}
			if e = s.append(stagedEvent{Kind: "prepared", Prepared: &stagedPrepared{Call: plan.Call, Wire: wire, Binding: binding}}); e != nil {
				return nil, e
			}
			s.attempt = &stagedPrepared{Call: plan.Call, Wire: wire, Binding: binding}
			s.phase = "prepared"
			if e = s.append(stagedEvent{Kind: "intent"}); e != nil {
				return nil, e
			}
			s.phase = "intent"
			if e = s.crash("before_dispatch"); e != nil {
				return nil, e
			}
			response, e := handle.Dispatch(ctx, binding)
			if e != nil || response.Binding != binding || response.ReceiptID == "" || !stagedValidUTF8(reflect.ValueOf(response)) {
				return nil, ErrStagedMappingUncertain
			}
			if e = s.append(stagedEvent{Kind: "response", Response: &response}); e != nil {
				return nil, e
			}
			s.response = &response
			s.phase = "response"
			break
		}
	}
}

func stagedSelectUnits(all []stagedUnit, ids []string) ([]stagedUnit, error) {
	m := map[string]stagedUnit{}
	for _, u := range all {
		m[u.ID] = u
	}
	out := make([]stagedUnit, 0, len(ids))
	for _, id := range ids {
		u, ok := m[id]
		if !ok {
			return nil, fmt.Errorf("staged mapping: unknown journal unit")
		}
		out = append(out, u)
	}
	return out, nil
}

func stagedPendingBatch(state stagedCatalogue, all []stagedUnit, ids []string) ([]stagedUnit, error) {
	done := map[string]bool{}
	for _, id := range state.Done {
		done[id] = true
	}
	var pending []stagedUnit
	for _, u := range all {
		if !done[u.ID] {
			pending = append(pending, u)
		}
	}
	if len(ids) < 1 || len(ids) > 32 || len(ids) > len(pending) {
		return nil, fmt.Errorf("staged mapping: invalid pending prefix")
	}
	for i, id := range ids {
		if pending[i].ID != id {
			return nil, fmt.Errorf("staged mapping: noncanonical pending prefix")
		}
	}
	return pending[:len(ids)], nil
}

func stagedCompleteResponse(r *StagedMappingDispatchResult, b StagedMappingWireBinding) bool {
	return r != nil && r.Binding == b && r.ReceiptID != "" && r.Response.FinishReason == "stop" && r.Response.InputTokens > 0 && r.Response.InputTokens == b.InputTokens && r.Response.OutputTokens > 0 && r.Response.OutputTokens <= b.ReservedOutputTokens && r.Response.InputTokens <= b.ContextLimit-b.ReservedOutputTokens
}

func stagedValidateWire(call StagedMappingCall, wire []byte, b StagedMappingWireBinding, pins StagedMappingAdmissionIdentity) error {
	if b.HandleID == "" || b.Identity != pins || b.RequestSHA256 != stagedBytesHash(wire) || b.ReservedOutputTokens != stagedMaxOutput || b.ContextLimit != 1000000 || b.InputTokens < 1 {
		return fmt.Errorf("staged mapping: unbound wire receipt")
	}
	var body struct {
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
	if stagedDecode(wire, &body) != nil || body.Model != pins.Model || body.MaxTokens != stagedMaxOutput || len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" || body.Messages[0].Content != call.Request.System || body.Messages[1].Content != call.Request.User || body.ResponseFormat.Type != "json_object" || body.Thinking.Type != "enabled" {
		return fmt.Errorf("staged mapping: wire does not match frozen request/profile")
	}
	if b.InputTokens > b.ContextLimit-b.ReservedOutputTokens {
		return ErrStagedMappingOverflow
	}
	return nil
}

// stagedDecode 拒绝重复键、未知字段和尾随值，避免验证与实际消费看到不同对象。
// Strict decoding happens before the legacy validator can normalize generated fields.
func stagedDecode(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(reflect.Type) error
	walk = func(typ reflect.Type) error {
		for typ != nil && typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		if v, ok := t.(json.Delim); ok {
			switch v {
			case '{':
				fields := map[string]reflect.Type{}
				if typ != nil && typ.Kind() == reflect.Struct {
					for i := 0; i < typ.NumField(); i++ {
						f := typ.Field(i)
						if f.PkgPath != "" {
							continue
						}
						name := strings.Split(f.Tag.Get("json"), ",")[0]
						if name == "-" {
							continue
						}
						if name == "" {
							name = f.Name
						}
						fields[name] = f.Type
					}
				}
				seen := map[string]bool{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return e
					}
					key, ok := k.(string)
					if !ok || seen[key] {
						return fmt.Errorf("duplicate key")
					}
					seen[key] = true
					var child reflect.Type
					if typ != nil && typ.Kind() == reflect.Struct {
						var ok bool
						child, ok = fields[key]
						if !ok {
							return fmt.Errorf("unknown or noncanonical key")
						}
					} else if typ != nil && typ.Kind() == reflect.Map {
						child = typ.Elem()
					}
					if e = walk(child); e != nil {
						return e
					}
				}
				_, e = d.Token()
				return e
			case '[':
				var child reflect.Type
				if typ != nil && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
					child = typ.Elem()
				}
				for d.More() {
					if e := walk(child); e != nil {
						return e
					}
				}
				_, e = d.Token()
				return e
			default:
				return fmt.Errorf("unexpected delimiter")
			}
		}
		return nil
	}
	if e := walk(reflect.TypeOf(out)); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func stagedValidUTF8(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return stagedValidUTF8(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !stagedValidUTF8(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !stagedValidUTF8(v.Index(i)) {
				return false
			}
		}
	}
	return true
}
