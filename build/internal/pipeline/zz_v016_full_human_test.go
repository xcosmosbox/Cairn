package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const fullHumanPump = "11111111-1111-4111-8111-111111111111"

// 原故障：已核验候选承载的枚举外事实被人工正文替换后，早期收据仍使最终状态complete。
// A complete candidate receipt cannot certify different text retained at materialization.
func TestV016FullReportsPostMaterializationSemanticUnverified(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"description", "summary", "name", "synonyms", "same", "fresh", "untouched", "ordinary"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			root, path, res, docs := fullHumanFixture(t)
			candidate := &res.Domains[0].Subdomains[0].Entities[0]
			candidate.Summary = "Human pump summary"
			candidate.Description = "Human pump detail: maintenance forbids automatic restart."
			const detail = "Valve V feeds the pump only while off; it does not feed the pump while on."
			switch field {
			case "description", "fresh", "ordinary":
				candidate.Description = detail
				docs[0].Items[0].Content, docs[0].Items[0].Detail = detail, detail
			case "summary":
				candidate.Summary = "Pump receives feed only while valve V is off."
			case "name":
				candidate.Name = "Feed pump"
			case "synonyms":
				candidate.Synonyms = []string{"Feed pump"}
			case "untouched":
				res.Domains[0].Subdomains[0].Entities = res.Domains[0].Subdomains[0].Entities[1:]
				docs = docs[1:]
			}
			if field == "ordinary" {
				db, openErr := storage.OpenExisting(path)
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, updateErr := db.Conn().Exec("UPDATE nodes SET provenance='llm_inferred' WHERE id=?", fullHumanPump)
				db.Close()
				if updateErr != nil {
					t.Fatal(updateErr)
				}
			}
			query := `SELECT json_array(id,label,name,summary,synonyms,domain,subdomain,description,properties,tags,related_entities,confidence,provenance,source_refs,file_slug,created_at,updated_at) FROM nodes WHERE id='` + fullHumanPump + `'`
			before := fullHumanRows(t, path, query)
			if field == "fresh" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			report, err := (&Orchestrator{repositoryIdentity: "local:full-human"}).MaterializeCompletedExtraction(ctx, root, path, res, docs, nil)
			// These are historical candidate-stage receipts, not a model assessment of human text.
			report.Describe = &extract.DescribeReport{Outcomes: []extract.NodeDescriptionOutcome{{ID: candidate.ID, Outcome: annotation.DocumentComplete, Summary: candidate.Summary, Description: candidate.Description}}}
			report.Relations = &extract.RelationProcessReport{Scopes: []extract.RelationScopeOutcome{{Scope: "fixture/references/pump.md", Outcome: annotation.DocumentComplete}}}
			report.finish(ctx, err)
			changed := field != "same" && field != "fresh" && field != "untouched" && field != "ordinary"
			if changed {
				if err == nil || report.Status != "partial" || !strings.Contains(strings.Join(report.Reasons, ";"), "post-materialization semantic-unverified") || !strings.Contains(strings.Join(report.Reasons, ";"), fullHumanPump) || !strings.Contains(strings.Join(report.Reasons, ";"), field) {
					t.Fatalf("different final human %s falsely certified by candidate receipts: status=%s reasons=%v err=%v", field, report.Status, report.Reasons, err)
				}
				for _, reason := range report.Reasons {
					if strings.Contains(reason, "Human pump") || strings.Contains(reason, detail) {
						t.Fatalf("semantic-unverified reason exposes node text: %s", reason)
					}
				}
				if report.FinalCoverage == nil || report.FinalCoverage.Outcome != "complete" || report.FinalCoverage.CoveredIncidences != 3 {
					t.Fatalf("semantic uncertainty corrupted factual mapping receipt: %+v", report.FinalCoverage)
				}
			} else if err != nil || report.Status != "complete" {
				t.Fatalf("%s path incorrectly degraded: status=%s reasons=%v err=%v", field, report.Status, report.Reasons, err)
			}
			if report.Ingest == nil || report.Ingest.NodesInserted == 0 || report.Ingest.NodeSourcesInserted != len(docs) || report.Writeback == nil || report.Writeback.DocsWritten == 0 {
				t.Fatalf("published graph/writeback falsely reported discarded: ingest=%+v writeback=%+v", report.Ingest, report.Writeback)
			}
			persisted, openErr := storage.OpenReadOnly(path)
			if openErr != nil {
				t.Fatal(openErr)
			}
			var nodes, edges int
			countErr := persisted.Conn().QueryRow("SELECT (SELECT count(*) FROM nodes),(SELECT count(*) FROM edges)").Scan(&nodes, &edges)
			persisted.Close()
			if countErr != nil || report.Ingest.NodesInserted != nodes || report.Ingest.EdgesInserted != edges {
				t.Fatalf("committed counts differ from published graph: nodes=%d edges=%d ingest=%+v err=%v", nodes, edges, report.Ingest, countErr)
			}
			if field != "fresh" && field != "ordinary" && fullHumanRows(t, path, query) != before {
				t.Fatal("semantic receipt changed original human UUID/payload")
			}
			if field != "untouched" && field != "fresh" && field != "ordinary" {
				md, readErr := os.ReadFile(filepath.Join(root, "references/pump.md"))
				if readErr != nil || !strings.Contains(string(md), "Human pump detail: maintenance forbids automatic restart.") || strings.Contains(string(md), detail) {
					t.Fatalf("partial receipt prevented authoritative human writeback: %s %v", md, readErr)
				}
			}
			if field == "fresh" && fullHumanRows(t, path, `SELECT description FROM nodes WHERE id='`+candidate.ID+`'`) != detail {
				t.Fatal("fresh build lost candidate detail")
			}
			if field == "ordinary" && fullHumanRows(t, path, `SELECT description FROM nodes WHERE id='`+fullHumanPump+`'`) != detail {
				t.Fatal("ordinary existing machine node did not receive checked candidate detail")
			}
			if got := fullHumanRows(t, path, `SELECT description FROM nodes WHERE id='`+extract.NodeUUID([]string{"new-member"})+`'`); got != "Only after isolation." {
				t.Fatalf("independent committed addition lost: %s", got)
			}
		})
	}
}

