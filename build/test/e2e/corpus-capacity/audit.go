package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/dktypes"
	_ "modernc.org/sqlite"
)

type auditDoc struct {
	Document            string   `json:"doc_id"`
	Path                string   `json:"path"`
	OriginalBodySHA     string   `json:"original_body_sha256"`
	RewrittenSHA        string   `json:"rewritten_sha256"`
	AnnotationItems     int      `json:"annotation_items"`
	Members             []string `json:"members"`
	RetainedMembers     []string `json:"retained_members"`
	MaterializedMembers []string `json:"materialized_members"`
	MissingMembers      []string `json:"missing_members"`
	AnnotationSHA       string   `json:"annotation_sha256"`
	AnnotationBytes     int      `json:"annotation_bytes"`
	DetailBytes         int      `json:"detail_bytes"`
}
type sourceRow struct {
	Node, Member, Path, Skill string
	Start, End                sql.NullInt64
}
type auditReport struct {
	Format                       string            `json:"format"`
	Pass                         bool              `json:"pass"`
	Errors                       []string          `json:"errors"`
	InputMemberIncidences        int               `json:"input_member_incidences"`
	CandidateMemberIncidences    int               `json:"candidate_member_incidences"`
	MaterializedMemberIncidences int               `json:"materialized_member_incidences"`
	ActualIncidenceCoverage      float64           `json:"actual_incidence_coverage"`
	MinimumIncidenceCoverage     float64           `json:"minimum_actual_incidence_coverage"`
	CoverageDenominator          string            `json:"coverage_denominator"`
	CrossRelations               *crossAuditReport `json:"cross_relations"`
	InputUniqueMembers           int               `json:"input_unique_members"`
	CandidateUniqueMembers       int               `json:"candidate_unique_members"`
	MaterializedUniqueMembers    int               `json:"materialized_unique_members"`
	SourceRows                   int               `json:"source_rows"`
	UnknownOriginalSpans         int               `json:"unknown_original_spans"`
	InvalidOriginalSpans         int               `json:"invalid_original_spans"`
	Documents                    []auditDoc        `json:"documents"`
	DBSHA                        string            `json:"db_sha256"`
	SpanMeaning                  string            `json:"span_meaning"`
	Bindings                     []map[string]any  `json:"source_bindings"`
	Trace                        map[string]int    `json:"trace"`
}

func sortedSet(values map[string]bool) []string {
	r := make([]string, 0, len(values))
	for v := range values {
		r = append(r, v)
	}
	sort.Strings(r)
	return r
}
func triple(node, member, path string) string { return node + "\x00" + member + "\x00" + path }

