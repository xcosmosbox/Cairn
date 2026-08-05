// Package publisher 实现 KB Bundle 的发布协议（产品化 Prompt §11）。
//
// 发布顺序：
//  1. 生成并校验 Bundle（kbbundle.Pack）。
//  2. 上传到 Catalog Repo 的不可变 GitHub Release（Forge.EnsureRelease，digest 一致才复用）。
//  3. Release 在 Catalog PR 合并前标记为 prerelease/candidate。
//  4. 在 Catalog Repo 分支写入 manifest，创建 Catalog PR。
//  5. Catalog PR 合并后，manifest 中的 digest 成为 stable。
//
// 禁止为「candidate 变 stable」重新打包或覆盖原 Bundle。
package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/workspace"
)

// Fingerprint 是 builder fingerprint（§7.3）：影响构建语义的稳定字段。
type Fingerprint struct {
	BuilderVersion       string `json:"builder_version"`
	BuilderCommit        string `json:"builder_commit"`
	ControllerSchemaVer  int    `json:"controller_schema_version"`
	DBSchemaVersion      int    `json:"db_schema_version"`
	PromptSetVersion     string `json:"prompt_set_version"`
	IdentityAlgorithmVer string `json:"identity_algorithm_version"`
	DiscoveryRulesDigest string `json:"discovery_rules_digest"`
	Model                string `json:"model"`
	Provider             string `json:"provider"`
	AnnotationSchemaVer  int    `json:"annotation_schema_version"`
}

// Digest 返回 fingerprint 的 sha256（canonical JSON，字段顺序固定）。
func (f Fingerprint) Digest() string {
	data, _ := json.Marshal(f)
	return kbbundle.DigestPrefix(kbbundle.DigestBytes(data))
}

// CandidateSpec 是发布 candidate Bundle 的输入。
type CandidateSpec struct {
	Repo          dkconfig.RepoConfig
	Catalog       dkconfig.CatalogConfig
	SourceSHA     string
	KBVersion     string
	Fingerprint   Fingerprint
	ConfigDigest  string
	KGDBPath      string
	BuildReport   []byte
	EvolutionDBPath string
	BundleDir     string // Bundle 落盘目录
	// ForgeToken 用于 git push catalog manifest（catalog repo 远端 URL push）。
	ForgeToken string
	// CatalogRemoteURL 是 catalog repo 的远端 URL（供 workspace push）。
	CatalogRemoteURL string
}

// PublishedBundle 是发布后的 candidate Bundle 信息。
type PublishedBundle struct {
	Bundle     *kbbundle.Bundle
	Release    githubapp.Release
	Manifest   kbbundle.Manifest
	CandidateDir string
}

// Publisher 依赖 Forge + workspace。
type Publisher struct {
	forge     githubapp.Forge
	ws        *workspace.Manager
	catalogRef githubapp.RepoRef
}

// New 创建 Publisher。catalogRef 是 catalog repo 的 owner/name。
func New(forge githubapp.Forge, ws *workspace.Manager, catalogRef githubapp.RepoRef) *Publisher {
	return &Publisher{forge: forge, ws: ws, catalogRef: catalogRef}
}

