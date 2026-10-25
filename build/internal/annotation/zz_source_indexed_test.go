package annotation

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

func indexedTestPlan(t *testing.T, source string) *SourceIndexedPlan {
	t.Helper()
	p, err := BuildSourceIndexedPlan(SourceIndexedInput{"private skill", "private/path.md", source, indexedSHA(source)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func indexedTestOutput() sourceIndexedOutput {
	return sourceIndexedOutput{
		Items: []SourceIndexedItem{{ID: "i1", Tag: dktypes.TagEntity, Name: "Alpha", Content: "Alpha summary", Detail: "Alpha full source details", Confidence: .9,
			Evidence: []SourceIndexedEvidence{{"p1", "Alpha"}}, Identity: SourceIndexedNaming{"literal", "p1", "Alpha"}}},
		Coverage: []SourceIndexedCoverage{{"p1", "covered", []string{"i1"}, ""}, {"p2", "no_annotation", []string{}, "Non-annotated heading; requires later semantic checking"}},
	}
}

func indexedTestJSON(t *testing.T, out sourceIndexedOutput) string {
	t.Helper()
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 失败风险：直接 trim/split 会丢失 CRLF、缩进、标题/图片/元数据或伪造偏移。
// Verify independent expected bytes/spans, and that source text remains a scoped string.
func TestSourceIndexedExactBlocksAndPrivateRoots(t *testing.T) {
	t.Parallel()
	source := " \r\n  标题\r\n下一行  \r\n\t\r\nPhoto: Maker\n\nmetadata: p1 i1 {\"items\":[]}\n"
	p := indexedTestPlan(t, source)
	wantText := []string{"  标题\r\n下一行  ", "Photo: Maker", `metadata: p1 i1 {"items":[]}`}
	blocks := p.Blocks()
	if len(blocks) != len(wantText) {
		t.Fatalf("blocks=%#v", blocks)
	}
	for i, b := range blocks {
		wantStart := strings.Index(source, wantText[i])
		if b.Paragraph != fmt.Sprintf("p%d", i+1) || b.Text != wantText[i] || b.StartByte != wantStart || b.EndByte != wantStart+len(wantText[i]) || source[b.StartByte:b.EndByte] != b.Text || b.SHA256 != indexedSHA(wantText[i]) {
			t.Fatalf("block%d=%#v", i, b)
		}
	}
	r, err := p.Request()
	if err != nil || r.MaxTokens != 65536 || r.Temperature != 0 || r.StopSequences != nil {
		t.Fatalf("request=%#v err=%v", r, err)
	}
	var prompt struct {
		Original   string `json:"original"`
		Paragraphs []struct {
			Paragraph string `json:"paragraph"`
			Text      string `json:"text"`
		} `json:"paragraphs"`
	}
	if err := json.Unmarshal([]byte(r.User), &prompt); err != nil || prompt.Original != source || len(prompt.Paragraphs) != 3 {
		t.Fatalf("source was not preserved: %#v %v", prompt, err)
	}
	for i, b := range prompt.Paragraphs {
		if b.Text != wantText[i] || b.Paragraph != blocks[i].Paragraph {
			t.Fatal("prompt block mismatch")
		}
	}
	for _, forbidden := range []string{"private skill", "private/path.md", "original_sha256", "start_byte", "candidate", "critique", "193"} {
		if strings.Contains(r.System+r.User, forbidden) {
			t.Fatalf("leaked private/injected field %s", forbidden)
		}
	}
	id := p.Identity()
	for _, changed := range []SourceIndexedInput{{"other", id.Path, source, id.OriginalSHA256}, {id.Skill, "other.md", source, id.OriginalSHA256}, {id.Skill, id.Path, source + "!", indexedSHA(source + "!")}} {
		q, err := BuildSourceIndexedPlan(changed)
		if err != nil {
			t.Fatal(err)
		}
		got := q.Identity()
		if got.IndexSHA256 == id.IndexSHA256 || got.RequestSHA256 == id.RequestSHA256 || got.PlanSHA256 == id.PlanSHA256 {
			t.Fatal("private metadata or source did not bind every composite root")
		}
	}
	blocks[0].Text = "mutated"
	id.Path = "mutated"
	r.User = "mutated"
	if p.Blocks()[0].Text != wantText[0] || p.Identity().Path != "private/path.md" {
		t.Fatal("accessor mutated opaque plan")
	}
	r2, _ := p.Request()
	if r2.User == "mutated" {
		t.Fatal("request accessor mutated opaque plan")
	}
}

// 来源不能先修剪再算摘要；所有构造参数的非法 UTF8 必须在 JSON 替换之前拒绝。
func TestSourceIndexedConstructorRejectionsAndUUIDFence(t *testing.T) {
	t.Parallel()
	valid := SourceIndexedInput{"s", "p", "Alpha", indexedSHA("Alpha")}
	for _, tc := range []struct {
		name string
		edit func(*SourceIndexedInput)
	}{
		{"independent-digest", func(i *SourceIndexedInput) { i.ExpectedOriginalSHA256 = indexedSHA("other") }},
		{"source-invalid-utf8", func(i *SourceIndexedInput) { i.Original = string([]byte{0xff}) }},
		{"skill-invalid-utf8", func(i *SourceIndexedInput) { i.Skill = string([]byte{0xff}) }},
		{"path-invalid-utf8", func(i *SourceIndexedInput) { i.Path = string([]byte{0xff}) }},
		{"expected-invalid-utf8", func(i *SourceIndexedInput) { i.ExpectedOriginalSHA256 = string([]byte{0xff}) }},
		{"blank-source", func(i *SourceIndexedInput) {
			i.Original = " \r\n\t\n"
			i.ExpectedOriginalSHA256 = indexedSHA(i.Original)
		}},
		{"blank-skill", func(i *SourceIndexedInput) { i.Skill = " " }},
		{"blank-path", func(i *SourceIndexedInput) { i.Path = "\n" }},
		{"uuid-source", func(i *SourceIndexedInput) {
			i.Original = "source contains ABCDEF12-1234-5678-9ABC-DEF012345678"
			i.ExpectedOriginalSHA256 = indexedSHA(i.Original)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := valid
			tc.edit(&in)
			if p, err := BuildSourceIndexedPlan(in); err == nil || p != nil {
				t.Fatal("expected failclosed constructor")
			}
		})
	}
	// UUID metadata is private, so it must bind roots without leaking into either prompt.
	uuid := "12345678-1234-5678-9abc-def012345678"
	in := valid
	in.Skill, in.Path = uuid, uuid+".md"
	p, err := BuildSourceIndexedPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := p.Request()
	if revisionUUIDPattern.MatchString(r.System + r.User) {
		t.Fatal("private provenance UUID leaked")
	}
	var zero SourceIndexedPlan
	for _, p := range []*SourceIndexedPlan{nil, &zero} {
		if _, err := p.Request(); err == nil {
			t.Fatal("zero plan request accepted")
		}
		if _, err := p.Validate(&llm.CompleteResponse{}); err == nil {
			t.Fatal("zero plan validation accepted")
		}
	}
}

func TestSourceIndexedBlockLimit(t *testing.T) {
	t.Parallel()
	for _, count := range []int{SourceIndexedMaxBlocks, SourceIndexedMaxBlocks + 1} {
		source := strings.Repeat("# heading\r\n\r\n", count)
		p, err := BuildSourceIndexedPlan(SourceIndexedInput{"s", "p", source, indexedSHA(source)})
		if count == SourceIndexedMaxBlocks {
			if err != nil || len(p.Blocks()) != count {
				t.Fatalf("boundary blocks: %v", err)
			}
		} else if err == nil || p != nil {
			t.Fatal("oversize blocks were truncated or admitted")
		}
	}
}

func TestSourceIndexedCandidateCopiesAndGateA(t *testing.T) {
	t.Parallel()
	var zero SourceIndexedCandidate
	for _, c := range []*SourceIndexedCandidate{nil, &zero} {
		if c.Document() != nil || c.Audit().StructuralValid {
			t.Fatal("uninitialized candidate projected a document or valid audit")
		}
	}
	p := indexedTestPlan(t, "Alpha full source details\n\n# Heading")
	out := indexedTestOutput()
	out.Items[0].Name = " `Alpha` "
	raw := indexedTestJSON(t, out)
	c, err := p.Validate(&llm.CompleteResponse{Text: raw})
	if err != nil {
		t.Fatal(err)
	}
	doc, audit := c.Document(), c.Audit()
	if len(doc.Items) != 1 || doc.Items[0].Name != "Alpha" || doc.Items[0].ID != "" || doc.Items[0].SourceSpan != nil || doc.Skill != "private skill" || !audit.StructuralValid || audit.RawResponse != raw || audit.RawResponseSHA256 != indexedSHA(raw) || audit.SemanticStatus != "not_evaluated_full_current_GateB_required" {
		t.Fatalf("candidate=%#v audit=%#v", doc, audit)
	}
	gateDoc, err := NewFormatGate(100).Check(doc)
	if err != nil || !reflect.DeepEqual(gateDoc, doc) {
		t.Fatal("projection did not survive unchanged current GateA")
	}
	doc.Items[0].Name = "mutated"
	audit.Items[0].Evidence[0].Quote = "mutated"
	audit.Coverage[0].Items[0] = "mutated"
	audit.Blocks[0].Text = "mutated"
	if c.Document().Items[0].Name != "Alpha" || c.Audit().Items[0].Evidence[0].Quote != "Alpha" || c.Audit().Coverage[0].Items[0] != "i1" || c.Audit().Blocks[0].Text == "mutated" {
		t.Fatal("candidate/audit aliasing")
	}
	if c.Audit().Coverage[1].Items == nil {
		t.Fatal("audit changed an exact empty array into null")
	}
	// This is deliberately NOT a semantic validator: a truthful quote cannot prove its summary.
	out.Items[0].Content = "Unsupported claim despite literal occurrence"
	if candidate, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, out)}); err != nil || candidate.Audit().SemanticStatus == "accepted" {
		t.Fatal("structural witness was confused with semantic judgment")
	}
}

// 标准 json.Unmarshal 会接受重复键、大小写兼容、孤立代理项；本模式必须整包失败。
func TestSourceIndexedStrictJSON(t *testing.T) {
	t.Parallel()
	p := indexedTestPlan(t, "Alpha full source details\n\n# Heading")
	raw := indexedTestJSON(t, indexedTestOutput())
	for name, bad := range map[string]string{
		"nil": "null", "empty": "", "array": "[]", "trailing": raw + "{}", "fence": "```json\n" + raw + "\n```",
		"duplicate-top":     strings.Replace(raw, `"items":`, `"items":[],"items":`, 1),
		"escaped-duplicate": strings.Replace(raw, `"name":"Alpha"`, `"name":"Alpha","na\u006de":"Alpha"`, 1),
		"duplicate-nested":  strings.Replace(raw, `"quote":"Alpha"`, `"quote":"Alpha","quote":"Alpha"`, 1),
		"key-case":          strings.Replace(raw, `"items":`, `"Items":`, 1),
		"tag-case":          strings.Replace(raw, `"tag":"entity"`, `"tag":"ENTITY"`, 1),
		"extra":             strings.Replace(raw, `"tag":"entity"`, `"tag":"entity","ID":"hidden"`, 1),
		"missing-field":     strings.Replace(raw, `"confidence":0.9,`, "", 1),
		"null-confidence":   strings.Replace(raw, `"confidence":0.9`, `"confidence":null`, 1),
		"string-confidence": strings.Replace(raw, `"confidence":0.9`, `"confidence":"0.9"`, 1),
		"overflow":          strings.Replace(raw, `"confidence":0.9`, `"confidence":1e999`, 1),
		"rounded-above-one": strings.Replace(raw, `"confidence":0.9`, `"confidence":1.00000000000000000000000001`, 1),
		"rounded-negative":  strings.Replace(raw, `"confidence":0.9`, `"confidence":-1e-999`, 1),
		"NaN":               strings.Replace(raw, `"confidence":0.9`, `"confidence":NaN`, 1),
		"null-items":        strings.Replace(raw, `"items":[{`, `"items":null,"extra":[{`, 1),
		"null-evidence":     strings.Replace(raw, `"evidence":[{"paragraph":"p1","quote":"Alpha"}]`, `"evidence":null`, 1),
		"high-surrogate":    strings.Replace(raw, "Alpha summary", `\ud800`, 1),
		"low-surrogate":     strings.Replace(raw, "Alpha summary", `\udc00`, 1),
		"bad-pair":          strings.Replace(raw, "Alpha summary", `\ud800\u0041`, 1),
		"invalid-escape":    strings.Replace(raw, "Alpha summary", `\x41`, 1),
		"invalid-utf8":      strings.Replace(raw, "Alpha summary", string([]byte{0xff}), 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if c, err := p.Validate(&llm.CompleteResponse{Text: bad}); err == nil || c != nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
	for _, good := range []string{raw, " \n" + raw + "\t", strings.Replace(raw, "Alpha summary", `valid \ud83d\ude00 and escaped \" text`, 1)} {
		if _, err := p.Validate(&llm.CompleteResponse{Text: good}); err != nil {
			t.Fatalf("valid escaped response rejected: %v", err)
		}
	}
	if _, err := p.Validate(nil); err == nil {
		t.Fatal("nil response accepted")
	}
}

func TestSourceIndexedCoverageAndWitnessRejections(t *testing.T) {
	t.Parallel()
	p := indexedTestPlan(t, "Alpha full source details\n\n# Heading")
	cases := []struct {
		name string
		edit func(*sourceIndexedOutput)
	}{
		{"unknown-item-alias", func(o *sourceIndexedOutput) { o.Items[0].ID = "i0" }},
		{"missing-block", func(o *sourceIndexedOutput) { o.Coverage = o.Coverage[:1] }},
		{"duplicate-block", func(o *sourceIndexedOutput) { o.Coverage[1] = o.Coverage[0] }},
		{"unknown-block", func(o *sourceIndexedOutput) { o.Coverage[1].Paragraph = "p3" }},
		{"covered-empty", func(o *sourceIndexedOutput) { o.Coverage[0].Items = []string{} }},
		{"covered-reason", func(o *sourceIndexedOutput) { o.Coverage[0].Reason = " " }},
		{"unknown-reference", func(o *sourceIndexedOutput) { o.Coverage[0].Items = []string{"i2"} }},
		{"duplicate-reference", func(o *sourceIndexedOutput) { o.Coverage[0].Items = []string{"i1", "i1"} }},
		{"no-annotation-blank", func(o *sourceIndexedOutput) { o.Coverage[1].Reason = "\t" }},
		{"no-annotation-reference", func(o *sourceIndexedOutput) { o.Coverage[1].Items = []string{"i1"} }},
		{"unknown-disposition", func(o *sourceIndexedOutput) { o.Coverage[1].Disposition = "skip" }},
		{"unused-item", func(o *sourceIndexedOutput) {
			o.Coverage[0] = SourceIndexedCoverage{"p1", "no_annotation", []string{}, "not covered"}
		}},
		{"unreciprocated-evidence", func(o *sourceIndexedOutput) {
			o.Items[0].Evidence = append(o.Items[0].Evidence, SourceIndexedEvidence{"p2", "Heading"})
		}},
		{"unwitnessed-coverage", func(o *sourceIndexedOutput) {
			o.Coverage[1] = SourceIndexedCoverage{"p2", "covered", []string{"i1"}, ""}
		}},
		{"wrong-quote-block", func(o *sourceIndexedOutput) { o.Items[0].Evidence[0].Paragraph = "p2" }},
		{"quote-not-substring", func(o *sourceIndexedOutput) { o.Items[0].Evidence[0].Quote = "alpha" }},
		{"blank-quote", func(o *sourceIndexedOutput) { o.Items[0].Evidence[0].Quote = " " }},
		{"empty-evidence", func(o *sourceIndexedOutput) { o.Items[0].Evidence = []SourceIndexedEvidence{} }},
		{"duplicate-evidence", func(o *sourceIndexedOutput) {
			o.Items[0].Evidence = append(o.Items[0].Evidence, o.Items[0].Evidence[0])
		}},
		{"identity-without-evidence", func(o *sourceIndexedOutput) {
			o.Items[0].Identity = SourceIndexedNaming{"descriptive", "p2", "Heading"}
		}},
		{"identity-bad-quote", func(o *sourceIndexedOutput) { o.Items[0].Identity.Quote = "Other" }},
		{"identity-unknown-mode", func(o *sourceIndexedOutput) { o.Items[0].Identity.Mode = "infer" }},
		{"identity-literal-mismatch", func(o *sourceIndexedOutput) { o.Items[0].Identity.Quote = "full" }},
		{"blank-name", func(o *sourceIndexedOutput) { o.Items[0].Name = " ` ` " }},
		{"blank-content", func(o *sourceIndexedOutput) { o.Items[0].Content = "\t" }},
		{"blank-detail", func(o *sourceIndexedOutput) { o.Items[0].Detail = "\n" }},
		{"negative-confidence", func(o *sourceIndexedOutput) { o.Items[0].Confidence = -.1 }},
		{"excess-confidence", func(o *sourceIndexedOutput) { o.Items[0].Confidence = 1.1 }},
		{"invalid-tag", func(o *sourceIndexedOutput) { o.Items[0].Tag = "domain" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := indexedTestOutput()
			tc.edit(&o)
			if c, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, o)}); err == nil || c != nil {
				t.Fatal("invalid coverage/witness accepted")
			}
		})
	}
	// 根明确要求非空候选；不能把 all-no_annotation 账本当作完成后交给付费 GateB。
	// A complete disposition table with zero items is still not an admissible candidate.
	empty := sourceIndexedOutput{Items: []SourceIndexedItem{}, Coverage: []SourceIndexedCoverage{
		{"p1", "no_annotation", []string{}, "No items"}, {"p2", "no_annotation", []string{}, "No items"},
	}}
	if c, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, empty)}); err == nil || c != nil {
		t.Fatal("all-no_annotation empty candidate admitted")
	}
	for _, tag := range []dktypes.AnnotationTag{dktypes.TagEntity, dktypes.TagConcept} {
		o := indexedTestOutput()
		i := o.Items[0]
		i.ID, i.Tag, i.Name = "i2", tag, " `Alpha` "
		o.Items = append(o.Items, i)
		o.Coverage[0].Items = append(o.Coverage[0].Items, "i2")
		if c, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, o)}); err == nil || c != nil {
			t.Fatal("normalized-name collision reached GateA deduplication")
		}
	}
	// A descriptive identity permits a neutral label; semantics remain unevaluated.
	o := indexedTestOutput()
	o.Items[0].Name, o.Items[0].Identity.Mode = "neutral source description", "descriptive"
	if _, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, o)}); err != nil {
		t.Fatal(err)
	}
}

