package incremental

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// The names/content reproduce mini11's four incoming units and old token
// shapes. Metadata is shortened, so ranks are not copied from the actual DB.
func v016C4RecallStores(t *testing.T) *stores {
	t.Helper()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "kg.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := newStores(db)
	ctx := context.Background()
	nodes := []*dktypes.Node{
		{ID: "private-ticket", Label: dktypes.LabelEntity, Name: "霜羽票签", Summary: "纸岚展馆夜场使用的票签；其2026年1月版有效期为9分钟。", Description: "霜羽票签是纸岚展馆夜场使用的票签。2026年1月版霜羽票签的有效期为9分钟。"},
		{ID: "private-january", Label: dktypes.LabelEntity, Name: "霜羽票签2026年1月版", Summary: "霜羽票签的2026年1月版本，其有效期为9分钟。", Description: "名称：霜羽票签2026年1月版。2026年1月版霜羽票签的有效期为9分钟。"},
		{ID: "private-night", Label: dktypes.LabelEntity, Name: "纸岚展馆夜场", Summary: "纸岚展馆夜场使用霜羽票签；霜羽票签是该夜场使用的票签。", Description: "名称：纸岚展馆夜场。霜羽票签是纸岚展馆夜场使用的票签。"},
		{ID: "private-expired", Label: dktypes.LabelEntity, Name: "过期票签", Summary: "过期票签无效，夜场入厅不接受过期票签。", Description: "过期票签无效。夜场入厅不接受过期票签。"},
		{ID: "private-human", Label: dktypes.LabelEntity, Name: "霜羽票签私密释义", Summary: "PRIVATE HUMAN SUMMARY", Description: "PRIVATE HUMAN DETAIL", Provenance: dktypes.ProvenanceHumanCurated},
		{ID: "private-domain", Label: dktypes.LabelDomain, Name: "运营规则"},
		{ID: "private-sub", Label: dktypes.LabelSubdomain, Name: "票签版本"},
	}
	for _, n := range nodes {
		n.Domain = "operations"
		n.Subdomain = "editions"
		if n.Provenance == "" {
			n.Provenance = dktypes.ProvenanceExtraction
		}
		if err := st.nodes.Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.fts.RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func v016C4RecallItems() []dktypes.AnnotatedItem {
	items := []dktypes.AnnotatedItem{
		{Name: "霜羽票签", Content: "2026年2月版将霜羽票签的有效期改为6分钟；9分钟仅适用于2026年1月版。过期票签仍不被夜场入厅接受。"},
		{Name: "2026年2月版", Content: "2026年2月版将霜羽票签的有效期改为6分钟。"},
		{Name: "2026年1月版", Content: "9分钟有效期仅适用于2026年1月版（霜羽票签有效期）。"},
		{Name: "过期票签", Content: "过期票签仍不被夜场入厅接受。"},
	}
	for i := range items {
		items[i].Tag = dktypes.TagConcept
		items[i].Detail = items[i].Content
		items[i].Confidence = 1
	}
	return items
}

func TestV016C4NameRecallRestoresEditionAndExistingUUID(t *testing.T) {
	ctx := context.Background()
	st := v016C4RecallStores(t)
	docs := extract.AssignIDs([]*dktypes.AnnotatedDocument{{Skill: "paperhall-mini", FilePath: "mini/references/pass.md", Items: v016C4RecallItems()}})
	calls := 0
	client := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls++
		for _, secret := range []string{"private-ticket", "private-january", "private-expired", "private-human", "霜羽票签私密释义", "PRIVATE HUMAN"} {
			if strings.Contains(req.User, secret) {
				t.Fatalf("private identity/prose in prompt: %s", secret)
			}
		}
		alias := func(name string) string {
			m := regexp.MustCompile(`alias=([^\s]+) \[Entity\] ` + regexp.QuoteMeta(name) + `（`).FindStringSubmatch(req.User)
			if len(m) != 2 {
				t.Fatalf("existing owner/edition missing from real recall: %s", name)
			}
			return m[1]
		}
		decisions := []alignDecision{}
		for i, item := range docs[0].Items {
			d := alignDecision{Unit: item.ID, Action: "merge_into"}
			switch i {
			case 0:
				d.Target = alias("霜羽票签")
			case 1:
				d.Action = "attach"
				d.Target = "sd#1"
				d.NodeName = "霜羽票签2026年2月版"
				d.Tag = "concept"
			case 2:
				d.Target = alias("霜羽票签2026年1月版")
			case 3:
				d.Target = alias("过期票签")
			}
			decisions = append(decisions, d)
		}
		return &llm.CompleteResponse{Text: v013IncrementalWire(t, alignLLMOutput{Decisions: decisions})}, nil
	})
	a := newAligner(client, 65536, st, 15)
	a.maxRetries = -1
	out, err := a.align(ctx, docs, newRunState())
	if err != nil || out.llFailed || calls != 1 || out.report.Outcome != "complete" {
		t.Fatalf("alignment flow: calls=%d outcome=%+v err=%v", calls, out, err)
	}
	for _, id := range []string{"private-ticket", "private-january", "private-expired"} {
		if out.pending[id] == nil || out.pending[id].IsNew {
			t.Fatalf("existing identity not pinned: %s", id)
		}
	}
	if len(out.pending) != 4 || len(out.sourceRows) != 4 {
		t.Fatalf("unexpected materialization: %+v", out)
	}
	// The placement responses above are scripted. This proves availability,
	// alias translation and UUID flow, not the model's semantic identity quality.
}

