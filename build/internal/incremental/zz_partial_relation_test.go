package incremental

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Missing I-7 output used to veto every pending node, including independent subdomains.
func TestMissingRelationResultPersistsIndependentSourceAndNode(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "kg.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := newStores(db)
	rs := newRunState()
	goodKey := SubdomainKey{Domain: "engineering", Subdomain: "good"}
	ao := &alignOutcome{pending: map[string]*pendingNode{}, unitsByID: map[string]*alignUnit{}}
	for _, item := range []struct{ id, sub, doc string }{{"bad-node", "bad", "bad.md"}, {"good-node", "good", "good.md"}} {
		ao.pending[item.id] = &pendingNode{UUID: item.id, IsNew: true, Label: "Concept", Name: item.id, Summary: "source summary", Confidence: .69, Domain: "engineering", Subdomain: item.sub, SourceSkills: []string{"skill"}}
		ao.sourceRows = append(ao.sourceRows, storage.NodeSource{NodeUUID: item.id, MemberID: item.id, Skill: "skill", FilePath: item.doc})
		rs.dirty.DirtyNodeFused(item.id, SubdomainKey{Domain: "engineering", Subdomain: item.sub})
	}
	rr := &reflowResult{descriptions: map[string]string{}, relations: map[SubdomainKey][]extract.Relation{goodKey: nil}, keptCross: map[SubdomainKey][]*dktypes.Edge{goodKey: nil}}
	if err := omitFailedRelationNodes(ao, rr, rs, map[string]string{"bad.md": "unabsorbed original"}); err != nil {
		t.Fatal(err)
	}
	if err := applyIncrementalIngest(ctx, st, rs.dirty, ao, rr, rs); err != nil {
		t.Fatal(err)
	}
	good, err := st.nodes.GetByID(ctx, "good-node")
	if err != nil || good == nil || good.Confidence != .69 {
		t.Fatalf("good node lost: %+v %v", good, err)
	}
	bad, err := st.nodes.GetByID(ctx, "bad-node")
	if err != nil || bad != nil {
		t.Fatalf("bad dependency committed: %+v %v", bad, err)
	}
	goodSources, err := st.sources.ListByFile(ctx, "good.md")
	if err != nil || len(goodSources) != 1 {
		t.Fatalf("good source lost: %v %v", goodSources, err)
	}
	badSources, err := st.sources.ListByFile(ctx, "bad.md")
	if err != nil || len(badSources) != 0 {
		t.Fatalf("bad source committed: %v %v", badSources, err)
	}
	if !rs.failedDocsSnapshot()["bad.md"] || rs.failedDocsSnapshot()["good.md"] || rs.c4Preserve["bad.md"] != "unabsorbed original" {
		t.Fatal("failure escaped actual dependency scope")
	}
}
