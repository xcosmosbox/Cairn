package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/service/internal/service"
)

func fixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	nodes := []*dktypes.Node{
		{ID: "a", Label: dktypes.LabelEntity, Name: "alpha", Summary: "alpha summary", Description: "Source evidence A", Confidence: 1, Provenance: dktypes.ProvenanceExtraction, SourceRefs: "guide.md"},
		{ID: "b", Label: dktypes.LabelConcept, Name: "alpha concept", Summary: "summary", Confidence: 1, Provenance: dktypes.ProvenanceExtraction},
		{ID: "c", Label: dktypes.LabelDomain, Name: "alpha domain", Summary: "domain summary", Confidence: 1, Provenance: dktypes.ProvenanceExtraction},
		{ID: "d", Label: dktypes.LabelEntity, Name: "neighbor", Summary: "Connected evidence", Confidence: 1, Provenance: dktypes.ProvenanceExtraction},
	}
	for _, n := range nodes {
		n.CreatedAt, n.UpdatedAt = time.Now(), time.Now()
	}
	if err := storage.NewNodeRepo(db).InsertBatch(ctx, nodes); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewEdgeRepo(db).Insert(ctx, &dktypes.Edge{SourceID: "a", TargetID: "d", Kind: dktypes.KindDependsOn, Description: "actual stored relationship", Confidence: 1, Provenance: dktypes.ProvenanceExtraction}); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: "a", MemberID: "member-a", Skill: "sample", FilePath: "guide.md", StartLine: 5, EndLine: 8}}); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewFTSIndex(db).RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	return path
}

func invoke(t *testing.T, path, mode, query string, extra ...string) (response, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	args := append([]string{"--db", path, "--mode", mode, "--query", query, "--limit", "10"}, extra...)
	err := run(args, &out, &stderr)
	var res response
	if decodeErr := json.Unmarshal(out.Bytes(), &res); decodeErr != nil {
		t.Fatalf("stdout is not JSON: %s: %v", out.String(), decodeErr)
	}
	return res, err
}

