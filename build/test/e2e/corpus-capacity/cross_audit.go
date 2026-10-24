package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type crossAuditReport struct {
	Candidates                 int               `json:"candidates"`
	Counts                     map[string]int    `json:"per_disposition"`
	Supported                  int               `json:"model_supported_candidates"`
	SafeRejected               int               `json:"safe_rejected_candidates"`
	Materialized               int               `json:"materialized_supported_candidates"`
	Filtered                   int               `json:"supported_candidates_with_filtered_endpoints"`
	MechanicallyVerifiedQuotes int               `json:"mechanically_verified_quotes"`
	Artifacts                  map[string]string `json:"artifact_sha256"`
	Outcomes                   []map[string]any  `json:"outcomes"`
	Errors                     []string          `json:"errors"`
	Meaning                    string            `json:"meaning"`
}

type scopedNode struct {
	domain, subdomain string
	node              extract.Node
}

func relationKey(domain, subdomain string, r extract.Relation) string {
	raw, _ := json.Marshal(r)
	return domain + "\x00" + subdomain + "\x00" + string(raw)
}
func edgeIdentity(source, target, kind string) string {
	return source + "\x00" + target + "\x00" + kind
}
func memberSet(members []string) map[string]bool {
	r := map[string]bool{}
	for _, m := range members {
		r[m] = true
	}
	return r
}
func scopeKey(domain, subdomain, alias string) string {
	return domain + "\x00" + subdomain + "\x00" + alias
}

func indexAuditNodes(result *extract.Result) (map[string][]scopedNode, map[string][]scopedNode) {
	local, global := map[string][]scopedNode{}, map[string][]scopedNode{}
	for _, d := range result.Domains {
		for _, s := range d.Subdomains {
			for _, group := range [][]extract.Node{s.Entities, s.Concepts} {
				for _, n := range group {
					if n.ID != "" {
						ref := scopedNode{d.Slug, s.Slug, n}
						key := scopeKey(d.Slug, s.Slug, n.ID)
						local[key] = append(local[key], ref)
						global[n.ID] = append(global[n.ID], ref)
					}
				}
			}
		}
	}
	return local, global
}
func resolveAuditEndpoint(local, global map[string][]scopedNode, domain, subdomain, alias string) (scopedNode, bool) {
	matches := local[scopeKey(domain, subdomain, alias)]
	if len(matches) == 0 {
		matches = global[alias]
	}
	if len(matches) != 1 {
		return scopedNode{}, false
	}
	return matches[0], true
}

// Slugs are derived json:"-" fields in production dumps; reconstruct only from
// persisted domain/subdomain names through the existing public deterministic function.
func restoreAuditScopes(result *extract.Result) bool {
	valid := true
	for di := range result.Domains {
		d := &result.Domains[di]
		if strings.TrimSpace(d.Name) == "" {
			valid = false
		}
		d.Slug = extract.Slugify(d.Name)
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			if strings.TrimSpace(sd.Name) == "" {
				valid = false
			}
			sd.Slug = extract.Slugify(sd.Name)
		}
	}
	return valid
}
func actualCrossKeys(result *extract.Result) map[string]int {
	local, _ := indexAuditNodes(result)
	r := map[string]int{}
	for _, d := range result.Domains {
		for _, s := range d.Subdomains {
			for _, rel := range s.Relations {
				if len(local[scopeKey(d.Slug, s.Slug, rel.Source)]) != 1 || len(local[scopeKey(d.Slug, s.Slug, rel.Target)]) != 1 {
					r[relationKey(d.Slug, s.Slug, rel)]++
				}
			}
		}
	}
	return r
}