// EnsureCandidate 打包 Bundle 并上传到 Catalog Repo 的 GitHub Release（§11.3 步骤 1-3）。
// 幂等：若 Release tag 已存在且 asset digest 一致，复用。
func (p *Publisher) EnsureCandidate(ctx context.Context, spec CandidateSpec) (PublishedBundle, error) {
	// 1. 构造 manifest。
	m := kbbundle.Manifest{
		KG:               spec.Repo.KGGroup,
		SourceRepo:       spec.Repo.Owner + "/" + spec.Repo.Name,
		SourceRef:        spec.Repo.Branch,
		SourceCommit:     spec.SourceSHA,
		BuilderVersion:   spec.Fingerprint.BuilderVersion,
		BuilderCommit:    spec.Fingerprint.BuilderCommit,
		SchemaVersion:    spec.Fingerprint.DBSchemaVersion,
		PromptSetVersion: spec.Fingerprint.PromptSetVersion,
		Model:            spec.Fingerprint.Model,
		ConfigDigest:     spec.ConfigDigest,
		KBVersion:        spec.KBVersion,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	// 2. Pack（先打包拿到 bundle_digest，再据此生成不冲突的 tag）。
	bundle, err := kbbundle.Pack(kbbundle.PackRequest{
		Manifest:        m,
		KGDBPath:        spec.KGDBPath,
		BuildReport:     spec.BuildReport,
		EvolutionDBPath: spec.EvolutionDBPath,
		OutDir:          spec.BundleDir,
	})
	if err != nil {
		return PublishedBundle{}, fmt.Errorf("publisher: 打包 Bundle: %w", err)
	}
	// 3. 生成 release tag：在 {source_sha} 基础上追加 bundle_digest 短码。
	//    同一 source_sha 的不同构建（回退重做 / builder 升级 / 打包格式变化）会有不同
	//    digest → 不同 tag → 各自独立、不可变的 Release，互不覆盖；内容完全一致时
	//    digest 相同 → 同 tag → 真幂等复用。彻底避免「asset 被覆盖导致与 stable.json 漂移」。
	tag := renderTag(spec.Catalog.ReleaseTagTemplate, spec.Repo.KGGroup, spec.SourceSHA) +
		"-" + shortDigest(bundle.Manifest.BundleDigest)
	// 4. 打包完整 Bundle tarball（含 knowledge.db / build-report / kb-manifest /
	//    checksums / evolution），供消费者解包后直接 Verify（digest 天然一致）。
	//    tarball 写到 BundleDir 之外，避免把自身打进包内。
	tarballPath := spec.BundleDir + ".tar.gz"
	if err := kbbundle.PackTarball(spec.BundleDir, tarballPath); err != nil {
		return PublishedBundle{}, fmt.Errorf("publisher: 打包 Bundle tarball: %w", err)
	}
	// 5. 上传到 Release（不可变：tag 已含 digest，同 tag 即同内容）。
	rel, err := p.forge.EnsureRelease(ctx, githubapp.ReleaseSpec{
		Owner:         p.catalogRef.Owner,
		Repo:          p.catalogRef.Name,
		Tag:           tag,
		Target:        spec.Catalog.Branch,
		Title:         tag,
		Body:          fmt.Sprintf("KB Bundle for %s @ %s\n\nbundle_digest: %s", spec.Repo.KGGroup, spec.SourceSHA[:12], bundle.Manifest.BundleDigest),
		Prelease:      spec.Catalog.ReleasePrerelease,
		AssetName:     "bundle.tar.gz",
		AssetDigest:   bundle.Manifest.BundleDigest,
		AssetFilePath: tarballPath, // 完整 Bundle tarball（含全部文件）
	})
	if err != nil {
		return PublishedBundle{}, fmt.Errorf("publisher: 上传 Release: %w", err)
	}
	return PublishedBundle{
		Bundle:       bundle,
		Release:      rel,
		Manifest:     bundle.Manifest,
		CandidateDir: spec.BundleDir,
	}, nil
}

// CatalogProposalSpec 是创建 Catalog manifest PR 的输入。
type CatalogProposalSpec struct {
	Repo         dkconfig.RepoConfig
	Catalog      dkconfig.CatalogConfig
	Bundle       PublishedBundle
	SourceSHA    string
	RunID        string
	Fingerprint  Fingerprint
	// CatalogBarePath 是 catalog repo 的本地 bare mirror 路径（供 workspace 写 manifest）。
	CatalogMirrorPath string
	CatalogRemoteURL  string
	ForgeToken        string
	// PRBody 是调用方构建的 PR body（含 LLM 摘要 + 结构化信息）。
	// 为空时 EnsureCatalogProposal 回退到机械模板。
	PRBody string
}

// EnsureCatalogProposal 在 Catalog Repo 创建 manifest 分支 + PR（§11.3 步骤 4）。
// 幂等：按 RunMarker 查找已有 PR。
func (p *Publisher) EnsureCatalogProposal(ctx context.Context, spec CatalogProposalSpec) (githubapp.PullRequest, error) {
	runMarker := "cairn-run-id:" + spec.RunID
	branch := "catalog/" + spec.Repo.ID + "/" + spec.RunID
	// 获取 catalog main SHA 作为 base。
	baseSHA, err := p.forge.GetBranchSHA(ctx, p.catalogRef.Owner, p.catalogRef.Name, spec.Catalog.Branch)
	if err != nil {
		return githubapp.PullRequest{}, fmt.Errorf("publisher: 获取 catalog base SHA: %w", err)
	}
	// 创建分支。
	if err := p.forge.CreateBranch(ctx, p.catalogRef.Owner, p.catalogRef.Name, branch, baseSHA); err != nil {
		return githubapp.PullRequest{}, fmt.Errorf("publisher: 创建 catalog 分支: %w", err)
	}
	// 在 catalog mirror 的 worktree 中写 manifest 文件。
	// 用 workspace checkout 一个临时 worktree。
	wt, err := p.ws.CheckoutWorktree(ctx, "catalog", spec.RunID+"-cat", baseSHA, branch)
	if err != nil {
		return githubapp.PullRequest{}, fmt.Errorf("publisher: checkout catalog worktree: %w", err)
	}
	// 写 manifest 文件。
	manifestDir := filepath.Join(wt, spec.Catalog.ManifestDir, spec.Repo.KGGroup)
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return githubapp.PullRequest{}, err
	}
	cm := kbbundle.CatalogManifest{
		Manifest:    spec.Bundle.Manifest,
		Channel:     "stable",
		PublishedAt: time.Now().UTC().Format(time.RFC3339),
		ReleaseTag:  spec.Bundle.Release.Tag,
		AssetName:   "bundle.tar.gz",
	}
	cmData, _ := json.MarshalIndent(cm, "", "  ")
	manifestPath := filepath.Join(manifestDir, "stable.json")
	if err := os.WriteFile(manifestPath, cmData, 0o644); err != nil {
		return githubapp.PullRequest{}, err
	}
	// 提交。
	msg := fmt.Sprintf("chore(kb): publish %s stable\n\n%s\nbundle_digest: %s\nsource_commit: %s",
		spec.Repo.KGGroup, runMarker, spec.Bundle.Manifest.BundleDigest, spec.SourceSHA[:12])
	if err := p.ws.CommitAll(ctx, wt, msg, "cairnd", "cairnd@bot"); err != nil && err != workspace.ErrNoChanges {
		return githubapp.PullRequest{}, fmt.Errorf("publisher: 提交 catalog manifest: %w", err)
	}
	// 推送。
	if err := p.ws.PushBranch(ctx, "catalog", spec.CatalogRemoteURL, branch, spec.ForgeToken); err != nil {
		return githubapp.PullRequest{}, fmt.Errorf("publisher: 推送 catalog 分支: %w", err)
	}
	// 创建 PR。Body 优先使用调用方构建的丰富版本（含 LLM 摘要），为空则回退机械模板。
	prBody := spec.PRBody
	if prBody == "" {
		prBody = fmt.Sprintf("%s\n\nbundle_digest: %s\nconfig_digest: %s\nfingerprint: %s",
			runMarker, spec.Bundle.Manifest.BundleDigest, spec.Bundle.Manifest.ConfigDigest, spec.Fingerprint.Digest())
	}
	pr, err := p.forge.EnsurePullRequest(ctx, githubapp.PullRequestSpec{
		Owner:      p.catalogRef.Owner,
		Repo:       p.catalogRef.Name,
		HeadBranch: branch,
		BaseBranch: spec.Catalog.Branch,
		Title:      fmt.Sprintf("chore(kb): publish %s stable @ %s", spec.Repo.KGGroup, spec.SourceSHA[:12]),
		Body:       prBody,
		RunMarker:  runMarker,
	})
	if err != nil {
		return githubapp.PullRequest{}, fmt.Errorf("publisher: 创建 catalog PR: %w", err)
	}
	return pr, nil
}

// renderTag 渲染 release tag 模板。
func renderTag(template, kgGroup, sourceSHA string) string {
	shortSHA := sourceSHA
	if len(shortSHA) > 12 {
		shortSHA = shortSHA[:12]
	}
	return strings.ReplaceAll(strings.ReplaceAll(template, "{group}", kgGroup), "{source_sha}", shortSHA)
}

// shortDigest 取 bundle_digest 的短码（去掉 sha256: 前缀后取前 12 位），用于 tag 后缀，
// 使同一 source_sha 的不同构建产物落到不同（且不可变）的 Release。
func shortDigest(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}