// auditMembership distinguishes actual candidate members and durable source rows
// from the production coverage gate's explained omissions. No gold is consulted.
func auditMembership(selected *selection, docs []*dktypes.AnnotatedDocument, candidate *extract.Result, rows []sourceRow, nodeIDs map[string]bool, sourceRoot, originalRoot string, minimum ...float64) *auditReport {
	r := &auditReport{Format: "cairn-capacity-audit/v2", SpanMeaning: "optional original-input span hints; NULL means unknown, bounded hints remain unverified; no rewritten-position or original-quote grounding claim"}
	threshold := 1.0
	if len(minimum) > 0 {
		threshold = minimum[0]
	}
	r.MinimumIncidenceCoverage = threshold
	r.CoverageDenominator = "distinct(original_doc_id,accepted04member_id), from 04_extract_input before fusion/confidence/drops; global unique IDs are supplemental diagnostics"
	fail := func(s string) { r.Errors = append(r.Errors, s) }
	byPath := map[string]*auditDoc{}
	docMembers := map[string]map[string]bool{}
	memberPaths := map[string]map[string]bool{}
	skills := map[string]string{}
	for _, d := range selected.Documents {
		r.Documents = append(r.Documents, auditDoc{Document: d.ID, Path: d.Path, OriginalBodySHA: d.BodySHA})
	}
	for i := range r.Documents {
		d := &r.Documents[i]
		byPath[d.Path] = d
		docMembers[d.Path] = map[string]bool{}
	}
	seenPaths := map[string]bool{}
	for _, d := range docs {
		if d == nil {
			fail("nil annotation document")
			continue
		}
		a, ok := byPath[d.FilePath]
		if !ok || seenPaths[d.FilePath] {
			fail("unexpected or duplicate annotation document")
			continue
		}
		seenPaths[d.FilePath] = true
		skills[d.FilePath] = d.Skill
		a.AnnotationItems = len(d.Items)
		annotationJSON, _ := json.Marshal(d)
		a.AnnotationSHA = sha(annotationJSON)
		a.AnnotationBytes = len(annotationJSON)
		if len(d.Items) == 0 {
			fail("empty annotation: " + d.FilePath)
		}
		for _, item := range d.Items {
			a.DetailBytes += len(item.Detail)
			if item.ID == "" {
				fail("empty member ID: " + d.FilePath)
				continue
			}
			docMembers[d.FilePath][item.ID] = true
			if memberPaths[item.ID] == nil {
				memberPaths[item.ID] = map[string]bool{}
			}
			memberPaths[item.ID][d.FilePath] = true
		}
	}
	expected := map[string]bool{}
	candidateMembers := map[string]bool{}
	candidateNodes := map[string]bool{}
	for _, domain := range candidate.Domains {
		for _, sub := range domain.Subdomains {
			for _, group := range [][]extract.Node{sub.Entities, sub.Concepts} {
				for _, node := range group {
					if node.ID == "" || candidateNodes[node.ID] {
						fail("missing or duplicate candidate UUID")
					}
					candidateNodes[node.ID] = true
					if !nodeIDs[node.ID] {
						fail("candidate node absent from database: " + node.ID)
					}
					for _, member := range node.Members {
						paths := memberPaths[member]
						if len(paths) == 0 {
							fail("unknown candidate member: " + member)
							continue
						}
						candidateMembers[member] = true
						for path := range paths {
							expected[triple(node.ID, member, path)] = true
						}
					}
				}
			}
		}
	}
	actual := map[string]bool{}
	materialized := map[string]bool{}
	perDoc := map[string]map[string]bool{}
	for _, row := range rows {
		r.SourceRows++
		key := triple(row.Node, row.Member, row.Path)
		if actual[key] || !expected[key] || !nodeIDs[row.Node] || row.Skill != skills[row.Path] {
			fail("unexpected/duplicate/unbound source row")
			continue
		}
		actual[key] = true
		materialized[row.Member] = true
		binding := map[string]any{"node_uuid": row.Node, "member_id": row.Member, "file_path": row.Path, "start_line": nil, "end_line": nil}
		if row.Start.Valid {
			binding["start_line"] = row.Start.Int64
		}
		if row.End.Valid {
			binding["end_line"] = row.End.Int64
		}
		r.Bindings = append(r.Bindings, binding)
		if perDoc[row.Path] == nil {
			perDoc[row.Path] = map[string]bool{}
		}
		perDoc[row.Path][row.Member] = true
		content, err := os.ReadFile(filepath.Join(originalRoot, row.Path))
		lineCount := len(strings.Split(string(content), "\n"))
		if !row.Start.Valid && !row.End.Valid {
			r.UnknownOriginalSpans++
			binding["original_span_status"] = "unknown"
		} else if err != nil || !row.Start.Valid || !row.End.Valid || row.Start.Int64 < 1 || row.End.Int64 < row.Start.Int64 || row.End.Int64 > int64(lineCount) {
			r.InvalidOriginalSpans++
			binding["original_span_status"] = "invalid"
			fail("invalid nonnull original span hint: " + row.Path)
		} else {
			binding["original_span_status"] = "unverified_hint"
		}
	}
	for key := range expected {
		if !actual[key] {
			fail("candidate member/source tuple not materialized")
		}
	}
	for i := range r.Documents {
		a := &r.Documents[i]
		if !seenPaths[a.Path] {
			fail("annotation document missing: " + a.Path)
		}
		a.Members = sortedSet(docMembers[a.Path])
		a.MaterializedMembers = sortedSet(perDoc[a.Path])
		retained := map[string]bool{}
		for member := range docMembers[a.Path] {
			if candidateMembers[member] {
				retained[member] = true
			}
			if !perDoc[a.Path][member] {
				a.MissingMembers = append(a.MissingMembers, member)
			}
		}
		sort.Strings(a.MissingMembers)
		a.RetainedMembers = sortedSet(retained)
		if len(a.MissingMembers) > 0 && threshold >= 1 {
			fail("input members absent from materialized document: " + a.Path)
		}
		r.InputMemberIncidences += len(a.Members)
		r.CandidateMemberIncidences += len(a.RetainedMembers)
		r.MaterializedMemberIncidences += len(a.MaterializedMembers)
		if len(a.MaterializedMembers) == 0 {
			fail("selected document has no actual materialized member: " + a.Path)
		}
		raw, err := os.ReadFile(filepath.Join(sourceRoot, a.Path))
		if err != nil {
			fail("rewritten document absent")
		} else {
			a.RewrittenSHA = sha(raw)
		}
	}
	if r.InputMemberIncidences > 0 {
		r.ActualIncidenceCoverage = float64(r.MaterializedMemberIncidences) / float64(r.InputMemberIncidences)
	}
	if threshold < 0.95 || threshold > 1 || r.InputMemberIncidences == 0 || r.ActualIncidenceCoverage < threshold {
		fail("actual prefilter document/member incidence coverage below frozen minimum")
	}
	r.InputUniqueMembers = len(memberPaths)
	r.CandidateUniqueMembers = len(candidateMembers)
	r.MaterializedUniqueMembers = len(materialized)
	r.Pass = len(r.Errors) == 0
	return r
}

