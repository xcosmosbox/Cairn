package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Exercise the ordinary Run, real managed writeback, discovery, annotation,
// AssignIDs and source-grounded reflow. Fakes supply semantics, not live efficacy.
func TestV016C4WritebackPreservesCanonicalSourceNamespace(t *testing.T) {
	for _, mode := range []string{"current", "missing", "stale", "conflicting-caller", "conflicting-persisted", "missing-namespace", "conflicting-bodies", "invalid-hash"} {
		t.Run(mode, func(t *testing.T) { v016C4NamespaceRun(t, mode) })
	}
}

func v016C4NamespaceRun(t *testing.T, mode string) {
	t.Helper()
	ctx := context.Background()
	root, dbPath := filepath.Join(t.TempDir(), "repo"), filepath.Join(t.TempDir(), "graph.db")
	const canonical = "paperhall-mini"
	paths := []string{"mini/SKILL.md", "mini/references/log.md", "mini/references/pass.md"}
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	st := newStores(db)
	pump := &dktypes.Node{ID: "11111111-1111-5111-8111-111111111111", Label: dktypes.LabelEntity, Name: "Atlas pump", Summary: "Atlas pump always runs.", Description: "Atlas pump must not run during maintenance.", Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceExtraction, Confidence: .9}
	valve := &dktypes.Node{ID: "22222222-2222-5222-8222-222222222222", Label: dktypes.LabelEntity, Name: "Safety valve", Summary: "Safety valve maintenance rule", Description: v016ValveDetail, Domain: pump.Domain, Subdomain: pump.Subdomain, Provenance: dktypes.ProvenanceExtraction, Confidence: .9}
	other := &dktypes.Node{ID: "33333333-3333-5333-8333-333333333333", Label: dktypes.LabelEntity, Name: "Logger L", Summary: "Independent logger", Description: "Logger L records inventory only.", Domain: pump.Domain, Subdomain: pump.Subdomain, Provenance: dktypes.ProvenanceExtraction, Confidence: .9}
	for _, n := range []*dktypes.Node{pump, valve, other, {ID: "domain::operations", Label: dktypes.LabelDomain, Name: "Operations", Domain: pump.Domain, Provenance: dktypes.ProvenanceExtraction}, {ID: "subdomain::operations/pumps", Label: dktypes.LabelSubdomain, Name: "Pumps", Domain: pump.Domain, Subdomain: pump.Subdomain, Provenance: dktypes.ProvenanceExtraction}} {
		if err := st.nodes.Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: pump.ID, TargetID: valve.ID, Kind: dktypes.KindReferences, Description: "Historical unverified inference", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0}); err != nil {
		t.Fatal(err)
	}
	var originals []extract.FrozenSource
	var docs []*dktypes.AnnotatedDocument
	var oldRows []storage.NodeSource
	for pi, path := range paths {
		raw := "# Original\n" + pump.Description + "\n" + valve.Description + "\n" + other.Description
		if pi == 0 {
			raw = "---\nname: " + canonical + "\n---\n" + raw
		}
		originals = append(originals, extract.FreezeSource(canonical, path, raw))
		doc := &dktypes.AnnotatedDocument{Skill: canonical, FilePath: path}
		for i := 0; i < []int{4, 4, 5}[pi]; i++ {
			n := []*dktypes.Node{pump, valve}[i%2]
			if pi == 1 && i == 0 {
				n = other
			}
			member := fmt.Sprintf("%s-old-member-%d-%d", canonical, pi, i)
			doc.Items = append(doc.Items, dktypes.AnnotatedItem{ID: member, Tag: dktypes.TagEntity, Name: n.Name, Content: n.Description, Detail: n.Description})
			oldRows = append(oldRows, storage.NodeSource{NodeUUID: n.ID, MemberID: member, Skill: canonical, FilePath: path})
		}
		docs = append(docs, doc)
	}
	if err := st.sources.InsertBatch(ctx, oldRows); err != nil {
		t.Fatal(err)
	}
	if mode == "conflicting-persisted" || mode == "missing-namespace" {
		skill := "foreign"
		if mode == "missing-namespace" {
			skill = ""
		}
		if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: pump.ID, MemberID: "ambiguous-old-member", Skill: skill, FilePath: paths[2]}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.identity.Set(ctx, "local:v016-namespace"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.version.Bump(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.fts.RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "mini"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, paths[0]), []byte(originals[0].Content), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs(paths...), nil, nil, root); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(filepath.Join(root, paths[0]))
	if err != nil || strings.Contains(string(generated), "name: "+canonical) || !strings.Contains(string(generated), "kg:uuid=") {
		t.Fatalf("fixture did not perform actual frontmatter-free writeback: %s / %v", generated, err)
	}
	db.Close()
	client := &v016ChangedClient{}
	base, err := NewIncrementalOrchestrator(Options{Client: client, RepositoryIdentity: "local:v016-namespace", MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Run(ctx, root, dbPath); err != nil {
		t.Fatal(err)
	}
	before := snapshotMoveLedger(t, dbPath)
	pass := filepath.Join(root, paths[2])
	old, err := os.ReadFile(pass)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pass, append(old, []byte("\n\n"+v016ChangedSource+"\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	client.source = originals[2].Content + "\n" + v016ChangedSource
	originals[2] = extract.FreezeSource(canonical, paths[2], client.source)
	switch mode {
	case "missing":
		originals = originals[:2]
		docs = docs[:2]
	case "stale":
		originals[2] = extract.FreezeSource(canonical, paths[2], strings.TrimSuffix(client.source, "\n"+v016ChangedSource))
	case "conflicting-caller":
		originals[2] = extract.FreezeSource("foreign", paths[2], client.source)
		docs = docs[:2]
	case "conflicting-bodies":
		originals = append(originals, extract.FreezeSource(canonical, paths[2], "conflicting raw"))
	case "invalid-hash":
		originals[2].SHA256 = strings.Repeat("0", 64)
	}
	o, err := NewIncrementalOrchestrator(Options{Client: client, RepositoryIdentity: "local:v016-namespace", MaxRetries: 1, Sources: originals, SourceDocuments: docs})
	if err != nil {
		if mode == "conflicting-bodies" || mode == "invalid-hash" {
			after := snapshotMoveLedger(t, dbPath)
			if !reflect.DeepEqual(before.Nodes, after.Nodes) || !reflect.DeepEqual(before.Sources, after.Sources) {
				t.Fatal("invalid caller context mutated graph")
			}
			return
		}
		t.Fatal(err)
	}
	report, runErr := o.Run(ctx, root, dbPath)
	after := snapshotMoveLedger(t, dbPath)
	if mode != "current" {
		if runErr == nil || report.Status == "complete" || len(report.UnresolvedDocs) == 0 {
			t.Fatalf("source failure certified: %+v / %v", report, runErr)
		}
		if !reflect.DeepEqual(before.Nodes, after.Nodes) || !reflect.DeepEqual(before.Sources, after.Sources) {
			t.Fatal("unavailable source mutated existing graph or source ledger")
		}
		return
	}
	if runErr != nil || report.Status != "complete" || len(report.Documents) != 1 || report.Documents[0].Skill != canonical || report.Documents[0].Outcome != annotation.DocumentComplete {
		raw, _ := json.Marshal(report)
		t.Fatalf("canonical C4 did not complete: %s / %v", raw, runErr)
	}
	if len(after.Sources) != len(before.Sources)+2 {
		t.Fatalf("expected exactly two accepted canonical C4 incidences: before=%d after=%d", len(before.Sources), len(after.Sources))
	}
	for _, row := range after.Sources {
		if row.Skill != canonical || (!strings.Contains(row.MemberID, "-old-member-") && !strings.HasPrefix(row.MemberID, canonical+"-")) {
			t.Fatalf("foreign source committed: %+v", row)
		}
	}
	var joined []string
	for _, request := range client.requests {
		if !strings.Contains(request.System, "SOURCE-RELATION GENERATE") {
			continue
		}
		var wire struct {
			Sources []struct {
				ID, Skill, Path string
				Members         []string
			} `json:"original_sources"`
			Material []struct {
				SourceID string                `json:"source_id"`
				Items    []struct{ ID string } `json:"items"`
			} `json:"member_material"`
			Nodes []struct {
				Members []string
				Sources []struct {
					MemberID string `json:"member_id"`
					SourceID string `json:"source_id"`
				} `json:"member_sources"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal([]byte(request.User), &wire); err != nil {
			t.Fatal(err)
		}
		for _, source := range wire.Sources {
			if source.Skill != canonical {
				t.Fatalf("foreign relation source: %+v", source)
			}
			if source.Path != paths[2] {
				continue
			}
			for _, row := range after.Sources {
				if row.FilePath != paths[2] || strings.Contains(row.MemberID, "-old-member-") {
					continue
				}
				member, material, owned := false, false, false
				for _, id := range source.Members {
					member = member || id == row.MemberID
				}
				for _, m := range wire.Material {
					if m.SourceID == source.ID {
						for _, item := range m.Items {
							material = material || item.ID == row.MemberID
						}
					}
				}
				for _, node := range wire.Nodes {
					for _, binding := range node.Sources {
						owned = owned || (binding.MemberID == row.MemberID && binding.SourceID == source.ID)
					}
				}
				if !member || !material || !owned {
					t.Fatalf("new canonical member not fully joined: %s members=%v material=%v owned=%v", row.MemberID, member, material, owned)
				}
				joined = append(joined, row.MemberID)
			}
		}
	}
	if len(joined) == 0 {
		t.Fatal("no actual relation source-material join observed")
	}
	for _, row := range before.Sources {
		found := false
		for _, now := range after.Sources {
			found = found || reflect.DeepEqual(row, now)
		}
		if !found {
			t.Fatalf("old incidence changed: %+v", row)
		}
	}
	for _, node := range before.Nodes {
		found := false
		for _, now := range after.Nodes {
			if now.ID == node.ID {
				found = true
				if node.ID == other.ID && !reflect.DeepEqual(node, now) {
					t.Fatal("unrelated node changed")
				}
			}
		}
		if !found {
			t.Fatalf("old UUID removed: %s", node.ID)
		}
	}
}
