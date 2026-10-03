package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/fakeforge"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/repoidentity"
	"github.com/xcosmosbox/cairn/core/storage"
)

// bootstrapFake 将所有写入限制在新的本地会话，不接触配置里的真实 Source/Catalog。
// Each invocation isolates its Git, controller state and artifacts because fake PRs are in memory.
func bootstrapFake(cfg *dkconfig.Config) (*fakeforge.FakeForge, error) {
	if cfg.Service.WorkspacesDir == "" {
		return nil, fmt.Errorf("fake workspace: workspaces_dir must be set")
	}
	base, err := filepath.Abs(cfg.Service.WorkspacesDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("fake workspace: %w", err)
	}
	session, err := os.MkdirTemp(base, "fake-session-")
	if err != nil {
		return nil, err
	}
	cfg.Service.StateDB = filepath.Join(session, "controller.db")
	cfg.Service.WorkspacesDir = filepath.Join(session, "workspaces")
	cfg.Service.CacheDir = filepath.Join(session, "cache")
	cfg.Service.BundleDir = filepath.Join(session, "bundles")
	remotes := filepath.Join(session, "remotes")
	forge := fakeforge.New()
	if _, err := forge.CreateRepo(remotes, parseOwner(cfg.Catalog.Repo), parseName(cfg.Catalog.Repo), cfg.Catalog.Branch); err != nil {
		return nil, err
	}
	for i := range cfg.Repos {
		r := &cfg.Repos[i]
		remote, err := forge.CreateRepo(remotes, r.Owner, r.Name, r.Branch)
		if err != nil {
			return nil, err
		}
		r.URL = remote
		// 演示固定使用 PR 模式，以验证 Source 与 Catalog 两次合并。
		// Fake mode exercises both PR lifecycles; it never uses a real direct-push target.
		r.Rewrite.Mode = "pr"
	}
	log.Printf("[fake] 本地模拟会话: %s（固定样例知识，无 GitHub/LLM；自动模拟合并本地 PR）", session)
	return forge, nil
}

// fakePipelineRunner 写入真实、可校验的样例 KG 与 Markdown，避免空库/虚报回写通过测试。
// This deterministic fixture verifies publication mechanics, not LLM extraction quality.
func fakePipelineRunner() runner.PipelineRunner {
	return &runner.FakeRunner{OnFull: func(ctx context.Context, req runner.FullRequest) (runner.BuildResult, error) {
		identity, err := repoidentity.Resolve(ctx, req.RepoPath, req.RepositoryIdentity)
		if err != nil {
			return runner.BuildResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(req.DBPath), 0o755); err != nil {
			return runner.BuildResult{}, err
		}
		// 重试重建到临时库，完成后原子替换；不会重复插入或留下部分构建的 candidate。
		// A full-build retry replaces a completed fixture instead of appending duplicate rows.
		file, err := os.CreateTemp(filepath.Dir(req.DBPath), ".fake-db-*")
		if err != nil {
			return runner.BuildResult{}, err
		}
		temporary := file.Name()
		if err := file.Close(); err != nil {
			os.Remove(temporary)
			return runner.BuildResult{}, err
		}
		defer os.Remove(temporary)
		defer os.Remove(temporary + "-wal")
		defer os.Remove(temporary + "-shm")
		db, err := storage.NewDB(storage.DBOptions{Path: temporary})
		if err != nil {
			return runner.BuildResult{}, err
		}
		defer db.Close()
		if err := storage.NewRepositoryIdentityRepo(db).Set(ctx, identity); err != nil {
			return runner.BuildResult{}, err
		}
		// 模拟的只是抽取结果；真实 ingest 和 writeback 维护 UUID、来源账本及文件对。
		// Fixed extraction data still uses the production graph and source materialization.
		const member = "fake-concept-blocking-queue"
		result := &extract.Result{Domains: []extract.Domain{{
			Name: "演示", Slug: "demo", Summary: "Synthetic knowledge domain", SourceSkills: []string{"fake"},
			Subdomains: []extract.Subdomain{{
				Name: "队列", Slug: "queue", Summary: "Synthetic knowledge subdomain",
				Concepts: []extract.Node{{
					ID: extract.NodeUUID([]string{member}), Name: "阻塞队列", Members: []string{member},
					Summary: "Bounded queue blocks when full or empty", Description: "Synthetic local fixture: bounded queue blocks when full or empty.",
					Confidence: 1, SourceSkills: []string{"fake"},
				}},
			}},
		}}}
		ingested, err := ingest.IngestDomains(ctx, db, ingest.IngestDomainsOptions{
			Result: result, MinConfidence: req.MinConfidence,
			Skills:        []ingest.SkillMeta{{Name: "fake", Summary: "Synthetic local fixture"}},
			MemberSources: map[string][]ingest.MemberSource{member: {{Skill: "fake", FilePath: "SKILL.md", StartLine: 6, EndLine: 8}}},
		})
		if err != nil {
			return runner.BuildResult{}, err
		}
		node, err := storage.NewNodeRepo(db).GetByID(ctx, result.Domains[0].Subdomains[0].Concepts[0].ID)
		if err != nil {
			return runner.BuildResult{}, err
		}
		if node == nil {
			return runner.BuildResult{}, fmt.Errorf("fake fixture: concept was not materialized")
		}
		updates := []writeback.NodeUpdate{{
			UUID: node.ID, FileSlug: node.FileSlug, Tag: string(node.Label), Name: node.Name,
			Domain: "演示", Subdomain: "队列", DomainSlug: node.Domain, SubdomainSlug: node.Subdomain,
			Summary: node.Summary, Description: node.Description, Provenance: string(node.Provenance),
			Members: []string{member}, SourceFiles: []string{"SKILL.md"}, SpanStart: 6, SpanEnd: 8,
		}}
		// 固定样例的来源时间也固定；重复构建不能仅因时钟制造新的 Source PR。
		// A synthetic source has a fixed timestamp; production pair writes and guards still apply.
		generatedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
		if err := writeback.WriteDocContentContext(ctx, req.RepoPath, "SKILL.md", writeback.RenderDoc(updates), updates, generatedAt); err != nil {
			return runner.BuildResult{}, err
		}
		if err := db.Close(); err != nil {
			return runner.BuildResult{}, err
		}
		validation, err := (&runner.Runner{}).Validate(ctx, runner.ValidateRequest{DBPath: temporary, RepoPath: req.RepoPath})
		if err != nil {
			return runner.BuildResult{}, err
		}
		if !validation.OK {
			return runner.BuildResult{}, fmt.Errorf("fake fixture: incomplete source materialization: %v", validation.Errors)
		}
		if err := os.Rename(temporary, req.DBPath); err != nil {
			return runner.BuildResult{}, err
		}
		return runner.BuildResult{DBPath: req.DBPath, NodesCreated: ingested.NodesInserted, EdgesCreated: ingested.EdgesInserted, DocsRewritten: 1, HasWriteback: true}, nil
	}}
}
