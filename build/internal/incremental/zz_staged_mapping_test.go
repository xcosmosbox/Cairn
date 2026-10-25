package incremental

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type stagedFake struct {
	pins                 StagedMappingAdmissionIdentity
	calls                []StagedMappingCall
	prepared, dispatched int
	invalid              int
	prepareFault         func(StagedMappingCall, *stagedFakeHandle) error
	dispatchFault        bool
	responseMutate       func(*StagedMappingDispatchResult)
}
type stagedFakeHandle struct {
	f       *stagedFake
	call    StagedMappingCall
	wire    []byte
	binding StagedMappingWireBinding
}

func (f *stagedFake) Identity() StagedMappingAdmissionIdentity { return f.pins }
func (f *stagedFake) Prepare(_ context.Context, c StagedMappingCall) (StagedMappingPreparedCall, error) {
	f.prepared++
	// 这是测试夹具，不是生产请求序列化器或 HTTP 客户端。
	// The fake deliberately has no network-capable implementation.
	fixture := map[string]any{"model": f.pins.Model, "messages": []map[string]string{{"role": "system", "content": c.Request.System}, {"role": "user", "content": c.Request.User}}, "max_tokens": c.Request.MaxTokens, "response_format": map[string]string{"type": "json_object"}, "thinking": map[string]string{"type": "enabled"}}
	wire, _ := json.Marshal(fixture)
	h := &stagedFakeHandle{f: f, call: c, wire: wire, binding: StagedMappingWireBinding{HandleID: fmt.Sprintf("fake-%d", f.prepared), RequestSHA256: stagedBytesHash(wire), Identity: f.pins, InputTokens: 100, ReservedOutputTokens: stagedMaxOutput, ContextLimit: 1000000}}
	if f.prepareFault != nil {
		if e := f.prepareFault(c, h); e != nil {
			return nil, e
		}
	}
	return h, nil
}
func (h *stagedFakeHandle) Wire() []byte                      { return h.wire }
func (h *stagedFakeHandle) Binding() StagedMappingWireBinding { return h.binding }
func (h *stagedFakeHandle) Dispatch(_ context.Context, b StagedMappingWireBinding) (StagedMappingDispatchResult, error) {
	if b != h.binding || b.RequestSHA256 != stagedBytesHash(h.wire) {
		return StagedMappingDispatchResult{}, errors.New("fake exact dispatch mismatch")
	}
	f := h.f
	f.dispatched++
	f.calls = append(f.calls, h.call)
	if f.dispatchFault {
		return StagedMappingDispatchResult{}, errors.New("indeterminate fake transport")
	}
	text := "{ invalid"
	if f.dispatched > f.invalid {
		var payload struct {
			Nodes []struct{ Alias string }
			Units []stagedUnit `json:"units"`
		}
		_ = json.Unmarshal([]byte(h.call.Request.User), &payload)
		out := alignLLMOutput{}
		for _, u := range payload.Units {
			if len(payload.Nodes) > 0 {
				out.Decisions = append(out.Decisions, alignDecision{Unit: u.ID, Action: "merge_into", Target: payload.Nodes[0].Alias})
			} else {
				out.Decisions = append(out.Decisions, alignDecision{Unit: u.ID, Action: "fuse", Group: "G1"})
			}
		}
		if len(payload.Nodes) == 0 {
			out.Groups = []alignGroup{{Group: "G1", NodeName: "alpha node", Summary: "alpha full summary", Tag: "entity", Domain: "alpha domain", Subdomain: "alpha subdomain"}}
		}
		raw, _ := json.Marshal(out)
		text = string(raw)
	}
	result := StagedMappingDispatchResult{Binding: b, ReceiptID: fmt.Sprintf("fake-receipt-%d", f.dispatched), Response: llm.CompleteResponse{Text: text, InputTokens: 100, OutputTokens: 100, FinishReason: "stop"}}
	if f.responseMutate != nil {
		f.responseMutate(&result)
	}
	return result, nil
}

