package discovery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeSource(t *testing.T, root, path string, data []byte) {
	t.Helper()
	abs := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func sourceAt(t *testing.T, report Report, path string) Source {
	t.Helper()
	for _, source := range report.Sources {
		if source.Path == path {
			return source
		}
	}
	t.Fatalf("missing disposition for %s", path)
	return Source{}
}

// Overlapping roots/directories previously duplicated originals, and marker bytes had no source roster.
func TestScanReportFullOriginalsStableIdentityAndOverlap(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	marker := []byte("---\nname: example\ndomain: systems\n---\n# Substantive body\nNever remove the UUID 6f9619ff-8b86-d011-b42d-00c04fc964ff.\n")
	large := []byte(strings.Repeat("conditions, negation and uncertainty survive\n", 80000))
	writeSource(t, root, "skills/a/SKILL.md", marker)
	writeSource(t, root, "skills/a/references/large.md", large)
	writeSource(t, root, "skills/a/references/empty.md", nil)
	// Equal content is not equal original identity.
	writeSource(t, root, "skills/a/references/copy.md", marker)
	rules := DiscoveryRules{SkillRootPaths: []string{"skills/a", ".", "skills"}, ReferenceDirs: []string{"references", "references/.", "references"}}
	scanner := NewFSScanner()
	report, err := scanner.ScanReport(context.Background(), root, rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skills) != 1 {
		t.Fatalf("skills = %#v", report.Skills)
	}
	skill := report.Skills[0]
	if skill.Name != "example" || skill.DomainHint != "systems" || len(skill.ReferenceFiles) != 3 {
		t.Fatalf("skill = %#v", skill)
	}
	if source := sourceAt(t, report, "skills/a/SKILL.md"); source.Kind != SourceSkillBody || source.Disposition != Supported || !bytes.Equal(source.Content, marker) {
		t.Fatalf("marker source = %#v", source)
	}
	if source := sourceAt(t, report, "skills/a/references/large.md"); source.Disposition != Supported || !bytes.Equal(source.Content, large) {
		t.Fatal("large source truncated or excluded")
	}
	if source := sourceAt(t, report, "skills/a/references/empty.md"); source.Disposition != Supported || len(source.Content) != 0 {
		t.Fatalf("empty source = %#v", source)
	}
	seen := make(map[string]bool)
	for _, source := range report.Sources {
		if seen[source.Path] {
			t.Fatalf("duplicate original %s", source.Path)
		}
		seen[source.Path] = true
	}
	rules.SkillRootPaths = []string{"skills", ".", "skills/a", "."}
	again, err := scanner.ScanReport(context.Background(), root, rules)
	if err != nil || !reflect.DeepEqual(report, again) {
		t.Fatalf("repeat changed discovery: %v", err)
	}
	legacy, err := scanner.Scan(context.Background(), root, rules)
	if err != nil || !reflect.DeepEqual(legacy, report.Skills) {
		t.Fatalf("legacy view differs: %v", err)
	}
}

// Unsupported files and exclusions previously had no inspectable disposition.
func TestScanReportDispositionsDoNotReadUnsupportedInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSource(t, root, "a/SKILL.md", []byte("# Knowledge\n"))
	writeSource(t, root, "a/references/good.md", []byte("valid"))
	writeSource(t, root, "a/references/binary.md", []byte{0, 255})
	writeSource(t, root, "a/references/image.png", []byte{255})
	writeSource(t, root, "a/references/ignored.md", []byte("ignored by explicit rule"))
	writeSource(t, root, "a/notes.md", []byte("outside configured reference scope"))
	writeSource(t, root, ".hidden/SKILL.md", []byte("hidden"))
	if err := os.Symlink("good.md", filepath.Join(root, "a/references/link.md")); err != nil {
		t.Fatal(err)
	}
	report, err := NewFSScanner().ScanReport(context.Background(), root, DiscoveryRules{IgnorePaths: []string{"ignored.md"}})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]Disposition{
		"a/references/good.md":    Supported,
		"a/references/binary.md":  Unsupported,
		"a/references/image.png":  Unsupported,
		"a/references/link.md":    Unsupported,
		"a/references/ignored.md": Excluded,
		"a/notes.md":              Excluded,
		".hidden":                 Excluded,
	} {
		source := sourceAt(t, report, path)
		if source.Disposition != want || (want != Supported && source.Reason == "") {
			t.Fatalf("%s = %#v", path, source)
		}
	}
	recursive, err := NewFSScanner().ScanReport(context.Background(), root, DiscoveryRules{RecurseIntoSkills: true})
	if err != nil || sourceAt(t, recursive, "a/references/image.png").Disposition != Unsupported {
		t.Fatal("recursive skill discovery erased unsupported reference disposition", err)
	}
	if got := report.Skills[0].ReferenceFiles; !reflect.DeepEqual(got, []string{"a/references/good.md"}) {
		t.Fatalf("unsupported input leaked into legacy refs: %v", got)
	}
}

