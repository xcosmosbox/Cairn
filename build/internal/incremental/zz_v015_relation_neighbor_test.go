package incremental

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
)

// Targeted accepted-helper composition: an actual C1 application dirties A,
// source-aware .4 selects a source-supported A->B facet, and I8 applies it.
// The unchanged independent B document also holds an incident snapshot.
// This does not claim the current legacy orchestrator supplies originals.
func TestV015R2ContractI8IncidentNeighborRefresh(t *testing.T) {
	ctx := context.Background()
	st, _, key, docs, originals := v014ReflowFixture(t)
	v014OldEdge(t, st, "a", "b")
	root := t.TempDir()
	if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs("a.md", "b.md", "c.md"), nil, nil, root); err != nil {
		t.Fatal(err)
	}
	rs := newRunState()
	if err := applyBypassR(ctx, st, BlockEdit{UUID: "a", DescriptionChanged: true, NewDescription: docs[0].Items[0].Detail + " Human note retained."}, rs); err != nil {
		t.Fatal(err)
	}
	r := newReflower(&v014ReflowClient{generate: []string{"Admission requires quota before 2025 except emergencies."}}, 4096, st)
	final, kept := r.recomputeRelationsWithSources(ctx, rs.dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
	if len(r.relationProcessReport().Selected) == 0 || len(final[key]) != 1 {
		t.Fatal("actual accepted source relation was not selected")
	}
	if err := applyIncrementalIngest(ctx, st, rs.dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
		t.Fatal(err)
	}
	if _, err := applyIncrementalWriteback(ctx, st, rs, nil, nil, root); err != nil {
		t.Fatal(err)
	}
	sc, err := writeback.ReadSidecar(filepath.Join(root, "b.md"))
	if err != nil {
		t.Fatal(err)
	}
	names, err := loadLayerDisplayNames(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	want, err := buildDocNodeUpdates(ctx, st, "b.md", names)
	if err != nil {
		t.Fatal(err)
	}
	if !writeback.SidecarMetadataMatches(*sc, "b.md", want) {
		t.Fatalf("actual accepted I8 relation changed, but B incident snapshot stale; affected docs=%v; sidecar=%+v expected=%+v", rs.affectedDocsSnapshot(), sc.Nodes[0].GraphMetadata.Relations, want[0].Relations)
	}
}

// 原失败模式：仅源子域受影响，丢失旧目标/新目标快照；人工保护和原样保留不能扩脏。
func TestV015RelationNeighborActualMutationScope(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"changed", "removed", "retargeted", "unchanged", "human-protected", "failed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "kg.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st := newStores(db)
			key := SubdomainKey{Domain: "d", Subdomain: "sd"}
			for _, id := range []string{"a", "b", "c"} {
				if err := st.nodes.Insert(ctx, &dktypes.Node{ID: id, Name: id, Label: dktypes.LabelConcept, Domain: key.Domain, Subdomain: key.Subdomain, Provenance: dktypes.ProvenanceExtraction, Confidence: 1}); err != nil {
					t.Fatal(err)
				}
				if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: id, MemberID: id, Skill: "one", FilePath: id + ".md"}}); err != nil {
					t.Fatal(err)
				}
			}
			v014OldEdge(t, st, "a", "b")
			if mode == "human-protected" {
				if _, err := db.Conn().Exec(`UPDATE edges SET provenance='human_curated'`); err != nil {
					t.Fatal(err)
				}
			}
			before, err := st.edges.ListAll(ctx)
			if err != nil || len(before) != 1 {
				t.Fatal(before, err)
			}
			dirty := NewDirtySet()
			dirty.DirtySubdomain(key)
			rs := newRunState()
			rel := extract.Relation{Source: "a", Target: "b", Kind: string(dktypes.KindDependsOn), Description: "New supported complete condition", Confidence: before[0].Confidence}
			rr := &reflowResult{relations: map[SubdomainKey][]extract.Relation{key: {rel}}}
			wantDocs := []string{"a.md", "b.md"}
			switch mode {
			case "removed":
				rr.relations[key] = nil
			case "retargeted":
				rr.relations[key][0].Target = "c"
				wantDocs = append(wantDocs, "c.md")
			case "unchanged":
				rr.relations[key] = nil
				rr.keptCross = map[SubdomainKey][]*dktypes.Edge{key: before}
				wantDocs = nil
			case "human-protected":
				wantDocs = nil
			case "failed":
				if _, err := db.Conn().Exec(`CREATE TRIGGER reject_relation_change BEFORE UPDATE ON edges BEGIN SELECT RAISE(ABORT,'injected relation failure'); END`); err != nil {
					t.Fatal(err)
				}
				wantDocs = nil
			}
			err = applyIncrementalIngest(ctx, st, dirty, nil, rr, rs)
			if (err != nil) != (mode == "failed") {
				t.Fatal("unexpected I8 outcome", err)
			}
			gotDocs := rs.affectedDocsSnapshot()
			if len(gotDocs) != len(wantDocs) {
				t.Fatalf("actual mutation docs=%v want=%v", gotDocs, wantDocs)
			}
			for _, doc := range wantDocs {
				if !gotDocs[doc] {
					t.Fatalf("missing incident source document %s", doc)
				}
			}
			if len(rs.touched) != 0 || !rs.dirty.Empty() {
				t.Fatal("relation writeback expanded semantic recomputation")
			}
			if mode == "failed" || mode == "human-protected" || mode == "unchanged" {
				after, err := st.edges.ListAll(ctx)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("protected/failed/no-op rows changed", before, after, err)
				}
			}
			if err == nil {
				repeat := newRunState()
				if err := applyIncrementalIngest(ctx, st, dirty, nil, rr, repeat); err != nil {
					t.Fatal(err)
				}
				if len(repeat.affectedDocsSnapshot()) != 0 {
					t.Fatal("unchanged repeat dirtied relation endpoint files")
				}
			}
		})
	}
}