func stagedTestInput(n int) StagedMappingInput {
	pins := StagedMappingAdmissionIdentity{Endpoint: "https://api.deepseek.com/chat/completions", Model: "deepseek-flash", ProfileSHA256: stagedHash("profile"), SerializerSHA256: stagedHash("serializer"), CounterID: "deepseek-recipe@935817c66f5d6e64838f419692545f06787e485f/v41", RuntimeSHA256: stagedHash("runtime")}
	root := StagedMappingDocumentRoot{"doc-a", "skill", "references/doc-a.md", stagedHash("original-a"), stagedHash("checkpoint-a")}
	d := StagedMappingDocument{Root: root, Annotation: dktypes.AnnotatedDocument{Skill: root.Skill, FilePath: root.FilePath}}
	for i := 0; i < n; i++ {
		d.Annotation.Items = append(d.Annotation.Items, dktypes.AnnotatedItem{ID: fmt.Sprintf("unit-%03d", i), Tag: dktypes.TagEntity, Name: fmt.Sprintf("alpha name %d", i), Content: "alpha source content", Detail: fmt.Sprintf("complete detail %d", i), Confidence: .7, SourceSpan: &dktypes.SourceSpan{StartLine: i + 1, EndLine: i + 1}})
	}
	in := StagedMappingInput{Identity: StagedMappingIdentity{CodeSourceCommit: strings.Repeat("a", 40), CorpusSHA256: stagedHash("corpus"), MethodSHA256: stagedHash("method"), ExpectedDocuments: 1, Admission: pins}, Roster: []StagedMappingDocumentRoot{root}, Documents: []StagedMappingDocument{d}}
	stagedRepin(&in)
	return in
}
func stagedRepin(in *StagedMappingInput) {
	in.Identity.RosterSHA256, in.Identity.AcceptedSetSHA256, in.Identity.InputSHA256, _ = StagedMappingInputDigests(*in)
}
func stagedTestRun(t *testing.T, in StagedMappingInput, f *stagedFake) (*StagedMappingResult, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "stage")
	r, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Admitter: f})
	if e != nil {
		t.Fatal(e)
	}
	return r, dir
}
func stagedLeaves(r *StagedMappingResult) []extract.Node {
	var out []extract.Node
	for _, d := range r.Extraction.Domains {
		for _, sd := range d.Subdomains {
			out = append(out, sd.Entities...)
			out = append(out, sd.Concepts...)
		}
	}
	return out
}

func TestStagedMappingCompleteCrossBatchPinnedIdentity(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(12)
	in.Documents[0].Annotation.Items[8].Confidence = 0
	stagedRepin(&in)
	f := &stagedFake{pins: in.Identity.Admission}
	r, dir := stagedTestRun(t, in, f)
	if f.dispatched < 2 {
		t.Fatal("fixture must cross batch boundary")
	}
	leaves := stagedLeaves(r)
	if len(leaves) != 1 || len(leaves[0].Members) != 12 || len(r.Sources) != 12 {
		t.Fatalf("lost crossbatch members/sources: %+v", r)
	}
	if leaves[0].ID != r.Births[0].UUID || leaves[0].ID != extract.NodeUUID(f.calls[0].Units) || len(r.Births[0].Members) == 12 {
		t.Fatal("UUID was rederived from accumulated membership")
	}
	if leaves[0].Confidence != 0 {
		t.Fatal("crossbatch source confidence was not preserved conservatively")
	}
	if f.calls[0].CatalogueSHA256 == f.calls[1].CatalogueSHA256 || !strings.Contains(f.calls[1].Request.User, "n#1") {
		t.Fatal("tail was not recalled against new catalogue")
	}
	for _, c := range f.calls {
		if strings.Contains(c.Request.User, leaves[0].ID) || strings.Contains(c.Request.System, leaves[0].ID) {
			t.Fatal("persisted UUID leaked into prompt")
		}
	}
	if r.Extraction.Domains[0].Slug == "" || r.Extraction.Domains[0].Subdomains[0].Slug == "" || leaves[0].Label != "Entity" {
		t.Fatal("hierarchy identity/label was not restored")
	}
	before := f.dispatched
	r2, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Resume: true, Admitter: f})
	if e != nil {
		t.Fatal(e)
	}
	if f.dispatched != before || r2.Receipt.CatalogueSHA256 != r.Receipt.CatalogueSHA256 {
		t.Fatal("accepted resume repaid or changed catalogue")
	}
}

