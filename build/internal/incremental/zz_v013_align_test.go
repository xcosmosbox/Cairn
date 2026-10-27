package incremental

import (
	"context"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// 首轮成功的单元不可因兄弟 alias 错误被重做；最终来源计数由实际行重算。
func TestV013AlignTargetedAliasRecoveryPinsUUIDAndAllSources(t *testing.T) {
	t.Parallel()
	c := &alignValidationSequence{responses: []string{`{"decisions":[{"unit":"a","action":"merge_into","target":"cache#1"},{"unit":"b","action":"merge_into","target":"invented"}]}`, `{"decisions":[{"unit":"b","action":"merge_into","target":"cache#1"}]}`}}
	a := &aligner{client: c, maxRetries: 1}
	units := []*alignUnit{{ID: "a", Content: "water", Detail: "only above2bar", FilePath: "one/a"}, {ID: "b", Content: "water", Detail: "never after2024", FilePath: "two/b"}}
	book := alignValidationBook()
	ps := &promptSections{Nodes: "cache#1 pump"}
	rs := newRunState()
	decisions, groups, rpt, err := a.judge(context.Background(), units, ps, book, rs)
	if err != nil || rpt.Outcome != "complete" || len(rpt.Attempts) != 2 || len(decisions) != 2 || strings.Contains(c.requests[1].User, "unit_id=a") || !strings.Contains(c.requests[1].User, "never after2024") {
		t.Fatalf("invalid recovery: %+v %v", rpt, err)
	}
	sources := map[string][]*alignUnit{"a": {units[0], {ID: "a", Skill: "s", FilePath: "third/a"}}, "b": {units[1]}}
	out := &alignOutcome{pending: map[string]*pendingNode{}, unitsByID: map[string]*alignUnit{"a": units[0], "b": units[1]}, sourcesByID: sources, report: rpt}
	a.materialize(context.Background(), out, decisions, groups, book, sources, rs)
	reconcileAlignCoverage(out, units, sources)
	if len(out.pending) != 1 || out.pending["node-entity"].UUID != "node-entity" || out.pending["node-entity"].IsNew || out.report.CoveredIncidences != 3 || len(out.sourceRows) != 3 {
		t.Fatalf("identity/source loss: %+v", out)
	}
}

func TestV013FlattenDoesNotMutateSourceSpecificMaterial(t *testing.T) {
	t.Parallel()
	docs := []*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a", Items: []dktypes.AnnotatedItem{{ID: "legacy", Content: "required", Detail: "enabled"}}}, {Skill: "s", FilePath: "b", Items: []dktypes.AnnotatedItem{{ID: "legacy", Content: "not required", Detail: "not enabled"}}}}
	units, sources := flattenUnits(docs)
	if len(units) != 1 || sources["legacy"][0].Content != "required" || sources["legacy"][0].Detail != "enabled" || !strings.Contains(units[0].Content, "required\n\nnot required") {
		t.Fatal("source content was mutated or substring-deduplicated")
	}
}

func TestV013AlignExistingMachineDetailAndHumanPrivacy(t *testing.T) {
	t.Parallel()
	_, ps := buildAliases([]*dktypes.Node{{ID: "private", Label: dktypes.LabelEntity, Name: "Atlas", Summary: "device", Description: "water pump only2bar"}, {ID: "private-human", Label: dktypes.LabelConcept, Name: "human secret name", Summary: "human secret summary", Description: "human secret detail", Provenance: dktypes.ProvenanceHumanCurated}}, nil, nil)
	if !strings.Contains(ps.Nodes, "water pump only2bar") || strings.Contains(ps.Nodes, "human secret") || strings.Contains(ps.Nodes, "private") {
		t.Fatal("existing object distinction/privacy lost")
	}
}

func TestV013AlignRecoveredEquivalentMemberJoinsAcceptedNewGroup(t *testing.T) {
	t.Parallel()
	client := &alignValidationSequence{responses: []string{`{"decisions":[{"unit":"a","action":"fuse","group":"g"}],"groups":[{"group":"g","node_name":"Pump","summary":"only2bar","tag":"entity","target":"sd#1"}]}`, `{"decisions":[{"unit":"b","action":"fuse","group":"attempt1:g"}]}`}}
	a := &aligner{client: client, maxRetries: 1}
	units := []*alignUnit{{ID: "a", Name: "Pump", Content: "only2bar"}, {ID: "b", Name: "Pump", Content: "only2bar"}}
	decisions, groups, rpt, err := a.judge(context.Background(), units, &promptSections{}, alignValidationBook(), newRunState())
	if err != nil || rpt.Outcome != "complete" || len(decisions) != 2 || decisions[0].Group != decisions[1].Group || len(groups) != 1 || !strings.Contains(client.requests[1].User, "attempt1:g") {
		t.Fatalf("recovery split equivalent object: decisions=%+v groups=%+v report=%+v err=%v", decisions, groups, rpt, err)
	}
}

func TestV013RecallCompilesSourcePunctuationAsLiteral(t *testing.T) {
	t.Parallel()
	query := buildRecallQuery(&alignUnit{Name: "Atlas", Content: "The Atlas pump may run only above2 bar."})
	if !strings.Contains(query, `"bar."`) || strings.Contains(query, "bar. OR") {
		t.Fatalf("unquoted FTS punctuation: %s", query)
	}
}
