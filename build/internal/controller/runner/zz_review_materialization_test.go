package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Build both the KG ledger and its documents with the production full-build
// APIs. Display names differ from slugs, members share files, and spans differ
// between documents so an incomplete fixture cannot hide a false rejection.
func materializedCandidate(t *testing.T) (ValidateRequest, map[string][]writeback.NodeUpdate, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "kg.db")
	docs := []string{"skill-a/references/a.md", "skill-b/references/b.md"}
	res := &extract.Result{Domains: []extract.Domain{{Name: "订单领域", Slug: "orders", SourceSkills: []string{"skill-a", "skill-b"}, Subdomains: []extract.Subdomain{{Name: "订单生命周期", Slug: "lifecycle", Entities: []extract.Node{{ID: "shared-node", Label: "Entity", Name: "订单聚合根", FileSlug: "order-aggregate", Summary: "订单摘要", Description: "订单详述", Confidence: 1, Members: []string{"member-b", "member-a"}, SourceSkills: []string{"skill-a", "skill-b"}}}, Concepts: []extract.Node{{ID: "private-node", Label: "Concept", Name: "付款概念", FileSlug: "payment", Summary: "付款摘要", Description: "付款详述", Confidence: 1, Members: []string{"member-single", "member-extra"}, SourceSkills: []string{"skill-b"}}}}}}}}
	ingestSources := map[string][]ingest.MemberSource{
		"member-a":      {{Skill: "skill-a", FilePath: docs[0], StartLine: 12, EndLine: 14}},
		"member-b":      {{Skill: "skill-b", FilePath: docs[1], StartLine: 22, EndLine: 28}},
		"member-single": {{Skill: "skill-b", FilePath: docs[1], StartLine: 40, EndLine: 42}},
		"member-extra":  {{Skill: "skill-b", FilePath: docs[1]}},
	}
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.IngestDomains(context.Background(), db, ingest.IngestDomainsOptions{Result: res, MemberSources: ingestSources}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writerSources := map[string][]writeback.MemberSource{}
	for member, sources := range ingestSources {
		for _, source := range sources {
			writerSources[member] = append(writerSources[member], writeback.MemberSource{Skill: source.Skill, FilePath: source.FilePath, StartLine: source.StartLine, EndLine: source.EndLine})
		}
	}
	report, err := writeback.Writeback(res, writerSources, root)
	if err != nil || report.DocsWritten != 2 || report.PrimaryFiles != 1 {
		t.Fatalf("production writeback: %+v %v", report, err)
	}
	shared := writeback.NodeUpdate{UUID: "shared-node", FileSlug: "order-aggregate", Tag: "Entity", Name: "订单聚合根", Domain: "订单领域", Subdomain: "订单生命周期", DomainSlug: "orders", SubdomainSlug: "lifecycle", Summary: "订单摘要", Description: "订单详述", Provenance: "llm_inferred", Members: []string{"member-a", "member-b"}, SourceFiles: docs, Shared: true, SpanStart: 22, SpanEnd: 28}
	private := writeback.NodeUpdate{UUID: "private-node", FileSlug: "payment", Tag: "Concept", Name: "付款概念", Domain: "订单领域", Subdomain: "订单生命周期", DomainSlug: "orders", SubdomainSlug: "lifecycle", Summary: "付款摘要", Description: "付款详述", Provenance: "llm_inferred", Members: []string{"member-extra", "member-single"}, SourceFiles: docs[1:], SpanStart: 40, SpanEnd: 42}
	primary := writeback.PrimaryRelPath(shared.DomainSlug, shared.FileSlug, shared.UUID)
	updates := map[string][]writeback.NodeUpdate{docs[0]: {shared}, docs[1]: {shared, private}, primary: {shared}}
	return ValidateRequest{DBPath: dbPath, RepoPath: root}, updates, primary
}

func requireCandidateValid(t *testing.T, request ValidateRequest) {
	t.Helper()
	report, err := validateCandidate(context.Background(), request)
	if err != nil || !report.OK {
		t.Fatalf("valid production materialization rejected: %+v %v", report, err)
	}
}

func requireCandidateInvalid(t *testing.T, request ValidateRequest) {
	t.Helper()
	report, err := validateCandidate(context.Background(), request)
	if err != nil || report.OK || !strings.Contains(strings.Join(report.Errors, " "), "source closure") {
		t.Fatalf("invalid materialization accepted or wrong validation failure: %+v %v", report, err)
	}
}

