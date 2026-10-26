package semanticevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/discovery"
)

const schemaVersion = "v011-evidence-1"

type sourceFixture struct {
	SourceID string `json:"source_id"`
	Path     string `json:"path"`
	Content  string `json:"content"`
}
type evidenceCase struct {
	CaseID          string   `json:"case_id"`
	SourceIDs       []string `json:"source_ids"`
	Obligation      string   `json:"obligation"`
	Expected        string   `json:"expected"`
	Owner           string   `json:"owner"`
	Stage           string   `json:"stage"`
	EvidenceKind    string   `json:"evidence_kind"`
	ExpectedOutcome string   `json:"expected_outcome"`
}
type supplement struct {
	SchemaVersion     string          `json:"schema_version"`
	Sources           []sourceFixture `json:"sources"`
	Cases             []evidenceCase  `json:"cases"`
	EmptySkillMarkers []string        `json:"empty_skill_markers"`
}

// 检查有限证据的身份与引用，避免缩减分母或把期望混入构建原文。
// Validate evidence identity and references; expectations remain separate from originals.
func validateSupplement(s supplement) error {
	if s.SchemaVersion != schemaVersion || len(s.Sources) == 0 || len(s.Cases) == 0 {
		return fmt.Errorf("missing version, sources or cases")
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	for _, src := range s.Sources {
		if src.SourceID == "" || ids[src.SourceID] {
			return fmt.Errorf("duplicate/empty source identity %q", src.SourceID)
		}
		if src.Path == "" || path.IsAbs(src.Path) || path.Clean(src.Path) != src.Path || src.Path == ".." || strings.HasPrefix(src.Path, "../") || strings.Contains(src.Path, "\\") || paths[src.Path] {
			return fmt.Errorf("invalid/duplicate source path %q", src.Path)
		}
		ids[src.SourceID], paths[src.Path] = true, true
	}
	for _, marker := range s.EmptySkillMarkers {
		if marker == "" || path.IsAbs(marker) || path.Clean(marker) != marker || strings.HasPrefix(marker, "../") || strings.Contains(marker, "\\") || paths[marker] || path.Base(marker) != "SKILL.md" {
			return fmt.Errorf("invalid/duplicate empty marker %q", marker)
		}
		paths[marker] = true
	}
	cases, used := map[string]bool{}, map[string]bool{}
	owners := map[string]string{"#110": "annotation", "#111": "node", "#112": "relation", "#113": "persistence", "#114": "orchestration"}
	stages := map[string]bool{"annotation": true, "node": true, "relation": true, "persistence": true, "orchestration": true}
	outcomes := map[string]bool{"": true, "complete": true, "partial": true, "unavailable": true, "cancelled": true}
	for _, c := range s.Cases {
		if c.CaseID == "" || cases[c.CaseID] {
			return fmt.Errorf("duplicate/empty case identity %q", c.CaseID)
		}
		cases[c.CaseID] = true
		if c.Obligation == "" || c.Expected == "" || c.EvidenceKind == "" || owners[c.Owner] != c.Stage || !stages[c.Stage] || !outcomes[c.ExpectedOutcome] || len(c.SourceIDs) == 0 {
			return fmt.Errorf("incomplete case %q", c.CaseID)
		}
		seen := map[string]bool{}
		for _, id := range c.SourceIDs {
			if !ids[id] || seen[id] {
				return fmt.Errorf("case %q has invalid source %q", c.CaseID, id)
			}
			seen[id], used[id] = true, true
		}
	}
	for id := range ids {
		if !used[id] {
			return fmt.Errorf("unaccounted source %q", id)
		}
	}
	return nil
}

func TestSupplementIdentityAndReferences(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/v011/supplement.json")
	if err != nil {
		t.Fatal(err)
	}
	var s supplement
	if err = json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if err = validateSupplement(s); err != nil {
		t.Fatal(err)
	}
	// 原先只有合法 JSON 就可能接受缺失来源、重复身份和失效状态。
	// Schema-valid JSON alone does not reject missing provenance or denominator drift.
	for name, mutate := range map[string]func(*supplement){
		"missing source":   func(x *supplement) { x.Cases[0].SourceIDs = []string{"absent"} },
		"duplicate source": func(x *supplement) { x.Sources = append(x.Sources, x.Sources[0]) },
		"traversal":        func(x *supplement) { x.Sources[0].Path = "../outside.md" },
		"false outcome":    func(x *supplement) { x.Cases[0].ExpectedOutcome = "pass" },
	} {
		t.Run(name, func(t *testing.T) {
			var bad supplement
			if err := json.Unmarshal(b, &bad); err != nil {
				t.Fatal(err)
			}
			mutate(&bad)
			if validateSupplement(bad) == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestSixOriginalSummary(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/v011/inventory_summary.json")
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		SchemaVersion string                       `json:"schema_version"`
		Sources       []map[string]json.RawMessage `json:"sources"`
		Totals        map[string]int               `json:"totals"`
	}
	if err = json.Unmarshal(b, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.SchemaVersion != schemaVersion || len(summary.Sources) != 6 {
		t.Fatal("six original denominator/version changed")
	}
	sums := map[string]int{}
	seen := map[string]bool{}
	for _, src := range summary.Sources {
		var id, ref string
		json.Unmarshal(src["source_id"], &id)
		json.Unmarshal(src["expected_inventory_ref"], &ref)
		expectedIDs := map[string]bool{"S01": true, "S02": true, "S03": true, "S04": true, "S05": true, "S06": true}
		if !expectedIDs[id] || seen[id] || ref == "" {
			t.Fatalf("invalid source identity/reference %q", id)
		}
		seen[id] = true
		counts := map[string]int{}
		for _, key := range []string{"original_bytes", "original_lines", "substantive_units", "excluded_units", "covered_units", "explicit_relations"} {
			var n int
			if err := json.Unmarshal(src[key], &n); err != nil || n < 0 {
				t.Fatalf("invalid %s/%s", id, key)
			}
			counts[key] = n
			sums[key] += n
		}
		if counts["covered_units"] != counts["substantive_units"] {
			t.Fatalf("uncovered substantive units in %s", id)
		}
		var classes, reasons map[string]int
		if json.Unmarshal(src["classifications"], &classes) != nil || len(classes) == 0 {
			t.Fatalf("missing semantic classifications in %s", id)
		}
		for label, n := range classes {
			if label == "" || n < 0 {
				t.Fatalf("invalid semantic classification in %s", id)
			}
		}
		if json.Unmarshal(src["exclusion_reasons"], &reasons) != nil {
			t.Fatalf("missing exclusion reasons in %s", id)
		}
		excluded := 0
		for reason, n := range reasons {
			if reason == "" || n < 0 {
				t.Fatalf("invalid exclusion reason in %s", id)
			}
			excluded += n
		}
		if excluded != counts["excluded_units"] {
			t.Fatalf("unexplained exclusion in %s", id)
		}
	}
	for key, n := range sums {
		if summary.Totals[key] != n {
			t.Fatalf("total %s: got %d want %d", key, summary.Totals[key], n)
		}
	}
	if sums["original_bytes"] != 51183 || sums["original_lines"] != 288 || sums["explicit_relations"] != 194 {
		t.Fatal("frozen original extent changed")
	}
}

// 补充证据必须走普通发现规则；没有 SKILL 标记时原文会全部消失。
// The finite fixture must be discoverable through the ordinary source rules.
func TestSupplementOrdinaryDiscovery(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/v011/supplement.json")
	if err != nil {
		t.Fatal(err)
	}
	var s supplement
	if err = json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if err = validateSupplement(s); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	want := map[string][]byte{}
	for _, marker := range s.EmptySkillMarkers {
		want[marker] = []byte{}
	}
	for _, src := range s.Sources {
		want[src.Path] = []byte(src.Content)
	}
	for rel, data := range want {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	report, err := discovery.NewFSScanner().ScanReport(context.Background(), root, discovery.DiscoveryRules{})
	if err != nil {
		t.Fatal(err)
	}
	supported := map[string]bool{}
	for _, src := range report.Sources {
		if src.Disposition != discovery.Supported {
			continue
		}
		data, ok := want[src.Path]
		if !ok || supported[src.Path] || !bytes.Equal(data, src.Content) {
			t.Fatalf("unexpected/duplicate/altered input %s", src.Path)
		}
		supported[src.Path] = true
	}
	if len(supported) != len(want) {
		t.Fatalf("ordinary discovery found %d of %d declared originals", len(supported), len(want))
	}
}
