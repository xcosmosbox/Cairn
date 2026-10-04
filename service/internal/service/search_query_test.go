package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

func TestPrepareSearchQueryContracts(t *testing.T) {
	rw := NewQueryRewriter(map[string]string{"PM": "Payment"})
	prepared, err := PrepareSearchQuery(context.Background(), rw, " PM\tOR foo.bar ", "", "")
	if err != nil || prepared.RewrittenText != "Payment OR foo.bar" || prepared.MatchExpression != `"Payment" AND "OR" AND "foo.bar"` || prepared.Syntax != dktypes.QuerySyntaxText || prepared.TextProfile != storage.FTSTextLiteral {
		t.Fatalf("incorrect default plain query: %+v %v", prepared, err)
	}
	advanced := " PM OR foo* "
	prepared, err = PrepareSearchQuery(context.Background(), rw, advanced, dktypes.QuerySyntaxFTS5, storage.FTSTextLiteral)
	if err != nil || prepared.MatchExpression != advanced || prepared.RewrittenText != advanced {
		t.Fatalf("advanced syntax rewritten: %+v %v", prepared, err)
	}
	if _, err := PrepareSearchQuery(context.Background(), rw, "alpha", "guess", ""); err == nil {
		t.Fatal("invalid syntax accepted")
	}
	if _, err := PrepareSearchQuery(context.Background(), rw, "alpha", "", "guess"); err == nil {
		t.Fatal("invalid profile accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PrepareSearchQuery(ctx, rw, "alpha", "", ""); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestKnowledgeSearchAndImpactUsePlainText(t *testing.T) {
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "query.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, n := range []*dktypes.Node{
		{ID: "a", Name: "net.ops-worker", Domain: "allowed", Label: dktypes.LabelEntity},
		{ID: "b", Name: "other", Domain: "blocked", Label: dktypes.LabelEntity},
	} {
		n.Confidence, n.Provenance = 1, dktypes.ProvenanceExtraction
		if err := storage.NewNodeRepo(db).Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.NewFTSIndex(db).RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	svc := NewKnowledgeService(db, nil, nil)
	result, err := svc.Search(ctx, "net.ops-worker", SearchOptions{Scope: []string{"allowed"}})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Node.ID != "a" || result.MatchExpression != `"net.ops-worker"` {
		t.Fatalf("plain Search failed: %+v %v", result, err)
	}
	impact, err := svc.Impact(ctx, "net.ops-worker", ImpactOptions{Scope: []string{"allowed"}})
	if err != nil || len(impact.StartNodes) != 1 || impact.StartNodes[0].ID != "a" {
		t.Fatalf("plain Impact failed: %+v %v", impact, err)
	}
	result, err = svc.Search(ctx, "net* OR other", SearchOptions{QuerySyntax: dktypes.QuerySyntaxFTS5})
	if err != nil || len(result.Hits) != 2 {
		t.Fatalf("advanced Search failed: %+v %v", result, err)
	}
	if _, err := svc.Search(ctx, `"`, SearchOptions{QuerySyntax: dktypes.QuerySyntaxFTS5}); err == nil {
		t.Fatal("advanced error hidden")
	}
	result, err = svc.Search(ctx, `"`, SearchOptions{})
	if err != nil || len(result.Hits) != 0 {
		t.Fatalf("literal quote should be zero hits: %+v %v", result, err)
	}
}
