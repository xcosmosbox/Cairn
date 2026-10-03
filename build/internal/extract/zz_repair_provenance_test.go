package extract

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

func repairProvenanceFixture() (*Result, []*dktypes.AnnotatedDocument) {
	return &Result{Domains: []Domain{{Name: "domain", Slug: "domain", Subdomains: []Subdomain{{
			Name: "subdomain", Slug: "subdomain",
			Entities:  []Node{{ID: "existing", Name: "Existing", Members: []string{"skill-entity-existing"}}},
			Relations: []Relation{{Source: "missing", Target: "existing", Kind: string(dktypes.KindReferences)}},
		}}}}}, []*dktypes.AnnotatedDocument{{Skill: "skill", FilePath: "references/source.md", Items: []dktypes.AnnotatedItem{
			{ID: "skill-entity-existing", Tag: dktypes.TagEntity, Name: "Existing", Detail: "Original source"},
			{ID: "skill-entity-missing", Tag: dktypes.TagEntity, Name: "Missing", Detail: "Missing source"},
		}}}
}

func repairAddPatches(members []string) []patch {
	return []patch{
		{Action: "add_entity", DomainSlug: "domain", SubdomainSlug: "subdomain", Node: patchNode{
			ID: "missing", Name: "Missing", Members: members, SourceSkills: []string{"fabricated-skill"},
		}},
		{Action: "restore_relation", DomainSlug: "domain", SubdomainSlug: "subdomain", Relation: Relation{
			Source: "missing", Target: "existing", Kind: string(dktypes.KindReferences),
		}},
	}
}

func repairMock(t *testing.T, patches []patch) *llm.MockClient {
	t.Helper()
	data, err := json.Marshal(patchSet{Patches: patches})
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewMockClient()
	client.Response = &llm.CompleteResponse{Text: string(data), FinishReason: "stop"}
	return client
}

// The real DeepSeek run returned skill names as members, bypassing coverage's
// earlier validation. Such nodes had neither details nor source ledger entries.
func TestRepairRejectsUnverifiableMembers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []string
	}{
		{"empty", nil},
		{"skill-name-is-not-unit-id", []string{"skill"}},
		{"partly-valid", []string{"skill-entity-missing", "fabricated-unit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, docs := repairProvenanceFixture()
			client := repairMock(t, repairAddPatches(tc.members))
			rpt := NewExtractor(client, 4096).RepairDanglingRelations(context.Background(), res, docs)
			if !rpt.Degraded || rpt.Rounds != maxRepairRounds || rpt.PatchesApplied != 0 || len(rpt.PatchErrors) == 0 {
				t.Fatalf("invalid repair reported success: %+v", rpt)
			}
			sd := res.Domains[0].Subdomains[0]
			if len(sd.Entities) != 1 || len(sd.Relations) != 0 {
				t.Fatalf("unverifiable node or dangling relation survived: %+v", sd)
			}
			if !reflect.DeepEqual(sd.Entities[0].Members, []string{"skill-entity-existing"}) ||
				!reflect.DeepEqual(sd.Entities[0].SourceSkills, []string{"skill"}) {
				t.Fatalf("existing provenance lost: %+v", sd.Entities[0])
			}
		})
	}
}

func TestRepairPreservesVerifiedMembersForSourceLedger(t *testing.T) {
	res, docs := repairProvenanceFixture()
	client := repairMock(t, repairAddPatches([]string{"skill-entity-missing", "skill-entity-missing"}))
	rpt := NewExtractor(client, 4096).RepairDanglingRelations(context.Background(), res, docs)
	if rpt.Degraded || rpt.Rounds != 1 || rpt.PatchesApplied != 2 || len(rpt.PatchErrors) != 0 {
		t.Fatalf("valid repair failed: %+v", rpt)
	}
	sd := res.Domains[0].Subdomains[0]
	if len(sd.Entities) != 2 || len(sd.Relations) != 1 {
		t.Fatalf("valid repair was lost: %+v", sd)
	}
	n := sd.Entities[1]
	if !reflect.DeepEqual(n.Members, []string{"skill-entity-missing"}) ||
		!reflect.DeepEqual(n.SourceSkills, []string{"skill"}) {
		t.Fatalf("source must come from verified members, not model claims: %+v", n)
	}
}

func TestRepairRetriesDoNotDuplicateDeclaredNodes(t *testing.T) {
	res, docs := repairProvenanceFixture()
	patches := repairAddPatches([]string{"skill-entity-missing"})[:1]
	if count, errs := applyPatches(res.Domains, patches, collectUnitIDs(docs), nil); count != 1 || len(errs) != 0 {
		t.Fatalf("first add: count=%d errors=%v", count, errs)
	}
	if count, errs := applyPatches(res.Domains, patches, collectUnitIDs(docs), nil); count != 0 || len(errs) != 1 {
		t.Fatalf("repeated add accepted: count=%d errors=%v", count, errs)
	}
	if len(res.Domains[0].Subdomains[0].Entities) != 2 {
		t.Fatal("retry duplicated node identity")
	}
}

func TestRepairFixRestoresCleanedRelation(t *testing.T) {
	res, docs := repairProvenanceFixture()
	sd := &res.Domains[0].Subdomains[0]
	sd.Entities = append(sd.Entities, Node{ID: "actual", Name: "Actual", Members: []string{"skill-entity-missing"}})
	sd.Relations[0].Description = "original evidence"
	client := repairMock(t, []patch{{Action: "fix_relation", DomainSlug: "domain", SubdomainSlug: "subdomain",
		OriginalSource: "missing", OriginalTarget: "existing", Kind: string(dktypes.KindReferences), FixedSource: "actual"}})
	rpt := NewExtractor(client, 4096).RepairDanglingRelations(context.Background(), res, docs)
	if rpt.Degraded || rpt.PatchesApplied != 1 || len(sd.Relations) != 1 {
		t.Fatalf("fix could not restore cleaned edge: report=%+v relations=%+v", rpt, sd.Relations)
	}
	if r := sd.Relations[0]; r.Source != "actual" || r.Target != "existing" || r.Description != "original evidence" {
		t.Fatalf("fix lost original evidence: %+v", r)
	}
	prompt := client.Requests[0].User
	for _, expected := range []string{"node id=actual", `members=["skill-entity-missing"]`, "所有 04 输入单元均已被覆盖"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("repair prompt omitted %q", expected)
		}
	}
}

func TestRepairFixRejectsUnknownEndpointWithoutMutation(t *testing.T) {
	res, docs := repairProvenanceFixture()
	sd := &res.Domains[0].Subdomains[0]
	original := sd.Relations[0]
	patches := []patch{{Action: "fix_relation", DomainSlug: "domain", SubdomainSlug: "subdomain",
		OriginalSource: original.Source, OriginalTarget: original.Target, Kind: original.Kind, FixedSource: "still-missing"}}
	count, errs := applyPatches(res.Domains, patches, collectUnitIDs(docs), nil)
	if count != 0 || len(errs) == 0 || !reflect.DeepEqual(sd.Relations[0], original) {
		t.Fatalf("invalid replacement changed original: count=%d errors=%v relations=%+v", count, errs, sd.Relations)
	}
}
