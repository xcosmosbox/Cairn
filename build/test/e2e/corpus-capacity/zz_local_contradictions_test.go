package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
)

func localContradictionAuditFixture(t *testing.T) *crossAuditFixture {
	t.Helper()
	f := newCrossAuditFixture(t)
	for _, graph := range []*extract.Result{f.before, f.after, f.final} {
		d := &graph.Domains[0]
		d.Subdomains[0].Concepts = append(d.Subdomains[0].Concepts, d.Subdomains[1].Concepts...)
		d.Subdomains[0].Relations[0].Kind = "contradicts"
		d.Subdomains = d.Subdomains[:1]
	}
	f.edges = map[string]bool{edgeIdentity(extract.NodeUUID([]string{"m0"}), extract.NodeUUID([]string{"m1"}), "contradicts"): true}
	f.report.ValidationPolicy = extract.CrossRelationPolicyV2
	d := &f.report.Decisions[0]
	d.Kind, d.Scope, d.Relation.Kind = "contradicts", extract.CrossRelationScopeLocal, "contradicts"
	d.Target.Subdomain = "a"
	// The offline auditor independently checks wire data, not model semantics.
	// Supply the same explicit typed predicates that the production verifier requires.
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["verifier"].(map[string]any)["contradiction"] = map[string]bool{"same_subject": true, "same_attribute": true, "same_time": true, "same_conditions": true, "incompatible": true}
	raw, _ = json.Marshal(fields)
	if err := json.Unmarshal(raw, d); err != nil {
		t.Fatal(err)
	}
	return f
}

// 原故障：本域矛盾从未进入候选分母，空 cross 报告可被误读为已经验证该边。
func TestLocalContradictionAuditRequiresCompleteV2Denominator(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"accepted", "missing-decision", "missing-before", "missing-after", "missing-candidate", "missing-db", "wrong-scope", "wrong-kind", "unknown-policy"} {
		t.Run(tc, func(t *testing.T) {
			f := localContradictionAuditFixture(t)
			switch tc {
			case "missing-decision":
				f.report.Candidates, f.report.Accepted, f.report.Decisions = 0, 0, nil
			case "missing-before":
				f.before.Domains[0].Subdomains[0].Relations = nil
			case "missing-after":
				f.after.Domains[0].Subdomains[0].Relations = nil
			case "missing-candidate":
				f.final.Domains[0].Subdomains[0].Relations = nil
			case "missing-db":
				f.edges = map[string]bool{}
			case "wrong-scope":
				f.report.Decisions[0].Scope = extract.CrossRelationScopeCross
			case "wrong-kind":
				f.report.Decisions[0].Kind = "triggers"
			case "unknown-policy":
				f.report.ValidationPolicy = "cross-subdomain-and-local-contradicts/v999"
			}
			f.write(t)
			r := f.audit()
			if tc == "accepted" {
				if len(r.Errors) != 0 || r.Supported != 1 || r.Materialized != 1 || r.ScopeCounts[extract.CrossRelationScopeLocal] != 1 || r.KindCounts["contradicts"] != 1 || r.ValidationPolicy != extract.CrossRelationPolicyV2 {
					t.Fatalf("valid local decision not fully audited: %+v", r)
				}
			} else if len(r.Errors) == 0 {
				t.Fatalf("incomplete or mislabeled local evidence passed: %+v", r)
			}
		})
	}
}

// 安全拒绝不获支持信用，而且拒绝身份不能残留于 validated/candidate/DB 任一层。
func TestLocalContradictionAuditRejectsRetainedRejectedIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"fully-removed", "retained-after", "retained-candidate", "retained-db"} {
		t.Run(tc, func(t *testing.T) {
			f := localContradictionAuditFixture(t)
			f.report.Accepted = 0
			f.report.Decisions[0].Disposition = "rejected_semantic"
			if tc != "retained-after" {
				f.after.Domains[0].Subdomains[0].Relations = nil
			}
			if tc != "retained-candidate" {
				f.final.Domains[0].Subdomains[0].Relations = nil
			}
			if tc != "retained-db" {
				f.edges = map[string]bool{}
			}
			f.write(t)
			r := f.audit()
			if r.Supported != 0 || r.SafeRejected != 1 {
				t.Fatalf("rejection got support credit: %+v", r)
			}
			if tc == "fully-removed" && len(r.Errors) != 0 {
				t.Fatalf("safe removal failed: %+v", r)
			}
			if tc != "fully-removed" && len(r.Errors) == 0 {
				t.Fatalf("rejected local identity persisted undetected: %+v", r)
			}
		})
	}
}

// 历史 JSON 不被回写或追认；即使本域矛盾仍在图中，v1 也明确只有跨域证据。
func TestLegacyRelationAuditPreservesPacketAndDisclosesLocalGap(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"", extract.CrossRelationPolicyV1} {
		t.Run(policy, func(t *testing.T) {
			f := localContradictionAuditFixture(t)
			f.report.ValidationPolicy = policy
			f.report.Candidates, f.report.Accepted, f.report.Decisions = 0, 0, nil
			f.write(t)
			path := filepath.Join(f.out, "stages", "05e_cross_relation_report.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			r := f.audit()
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Errors) != 0 || r.ValidationPolicy != extract.CrossRelationPolicyV1 || r.Candidates != 0 || r.ScopeCounts[extract.CrossRelationScopeLocal] != 0 || !strings.Contains(r.Meaning, "same-subdomain contradictions were not verified") || string(before) != string(after) {
				t.Fatalf("historical v1 packet rewritten or given local support: %+v", r)
			}
		})
	}
}