func TestV016C4NameRecallCandidatesDoNotForceSameMonthMerge(t *testing.T) {
	ctx := context.Background()
	st := v016C4RecallStores(t)
	docs := extract.AssignIDs([]*dktypes.AnnotatedDocument{{Skill: "different-owner", FilePath: "other.md", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagConcept, Name: "2026年1月版", Content: "松烟票签的2026年1月版有效期6分钟，不是霜羽票签版本。", Detail: "松烟票签的2026年1月版有效期6分钟，不是霜羽票签版本。", Confidence: 1}}}})
	calls := 0
	client := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls++
		if !strings.Contains(req.User, "霜羽票签2026年1月版") || !strings.Contains(req.User, "松烟票签") {
			t.Fatal("candidate or different owner context missing")
		}
		return &llm.CompleteResponse{Text: v013IncrementalWire(t, alignLLMOutput{Decisions: []alignDecision{{Unit: docs[0].Items[0].ID, Action: "attach", Target: "sd#1", NodeName: "松烟票签2026年1月版", Tag: "concept"}}})}, nil
	})
	a := newAligner(client, 65536, st, 15)
	a.maxRetries = -1
	out, err := a.align(ctx, docs, newRunState())
	if err != nil || out.llFailed || calls != 1 || len(out.pending) != 1 || out.pending["private-january"] != nil {
		t.Fatalf("recall forced identity: calls=%d out=%+v err=%v", calls, out, err)
	}
	for _, p := range out.pending {
		if !p.IsNew || p.Name != "松烟票签2026年1月版" {
			t.Fatalf("scripted new owner not preserved: %+v", p)
		}
	}
}

func TestV016C4NameRecallLiteralPrefixSafetyAndEmptyContentFallback(t *testing.T) {
	ctx := context.Background()
	st := v016C4RecallStores(t)
	for _, name := range []string{`Atlas"Pump OR (blue):*`, `OR NOT AND`, `" () : *`, `Atlas Pump`} {
		t.Run(name, func(t *testing.T) {
			query := buildRecallQuery(&alignUnit{Name: name})
			hits, err := st.fts.Search(ctx, query, nil, "", 15)
			if err != nil {
				t.Fatalf("source punctuation became syntax: %q: %v", query, err)
			}
			if len(hits) != 0 {
				t.Fatalf("operator-like data matched unrelated fixture: %q", query)
			}
		})
	}
	if got := buildRecallQuery(&alignUnit{Name: "Atlas Pump"}); got != `"Atlas" OR "Pump" OR "Atlas"* OR "Pump"*` {
		t.Fatalf("prefix must apply to each quoted atom: %s", got)
	}
	content := "only source content with more than forty Unicode runes follows this boundary unchanged"
	if got := buildRecallQuery(&alignUnit{Name: " \t", Content: content}); got != sanitizeFTSQuery(truncateRunes(content, 40)) {
		t.Fatalf("blank name changed old content branch: %s", got)
	}
	if got := buildRecallQuery(&alignUnit{}); got != "" {
		t.Fatalf("blank unit gained syntax: %q", got)
	}
	if got := buildRecallQuery(&alignUnit{Name: "bad\x00name", Content: "safe"}); got != `"safe"` {
		t.Fatalf("invalid name did not retain safe content: %q", got)
	}
}