func fullHumanFixture(t *testing.T) (string, string, *extract.Result, []*dktypes.AnnotatedDocument) {
	t.Helper()
	ctx := context.Background()
	root, path := t.TempDir(), filepath.Join(t.TempDir(), "knowledge.db")
	for _, rel := range []string{"references/pump.md", "references/controller.md", "references/new.md", "old-skill/references/clean.md"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte("original source\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	nodes := []*dktypes.Node{
		{ID: "domain::d", Label: dktypes.LabelDomain, Name: "D", Summary: "D", Domain: "d", Provenance: dktypes.ProvenanceLLMInferred},
		{ID: "subdomain::d::s", Label: dktypes.LabelSubdomain, Name: "S", Summary: "S", Domain: "d", Subdomain: "s", Provenance: dktypes.ProvenanceLLMInferred},
		{ID: fullHumanPump, Label: dktypes.LabelEntity, Name: "Pump", Summary: "Human pump summary", Description: "Human pump detail: maintenance forbids automatic restart.", Domain: "d", Subdomain: "s", Provenance: dktypes.ProvenanceHumanCurated, Confidence: .91, Properties: `{"owner":"operator"}`, SourceRefs: "fixture"},
		{ID: "old-controller", Label: dktypes.LabelEntity, Name: "Controller C", Summary: "Controller", Description: "C supplies the shutdown signal.", Domain: "d", Subdomain: "s", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .9, SourceRefs: "fixture"},
		{ID: "clean", Label: dktypes.LabelConcept, Name: "Unrelated", Summary: "Clean summary", Description: "Untouched independent content.", Domain: "clean", Subdomain: "clean", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .87, SourceRefs: "old-skill"},
		{ID: "stale", Label: dktypes.LabelConcept, Name: "Stale", Summary: "Stale", Domain: "d", Subdomain: "s", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .8},
	}
	for _, n := range nodes {
		n.CreatedAt = now
		n.UpdatedAt = now
		if err := storage.NewNodeRepo(db).Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	rows := []storage.NodeSource{
		{NodeUUID: fullHumanPump, MemberID: "pump-member", Skill: "fixture", FilePath: "references/pump.md", StartLine: 2, EndLine: 3},
		{NodeUUID: "old-controller", MemberID: "controller-member", Skill: "fixture", FilePath: "references/controller.md", StartLine: 1, EndLine: 1},
		{NodeUUID: "clean", MemberID: "clean-member", Skill: "old-skill", FilePath: "old-skill/references/clean.md", StartLine: 4, EndLine: 5},
		{NodeUUID: "stale", MemberID: "stale-member", Skill: "fixture", FilePath: "references/pump.md", StartLine: 8, EndLine: 8},
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []*dktypes.Edge{
		{SourceID: "old-controller", TargetID: fullHumanPump, Kind: dktypes.KindTriggers, Description: "Human C-to-pump signal only during shutdown.", Provenance: dktypes.ProvenanceHumanCurated, Confidence: .93, Properties: `{"operator":true}`, SourceRefs: "fixture", Bidirectional: 1, Cardinality: "1:N", CreatedAt: now},
		{SourceID: "old-controller", TargetID: fullHumanPump, Kind: dktypes.KindTriggers, Description: "Distinct legacy row under the protected identity.", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0, CreatedAt: now},
	} {
		if err := storage.NewEdgeRepo(db).Insert(ctx, edge); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Conn().Exec(`UPDATE edges SET properties=NULL,cardinality=NULL WHERE id=2; INSERT INTO kg_manifest(key,value) VALUES('previous','remove ordinary stale metadata')`); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewRepositoryIdentityRepo(db).Set(ctx, "local:full-human"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("PRAGMA wal_checkpoint(TRUNCATE); PRAGMA journal_mode=DELETE"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	docs := []*dktypes.AnnotatedDocument{
		{Skill: "fixture", FilePath: "references/pump.md", Items: []dktypes.AnnotatedItem{{ID: "pump-member", Tag: dktypes.TagEntity, Name: "Pump", Content: "Pump source", Detail: "Pump source"}}},
		{Skill: "fixture", FilePath: "references/controller.md", Items: []dktypes.AnnotatedItem{{ID: "controller-member", Tag: dktypes.TagEntity, Name: "Controller C", Content: "Controller source", Detail: "Controller source"}}},
		{Skill: "fixture", FilePath: "references/new.md", Items: []dktypes.AnnotatedItem{{ID: "new-member", Tag: dktypes.TagConcept, Name: "New condition", Content: "Only after isolation.", Detail: "Only after isolation."}}},
	}
	res := &extract.Result{Domains: []extract.Domain{{Slug: "d", Name: "D", SourceSkills: []string{"fixture"}, Subdomains: []extract.Subdomain{{Slug: "s", Name: "S", Entities: []extract.Node{
		{ID: extract.NodeUUID([]string{"pump-member"}), Label: "Entity", Name: "Pump", Summary: "Generated pump", Description: "Generated replacement must not overwrite human text.", Confidence: .95, Members: []string{"pump-member"}, SourceSkills: []string{"fixture"}},
		{ID: extract.NodeUUID([]string{"controller-member"}), Label: "Entity", Name: "Controller C", Summary: "Controller", Description: "C supplies the shutdown signal.", Confidence: .9, Members: []string{"controller-member"}, SourceSkills: []string{"fixture"}},
	}, Concepts: []extract.Node{{ID: extract.NodeUUID([]string{"new-member"}), Label: "Concept", Name: "New condition", Summary: "Only after isolation.", Description: "Only after isolation.", Confidence: .92, Members: []string{"new-member"}, SourceSkills: []string{"fixture"}}}}}}}}
	return root, path, res, docs
}

func fullHumanRows(t *testing.T, path, query string) string {
	t.Helper()
	db, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Conn().Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

func requireFullHumanSemanticUnverified(t *testing.T, report *RunReport, err error) {
	t.Helper()
	if err == nil || report.Status != "partial" || !strings.Contains(strings.Join(report.Reasons, ";"), "post-materialization semantic-unverified") {
		t.Fatalf("retained different human text must be explicitly unverified: report=%+v err=%v", report, err)
	}
}

// 原故障：成功Full用全新DB覆盖人工UUID/完整边，再把机器正文写入MD和sidecar。
// This runs the production no-LLM materialization boundary over a seeded closed DB.
func TestV016FullPreservesExistingHumanAndUntouchedScope(t *testing.T) {
	t.Parallel()
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "omitted-edge", true: "generated-collision"}[collision], func(t *testing.T) {
			root, path, res, docs := fullHumanFixture(t)
			edgeQuery := `SELECT json_array(id,source_id,target_id,kind,description,properties,provenance,confidence,source_refs,bidirectional,cardinality,created_at) FROM edges ORDER BY id`
			beforeEdges := fullHumanRows(t, path, edgeQuery)
			cleanQuery := `SELECT json_array(id,label,name,summary,description,properties,provenance,confidence,source_refs,created_at,updated_at) FROM nodes WHERE id='clean'`
			beforeClean := fullHumanRows(t, path, cleanQuery)
			pumpQuery := `SELECT json_array(id,label,name,summary,synonyms,domain,subdomain,description,properties,tags,related_entities,confidence,provenance,source_refs,file_slug,created_at,updated_at) FROM nodes WHERE id='` + fullHumanPump + `'`
			beforePump := fullHumanRows(t, path, pumpQuery)
			if collision {
				res.Domains[0].Subdomains[0].Relations = []extract.Relation{{Source: res.Domains[0].Subdomains[0].Entities[1].ID, Target: res.Domains[0].Subdomains[0].Entities[0].ID, Kind: "triggers", Description: "Machine replacement", Confidence: .6}}
			}
			o := &Orchestrator{repositoryIdentity: "local:full-human"}
			report, materializeErr := o.MaterializeCompletedExtraction(context.Background(), root, path, res, docs, []ingest.SkillMeta{{Name: "fixture"}})
			if collision {
				if materializeErr == nil || report.Status != "partial" || !strings.Contains(materializeErr.Error(), "generated payload not applied") {
					t.Fatalf("protected generated collision falsely applied: report=%+v err=%v", report, materializeErr)
				}
			} else {
				requireFullHumanSemanticUnverified(t, report, materializeErr)
			}
			db, err := storage.OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			pump, err := storage.NewNodeRepo(db).GetByID(context.Background(), fullHumanPump)
			if err != nil {
				t.Fatal(err)
			}
			if pump == nil || pump.Description != "Human pump detail: maintenance forbids automatic restart." || pump.Provenance != dktypes.ProvenanceHumanCurated {
				t.Fatalf("Full discarded existing curated UUID/payload: %+v", pump)
			}
			added, err := storage.NewNodeRepo(db).GetByID(context.Background(), extract.NodeUUID([]string{"new-member"}))
			if err != nil || added == nil || added.Description != "Only after isolation." {
				t.Fatalf("independent qualified machine addition lost: %+v %v", added, err)
			}
			selected, err := storage.NewNodeSourceRepo(db).ListByNode(context.Background(), added.ID)
			if err != nil || len(selected) != 1 || selected[0].MemberID != "new-member" || selected[0].Skill != "fixture" || selected[0].FilePath != "references/new.md" {
				t.Fatalf("new machine source qualification lost: %+v %v", selected, err)
			}
			stale, _ := storage.NewNodeRepo(db).GetByID(context.Background(), "stale")
			if stale != nil {
				t.Fatal("stale in-scope machine object retained")
			}
			var staleManifest int
			db.Conn().QueryRow(`SELECT count(*) FROM kg_manifest WHERE key='previous'`).Scan(&staleManifest)
			db.Close()
			if staleManifest != 0 {
				t.Fatal("stale manifest resurrected")
			}
			if got := fullHumanRows(t, path, edgeQuery); !strings.Contains(got, beforeEdges) {
				t.Fatalf("protected full edge rows changed:\nbefore=%s\nafter=%s", beforeEdges, got)
			}
			if got := fullHumanRows(t, path, pumpQuery); got != beforePump {
				t.Fatalf("curated complete node row changed: %s -> %s", beforePump, got)
			}
			if got := fullHumanRows(t, path, cleanQuery); got != beforeClean {
				t.Fatalf("unrelated clean object changed: %s -> %s", beforeClean, got)
			}
			md, err := os.ReadFile(filepath.Join(root, "references/pump.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(md), pump.Description) || strings.Contains(string(md), "Generated replacement") {
				t.Fatalf("writeback disagrees with protected persisted payload: %s", md)
			}
			sidecar, err := writeback.ReadSidecar(filepath.Join(root, "references/pump.md"))
			if err != nil {
				t.Fatal(err)
			}
			if len(sidecar.Nodes) != 1 || sidecar.Nodes[0].UUID != fullHumanPump || sidecar.Nodes[0].Provenance != "human_curated" {
				t.Fatalf("sidecar identity/provenance mismatch: %+v", sidecar)
			}

			if diff := incremental.DiffDoc("references/pump.md", sidecar, string(md), false); diff.HasChanges() {
				t.Fatalf("next unchanged diff is not a no-op: %+v", diff)
			}
			persisted, err := storage.OpenExisting(path)
			if err != nil {
				t.Fatal(err)
			}
			actualEdges, err := storage.NewEdgeRepo(persisted).ListAll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			expected := []writeback.NodeUpdate{{UUID: fullHumanPump, Tag: "Entity", Name: "Pump", Domain: "D", Subdomain: "S", DomainSlug: "d", SubdomainSlug: "s", Summary: "Human pump summary", Description: "Human pump detail: maintenance forbids automatic restart.", Members: []string{"pump-member"}, SourceFiles: []string{"references/pump.md"}, Provenance: "human_curated", Sources: []writeback.SourceReference{{MemberID: "pump-member", Skill: "fixture", FilePath: "references/pump.md", CoordinateSpace: "source_document"}}, Relations: writeback.IncidentRelations(actualEdges, fullHumanPump)}}

			if !writeback.SidecarMetadataMatches(*sidecar, "references/pump.md", expected) {
				t.Fatal("sidecar no longer agrees with authoritative source/member/relation baseline")
			}
			var nodesBefore, edgesBefore int
			persisted.Conn().QueryRow("SELECT count(*) FROM nodes").Scan(&nodesBefore)
			persisted.Conn().QueryRow("SELECT count(*) FROM edges").Scan(&edgesBefore)
			persisted.Close()
			repeatReport, repeatErr := o.MaterializeCompletedExtraction(context.Background(), root, path, res, docs, nil)
			if collision {
				if repeatErr == nil {
					t.Fatal("replayed human collision falsely complete")
				}
			} else {
				requireFullHumanSemanticUnverified(t, repeatReport, repeatErr)
			}
			repeated, err := storage.OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			var nodesAfter, edgesAfter int
			repeated.Conn().QueryRow("SELECT count(*) FROM nodes").Scan(&nodesAfter)
			repeated.Conn().QueryRow("SELECT count(*) FROM edges").Scan(&edgesAfter)
			repeated.Close()
			if nodesAfter != nodesBefore || edgesAfter != edgesBefore {
				t.Fatalf("repeat grew duplicate graph: nodes %d->%d edges %d->%d", nodesBefore, nodesAfter, edgesBefore, edgesAfter)
			}
		})
	}
}

// Protection cannot turn unrelated or ambiguous new machine meaning into verified human coverage.
func TestV016FullHumanIdentityConflictsRetainPriorState(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"new-material", "unmatched-paraphrase", "ambiguous-ownership", "dangling-ownership", "taxonomy-change", "repository-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			root, path, res, docs := fullHumanFixture(t)
			switch mode {
			case "taxonomy-change":
				res.Domains[0].Slug = "changed-domain"
				res.Domains[0].Subdomains[0].Slug = "changed-subdomain"
			case "new-material":
				res.Domains[0].Subdomains[0].Entities[0].Members = append(res.Domains[0].Subdomains[0].Entities[0].Members, "new-member")
				res.Domains[0].Subdomains[0].Concepts = nil
			case "unmatched-paraphrase":
				docs[0].Items[0].ID = "paraphrased-pump-member"
				res.Domains[0].Subdomains[0].Entities[0].Members = []string{"paraphrased-pump-member"}
			case "dangling-ownership":
				db, err := storage.OpenExisting(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := storage.NewNodeSourceRepo(db).InsertBatch(context.Background(), []storage.NodeSource{{NodeUUID: "missing-old-owner", MemberID: "new-member", Skill: "fixture", FilePath: "references/new.md"}}); err != nil {
					t.Fatal(err)
				}
				db.Close()
			case "ambiguous-ownership":
				db, err := storage.OpenExisting(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := storage.NewNodeSourceRepo(db).InsertBatch(context.Background(), []storage.NodeSource{{NodeUUID: "stale", MemberID: "pump-member", Skill: "fixture", FilePath: "references/pump.md"}}); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			identity := "local:full-human"
			if mode == "repository-mismatch" {
				identity = "local:another"
			}
			report, err := (&Orchestrator{repositoryIdentity: identity}).MaterializeCompletedExtraction(context.Background(), root, path, res, docs, nil)
			if err == nil {
				t.Fatal("conflict was accepted as complete")
			}
			if mode == "taxonomy-change" && !strings.Contains(err.Error(), "protected taxonomy conflict") {
				t.Fatalf("taxonomy conflict reason lost: %v", err)
			}
			if report.Ingest == nil || report.Ingest.NodesInserted != 0 || report.Ingest.EdgesInserted != 0 || report.Ingest.NodeSourcesInserted != 0 || report.Ingest.NodeSourcesAttempted == 0 || report.Ingest.NodeSourcesSelectedDistinct == 0 {
				t.Fatalf("unpublished candidate reported committed rows or lost selection diagnostics: %+v", report.Ingest)
			}

			report.finish(context.Background(), err)
			if report.Status == "complete" || len(report.Reasons) == 0 {
				t.Fatalf("conflict hidden: %+v", report)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(before) != string(after) {
				t.Fatal("failed identity/material validation published a replacement")
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".cairn-full-*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("private temporary candidate leaked: %v %v", files, err)
			}
		})
	}
}

func TestV016FullRemovesConfirmedAbsentMachineSource(t *testing.T) {
	t.Parallel()
	root, path, res, docs := fullHumanFixture(t)
	if err := os.Remove(filepath.Join(root, "old-skill/references/clean.md")); err != nil {
		t.Fatal(err)
	}
	report, materializeErr := (&Orchestrator{repositoryIdentity: "local:full-human"}).MaterializeCompletedExtraction(context.Background(), root, path, res, docs, nil)
	requireFullHumanSemanticUnverified(t, report, materializeErr)
	db, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node, err := storage.NewNodeRepo(db).GetByID(context.Background(), "clean")
	if err != nil || node != nil {
		t.Fatalf("confirmed removed machine source was retained: %+v %v", node, err)
	}
}

// An edge deletion changes a surviving neighbor's sidecar, but must not overwrite
// pending human Markdown or advance its editable baseline while refreshing metadata.
func TestV016FullRefreshesUntouchedNeighborWithoutOverwritingPendingEdit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root, path, res, docs := fullHumanFixture(t)
	db, err := storage.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.NewEdgeRepo(db).Insert(ctx, &dktypes.Edge{SourceID: "stale", TargetID: "clean", Kind: dktypes.KindReferences, Description: "Old machine dependency", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .8}); err != nil {
		t.Fatal(err)
	}
	actualEdges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	clean := []writeback.NodeUpdate{{UUID: "clean", Tag: "Concept", Name: "Unrelated", Domain: "clean", Subdomain: "clean", DomainSlug: "clean", SubdomainSlug: "clean", Summary: "Clean summary", Description: "Untouched independent content.", Members: []string{"clean-member"}, SourceFiles: []string{"old-skill/references/clean.md"}, SpanStart: 4, SpanEnd: 5, Provenance: "llm_inferred", Sources: []writeback.SourceReference{{MemberID: "clean-member", Skill: "old-skill", FilePath: "old-skill/references/clean.md", StartLine: 4, EndLine: 5, CoordinateSpace: "source_document"}}, Relations: writeback.IncidentRelations(actualEdges, "clean")}}

	const rel = "old-skill/references/clean.md"
	if err := writeback.RewriteDoc(root, rel, clean, time.Now()); err != nil {
		t.Fatal(err)
	}
	old, err := writeback.ReadSidecar(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(md), "Untouched independent content.", "Pending operator correction.", 1)
	if err := os.WriteFile(filepath.Join(root, rel), []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	report, materializeErr := (&Orchestrator{repositoryIdentity: "local:full-human"}).MaterializeCompletedExtraction(ctx, root, path, res, docs, nil)
	requireFullHumanSemanticUnverified(t, report, materializeErr)
	after, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil || string(after) != edited {
		t.Fatal("neighbor metadata refresh overwrote pending human content")
	}
	sf, err := writeback.ReadSidecar(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if sf.DocHash != old.DocHash || sf.Nodes[0].DescriptionHash != old.Nodes[0].DescriptionHash {
		t.Fatal("neighbor refresh advanced pending human baseline")
	}
	if sf.Nodes[0].GraphMetadata == nil || !sf.Nodes[0].GraphMetadata.RelationsKnown || len(sf.Nodes[0].GraphMetadata.Relations) != 0 {
		t.Fatalf("neighbor retains deleted incident edge: %+v", sf.Nodes[0].GraphMetadata)
	}
}

// A surviving shared object keeps only its extant source credit after a known source deletion.
func TestV016FullReconcilesSurvivingUntouchedSourceRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root, path, res, docs := fullHumanFixture(t)
	db, err := storage.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: "clean", MemberID: "removed-member", Skill: "removed-skill", FilePath: "removed/references/gone.md"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`UPDATE nodes SET source_refs='old-skill,removed-skill' WHERE id='clean'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	report, materializeErr := (&Orchestrator{repositoryIdentity: "local:full-human"}).MaterializeCompletedExtraction(ctx, root, path, res, docs, nil)
	if materializeErr == nil || report.Status != "partial" || !strings.Contains(materializeErr.Error(), "retained source metadata unavailable") {
		t.Fatalf("changed retained source without usable file metadata falsely complete: %+v %v", report, materializeErr)
	}
	persisted, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer persisted.Close()
	node, err := storage.NewNodeRepo(persisted).GetByID(ctx, "clean")
	if err != nil || node == nil || node.SourceRefs != "old-skill" || node.Description != "Untouched independent content." {
		t.Fatalf("surviving provenance is stale or content changed: %+v %v", node, err)
	}
	rows, err := storage.NewNodeSourceRepo(persisted).ListByNode(ctx, "clean")
	if err != nil || len(rows) != 1 || rows[0].FilePath != "old-skill/references/clean.md" {
		t.Fatalf("removed source credit survived: %+v %v", rows, err)
	}
}

// No members can mean a successfully empty source, not a failed/unrepresented source.
func TestV016FullDistinguishesAcceptedEmptyFromUnrepresentedSource(t *testing.T) {
	t.Parallel()
	for _, represented := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrepresented-extant", true: "accepted-empty"}[represented], func(t *testing.T) {
			ctx := context.Background()
			root, path, res, docs := fullHumanFixture(t)
			const rel = "old-skill/references/clean.md"
			db, err := storage.OpenExisting(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := storage.NewEdgeRepo(db).Insert(ctx, &dktypes.Edge{SourceID: "old-controller", TargetID: "clean", Kind: dktypes.KindReferences, Description: "Stale machine relation", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .8}); err != nil {
				t.Fatal(err)
			}
			edges, err := storage.NewEdgeRepo(db).ListAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			oldView := []writeback.NodeUpdate{{UUID: "clean", Tag: "Concept", Name: "Unrelated", Domain: "clean", Subdomain: "clean", DomainSlug: "clean", SubdomainSlug: "clean", Summary: "Clean summary", Description: "Untouched independent content.", Members: []string{"clean-member"}, SourceFiles: []string{rel}, SpanStart: 4, SpanEnd: 5, Provenance: "llm_inferred", Sources: []writeback.SourceReference{{MemberID: "clean-member", Skill: "old-skill", FilePath: rel, StartLine: 4, EndLine: 5, CoordinateSpace: "source_document"}}, Relations: writeback.IncidentRelations(edges, "clean")}}
			if err := writeback.RewriteDoc(root, rel, oldView, time.Now()); err != nil {
				t.Fatal(err)
			}
			if represented {
				current := []byte("---\nname: source-with-empty-body\n---\n")
				if err := os.WriteFile(filepath.Join(root, rel), current, 0644); err != nil {
					t.Fatal(err)
				}
				docs = append(docs, &dktypes.AnnotatedDocument{Skill: "old-skill", FilePath: rel})
			}
			before, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			report, materializeErr := (&Orchestrator{repositoryIdentity: "local:full-human"}).MaterializeCompletedExtraction(ctx, root, path, res, docs, nil)
			requireFullHumanSemanticUnverified(t, report, materializeErr)
			persisted, err := storage.OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer persisted.Close()
			node, err := storage.NewNodeRepo(persisted).GetByID(ctx, "clean")
			if err != nil {
				t.Fatal(err)
			}
			if represented && node != nil {
				t.Fatalf("accepted empty source kept stale machine knowledge: %+v", node)
			}
			if !represented && (node == nil || node.Description != "Untouched independent content.") {
				t.Fatalf("unrepresented extant source was erased: %+v", node)
			}
			after, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil || string(before) != string(after) {
				t.Fatal("accepted empty or unrepresented source bytes/frontmatter changed")
			}
			sf, err := writeback.ReadSidecar(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			if represented && len(sf.Nodes) != 0 {
				t.Fatalf("accepted empty source retained stale sidecar member/relation ownership: %+v", sf.Nodes)
			}
			if !represented && (len(sf.Nodes) != 1 || sf.Nodes[0].UUID != "clean" || len(sf.Nodes[0].Members) != 1 || sf.Nodes[0].Members[0] != "clean-member") {
				t.Fatalf("unrepresented source lost sidecar ownership: %+v", sf.Nodes)
			}
		})
	}
}

// composes is also a semantic fact-to-fact relation; it must not masquerade as ancestry.
func TestV016FullDistinguishesHierarchyFromSemanticComposes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root, path, res, docs := fullHumanFixture(t)
	db, err := storage.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []*dktypes.Node{
		{ID: "skill::legacy-skill", Label: dktypes.LabelSkill, Name: "Legacy skill", Summary: "Legacy skill", Domain: "legacy-skill", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1},
		{ID: "domain::legacy", Label: dktypes.LabelDomain, Name: "Legacy", Summary: "Legacy", Domain: "legacy", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1},
		{ID: "subdomain::legacy::old", Label: dktypes.LabelSubdomain, Name: "Old", Summary: "Old", Domain: "legacy", Subdomain: "old", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1},
		{ID: "manual-independent", Label: dktypes.LabelConcept, Name: "Independent human knowledge", Summary: "Operator rule", Description: "Retain independent human knowledge and its real layers.", Domain: "legacy", Subdomain: "old", Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1},
	} {
		if err := storage.NewNodeRepo(db).Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []*dktypes.Edge{
		{SourceID: "old-controller", TargetID: fullHumanPump, Kind: dktypes.KindComposes, Description: "Human semantic composition between facts.", Provenance: dktypes.ProvenanceHumanCurated, Confidence: .9},
		{SourceID: "stale", TargetID: fullHumanPump, Kind: dktypes.KindComposes, Description: "Machine semantic composition, not a layer edge.", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .8},
		{SourceID: "skill::legacy-skill", TargetID: "domain::legacy", Kind: dktypes.KindProvides, Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1},
		{SourceID: "domain::legacy", TargetID: "subdomain::legacy::old", Kind: dktypes.KindComposes, Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1},
		{SourceID: "subdomain::legacy::old", TargetID: "manual-independent", Kind: dktypes.KindComposes, Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1},
	} {
		if err := storage.NewEdgeRepo(db).Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	report, err := (&Orchestrator{repositoryIdentity: "local:full-human"}).MaterializeCompletedExtraction(ctx, root, path, res, docs, nil)
	requireFullHumanSemanticUnverified(t, report, err)
	if report.Ingest.SemanticEdges != 3 || report.Ingest.ComposesEdges != 6 || report.Ingest.ProvidesEdges != 2 {
		t.Fatalf("semantic composes was counted as hierarchy: %+v", report.Ingest)
	}
	current, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	stale, err := storage.NewNodeRepo(current).GetByID(ctx, "stale")
	if err != nil || stale != nil {
		t.Fatalf("machine semantic composes resurrected stale knowledge as an ancestor: %+v %v", stale, err)
	}
	for _, id := range []string{"skill::legacy-skill", "domain::legacy", "subdomain::legacy::old", "manual-independent"} {
		n, err := storage.NewNodeRepo(current).GetByID(ctx, id)
		if err != nil || n == nil {
			t.Fatalf("real retained human hierarchy lost %s: %+v %v", id, n, err)
		}
	}
	edges, err := storage.NewEdgeRepo(current).ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	layers := 0
	for _, e := range edges {
		if e.SourceID == "stale" || e.TargetID == "stale" {
			t.Fatalf("stale machine semantic edge retained: %+v", e)
		}
		if e.SourceID == "skill::legacy-skill" || e.SourceID == "domain::legacy" || e.SourceID == "subdomain::legacy::old" {
			layers++
		}
	}
	if layers != 3 {
		t.Fatalf("actual hierarchy edges lost: %d", layers)
	}
}

func TestV016FullAllowsFreshAndUnprotectedClassification(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fresh", "unprotected-existing"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root, path, res, docs := fullHumanFixture(t)
			expected := extract.NodeUUID([]string{"new-member"})
			if mode == "fresh" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				res.Domains[0].Slug = "changed-domain"
				res.Domains[0].Subdomains[0].Slug = "changed-subdomain"
			} else {
				expected = "old-unprotected-machine"
				db, err := storage.OpenExisting(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := storage.NewNodeRepo(db).Insert(ctx, &dktypes.Node{ID: expected, Label: dktypes.LabelConcept, Name: "New condition", Summary: "Old classification", Description: "Only after isolation.", Domain: "old-classification", Subdomain: "old", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .9}); err != nil {
					t.Fatal(err)
				}
				if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: expected, MemberID: "new-member", Skill: "fixture", FilePath: "references/new.md"}}); err != nil {
					t.Fatal(err)
				}
				db.Close()
				concept := res.Domains[0].Subdomains[0].Concepts[0]
				res.Domains[0].Subdomains[0].Concepts = nil
				res.Domains = append(res.Domains, extract.Domain{Slug: "changed-domain", Name: "Changed", SourceSkills: []string{"fixture"}, Subdomains: []extract.Subdomain{{Slug: "changed-subdomain", Name: "Changed subdomain", Concepts: []extract.Node{concept}}}})
			}
			report, materializeErr := (&Orchestrator{repositoryIdentity: "local:full-human"}).MaterializeCompletedExtraction(ctx, root, path, res, docs, nil)
			if mode == "fresh" {
				if materializeErr != nil {
					t.Fatal(materializeErr)
				}
			} else {
				requireFullHumanSemanticUnverified(t, report, materializeErr)
			}
			db, err := storage.OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			node, err := storage.NewNodeRepo(db).GetByID(ctx, expected)
			if err != nil || node == nil || node.Domain != "changed-domain" || node.Subdomain != "changed-subdomain" {
				t.Fatalf("unprotected/fresh classification was blocked or lost: %+v %v", node, err)
			}
		})
	}
}