// 已提交关系缓存过期只允许元数据恢复，不能吸收人工 Markdown 或放宽身份/来源/hash。
func TestV015RelationNeighborRecoveryKeepsBaselineAndIdentity(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "doc", "uuid", "member", "source", "readonly", "summary-hash", "description-hash", "failed-scope"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st, _, _, _, _ := v014ReflowFixture(t)
			v014OldEdge(t, st, "a", "b")
			root := t.TempDir()
			if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs("a.md", "b.md", "c.md"), nil, nil, root); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "b.md")
			sc, err := writeback.ReadSidecar(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.edges.DeleteBetween(ctx, "a", "b", dktypes.KindDependsOn); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "doc":
				sc.Doc = "other.md"
			case "uuid":
				sc.Nodes[0].UUID = "a"
			case "member":
				sc.Nodes[0].Members = []string{"forged-member"}
			case "source":
				sc.Nodes[0].GraphMetadata.Sources[0].StartLine += 10
			case "readonly":
				sc.Nodes[0].Name = "forged readonly name"
			case "summary-hash":
				sc.Nodes[0].SummaryHash = writeback.HashEditable("forged editable baseline")
			case "description-hash":
				sc.Nodes[0].DescriptionHash = writeback.HashEditable("forged editable baseline")
			}
			if err := writeback.WriteSidecar(path, *sc); err != nil {
				t.Fatal(err)
			}
			oldSidecar, err := os.ReadFile(path + ".kg.yaml")
			if err != nil {
				t.Fatal(err)
			}
			oldMD, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// A legal pending edit is deliberately not represented by the old hashes.
			humanMD := strings.Replace(string(oldMD), "Quota is required", "Human qualification: quota is required", 1)
			if humanMD == string(oldMD) {
				t.Fatal("pending edit fixture failed")
			}
			if err := os.WriteFile(path, []byte(humanMD), 0644); err != nil {
				t.Fatal(err)
			}
			rs := newRunState()
			if mode == "failed-scope" {
				rs.failDoc("b.md")
			}
			if mode == "valid" {
				o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}})
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := o.detect(ctx, st, root, "test"); err != nil {
					t.Fatal(err)
				}
				afterDetect, _ := os.ReadFile(path + ".kg.yaml")
				if string(afterDetect) != string(oldSidecar) {
					t.Fatal("read-only detection mutated stale relation metadata")
				}
			}
			if err := recoverRelationSnapshots(ctx, st, root, "test", rs); err != nil {
				t.Fatal(err)
			}
			newMD, _ := os.ReadFile(path)
			if string(newMD) != humanMD {
				t.Fatal("relation recovery overwrote pending human Markdown")
			}
			if mode != "valid" {
				afterSidecar, _ := os.ReadFile(path + ".kg.yaml")
				if string(afterSidecar) != string(oldSidecar) {
					t.Fatal("relation recovery laundered identity/hash mismatch")
				}
				return
			}
			fresh, err := writeback.ReadSidecar(path)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.DocHash != sc.DocHash || fresh.Nodes[0].SummaryHash != sc.Nodes[0].SummaryHash || fresh.Nodes[0].DescriptionHash != sc.Nodes[0].DescriptionHash {
				t.Fatal("relation recovery advanced original editable baseline")
			}
			names, err := loadLayerDisplayNames(ctx, st)
			if err != nil {
				t.Fatal(err)
			}
			views, err := buildDocNodeUpdates(ctx, st, "b.md", names)
			if err != nil || !writeback.SidecarMetadataMatches(*fresh, "b.md", views) || len(fresh.Nodes[0].GraphMetadata.Relations) != 0 {
				t.Fatal("recovery did not restore exact authoritative graph", err)
			}
			changes := DiffDoc("b.md", fresh, humanMD, false)
			if len(changes.C1) != 1 || len(changes.C3) != 0 {
				t.Fatalf("pending edit lost its baseline: %+v", changes)
			}
		})
	}
}