func TestAdapterProductionResultsAndReadOnly(t *testing.T) {
	path := fixtureDB(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fts, err := invoke(t, path, "fts", "  alpha \t")
	if err != nil {
		t.Fatal(err)
	}
	if len(fts.Evidence) != 3 || fts.Diagnostics.RewrittenQuery != "alpha" {
		t.Fatalf("wrong unfiltered FTS results: %+v", fts)
	}
	foundDomain := false
	for _, ev := range fts.Evidence {
		foundDomain = foundDomain || ev.Label == string(dktypes.LabelDomain)
		if len(ev.Edges) != 0 || ev.BM25Rank == nil || ev.Score != -*ev.BM25Rank {
			t.Fatalf("FTS unexpectedly expands graph or changes score: %+v", ev)
		}
	}
	if !foundDomain {
		t.Fatal("FTS baseline excluded a structural seed")
	}
	graph, err := invoke(t, path, "graph", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(fts.Evidence) != len(graph.Diagnostics.FTSSeeds) {
		t.Fatal("FTS and graph do not share the same seed count")
	}
	for i, ev := range fts.Evidence {
		seed := graph.Diagnostics.FTSSeeds[i]
		if ev.ID != seed.ID || ev.Label != seed.Label || *ev.BM25Rank != seed.BM25Rank {
			t.Fatalf("FTS and graph do not share seed %d", i)
		}
	}
	if len(fts.Diagnostics.BypassedStages) != 3 || len(graph.Diagnostics.BypassedStages) != 3 {
		t.Fatal("adapter does not disclose bypassed presentation stages")
	}
	db, err := openDB(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sc, err := service.NewSearchPipeline(db, service.NewQueryRewriter(nil), service.DefaultServiceConfig()).Execute(context.Background(), &dktypes.QueryRequest{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Evidence) != len(sc.ScoredResults) || len(graph.Evidence) != 4 {
		t.Fatalf("adapter did not preserve production graph results: %+v", graph)
	}
	foundSource, foundNeighbor := false, false
	for i, ev := range graph.Evidence {
		if ev.ID != sc.ScoredResults[i].Node.ID || ev.Score != sc.ScoredResults[i].FinalScore {
			t.Fatalf("adapter altered production rank/score at %d", i)
		}
		if ev.ID == "a" {
			foundSource = len(ev.Sources) == 1 && ev.Sources[0].Path == "guide.md" && ev.Sources[0].StartLine == 5 && ev.Sources[0].EndLine == 8
			if len(ev.Edges) != 1 || !strings.Contains(ev.Body, "actual stored relationship") {
				t.Fatal("lost stored graph relationship")
			}
		}
		if ev.ID == "d" {
			foundNeighbor = ev.Depth != nil && *ev.Depth == 1
		}
	}
	if !foundSource || !foundNeighbor {
		t.Fatal("lost source spans or production BFS neighbor")
	}
	if _, err := db.Conn().Exec("DELETE FROM nodes"); err == nil {
		t.Fatal("adapter database connection permits writes")
	}
	db.Close()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("adapter changed database bytes: %v", err)
	}
}

func TestAdapterFailureIsNotZeroMatches(t *testing.T) {
	path := fixtureDB(t)
	for _, mode := range []string{"fts", "graph"} {
		res, err := invoke(t, path, mode, "\"", "--query-syntax", "fts5")
		if err == nil || res.Error == "" || len(res.Evidence) != 0 {
			t.Fatalf("invalid query returned success: %+v, %v", res, err)
		}
		res, err = invoke(t, path, mode, "\"")
		if err != nil || res.Error != "" || len(res.Evidence) != 0 {
			t.Fatalf("literal punctuation must not be a syntax error: %+v, %v", res, err)
		}
		res, err = invoke(t, path, mode, "absenttoken")
		if err != nil || res.Error != "" || len(res.Evidence) != 0 {
			t.Fatalf("valid zero-hit query failed: %+v, %v", res, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	res, err := invoke(t, missing, "fts", "alpha")
	if err == nil || res.Error == "" {
		t.Fatal("missing database succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("adapter created missing database: %v", err)
	}
}

func TestAdapterExplicitQuerySyntax(t *testing.T) {
	path := fixtureDB(t)
	for _, mode := range []string{"fts", "graph"} {
		literal, err := invoke(t, path, mode, "alpha OR neighbor")
		if err != nil || len(literal.Evidence) != 0 || literal.Diagnostics.MatchExpression != `"alpha" AND "OR" AND "neighbor"` {
			t.Fatalf("OR interpreted as plain-text operator: %+v %v", literal, err)
		}
		advanced, err := invoke(t, path, mode, "alpha OR neighbor", "--query-syntax", "fts5")
		if err != nil || len(advanced.Evidence) != 4 || advanced.Diagnostics.MatchExpression != "alpha OR neighbor" || advanced.Diagnostics.QuerySyntax != dktypes.QuerySyntaxFTS5 {
			t.Fatalf("explicit advanced syntax failed: %+v %v", advanced, err)
		}
	}
}

func TestAdapterHanManifestBindingAndSameSeeds(t *testing.T) {
	path := fixtureDB(t)
	db, err := storage.NewDB(storage.DBOptions{Path: path, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := storage.NewNodeRepo(db).Insert(ctx, &dktypes.Node{ID: "han", Name: "权限控制", Description: "原始正文 net.ops-worker", Label: dktypes.LabelConcept, Confidence: 1, Provenance: dktypes.ProvenanceExtraction}); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewFTSIndex(db).RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	// Only the test index text is transformed; graph/source values stay raw.
	name, _ := storage.NormalizeFTSText(storage.FTSTextHanV1, "权限控制")
	description, _ := storage.NormalizeFTSText(storage.FTSTextHanV1, "原始正文 net.ops-worker")
	if _, err := db.Conn().Exec(`UPDATE nodes_fts SET name=?,description=? WHERE rowid=(SELECT rowid FROM nodes WHERE id='han')`, name, description); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]interface{}{
		"format": "cairn-fts-index-copy/v1", "index_profile": "han-v1", "text_profile": "han-v1",
		"source_db_sha256": strings.Repeat("0", 64), "output_db_sha256": fmt.Sprintf("%x", sha256.Sum256(data)),
		"all_non_fts_tables_unchanged": true,
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest := func() {
		t.Helper()
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest()
	if _, err := invoke(t, path, "fts", "权限", "--text-profile", "han-v1"); err == nil {
		t.Fatal("unbound Han index accepted")
	}
	if _, err := invoke(t, path, "fts", "权限", "--index-manifest", manifestPath); err == nil {
		t.Fatal("mismatched query/index profiles accepted")
	}
	args := []string{"--text-profile", "han-v1", "--index-manifest", manifestPath}
	fts, err := invoke(t, path, "fts", "权限", args...)
	if err != nil || len(fts.Evidence) != 1 || fts.Evidence[0].Name != "权限控制" || !strings.Contains(fts.Evidence[0].Body, "原始正文") {
		t.Fatalf("Han index or raw evidence failed: %+v %v", fts, err)
	}
	graph, err := invoke(t, path, "graph", "权限", args...)
	if err != nil || len(graph.Diagnostics.FTSSeeds) != 1 || graph.Diagnostics.FTSSeeds[0] != fts.Diagnostics.FTSSeeds[0] || graph.Diagnostics.MatchExpression != fts.Diagnostics.MatchExpression {
		t.Fatalf("Han seed/compile mismatch: %+v %v", graph, err)
	}
	if fts.Diagnostics.IndexManifestSHA256 == "" || fts.Diagnostics.DatabaseSHA256 == "" || fts.Diagnostics.TextProfile != storage.FTSTextHanV1 {
		t.Fatal("index binding diagnostics missing")
	}
	manifest["output_db_sha256"] = strings.Repeat("1", 64)
	writeManifest()
	if _, err := invoke(t, path, "graph", "权限", args...); err == nil {
		t.Fatal("base DB mismatch accepted")
	}
	manifest["output_db_sha256"] = fmt.Sprintf("%x", sha256.Sum256(data))
	manifest["all_non_fts_tables_unchanged"] = false
	writeManifest()
	if _, err := invoke(t, path, "graph", "权限", args...); err == nil {
		t.Fatal("unverified non-FTS tables accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("adapter mutated experimental index")
	}
}

func TestAdapterManifestRejectsUncheckpointedWAL(t *testing.T) {
	path := fixtureDB(t)
	writer, err := storage.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Conn().Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Conn().Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Conn().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]interface{}{
		"format": "cairn-fts-index-copy/v1", "index_profile": "literal", "text_profile": "literal",
		"source_db_sha256": strings.Repeat("0", 64), "output_db_sha256": fmt.Sprintf("%x", sha256.Sum256(before)),
		"all_non_fts_tables_unchanged": true,
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Conn().Exec(`UPDATE nodes SET description='changed only in WAL' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("test did not isolate uncheckpointed WAL from main DB bytes")
	}
	// Resolve symlinks before checking journals; adjacent symlink sidecars do
	// not describe SQLite's actual database state.
	link := filepath.Join(t.TempDir(), "linked.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, dbPath := range []string{path, link} {
		res, err := invoke(t, dbPath, "fts", "alpha", "--index-manifest", manifestPath)
		if err == nil || !strings.Contains(res.Error, "nonempty -wal") || len(res.Evidence) != 0 {
			t.Fatalf("unbound WAL content accepted: %+v %v", res, err)
		}
	}
}