func TestCandidateMaterializationAcceptsFullAndIncrementalSpans(t *testing.T) {
	t.Parallel()
	request, updates, primary := materializedCandidate(t)
	requireCandidateValid(t, request)
	// Full Writeback copied the first member's span into both mirrors and the
	// primary. Incremental writes replace mirrors with their local span.
	for doc, nodes := range updates {
		if doc == primary {
			continue
		}
		if strings.HasPrefix(doc, "skill-a/") {
			nodes[0].SpanStart, nodes[0].SpanEnd = 12, 14
		}
		if err := writeback.RewriteDoc(request.RepoPath, doc, nodes, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	requireCandidateValid(t, request)
}

func TestReviewSharedMirrorsWithoutPrimaryMustNotValidate(t *testing.T) {
	for _, removal := range []string{"document", "sidecar", "pair"} {
		t.Run(removal, func(t *testing.T) {
			t.Parallel()
			request, _, primary := materializedCandidate(t)
			requireCandidateValid(t, request)
			for _, suffix := range []string{"", ".kg.yaml"} {
				if (removal == "document" && suffix != "") || (removal == "sidecar" && suffix == "") {
					continue
				}
				if err := os.Remove(filepath.Join(request.RepoPath, primary) + suffix); err != nil {
					t.Fatal(err)
				}
			}
			requireCandidateInvalid(t, request)
		})
	}
}

func TestReviewMatchedDocumentSidecarWithWrongDomainMustNotValidate(t *testing.T) {
	t.Parallel()
	request, updates, _ := materializedCandidate(t)
	requireCandidateValid(t, request)
	doc := "skill-a/references/a.md"
	nodes := updates[doc]
	nodes[0].Domain, nodes[0].Subdomain = "其他领域", "其他子域"
	nodes[0].DomainSlug, nodes[0].SubdomainSlug = "other-domain", "other-subdomain"
	if err := writeback.RewriteDoc(request.RepoPath, doc, nodes, time.Now()); err != nil {
		t.Fatal(err)
	}
	requireCandidateInvalid(t, request)
}

func TestCandidateMaterializationChecksAllReadOnlyMetadata(t *testing.T) {
	for _, field := range []string{"name", "tag", "domain", "subdomain", "domain_slug", "subdomain_slug", "file_slug", "members", "shared", "provenance", "span", "summary", "description"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			request, updates, _ := materializedCandidate(t)
			requireCandidateValid(t, request)
			doc := "skill-b/references/b.md"
			nodes := updates[doc]
			node := &nodes[1] // One file with two members is still not shared.
			switch field {
			case "name":
				node.Name = "错误名称"
			case "tag":
				node.Tag = "Entity"
			case "domain":
				node.Domain = "错误领域"
			case "subdomain":
				node.Subdomain = "错误子域"
			case "domain_slug":
				node.DomainSlug = "wrong-domain"
			case "subdomain_slug":
				node.SubdomainSlug = "wrong-subdomain"
			case "file_slug":
				node.FileSlug = "wrong-file"
			case "members":
				node.Members = []string{"foreign-member"}
			case "shared":
				node.Shared = true
			case "provenance":
				node.Provenance = "human_curated"
			case "span":
				node.SpanStart, node.SpanEnd = 99, 100
			case "summary":
				node.Summary = "未入库摘要"
			case "description":
				node.Description = "未入库详述"
			}
			// Producer writes a mutually consistent MD + sidecar pair, so comparing
			// the pair alone cannot identify its disagreement with the KG ledger.
			if err := writeback.RewriteDoc(request.RepoPath, doc, nodes, time.Now()); err != nil {
				t.Fatal(err)
			}
			requireCandidateInvalid(t, request)
		})
	}
}

func TestCandidateMaterializationAllowsCanonicalEmptyDeletedSource(t *testing.T) {
	t.Parallel()
	request, _, _ := materializedCandidate(t)
	if err := writeback.RewriteDoc(request.RepoPath, "skill-a/references/deleted.md", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	requireCandidateValid(t, request)
}

func TestCandidateMaterializationRequiresExactSourceNodeSets(t *testing.T) {
	for _, fault := range []string{"missing-doc-node", "foreign-doc", "node-without-source", "stale-primary"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			request, updates, _ := materializedCandidate(t)
			requireCandidateValid(t, request)
			switch fault {
			case "missing-doc-node":
				doc := "skill-b/references/b.md"
				if err := writeback.RewriteDoc(request.RepoPath, doc, updates[doc][:1], time.Now()); err != nil {
					t.Fatal(err)
				}
			case "foreign-doc":
				if err := writeback.RewriteDoc(request.RepoPath, "skill-b/references/foreign.md", updates["skill-a/references/a.md"], time.Now()); err != nil {
					t.Fatal(err)
				}
			case "node-without-source":
				db, err := storage.OpenExisting(request.DBPath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Conn().Exec(`DELETE FROM node_sources WHERE node_uuid='private-node'`)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "stale-primary":
				if err := writeback.RewriteDoc(request.RepoPath, "_shared/orders/obsolete.md", nil, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			requireCandidateInvalid(t, request)
		})
	}
}

func TestCandidateMaterializationValidationIsReadOnly(t *testing.T) {
	t.Parallel()
	request, updates, _ := materializedCandidate(t)
	paths := []string{request.DBPath}
	for doc := range updates {
		paths = append(paths, filepath.Join(request.RepoPath, doc), filepath.Join(request.RepoPath, doc)+".kg.yaml")
	}
	type baseline struct {
		data  []byte
		mtime time.Time
	}
	before := map[string]baseline{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = baseline{data: data, mtime: info.ModTime()}
	}
	requireCandidateValid(t, request)
	for path, old := range before {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(old.data) || !info.ModTime().Equal(old.mtime) {
			t.Fatalf("validation modified %s", path)
		}
	}
}
