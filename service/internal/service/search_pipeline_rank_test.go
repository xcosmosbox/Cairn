package service

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Real SQLite ranks must survive the FTS -> BFS -> scorer boundary. IDs are
// deliberately ordered opposite to relevance, so zero-score ID tie breaking
// cannot accidentally make this test pass. Passing raw negative ranks also
// fails: it would place the graph-only neighbor above both textual matches.
func TestSearchPipelinePreservesSQLiteTextRelevance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "ranking.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	nodes := []*dktypes.Node{
		{ID: "z-strong", Name: "strong", Description: "rankprobe rankprobe rankprobe rankprobe"},
		{ID: "y-weak", Name: "weak", Description: "rankprobe " + strings.Repeat("filler ", 100)},
		{ID: "a-neighbor", Name: "neighbor", Description: "related graph context"},
	}
	for _, node := range nodes {
		node.Label = dktypes.LabelConcept
		node.Domain = "test"
		node.Subdomain = "ranking"
		node.Confidence = 1
		node.Provenance = dktypes.ProvenanceExtraction
		if err := storage.NewNodeRepo(db).Insert(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.NewEdgeRepo(db).Insert(ctx, &dktypes.Edge{
		SourceID: "z-strong", TargetID: "a-neighbor", Kind: dktypes.KindReferences,
		Confidence: 1, Provenance: dktypes.ProvenanceExtraction,
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewFTSIndex(db).RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}

	pipeline := NewSearchPipeline(db, NewQueryRewriter(nil), &ServiceConfig{
		MaxSearchResults: 3, MaxTraversalNodes: 10, MaxBFSDepth: 1,
		DefaultMinConfidence: 0.7, BM25Weight: 0.6, GraphWeight: 0.4,
	})
	result, err := pipeline.Execute(ctx, &dktypes.QueryRequest{Query: "rankprobe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FTS5Hits) != 2 || result.FTS5Hits[0].Node.ID != "z-strong" || result.FTS5Hits[1].Node.ID != "y-weak" {
		t.Fatalf("unexpected SQLite hit order: %+v", result.FTS5Hits)
	}
	strongRank, weakRank := result.FTS5Hits[0].BM25Rank, result.FTS5Hits[1].BM25Rank
	if !(strongRank < weakRank && weakRank < 0) {
		t.Fatalf("fixture must produce distinct negative raw ranks: strong=%g weak=%g", strongRank, weakRank)
	}
	if len(result.TraversalResult.Nodes) != 3 || len(result.TraversalResult.Edges) != 1 {
		t.Fatalf("graph expansion was lost: %+v", result.TraversalResult)
	}
	scores := result.TraversalResult.FTS5Hits
	if len(scores) != 2 || scores["z-strong"] != 1 || !(scores["y-weak"] > 0 && scores["y-weak"] < 1) {
		t.Fatalf("raw ranks were not normalized and transferred: %+v", scores)
	}
	if _, ok := scores["a-neighbor"]; ok {
		t.Fatal("graph-only neighbor was given a text score")
	}
	wantOrder := []string{"z-strong", "y-weak", "a-neighbor"}
	if len(result.ScoredResults) != len(wantOrder) {
		t.Fatalf("got %d ranked nodes, want %d", len(result.ScoredResults), len(wantOrder))
	}
	for i, want := range wantOrder {
		node := result.ScoredResults[i]
		if node.Node.ID != want {
			t.Errorf("rank %d: got %s, want %s", i, node.Node.ID, want)
		}
		if node.BM25Score != scores[want] || node.GraphScore != 1 {
			t.Errorf("rank %d changed score contracts: %+v", i, node)
		}
		if i > 0 && !(result.ScoredResults[i-1].FinalScore > node.FinalScore) {
			t.Errorf("rank %d should be strictly less relevant than previous node", i)
		}
	}
	if result.TraversalResult.Depths["z-strong"] != 0 || result.TraversalResult.Depths["a-neighbor"] != 1 {
		t.Fatal("ranking changed traversal depths")
	}

	empty, err := pipeline.Execute(ctx, &dktypes.QueryRequest{Query: "absentterm"})
	if err != nil || len(empty.FTS5Hits) != 0 || len(empty.ScoredResults) != 0 {
		t.Fatalf("zero-hit query changed behavior: result=%+v error=%v", empty, err)
	}
}

func TestNormalizeFTS5Ranks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		ranks []float64
		want  []float64
	}{
		{name: "negative", ranks: []float64{-4, -1}, want: []float64{1, 0.25}},
		{name: "same ratios small scale", ranks: []float64{-4e-6, -1e-6}, want: []float64{1, 0.25}},
		{name: "single", ranks: []float64{-1e-6}, want: []float64{1}},
		{name: "tied", ranks: []float64{-2, -2}, want: []float64{1, 1}},
		{name: "zero tied", ranks: []float64{0, 0}, want: []float64{1, 1}},
		{name: "zero with nonzero", ranks: []float64{-2, 0}, want: []float64{1, 0}},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := []*storage.FTS5Hit{nil, {Node: nil}}
			for i, rank := range tc.ranks {
				hits = append(hits, &storage.FTS5Hit{Node: &dktypes.Node{ID: string(rune('a' + i))}, BM25Rank: rank})
			}
			scores, err := normalizeFTS5Ranks(hits)
			if err != nil {
				t.Fatal(err)
			}
			if len(scores) != len(tc.want) {
				t.Fatalf("got %d scores, want %d", len(scores), len(tc.want))
			}
			for i, want := range tc.want {
				if got := scores[string(rune('a'+i))]; got != want {
					t.Errorf("score %d: got %g want %g", i, got, want)
				}
				if hits[i+2].BM25Rank != tc.ranks[i] {
					t.Fatal("normalization overwrote diagnostic raw rank")
				}
			}
		})
	}
}

func TestNormalizeFTS5RanksRejectsInvalidNativeRanks(t *testing.T) {
	t.Parallel()
	for _, rank := range []float64{1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		scores, err := normalizeFTS5Ranks([]*storage.FTS5Hit{{Node: &dktypes.Node{ID: "invalid"}, BM25Rank: rank}})
		if err == nil || scores != nil {
			t.Errorf("invalid raw rank %g accepted: scores=%v error=%v", rank, scores, err)
		}
	}
}