func audit(ctx context.Context, out string) (*auditReport, error) {
	trace, traceErrors, err := writeTraceAudit(out)
	if err != nil {
		return nil, err
	}
	s, err := verifyPrepared(out, true)
	if err != nil {
		return nil, err
	}
	var start struct {
		OverlaySHA         string  `json:"runtime_overlay_sha256"`
		CrossEvidenceBytes int     `json:"cross_relation_evidence_bytes"`
		MinimumIncidence   float64 `json:"minimum_actual_incidence_coverage"`
	}
	if err := readJSON(filepath.Join(out, "run-start.json"), &start); err != nil {
		return nil, err
	}
	selectionRaw, err := os.ReadFile(filepath.Join(out, "selection.json"))
	if err != nil {
		return nil, err
	}
	cfg, _, err := loadRuntimeOverlay(filepath.Join(out, "runtime-overlay.json"), start.OverlaySHA, sha(selectionRaw), s)
	if err != nil {
		return nil, err
	}
	if start.CrossEvidenceBytes != cfg.CrossEvidenceBytes || start.MinimumIncidence != cfg.MinimumIncidence {
		return nil, fmt.Errorf("run-start options differ from pinned runtime overlay")
	}
	var finish struct {
		BuildError   *bool `json:"build_error"`
		ContextError *bool `json:"context_error"`
		LedgerOK     *bool `json:"ledger_ok"`
		LogicalCalls int   `json:"logical_calls"`
	}
	if err := readJSON(filepath.Join(out, "run-finish.json"), &finish); err != nil {
		return nil, err
	}
	freeze, err := freezeRuntime(out, start.OverlaySHA, sha(selectionRaw), s, cfg)
	if err != nil {
		return nil, err
	}
	var savedFreeze runtimeFreeze
	if err := readJSON(filepath.Join(out, "runtime-freeze.json"), &savedFreeze); err != nil {
		return nil, err
	}
	wantFreeze, _ := json.Marshal(freeze)
	savedBytes, _ := json.Marshal(savedFreeze)
	if !bytes.Equal(wantFreeze, savedBytes) {
		return nil, fmt.Errorf("runtime freeze no longer binds originals/profile")
	}
	var docs []*dktypes.AnnotatedDocument
	var candidate extract.Result
	var report pipeline.RunReport
	for _, p := range []struct {
		path  string
		value any
	}{{"stages/04_extract_input.json", &docs}, {"stages/05h_materialization_candidate.json", &candidate}, {"build-report.json", &report}} {
		if err = readJSON(filepath.Join(out, p.path), p.value); err != nil {
			return nil, err
		}
	}
	dbPath := filepath.Join(out, "knowledge.db")
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, err
	}
	before := sha(raw)
	absolute, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	query, err := db.QueryContext(ctx, "SELECT id FROM nodes WHERE label IN ('Entity','Concept')")
	if err != nil {
		return nil, err
	}
	nodes := map[string]bool{}
	for query.Next() {
		var id string
		if err = query.Scan(&id); err != nil {
			query.Close()
			return nil, err
		}
		nodes[id] = true
	}
	err = query.Err()
	query.Close()
	if err != nil {
		return nil, err
	}
	query, err = db.QueryContext(ctx, "SELECT node_uuid,member_id,file_path,skill,start_line,end_line FROM node_sources ORDER BY node_uuid,member_id,file_path")
	if err != nil {
		return nil, err
	}
	var sources []sourceRow
	for query.Next() {
		var row sourceRow
		if err = query.Scan(&row.Node, &row.Member, &row.Path, &row.Skill, &row.Start, &row.End); err != nil {
			query.Close()
			return nil, err
		}
		sources = append(sources, row)
	}
	err = query.Err()
	query.Close()
	if err != nil {
		return nil, err
	}
	dbEdges := map[string]bool{}
	edgeRows, err := db.QueryContext(ctx, "SELECT source_id,target_id,kind FROM edges")
	if err != nil {
		return nil, err
	}
	for edgeRows.Next() {
		var source, target, kind string
		if err := edgeRows.Scan(&source, &target, &kind); err != nil {
			edgeRows.Close()
			return nil, err
		}
		dbEdges[edgeIdentity(source, target, kind)] = true
	}
	if err := edgeRows.Err(); err != nil {
		edgeRows.Close()
		return nil, err
	}
	edgeRows.Close()
	r := auditMembership(s, docs, &candidate, sources, nodes, filepath.Join(out, "source"), filepath.Join(out, "original"), cfg.MinimumIncidence)
	r.CrossRelations = auditCrossArtifacts(out, s, docs, &candidate, cfg.CrossEvidenceBytes, report.CrossRelations, dbEdges)
	r.Errors = append(r.Errors, r.CrossRelations.Errors...)
	if finish.BuildError == nil || *finish.BuildError || finish.ContextError == nil || *finish.ContextError || finish.LedgerOK == nil || !*finish.LedgerOK || finish.LogicalCalls != trace["logical_calls"] {
		r.Errors = append(r.Errors, "run-finish contains unresolved build/context/ledger boundary")
	}
	if trace["unexposed_stage_calls"] > 0 {
		r.Errors = append(r.Errors, "actual run has unexposed logical stages")
	}
	r.Trace = trace
	r.Errors = append(r.Errors, traceErrors...)
	r.DBSHA = before
	if report.DocsTotal != 12 || report.DocsRewritten != 12 || len(report.Skipped) != 0 {
		r.Errors = append(r.Errors, "build skipped or failed selected documents")
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || sha(after) != before {
		r.Errors = append(r.Errors, "database changed during readonly audit")
	}
	r.Pass = len(r.Errors) == 0
	if err = writeNew(filepath.Join(out, "audit.json"), r); err != nil {
		return r, err
	}
	if !r.Pass {
		return r, fmt.Errorf("capacity audit failed; complete denominator retained")
	}
	return r, nil
}