// Shared primary 也有只读关系缓存；恢复只能刷新缓存，不能吞掉后来的 Description 编辑。
func TestV015RelationNeighborRecoverySharedPrimary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _, _, _, _ := v014ReflowFixture(t)
	v014OldEdge(t, st, "a", "b")
	if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: "b", MemberID: "b-mirror", Skill: "two", FilePath: "mirror.md", StartLine: 21, EndLine: 22}}); err != nil {
		t.Fatal(err)
	}
	node, err := st.nodes.GetByID(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ao := &alignOutcome{pending: map[string]*pendingNode{"b": {}}}
	if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs("a.md", "b.md", "c.md", "mirror.md"), ao, nil, root); err != nil {
		t.Fatal(err)
	}
	primary := writeback.PrimaryRelPath(node.Domain, node.FileSlug, node.ID)
	path := filepath.Join(root, primary)
	sc, err := writeback.ReadSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.edges.DeleteBetween(ctx, "a", "b", dktypes.KindDependsOn); err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pos := strings.LastIndex(string(md), node.Description)
	if pos < 0 {
		t.Fatal("primary description missing")
	}
	humanMD := string(md[:pos]) + "Human primary qualification." + string(md[pos+len(node.Description):])
	if err := os.WriteFile(path, []byte(humanMD), 0644); err != nil {
		t.Fatal(err)
	}
	if err := recoverRelationSnapshots(ctx, st, root, "test", newRunState()); err != nil {
		t.Fatal(err)
	}
	fresh, err := writeback.ReadSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.DocHash != sc.DocHash || fresh.Nodes[0].DescriptionHash != sc.Nodes[0].DescriptionHash || fresh.Nodes[0].SummaryHash != sc.Nodes[0].SummaryHash {
		t.Fatal("primary recovery advanced human baseline")
	}
	if !fresh.Nodes[0].GraphMetadata.RelationsKnown || len(fresh.Nodes[0].GraphMetadata.Relations) != 0 {
		t.Fatal("primary relation cache was not refreshed to known-empty")
	}
	data, _ := os.ReadFile(path)
	if string(data) != humanMD {
		t.Fatal("primary pending edit overwritten")
	}
	changes := DiffDoc(primary, fresh, humanMD, false)
	if len(changes.C1) != 1 || !changes.C1[0].DescriptionChanged || len(changes.C3) != 0 {
		t.Fatalf("primary edit baseline lost: %+v", changes)
	}
	before, _ := os.ReadFile(path + ".kg.yaml")
	if err := recoverRelationSnapshots(ctx, st, root, "test", newRunState()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path + ".kg.yaml")
	if string(before) != string(after) {
		t.Fatal("primary recovery repeat did not settle")
	}
}
