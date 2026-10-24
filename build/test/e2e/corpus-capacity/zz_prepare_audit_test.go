package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

func TestSelectionIsOrderIndependentAndLengthStratified(t *testing.T) {
	t.Parallel()
	var docs []corpusDoc
	for i := 0; i < 16; i++ {
		docs = append(docs, corpusDoc{URL: fmt.Sprintf("https://example.test/%d", i), Body: strings.Repeat("word ", i/2+1)})
	}
	_, first, err := choose(docs)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(docs)-1; i < j; i, j = i+1, j-1 {
		docs[i], docs[j] = docs[j], docs[i]
	}
	_, second, err := choose(docs)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("corpus order changed selection")
	}
	want := []int{0, 1, 3, 4, 5, 7, 8, 9, 11, 12, 13, 15}
	seen := map[string]bool{}
	for i, d := range first {
		if d.Rank != want[i] || d.Stratum != i/3 || seen[d.ID] {
			t.Fatal("selection stratum/quantile overlap")
		}
		seen[d.ID] = true
	}
	if whitespaceCount("a\u001cb\u0085c\u00a0d") != 4 {
		t.Fatal("selection whitespace differs from frozen Python rule")
	}
}

func TestCorpusRejectsQAAndWrappersRetainExactOriginalBytes(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`[{"query":"q","answer":"a"}]`, `[{"title":"t","author":"a","source":"s","published_at":"p","category":"c","url":"u","body":"b","gold":"x"}]`} {
		if _, err := parseCorpus([]byte(raw)); err == nil {
			t.Fatal("QA/extra field accepted")
		}
	}
	d := corpusDoc{URL: "u", Body: "\n原文 😀\n\n", Title: "T"}
	raw, start, runes := wrap(d)
	if string(raw[start:]) != d.Body || runes < 1 {
		t.Fatal("wrapper lost original body bytes")
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := writeNew(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(path, map[string]int{"a": 2}); err == nil {
		t.Fatal("historical receipt overwritten")
	}
}

// Explained omissions may satisfy the existing extraction threshold but are not
// materialized members. Shared IDs also require one source incidence per document.
func TestMembershipAuditRejectsOmissionsAndInvalidHintsButReportsNullUnknown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, path := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("one\ntwo\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := &selection{Documents: []selectedDoc{{ID: "a", Path: "a.md"}, {ID: "b", Path: "b.md"}}}
	docs := []*dktypes.AnnotatedDocument{{Skill: "s", FilePath: "a.md", Items: []dktypes.AnnotatedItem{{ID: "shared"}, {ID: "omitted"}}}, {Skill: "s", FilePath: "b.md", Items: []dktypes.AnnotatedItem{{ID: "shared"}}}}
	candidate := &extract.Result{Domains: []extract.Domain{{Subdomains: []extract.Subdomain{{Entities: []extract.Node{{ID: "uuid", Members: []string{"shared"}}}}}}}}
	valid := sql.NullInt64{Int64: 1, Valid: true}
	rows := []sourceRow{{Node: "uuid", Member: "shared", Path: "a.md", Skill: "s", Start: valid, End: valid}}
	r := auditMembership(s, docs, candidate, rows, map[string]bool{"uuid": true}, root, root)
	if r.Pass || r.InputUniqueMembers != 2 || r.MaterializedUniqueMembers != 1 || len(r.Documents[0].MissingMembers) != 1 || len(r.Documents[1].MissingMembers) != 1 {
		t.Fatalf("omission/incidence hidden: %#v", r)
	}
	docs[0].Items = docs[0].Items[:1]
	rows = append(rows, sourceRow{Node: "uuid", Member: "shared", Path: "b.md", Skill: "s", Start: valid, End: valid})
	r = auditMembership(s, docs, candidate, rows, map[string]bool{"uuid": true}, root, root)
	if !r.Pass {
		b, _ := json.Marshal(r)
		t.Fatalf("complete membership failed: %s", b)
	}
	rows[0].Start = sql.NullInt64{}
	rows[0].End = sql.NullInt64{}
	r = auditMembership(s, docs, candidate, rows, map[string]bool{"uuid": true}, root, root)
	if !r.Pass || r.UnknownOriginalSpans != 1 {
		t.Fatal("optional NULL hint must remain explicit unknown")
	}
	rows[0].End = valid
	r = auditMembership(s, docs, candidate, rows, map[string]bool{"uuid": true}, root, root)
	if r.Pass || r.InvalidOriginalSpans != 1 {
		t.Fatal("half-NULL original hint passed")
	}
	rows[0].Start = valid
	rows[0].Node = "unknown"
	r = auditMembership(s, docs, candidate, rows, map[string]bool{"uuid": true}, root, root)
	if r.Pass {
		t.Fatal("unbound source UUID passed")
	}
}

func TestTraceAuditRetainsKilledAttemptAsUnknownAndExposesUsage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	if err := os.WriteFile(path, []byte("{\"event\":\"logical_start\",\"call_id\":1}\n{\"event\":\"request\",\"call_id\":1,\"http_attempt\":1,\"provider_attempt\":1,\"admitted\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	counts, failures := auditTrace(path)
	if counts["admitted_without_outcome"] != 1 || len(failures) == 0 {
		t.Fatal("killed admitted request passed")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{\"event\":\"response\",\"call_id\":1,\"http_attempt\":1,\"provider_attempt\":1,\"http_status\":429,\"usage_status\":\"unknown\"}\n{\"event\":\"logical_end\",\"call_id\":1}\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	counts, failures = auditTrace(path)
	if counts["attempts_with_unknown_usage"] != 1 || len(failures) != 0 {
		t.Fatalf("unknown provider usage must remain explicit: %#v %#v", counts, failures)
	}
}

func TestTraceOnlyCommandWorksWithoutStageOrDatabaseArtifacts(t *testing.T) {
	dir := t.TempDir()
	raw := "{\"event\":\"logical_start\",\"call_id\":1}\n{\"event\":\"request\",\"call_id\":1,\"http_attempt\":1,\"provider_attempt\":1,\"admitted\":true}\n"
	if err := os.WriteFile(filepath.Join(dir, "requests.jsonl"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestTraceOnlyCommandHelper$")
	command.Env = append(os.Environ(), "CAPACITY_TRACE_TEST_DIR="+dir)
	if err := command.Run(); err == nil {
		t.Fatal("killed snapshot must return failure after writing forensic receipt")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "trace-audit-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("forensic receipt missing without stages/database")
	}
	var receipt struct {
		Counts map[string]int `json:"counts"`
		SHA    string         `json:"ledger_sha256"`
	}
	if err = readJSON(paths[0], &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Counts["admitted_without_outcome"] != 1 || receipt.SHA != sha([]byte(raw)) {
		t.Fatal("unknown charged attempt lost")
	}
}

func TestTraceOnlyCommandHelper(t *testing.T) {
	dir := os.Getenv("CAPACITY_TRACE_TEST_DIR")
	if dir == "" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("corpus-capacity", flag.ContinueOnError)
	os.Args = []string{"corpus-capacity", "--mode", "trace-audit", "--out", dir}
	if err := run(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestTraceAuditRejectsSwappedLogicalAndProviderBindings(t *testing.T) {
	t.Parallel()
	for _, identity := range []string{`"call_id":99,"provider_attempt":1`, `"call_id":1,"provider_attempt":2`} {
		path := filepath.Join(t.TempDir(), "requests.jsonl")
		raw := "{\"event\":\"logical_start\",\"call_id\":1}\n{\"event\":\"request\",\"call_id\":1,\"provider_attempt\":1,\"http_attempt\":1,\"admitted\":true}\n{\"event\":\"response\",\"http_attempt\":1," + identity + "}\n{\"event\":\"logical_end\",\"call_id\":1}\n"
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		counts, failures := auditTrace(path)
		if len(failures) == 0 || counts["unbound_outcomes"] != 1 || counts["admitted_without_outcome"] != 1 {
			t.Fatal("swapped response satisfied admitted request")
		}
	}
}