func writeTraceAudit(out string) (map[string]int, []string, error) {
	path := filepath.Join(out, "requests.jsonl")
	raw, readErr := os.ReadFile(path)
	counts, failures := auditTraceBytes(raw)
	if readErr != nil {
		failures = append(failures, "request ledger snapshot read failed")
	}
	receipt := map[string]any{"format": "cairn-capacity-trace-forensics/v1", "created_at": now(), "counts": counts, "errors": failures, "ledger_read_ok": readErr == nil, "ledger_sha256": sha(raw), "ledger_bytes": len(raw), "snapshot_semantics": "counts/hash/bytes bind one read snapshot; a concurrently advancing run may admit later requests outside this prefix", "source_graph_and_quality": "not_checked; trace-only forensic evidence"}
	name := "trace-audit-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".json"
	return counts, failures, writeNew(filepath.Join(out, name), receipt)
}

// A start without an outcome remains a potentially charged, unknown attempt.
// Never call a killed/incomplete run successful and never silently resume it.
func auditTrace(path string) (map[string]int, []string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]int{}, []string{"request ledger absent"}
	}
	return auditTraceBytes(raw)
}

func auditTraceBytes(raw []byte) (map[string]int, []string) {
	counts := map[string]int{}
	var failures []string
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		failures = append(failures, "ledger snapshot has an incomplete tail")
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	type binding struct {
		call, provider int64
		stage          string
	}
	admitted := map[int64]binding{}
	outcomes := map[int64]bool{}
	logical := map[int64]bool{}
	logicalStages := map[int64]string{}
	finished := map[int64]bool{}
	for scanner.Scan() {
		var row struct {
			Event         string `json:"event"`
			Attempt       int64  `json:"http_attempt"`
			Call          int64  `json:"call_id"`
			Provider      int64  `json:"provider_attempt"`
			Stage         string `json:"stage"`
			Admitted      bool   `json:"admitted"`
			Usage         string `json:"usage_status"`
			Finish        string `json:"finish_reason"`
			Expected      *bool  `json:"model_identity_expected"`
			Status        int    `json:"http_status"`
			CompleteError bool   `json:"complete_error"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			failures = append(failures, "malformed ledger row")
			continue
		}
		switch row.Event {
		case "logical_start":
			if row.Stage != "" && !knownStageName(row.Stage) {
				counts["unknown_stage_calls"]++
				failures = append(failures, "unknown logical stage")
			}
			if logical[row.Call] {
				failures = append(failures, "duplicate logical call")
			}
			logical[row.Call] = true
			logicalStages[row.Call] = row.Stage
			counts["logical_calls"]++
			if row.Stage == "" {
				counts["unexposed_stage_calls"]++
			}
			if knownStageName(row.Stage) {
				counts["stage:"+row.Stage+":logical_calls"]++
			}
		case "logical_end":
			if row.Stage != logicalStages[row.Call] {
				failures = append(failures, "logical completion stage mismatch")
			}
			if row.CompleteError {
				counts["logical_execution_errors"]++
				failures = append(failures, "logical call ended with unresolved execution error")
			}
			if finished[row.Call] || !logical[row.Call] {
				failures = append(failures, "unbound logical completion")
			}
			finished[row.Call] = true
		case "request":
			if row.Stage != logicalStages[row.Call] {
				failures = append(failures, "wire/logical stage mismatch")
			}
			if !logical[row.Call] {
				failures = append(failures, "wire request missing logical start")
			}
			if row.Admitted {
				if _, ok := admitted[row.Attempt]; ok {
					failures = append(failures, "duplicate admitted attempt")
				}
				if row.Attempt < 1 || row.Call < 1 || row.Provider < 1 {
					failures = append(failures, "invalid attempt identity")
				}
				admitted[row.Attempt] = binding{row.Call, row.Provider, row.Stage}
				counts["admitted_attempts"]++
				if knownStageName(row.Stage) {
					counts["stage:"+row.Stage+":admitted_attempts"]++
				}
			} else {
				counts["rejected_attempts"]++
			}
		case "response":
			want, exists := admitted[row.Attempt]
			if outcomes[row.Attempt] || !exists || want.call != row.Call || want.provider != row.Provider || want.stage != row.Stage {
				failures = append(failures, "unbound/duplicate response")
				counts["unbound_outcomes"]++
				continue
			}
			outcomes[row.Attempt] = true
			counts["attempt_outcomes"]++
			if knownStageName(row.Stage) {
				counts["stage:"+row.Stage+":attempt_outcomes"]++
			}
			if row.Usage != "reported" {
				counts["attempts_with_unknown_usage"]++
			}
			if row.Finish == "length" || row.Finish == "max_tokens" {
				counts["length_responses"]++
				failures = append(failures, "truncated response cannot establish capacity readiness")
			}
			if row.Status == 200 && (row.Expected == nil || !*row.Expected) {
				failures = append(failures, "successful HTTP response model identity unknown/unexpected")
			}
		case "progress":
		default:
			failures = append(failures, "unknown ledger event")
		}
	}
	if scanner.Err() != nil {
		failures = append(failures, "ledger read failed")
	}
	for id := range admitted {
		if !outcomes[id] {
			counts["admitted_without_outcome"]++
			failures = append(failures, "admitted request outcome unknown")
		}
	}
	for id := range logical {
		if !finished[id] {
			failures = append(failures, "logical call incomplete")
		}
	}
	if len(admitted) == 0 {
		failures = append(failures, "no real request admitted")
	}
	if counts["rejected_attempts"] > 0 {
		failures = append(failures, "request admission rejected")
	}
	return counts, failures
}