// Failed reads previously looked like successful empty or smaller input sets.
func TestScanReportFailedReadsRetainHealthySiblings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSource(t, root, "a/SKILL.md", []byte("# A"))
	writeSource(t, root, "a/references/bad.md", []byte("unreadable"))
	writeSource(t, root, "b/SKILL.md", []byte("# B"))
	writeSource(t, root, "b/references/empty.md", nil)
	scanner := &FSScanner{readFile: func(path string) ([]byte, error) {
		if filepath.Base(path) == "bad.md" {
			return nil, os.ErrPermission
		}
		return os.ReadFile(path)
	}}
	rules := DiscoveryRules{SkillRootPaths: []string{"missing", "a", "b"}}
	report, err := scanner.ScanReport(context.Background(), root, rules)
	if !errors.Is(err, os.ErrPermission) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing explicit read errors: %v", err)
	}
	if len(report.Skills) != 2 {
		t.Fatalf("healthy sibling lost: %#v", report.Skills)
	}
	for _, path := range []string{"missing", "a/references/bad.md"} {
		source := sourceAt(t, report, path)
		if source.Disposition != Unreadable || source.Reason == "" {
			t.Fatalf("%s = %#v", path, source)
		}
	}
	if sourceAt(t, report, "b/references/empty.md").Disposition != Supported {
		t.Fatal("legitimate empty source confused with failed read")
	}
	legacy, err := scanner.Scan(context.Background(), root, rules)
	if err == nil || len(legacy) != 2 {
		t.Fatalf("legacy Scan silently succeeded or dropped healthy skills: %v %#v", err, legacy)
	}
}

// Cancellation previously discarded all discovered skills and exposed no unfinished boundary.
func TestScanReportCancellationIsExplicitAndKeepsReadSources(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSource(t, root, "a/SKILL.md", []byte("# A"))
	writeSource(t, root, "a/references/first.md", []byte("successful source"))
	writeSource(t, root, "a/references/next.md", []byte("not read"))
	ctx, cancel := context.WithCancel(context.Background())
	scanner := &FSScanner{readFile: func(path string) ([]byte, error) {
		data, err := os.ReadFile(path)
		if filepath.Base(path) == "first.md" {
			cancel()
		}
		return data, err
	}}
	report, err := scanner.ScanReport(ctx, root, DiscoveryRules{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
	if sourceAt(t, report, "a/references/first.md").Disposition != Supported {
		t.Fatal("successful source lost on cancellation")
	}
	found := false
	for _, source := range report.Sources {
		if source.Disposition == Cancelled && source.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("no cancelled boundary")
	}
	pre, err := scanner.ScanReport(ctx, root, DiscoveryRules{})
	if !errors.Is(err, context.Canceled) || sourceAt(t, pre, ".").Disposition != Cancelled {
		t.Fatal("pre-cancelled scan appears empty-successful")
	}
}

// Nested markers and overlapping reference scopes previously allowed double consumption.
func TestScanReportNestedMarkerAndOverlappingReferenceRoots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSource(t, root, "a/SKILL.md", []byte("# Parent"))
	writeSource(t, root, "a/references/child/SKILL.md", []byte("# Child body"))
	writeSource(t, root, "a/references/child/references/fact.md", []byte("fact"))
	report, err := NewFSScanner().ScanReport(context.Background(), root, DiscoveryRules{RecurseIntoSkills: true, ReferenceDirs: []string{"references", "references/child"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skills) != 2 {
		t.Fatalf("nested skill missing: %#v", report.Skills)
	}
	if sourceAt(t, report, "a/references/child/SKILL.md").Kind != SourceSkillBody {
		t.Fatal("nested marker double-classified as reference")
	}
	refs := make(map[string]bool)
	for _, skill := range report.Skills {
		for _, path := range skill.ReferenceFiles {
			if refs[path] || strings.HasSuffix(path, "SKILL.md") {
				t.Fatalf("duplicate reference/body consumption: %s", path)
			}
			refs[path] = true
		}
	}
}

// Configured nested roots could follow symlink ancestors outside source scope.
func TestScanReportConfigurationAndSymlinkBoundaries(t *testing.T) {
	t.Parallel()
	root, outside := t.TempDir(), t.TempDir()
	writeSource(t, outside, "child/SKILL.md", []byte("must not follow"))
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	report, err := NewFSScanner().ScanReport(context.Background(), root, DiscoveryRules{SkillRootPaths: []string{"link/child"}})
	if err != nil || len(report.Skills) != 0 || sourceAt(t, report, "link").Disposition != Unsupported {
		t.Fatal("configured root bypassed symlink boundary", err)
	}
	for _, rules := range []DiscoveryRules{
		{SkillRootPaths: []string{"../outside"}}, {ReferenceDirs: []string{"../outside"}},
		{Markers: []string{"../SKILL.md"}}, {ReferenceGlob: "["}, {IgnorePaths: []string{"["}},
	} {
		if _, err := NewFSScanner().ScanReport(context.Background(), root, rules); err == nil {
			t.Fatalf("invalid rule silently accepted: %#v", rules)
		}
	}
}

// A directory named SKILL.md must not hide valid descendant skills; absent optional refs are normal.
func TestInvalidMarkerDoesNotHideDescendantsAndOptionalNestedRefs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSource(t, root, "SKILL.md/child/SKILL.md", []byte("# Valid descendant"))
	report, err := NewFSScanner().ScanReport(context.Background(), root, DiscoveryRules{ReferenceDirs: []string{"docs/references"}})
	if err != nil {
		t.Fatalf("absent optional nested reference directory failed scan: %v", err)
	}
	if len(report.Skills) != 1 || report.Skills[0].Path != "SKILL.md/child" {
		t.Fatalf("invalid directory marker hid valid descendants: %#v", report.Skills)
	}
	if sourceAt(t, report, "SKILL.md").Disposition != Unsupported {
		t.Fatal("directory marker not classified unsupported")
	}
}