// auditCrossArtifacts 对 accepted 的原文/成员/最终边做独立机械核对；安全拒绝只记质量问题，不能获支持信用。
// auditCrossArtifacts distinguishes fail-closed rejection from invalid accepted data and unresolved execution.
func auditCrossArtifacts(out string, selected *selection, docs []*dktypes.AnnotatedDocument, candidate *extract.Result, limit int, buildReport *extract.CrossRelationReport, dbEdges map[string]bool) *crossAuditReport {
	r := &crossAuditReport{Counts: map[string]int{}, Artifacts: map[string]string{}, Outcomes: []map[string]any{}, Meaning: "semantic support/rejection is model-judged; original source/quote/member/edge binding is mechanically verified; aggregate readiness does not grant full609 or paid admission"}
	for _, status := range []string{"accepted", "rejected_semantic", "rejected_evidence", "rejected_endpoint", "rejected_relation", "insufficient_evidence", "execution_error", "invalid_verdict"} {
		r.Counts[status] = 0
	}
	fail := func(reason string) { r.Errors = append(r.Errors, reason) }
	var frozen []extract.FrozenSource
	var report extract.CrossRelationReport
	var before, after extract.Result
	for _, item := range []struct {
		path  string
		value any
	}{{"00_frozen_sources.json", &frozen}, {"05e_extract_related.json", &before}, {"05e_cross_relation_report.json", &report}, {"05e_cross_validated.json", &after}} {
		raw, err := os.ReadFile(filepath.Join(out, "stages", item.path))
		if err != nil {
			fail("required cross-verifier artifact missing: " + item.path)
			continue
		}
		r.Artifacts[item.path] = sha(raw)
		if err := json.Unmarshal(raw, item.value); err != nil {
			fail("invalid cross-verifier artifact: " + item.path)
		}
	}
	if len(r.Errors) > 0 {
		return r
	}
	stageBytes, stageErr := json.Marshal(report)
	buildBytes, buildErr := json.Marshal(buildReport)
	if stageErr != nil || buildErr != nil || buildReport == nil || !bytesEqual(stageBytes, buildBytes) {
		fail("build and stage cross-verifier reports differ")
	}
	if report.MaxEvidenceBytes != limit || report.Candidates != len(report.Decisions) {
		fail("cross-verifier capacity or candidate denominator mismatch")
	}
	r.Candidates = report.Candidates
	originals := map[string]extract.FrozenSource{}
	selectedByPath := map[string]selectedDoc{}
	skills := map[string]string{}
	for _, doc := range docs {
		if doc != nil {
			skills[doc.FilePath] = doc.Skill
		}
	}
	for _, s := range selected.Documents {
		selectedByPath[s.Path] = s
	}
	for _, src := range frozen {
		entry, ok := selectedByPath[src.Path]
		if !ok {
			fail("frozen original path is outside the selected corpus")
			continue
		}
		raw, err := os.ReadFile(filepath.Join(out, "original", src.Path))
		if err != nil || originals[src.Path].Path != "" || src.SHA256 != sha(raw) || src.SHA256 != entry.WrapperSHA || src.Content != string(raw) || src.Skill != skills[src.Path] {
			fail("frozen original source identity mismatch")
			continue
		}
		originals[src.Path] = src
	}
	if len(originals) != len(selected.Documents) {
		fail("frozen original document denominator incomplete")
	}
	bindings := map[string]map[string]bool{}
	memberPaths := map[string]map[string]bool{}
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		bindings[doc.FilePath] = map[string]bool{}
		for _, item := range doc.Items {
			bindings[doc.FilePath][item.ID] = true
			if memberPaths[item.ID] == nil {
				memberPaths[item.ID] = map[string]bool{}
			}
			memberPaths[item.ID][doc.FilePath] = true
		}
	}
	if !restoreAuditScopes(&before) || !restoreAuditScopes(&after) {
		fail("persisted graph scope names missing")
	}
	expected, accepted := actualCrossKeys(&before), map[string]int{}
	local, global := indexAuditNodes(&before)
	finalNodes := map[string]bool{}
	finalEdges := map[string]bool{}
	for _, domain := range candidate.Domains {
		for _, sub := range domain.Subdomains {
			for _, group := range [][]extract.Node{sub.Entities, sub.Concepts} {
				for _, node := range group {
					finalNodes[node.ID] = true
				}
			}
			for _, rel := range sub.Relations {
				finalEdges[edgeIdentity(rel.Source, rel.Target, rel.Kind)] = true
			}
		}
	}
	for i, decision := range report.Decisions {
		key := relationKey(decision.OwnerDomain, decision.OwnerSubdomain, decision.Relation)
		if expected[key] < 1 {
			fail("unexpected or duplicate cross-candidate outcome")
		} else {
			expected[key]--
		}
		r.Counts[decision.Disposition]++
		r.Outcomes = append(r.Outcomes, map[string]any{"candidate_index": i, "disposition": decision.Disposition, "reason_sha256": sha([]byte(decision.Reason)), "source_documents": len(decision.Sources), "evidence_bytes": decision.EvidenceBytes, "quoted_originals": len(decision.Evidence)})
		switch decision.Disposition {
		case "rejected_semantic", "rejected_evidence", "rejected_endpoint", "rejected_relation":
			r.SafeRejected++
			continue
		case "insufficient_evidence", "execution_error", "invalid_verdict":
			fail("unresolved cross-verifier capacity/execution outcome: " + decision.Disposition)
			continue
		case "accepted":
			r.Supported++
			accepted[key]++
		default:
			fail("unknown cross-verifier disposition")
			continue
		}
		if strings.TrimSpace(decision.Reason) == "" || decision.EvidenceBytes > limit || decision.EvidenceBytes < 1 || decision.Verifier == nil || decision.Verifier.Disposition != "supported" || strings.TrimSpace(decision.Verifier.Reason) == "" || len(decision.Source.Members) == 0 || len(decision.Target.Members) == 0 || decision.Verifier.RelationSupported == nil || !*decision.Verifier.RelationSupported || decision.Verifier.DirectionSupported == nil || !*decision.Verifier.DirectionSupported {
			fail("accepted candidate lacks explicit bounded semantic verdict")
		}
		for _, endpoint := range []struct {
			alias string
			value extract.CrossEndpoint
		}{{decision.Relation.Source, decision.Source}, {decision.Relation.Target, decision.Target}} {
			ref, ok := resolveAuditEndpoint(local, global, decision.OwnerDomain, decision.OwnerSubdomain, endpoint.alias)
			if !ok || ref.domain != endpoint.value.Domain || ref.subdomain != endpoint.value.Subdomain || ref.node.ID != endpoint.value.Alias || !reflect.DeepEqual(memberSet(ref.node.Members), memberSet(endpoint.value.Members)) {
				fail("accepted endpoint binding differs from pre-verifier graph")
			}
		}
		if decision.Relation.Kind == "contradicts" {
			v := decision.Verifier
			if v == nil || v.Contradiction == nil {
				fail("accepted contradiction lacks context predicates")
			} else {
				p := v.Contradiction
				for _, value := range []*bool{p.SameSubject, p.SameAttribute, p.SameTime, p.SameConditions, p.Incompatible} {
					if value == nil || !*value {
						fail("accepted contradiction predicate missing or false")
					}
				}
			}
		}
		sourceIDs := map[string]extract.CrossSource{}
		sourcePaths := map[string]bool{}
		for _, src := range decision.Sources {
			original, ok := originals[src.Path]
			if !ok || sourceIDs[src.ID].ID != "" || src.ID == "" || src.Skill != original.Skill || src.SHA256 != original.SHA256 {
				fail("accepted candidate source manifest invalid")
			}
			sourceIDs[src.ID] = src
			sourcePaths[src.Path] = true
		}
		for _, endpoint := range []extract.CrossEndpoint{decision.Source, decision.Target} {
			for _, member := range endpoint.Members {
				if len(memberPaths[member]) == 0 {
					fail("accepted endpoint member absent from accepted04 input")
				}
				for path := range memberPaths[member] {
					if !sourcePaths[path] {
						fail("accepted candidate omitted an endpoint member original")
					}
				}
			}
		}
		roles := map[string]bool{}
		for _, quote := range decision.Evidence {
			endpoint := decision.Source
			if quote.Endpoint == "target" {
				endpoint = decision.Target
			} else if quote.Endpoint != "source" {
				fail("accepted evidence role invalid")
			}
			src, ok := sourceIDs[quote.SourceID]
			original := originals[src.Path]
			start, end := quote.StartByte, quote.EndByte
			if !ok || !memberSet(endpoint.Members)[quote.MemberID] || !bindings[src.Path][quote.MemberID] || strings.TrimSpace(quote.Quote) == "" || len(quote.Quote) > 512 || start < 0 || end <= start || end > len(original.Content) {
				fail("accepted quote ownership or bounds invalid")
				continue
			}
			if original.Content[start:end] != quote.Quote || quote.StartLine != strings.Count(original.Content[:start], "\n")+1 || quote.EndLine != strings.Count(original.Content[:end-1], "\n")+1 {
				fail("accepted quote differs from original bytes/positions")
				continue
			}
			roles[quote.Endpoint] = true
			r.MechanicallyVerifiedQuotes++
		}
		if !roles["source"] || !roles["target"] {
			fail("accepted evidence missing an endpoint")
		}
		source, target := extract.NodeUUID(decision.Source.Members), extract.NodeUUID(decision.Target.Members)
		if !finalNodes[source] || !finalNodes[target] {
			r.Filtered++
			continue
		}
		identity := edgeIdentity(source, target, decision.Relation.Kind)
		if !finalEdges[identity] || !dbEdges[identity] {
			fail("accepted eligible cross edge missing from final candidate or database")
		} else {
			r.Materialized++
		}
	}
	for _, remaining := range expected {
		if remaining != 0 {
			fail("cross-verifier omitted an actual pre-verifier candidate")
		}
	}
	if report.Accepted != r.Supported {
		fail("cross-verifier accepted count mismatch")
	}
	if !reflect.DeepEqual(accepted, actualCrossKeys(&after)) {
		fail("validated graph differs from accepted candidate multiset")
	}
	return r
}
func bytesEqual(a, b []byte) bool { return string(a) == string(b) }
