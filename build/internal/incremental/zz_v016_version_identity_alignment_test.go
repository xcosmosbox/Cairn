package incremental

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Scripted placements exercise the real recall/judge/alias/materialize path,
// not a model's ability to establish identity. They also pass the parent prompt.
func TestV016VersionIdentityAlignmentContextAndMaterial(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, oldDetail, batchContext, detail, extraDetail string
		merge                                              bool
	}{
		{
			name:         "same owner and edition across sources",
			oldDetail:    "Atlas ticket January edition has a blue seal.",
			batchContext: "This page describes Atlas ticket and its January edition.",
			detail:       "Atlas ticket January edition is valid for9minutes only when sealed.",
			extraDetail:  "The same Atlas ticket January edition is not valid after closing.", merge: true,
		},
		{
			name:         "explicit shared global release identity",
			oldDetail:    "Global release urn:release:shared-17 is shared by Atlas ticket and Birch counter; this is one release object.",
			batchContext: "Birch counter uses the same shared release object urn:release:shared-17 as Atlas ticket.",
			detail:       "Global release urn:release:shared-17 enables Birch counting only after closing.",
			extraDetail:  "The shared release urn:release:shared-17 excludes admission counting.", merge: true,
		},
		{
			name:         "same month with different owner context",
			oldDetail:    "Birch counter January edition counts4tokens after closing; it is not the Atlas ticket edition.",
			batchContext: "This page concerns Atlas ticket. Its January edition is valid for9minutes.",
			detail:       "January edition is valid for9minutes only when sealed.",
			extraDetail:  "Its validity ends at closing; this source concerns Atlas ticket.",
		},
		{
			name:         "unproven owner remains unresolved identity",
			oldDetail:    "Birch counter January edition counts4tokens after closing.",
			batchContext: "The note mentions a January edition but does not identify its owning object.",
			detail:       "January edition carries a blue seal; the note does not identify its owner.",
			extraDetail:  "No identity with the Birch counter edition is stated in this note.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "kg.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st := newStores(db)
			old := &dktypes.Node{ID: "private-existing-edition", Label: dktypes.LabelConcept,
				Name: "January edition", Summary: tc.oldDetail, Description: tc.oldDetail,
				Domain: "service", Subdomain: "editions", Provenance: dktypes.ProvenanceExtraction}
			for _, n := range []*dktypes.Node{old,
				{ID: "private-sub", Label: dktypes.LabelSubdomain, Name: "版本事项", Domain: "service", Subdomain: "editions", Provenance: dktypes.ProvenanceExtraction},
				{ID: "private-domain", Label: dktypes.LabelDomain, Name: "服务领域", Domain: "service", Provenance: dktypes.ProvenanceExtraction},
			} {
				if err := st.nodes.Insert(ctx, n); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.fts.RebuildAll(ctx); err != nil {
				t.Fatal(err)
			}
			oldRows := []storage.NodeSource{{NodeUUID: old.ID, MemberID: "old-member", Skill: "s", FilePath: "s/old.md"}}
			if err := st.sources.InsertBatch(ctx, oldRows); err != nil {
				t.Fatal(err)
			}
			before, err := st.nodes.GetByID(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeSources, err := st.sources.ListByNode(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			decision := alignDecision{Unit: "edition", Action: "attach", Target: "sd#1", NodeName: "一月版本", Tag: "concept"}
			if tc.merge {
				decision = alignDecision{Unit: "edition", Action: "merge_into", Target: "editions#1"}
			}
			payload, err := json.Marshal(alignLLMOutput{Decisions: []alignDecision{decision,
				{Unit: "context", Action: "attach", Target: "sd#1", NodeName: "来源上下文", Tag: "concept"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			client := &alignValidationSequence{responses: []string{string(payload)}}
			item := dktypes.AnnotatedItem{ID: "edition", Tag: dktypes.TagConcept, Name: "January edition", Content: "January edition", Detail: tc.detail, Confidence: 1}
			extra := item
			extra.Detail = tc.extraDetail
			docs := []*dktypes.AnnotatedDocument{
				{Skill: "s", FilePath: "s/incoming.md", Items: []dktypes.AnnotatedItem{item,
					{ID: "context", Tag: dktypes.TagConcept, Name: "Page context", Content: "Page context", Detail: tc.batchContext, Confidence: 1}}},
				{Skill: "s", FilePath: "s/second.md", Items: []dktypes.AnnotatedItem{extra}},
			}
			a := newAligner(client, 65536, st, 15)
			a.maxRetries = -1
			out, err := a.align(ctx, docs, newRunState())
			if err != nil || out.llFailed || len(client.requests) != 1 || out.report.Outcome != "complete" {
				t.Fatalf("alignment failed: calls=%d out=%+v err=%v", len(client.requests), out, err)
			}
			user := client.requests[0].User
			for _, material := range []string{tc.oldDetail, tc.batchContext, tc.detail, tc.extraDetail, "s/incoming.md", "s/second.md", "alias=editions#1"} {
				if !strings.Contains(user, material) {
					t.Fatalf("missing supplied context %q", material)
				}
			}
			if strings.Contains(user, old.ID) {
				t.Fatal("persistent UUID leaked into prompt")
			}
			var placed *pendingNode
			for _, pn := range out.pending {
				for _, id := range pn.NewMembers {
					if id == "edition" {
						placed = pn
					}
				}
			}
			if placed == nil || len(out.pending) != 2 || placed.IsNew == tc.merge || (placed.UUID == old.ID) != tc.merge {
				t.Fatalf("scripted identity/UUID placement lost: %+v", out.pending)
			}
			if out.report.CoveredIncidences != 3 || len(out.sourceRows) != 3 || len(out.sourcesByID["edition"]) != 2 {
				t.Fatalf("source incidence lost: %+v", out.report)
			}
			for i, want := range []string{tc.detail, tc.extraDetail} {
				if out.sourcesByID["edition"][i].Detail != want {
					t.Fatal("source-specific detail changed")
				}
			}
			for _, row := range out.sourceRows {
				if row.MemberID == "edition" && row.NodeUUID != placed.UUID {
					t.Fatal("source assigned to wrong identity")
				}
			}
			after, err := st.nodes.GetByID(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			afterSources, err := st.sources.ListByNode(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeSources, afterSources) {
				t.Fatal("alignment preparation changed stored old members/source")
			}
		})
	}
}