func TestStagedMappingSharedFullIncidencesAndShuffle(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(1)
	second := in.Documents[0]
	second.Root = StagedMappingDocumentRoot{"doc-b", "skill", "references/doc-b.md", stagedHash("original-b"), stagedHash("checkpoint-b")}
	second.Annotation.FilePath = second.Root.FilePath
	second.Annotation.Items = append([]dktypes.AnnotatedItem(nil), second.Annotation.Items...)
	second.Annotation.Items[0].Name = "SECOND full name"
	second.Annotation.Items[0].Content = "SECOND content"
	second.Annotation.Items[0].Detail = "SECOND complete detail"
	in.Documents = append(in.Documents, second)
	in.Roster = append(in.Roster, second.Root)
	in.Identity.ExpectedDocuments = 2
	stagedRepin(&in)
	f := &stagedFake{pins: in.Identity.Admission}
	r, _ := stagedTestRun(t, in, f)
	for _, s := range []string{"SECOND full name", "SECOND content", "SECOND complete detail", "complete detail 0"} {
		if !strings.Contains(f.calls[0].Request.User, s) {
			t.Fatalf("lost source field %q", s)
		}
	}
	if len(r.Sources) != 2 || len(stagedLeaves(r)[0].Members) != 1 {
		t.Fatal("shared incidence denominator collapsed")
	}
	in.Documents[0], in.Documents[1] = in.Documents[1], in.Documents[0]
	in.Roster[0], in.Roster[1] = in.Roster[1], in.Roster[0]
	f2 := &stagedFake{pins: in.Identity.Admission}
	r2, _ := stagedTestRun(t, in, f2)
	if f.calls[0].Request.User != f2.calls[0].Request.User || r.Receipt.CatalogueSHA256 != r2.Receipt.CatalogueSHA256 {
		t.Fatal("input order changed canonical mapping")
	}
	_, units, e := stagedCanonicalInput(in)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(stagedRecallQuery(units[0]), "SECOND") {
		t.Fatal("query did not use first canonical incidence")
	}
}

func TestStagedMappingAdmissionAndInputRejectBeforeSideEffects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		mutate      func(*StagedMappingInput, *stagedFake)
		nilAdmitter bool
	}{
		{"missing-roster", func(in *StagedMappingInput, _ *stagedFake) { in.Identity.ExpectedDocuments = 2 }, false},
		{"invalid-utf8-before-clone", func(in *StagedMappingInput, _ *stagedFake) {
			in.Documents[0].Annotation.Items[0].Name = string([]byte{0xff})
		}, false},
		{"dot-path", func(in *StagedMappingInput, _ *stagedFake) {
			in.Roster[0].FilePath = "."
			in.Documents[0].Root = in.Roster[0]
			in.Documents[0].Annotation.FilePath = "."
			stagedRepin(in)
		}, false},
		{"unbound-admitter", func(_ *StagedMappingInput, f *stagedFake) { f.pins.RuntimeSHA256 = stagedHash("wrong") }, false},
		{"nil-admitter", func(_ *StagedMappingInput, _ *stagedFake) {}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := stagedTestInput(1)
			f := &stagedFake{pins: in.Identity.Admission}
			tc.mutate(&in, f)
			dir := filepath.Join(t.TempDir(), "stage")
			var a StagedMappingAdmitter = f
			if tc.nilAdmitter {
				var typedNil *stagedFake
				a = typedNil
			}
			_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Admitter: a})
			if e == nil {
				t.Fatal("accepted invalid input/admitter")
			}
			if _, e = os.Stat(dir); !os.IsNotExist(e) {
				t.Fatal("created stage before input/admission binding")
			}
			if f.prepared != 0 || f.dispatched != 0 {
				t.Fatal("admitted invalid input")
			}
		})
	}
}