// 独立评审发现 big.Float 也会把极端负指数下溢为 -0，从而误接纳负置信度。
// Preserve those failed probes, true signed zeros, and exact decimal/exponent boundaries.
func TestSourceIndexedExactDecimalConfidence(t *testing.T) {
	t.Parallel()
	p := indexedTestPlan(t, "Alpha full source details\n\n# Heading")
	raw := indexedTestJSON(t, indexedTestOutput())
	for _, tc := range []struct {
		number string
		valid  bool
	}{
		{"-1e-646456994", false}, {"-1e-646456995", false}, {"-1e-999999999", false}, {"-1e-2147483648", false},
		{"-1e-999999999999999999999999999999999999", false}, {"-0.00000001e-999999999", false},
		{"1.000000000000000000000000000001", false}, {"1000000000000000000001e-21", false},
		{"1e999999999999999999999999999999999999", false}, {"2", false}, {"-0.1", false},
		{"-0", true}, {"-0.0000e+999999999", true}, {"0e-999999999999999999999999999999999999", true},
		{"1e-999999999", true}, {"1e-999999999999999999999999999999999999", true},
		{"10e-1", true}, {"1000.0000e-3", true}, {"0.0001e4", true},
		{"0.9999999999999999999999999999999", true}, {"1", true},
	} {
		t.Run(tc.number, func(t *testing.T) {
			t.Parallel()
			c, err := p.Validate(&llm.CompleteResponse{Text: strings.Replace(raw, `"confidence":0.9`, `"confidence":`+tc.number, 1)})
			if (err == nil) != tc.valid || (c != nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestSourceIndexedBounds(t *testing.T) {
	t.Parallel()
	t.Run("quotes-and-names", func(t *testing.T) {
		t.Parallel()
		for _, quoteBytes := range []int{4096, 4097} {
			q := strings.Repeat("x", quoteBytes)
			p := indexedTestPlan(t, "Alpha "+q+"\n\n# Heading")
			o := indexedTestOutput()
			o.Items[0].Evidence[0].Quote = q
			_, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, o)})
			if (err == nil) != (quoteBytes == 4096) {
				t.Fatalf("quote bytes%d err=%v", quoteBytes, err)
			}
		}
		for _, count := range []int{100, 101} {
			name := strings.Repeat("界", count)
			p := indexedTestPlan(t, name+"\n\n# Heading")
			o := indexedTestOutput()
			o.Items[0].Name = name
			o.Items[0].Evidence[0].Quote = name
			o.Items[0].Identity.Quote = name
			_, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, o)})
			if (err == nil) != (count == 100) || utf8.RuneCountInString(name) != count {
				t.Fatalf("name runes%d err=%v", count, err)
			}
		}
	})
	t.Run("evidence", func(t *testing.T) {
		t.Parallel()
		for _, count := range []int{16, 17} {
			var source []string
			o := indexedTestOutput()
			o.Coverage, o.Items[0].Evidence = nil, nil
			for i := 1; i <= count; i++ {
				source = append(source, fmt.Sprintf("Alpha paragraph%d", i))
				alias := fmt.Sprintf("p%d", i)
				o.Coverage = append(o.Coverage, SourceIndexedCoverage{alias, "covered", []string{"i1"}, ""})
				o.Items[0].Evidence = append(o.Items[0].Evidence, SourceIndexedEvidence{alias, "Alpha"})
			}
			p := indexedTestPlan(t, strings.Join(source, "\n\n"))
			_, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, o)})
			if (err == nil) != (count == 16) {
				t.Fatalf("evidence%d err=%v", count, err)
			}
		}
	})
	t.Run("items", func(t *testing.T) {
		t.Parallel()
		for _, count := range []int{512, 513} {
			var names []string
			o := sourceIndexedOutput{Coverage: []SourceIndexedCoverage{{"p1", "covered", []string{}, ""}}}
			for i := 1; i <= count; i++ {
				it := indexedTestOutput().Items[0]
				it.ID, it.Name = fmt.Sprintf("i%d", i), fmt.Sprintf("Name%03d", i)
				it.Identity.Quote, it.Evidence[0].Quote = it.Name, it.Name
				o.Items = append(o.Items, it)
				o.Coverage[0].Items = append(o.Coverage[0].Items, it.ID)
				names = append(names, it.Name)
			}
			p := indexedTestPlan(t, strings.Join(names, " "))
			_, err := p.Validate(&llm.CompleteResponse{Text: indexedTestJSON(t, o)})
			if (err == nil) != (count == 512) {
				t.Fatalf("items%d err=%v", count, err)
			}
		}
	})
	t.Run("canonical-output-bytes", func(t *testing.T) {
		t.Parallel()
		p := indexedTestPlan(t, "Alpha full source details\n\n# Heading")
		o := indexedTestOutput()
		o.Items[0].Detail = ""
		overhead := len(indexedTestJSON(t, o))
		for _, size := range []int{SourceIndexedMaxCanonicalBytes, SourceIndexedMaxCanonicalBytes + 1} {
			o.Items[0].Detail = strings.Repeat("x", size-overhead)
			raw := indexedTestJSON(t, o)
			if len(raw) != size {
				t.Fatal("bad boundary fixture")
			}
			_, err := p.Validate(&llm.CompleteResponse{Text: raw})
			if (err == nil) != (size == SourceIndexedMaxCanonicalBytes) {
				t.Fatalf("canonical%d err=%v", size, err)
			}
		}
	})
}