func TestStagedMappingExactWireAndTwoSchemaAttempts(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(1)
	for _, mode := range []string{"wire-text", "model-case", "nested-case", "counter", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			f := &stagedFake{pins: in.Identity.Admission, prepareFault: func(_ StagedMappingCall, h *stagedFakeHandle) error {
				switch mode {
				case "wire-text":
					h.wire = []byte(strings.Replace(string(h.wire), "alpha name", "changed name", 1))
				case "model-case":
					h.wire = []byte(strings.Replace(string(h.wire), `"model":`, `"Model":`, 1))
				case "nested-case":
					h.wire = []byte(strings.Replace(string(h.wire), `"role":`, `"Role":`, 1))
				case "counter":
					h.binding.Identity.CounterID = "unbound"
				case "unknown-field":
					h.wire = append([]byte(`{"extra":0,`), h.wire[1:]...)
				}
				h.binding.RequestSHA256 = stagedBytesHash(h.wire)
				return nil
			}}
			_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage"), Admitter: f})
			if e == nil || f.dispatched != 0 {
				t.Fatal("mismatched wire dispatched")
			}
		})
	}
	f := &stagedFake{pins: in.Identity.Admission, invalid: 1}
	_, _ = stagedTestRun(t, in, f)
	if f.dispatched != 2 || f.calls[0].BatchID != f.calls[1].BatchID || f.calls[0].CatalogueSHA256 != f.calls[1].CatalogueSHA256 || !strings.Contains(f.calls[1].Request.User, "输出结构校验失败") {
		t.Fatal("fixed schema retry contract lost")
	}
	f = &stagedFake{pins: in.Identity.Admission, invalid: 9}
	_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage"), Admitter: f})
	if e == nil || f.dispatched != 2 {
		t.Fatal("schema retries were not fixed at two")
	}
}

func TestStagedMappingWireOverflowSplitBeforeDispatch(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(8)
	f := &stagedFake{pins: in.Identity.Admission, prepareFault: func(c StagedMappingCall, h *stagedFakeHandle) error {
		if len(c.Units) > 1 {
			h.binding.InputTokens = 1000000
		}
		return nil
	}}
	r, _ := stagedTestRun(t, in, f)
	if len(stagedLeaves(r)[0].Members) != 8 {
		t.Fatal("split lost units")
	}
	for _, c := range f.calls {
		if len(c.Units) != 1 {
			t.Fatal("overflow dispatched before splitting")
		}
	}
	f = &stagedFake{pins: in.Identity.Admission, prepareFault: func(_ StagedMappingCall, h *stagedFakeHandle) error { h.binding.InputTokens = 1000000; return nil }}
	_, e := RunStagedMapping(context.Background(), stagedTestInput(1), StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage"), Admitter: f})
	if !errors.Is(e, ErrStagedMappingOverflow) || f.dispatched != 0 {
		t.Fatal("singleton overflow did not fail closed")
	}
}

func TestStagedMappingOutputByteBoundsCoverageAndExactKeys(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(1)
	canonical, units, e := stagedCanonicalInput(in)
	if e != nil {
		t.Fatal(e)
	}
	s, e := openStagedSession(context.Background(), canonical, StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage")})
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	plan, e := s.preparePlan(context.Background(), units, 1, "")
	if e != nil {
		t.Fatal(e)
	}
	base := alignLLMOutput{Decisions: []alignDecision{{Unit: units[0].ID, Action: "new_subdomain", Domain: "domain", Subdomain: "sub", NodeName: "node", Summary: "summary", Tag: "entity"}}}
	for _, mode := range []string{"escaped-name", "summary", "record", "unused-group", "duplicate-unit", "wrong-case", "duplicate-key"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var out alignLLMOutput
			_ = json.Unmarshal(raw, &out)
			switch mode {
			case "escaped-name":
				out.Decisions[0].NodeName = strings.Repeat(`"`, 128)
			case "summary":
				out.Decisions[0].Summary = strings.Repeat("s", 1023)
			case "record":
				out.Decisions[0].Group = strings.Repeat("g", 2049)
			case "unused-group":
				out.Groups = []alignGroup{{Group: "unused", NodeName: "name", Summary: "summary", Tag: "entity", Domain: "domain", Subdomain: "sub"}}
			case "duplicate-unit":
				out.Decisions = append(out.Decisions, out.Decisions[0])
			}
			raw, _ = json.Marshal(out)
			if mode == "wrong-case" {
				raw = []byte(strings.Replace(string(raw), `"action":`, `"Action":`, 1))
			}
			if mode == "duplicate-key" {
				raw = []byte(strings.Replace(string(raw), `"action":`, `"action":"merge_into","action":`, 1))
			}
			if _, _, e = stagedValidateOutput(string(raw), plan); e == nil {
				t.Fatal("accepted invalid output")
			}
		})
	}
	// 空 Detail 是真实可选值，不能静默合成内容以绕过将来的物化门。
	in.Documents[0].Annotation.Items[0].Detail = ""
	stagedRepin(&in)
	f := &stagedFake{pins: in.Identity.Admission}
	r, _ := stagedTestRun(t, in, f)
	if r.Documents[0].Annotation.Items[0].Detail != "" {
		t.Fatal("fabricated optional Detail")
	}
}

// 旧校验在归一化前检查占位符，随后却允许空摘要触发正文回退，突破输出字段上限。
// Reject placeholders after normalization rather than substituting source Content.
func TestStagedMappingRejectsNormalizedEmptySummary(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(1)
	canonical, units, _ := stagedCanonicalInput(in)
	s, e := openStagedSession(context.Background(), canonical, StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage")})
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	plan, e := s.preparePlan(context.Background(), units, 1, "")
	if e != nil {
		t.Fatal(e)
	}
	plan.Book.subByAlias["sd#1"] = &dktypes.Node{ID: subdomainNodeID("domain", "sub"), Label: dktypes.LabelSubdomain, Domain: "domain", Subdomain: "sub"}
	for _, summary := range []string{"（暂无摘要）", "（暂无详述）"} {
		for _, action := range []string{"attach", "new_subdomain", "fuse"} {
			d := alignDecision{Unit: units[0].ID, Action: action, NodeName: "name", Summary: summary, Tag: "entity", Domain: "domain", Subdomain: "sub"}
			out := alignLLMOutput{Decisions: []alignDecision{d}}
			if action == "attach" {
				out.Decisions[0].Target = "sd#1"
			}
			if action == "fuse" {
				out.Decisions[0] = alignDecision{Unit: units[0].ID, Action: "fuse", Group: "G1"}
				out.Groups = []alignGroup{{Group: "G1", NodeName: "name", Summary: summary, Tag: "entity", Domain: "domain", Subdomain: "sub"}}
			}
			raw, _ := json.Marshal(out)
			if _, _, e = stagedValidateOutput(string(raw), plan); e == nil {
				t.Fatalf("normalized placeholder accepted for %s/%s", action, summary)
			}
		}
	}
}

// 完整的 stop + 确切绑定用量是 schema 修复的先决条件，不能把截断或缺失用量当作修复预算。
// Every rejected completion remains in the journal, and resume must not dispatch again.
func TestStagedMappingIncompleteOrUnboundUsageStopsWithoutRepair(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"non-stop", "zero-input", "mismatched-input", "zero-output", "excess-output"} {
		t.Run(mode, func(t *testing.T) {
			in := stagedTestInput(1)
			f := &stagedFake{pins: in.Identity.Admission, responseMutate: func(r *StagedMappingDispatchResult) {
				switch mode {
				case "non-stop":
					r.Response.FinishReason = "length"
				case "zero-input":
					r.Response.InputTokens = 0
				case "mismatched-input":
					r.Response.InputTokens = 101
				case "zero-output":
					r.Response.OutputTokens = 0
				case "excess-output":
					r.Response.OutputTokens = stagedMaxOutput + 1
				}
			}}
			dir := filepath.Join(t.TempDir(), "stage")
			_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Admitter: f})
			if !errors.Is(e, ErrStagedMappingUncertain) || f.dispatched != 1 {
				t.Fatalf("completion repaired/accepted: %v calls%d", e, f.dispatched)
			}
			_, e = RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Resume: true, Admitter: f})
			if !errors.Is(e, ErrStagedMappingUncertain) || f.dispatched != 1 {
				t.Fatalf("completion repaid on resume: %v calls%d", e, f.dispatched)
			}
			raw, e := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(string(raw), `"Kind":"response"`) || strings.Contains(string(raw), `"Kind":"invalid"`) {
				t.Fatal("incomplete completion lost evidence or became schema-invalid")
			}
		})
	}
}
