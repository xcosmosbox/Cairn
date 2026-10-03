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

	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/controller/workspace"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/core/storage"
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
	ConfigDigest         string `json:"config_digest"`
}

// Digest 返回 fingerprint 的 sha256（canonical JSON，字段顺序固定）。
func (f Fingerprint) Digest() string {
	data, _ := json.Marshal(f)
	return kbbundle.DigestPrefix(kbbundle.DigestBytes(data))
}

// CandidateSpec 是发布 candidate Bundle 的输入。
type CandidateSpec struct {
	Repo            dkconfig.RepoConfig
	Catalog         dkconfig.CatalogConfig
	SourceSHA       string
	KBVersion       string
	Fingerprint     Fingerprint
	ConfigDigest    string
	KGDBPath        string
	BuildReport     []byte
	EvolutionDBPath string
	BundleDir       string    // Bundle 落盘目录
	CreatedAt       time.Time // 同一 run 固定，不随重试漂移。
	// ForgeToken 用于 git push catalog manifest（catalog repo 远端 URL push）。
	ForgeToken string
	// CatalogRemoteURL 是 catalog repo 的远端 URL（供 workspace push）。
	CatalogRemoteURL string
}

// PublishedBundle 是发布后的 candidate Bundle 信息。
type PublishedBundle struct {
	Bundle       *kbbundle.Bundle
	Release      githubapp.Release
	Manifest     kbbundle.Manifest
	CandidateDir string
}

// Publisher 依赖 Forge + workspace。
type Publisher struct {
	forge      githubapp.Forge
	ws         *workspace.Manager
	catalogRef githubapp.RepoRef
	journal    EffectJournal
}

// New 创建 Publisher。catalogRef 是 catalog repo 的 owner/name。
func New(forge githubapp.Forge, ws *workspace.Manager, catalogRef githubapp.RepoRef) *Publisher {
	return &Publisher{forge: forge, ws: ws, catalogRef: catalogRef}
}

// SetEffectJournal 接通 controller 的持久化外部动作账本。
func (p *Publisher) SetEffectJournal(j EffectJournal) { p.journal = j }

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
		CreatedAt:        spec.CreatedAt.UTC().Format(time.RFC3339),
	}
	// 读取实际产物版本并冻结本次输入；WAL 快照避免只复制主文件漏数据。
	kg, err := storage.OpenReadOnly(spec.KGDBPath)
	if err != nil {
		return PublishedBundle{}, err
	}
	schema, err := kg.SchemaVersion()
	if err != nil {
		kg.Close()
		return PublishedBundle{}, err
	}
	if spec.Fingerprint.DBSchemaVersion != schema {
		kg.Close()
		return PublishedBundle{}, fmt.Errorf("publisher: actual schema %d != fingerprint %d", schema, spec.Fingerprint.DBSchemaVersion)
	}
	if spec.KBVersion == "" {
		m.KBVersion, err = storage.NewKBVersionRepo(kg).Current(ctx)
	}
	kg.Close()
	if err != nil {
		return PublishedBundle{}, err
	}
	m.SchemaVersion = schema
	bundle, err := prepareCandidate(ctx, spec, m)
	if err != nil {
		return PublishedBundle{}, err
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
	releaseSpec := githubapp.ReleaseSpec{
		Owner:         p.catalogRef.Owner,
		Repo:          p.catalogRef.Name,
		Tag:           tag,
		Target:        spec.Catalog.Branch,
		Title:         tag,
		Body:          fmt.Sprintf("KB Bundle for %s @ %s\n\nbundle_digest: %s", spec.Repo.KGGroup, shortSHA(spec.SourceSHA), bundle.Manifest.BundleDigest),
		Prelease:      spec.Catalog.ReleasePrerelease,
		AssetName:     "bundle.tar.gz",
		AssetDigest:   bundle.Manifest.BundleDigest,
		AssetFilePath: tarballPath, // 完整 Bundle tarball（含全部文件）
	}
	rel, err := Effect(ctx, p.journal, "create_release", p.catalogRef.Owner+"/"+p.catalogRef.Name, releaseIdentity(releaseSpec), func() (githubapp.Release, error) { return p.forge.EnsureRelease(ctx, releaseSpec) })
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
	Repo        dkconfig.RepoConfig
	Catalog     dkconfig.CatalogConfig
	Bundle      PublishedBundle
	SourceSHA   string
	RunID       string
	CreatedAt   time.Time
	Fingerprint Fingerprint
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
	runMarker := "cairn-run-id:" + spec.RunID + ":catalog:" + spec.Bundle.Manifest.BundleDigest
	branch := "catalog/" + spec.Repo.ID + "/" + spec.RunID + "-" + shortDigest(spec.Bundle.Manifest.BundleDigest)
	// 分支 base 与产物一起冻结；重试不能拿最新 main 制造不同提交。
	baseSHA, err := p.proposalBase(ctx, spec, branch)
	if err != nil {
		return githubapp.PullRequest{}, err
	}
	_, err = Effect(ctx, p.journal, "create_branch", p.catalogRef.Owner+"/"+p.catalogRef.Name, struct{ Branch, Base string }{branch, baseSHA}, func() (string, error) {
		return branch, p.forge.CreateBranch(ctx, p.catalogRef.Owner, p.catalogRef.Name, branch, baseSHA)
	})
	if err != nil {
		return githubapp.PullRequest{}, err
	}
	wt := p.ws.SourceWorktree(spec.RunID + "-cat")
	if _, err := os.Stat(filepath.Join(wt, ".git")); os.IsNotExist(err) {
		wt, err = p.ws.CheckoutWorktree(ctx, "catalog", spec.RunID+"-cat", baseSHA, "")
		if err != nil {
			return githubapp.PullRequest{}, err
		}
	} else if err != nil {
		return githubapp.PullRequest{}, err
	}
	if err := p.ws.EnsureBranchInWorktree(ctx, wt, branch); err != nil {
		return githubapp.PullRequest{}, err
	}
	// 写 manifest 文件。
	if err := store.CheckLease(ctx); err != nil {
		return githubapp.PullRequest{}, err
	}
	manifestDir := filepath.Join(wt, spec.Catalog.ManifestDir, spec.Repo.KGGroup)
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return githubapp.PullRequest{}, err
	}
	cm := kbbundle.CatalogManifest{
		Manifest:    spec.Bundle.Manifest,
		Channel:     "stable",
		PublishedAt: spec.Bundle.Manifest.CreatedAt,
		ReleaseTag:  spec.Bundle.Release.Tag,
		AssetName:   "bundle.tar.gz",
	}
	cmData, _ := json.MarshalIndent(cm, "", "  ")
	manifestPath := filepath.Join(manifestDir, "stable.json")
	if err := store.CheckLease(ctx); err != nil {
		return githubapp.PullRequest{}, err
	}
	if err := os.WriteFile(manifestPath, cmData, 0o644); err != nil {
		return githubapp.PullRequest{}, err
	}
	// 提交。
	msg := fmt.Sprintf("chore(kb): publish %s stable\n\n%s\nbundle_digest: %s\nsource_commit: %s",
		spec.Repo.KGGroup, runMarker, spec.Bundle.Manifest.BundleDigest, shortSHA(spec.SourceSHA))
	if err := p.ws.CommitAll(ctx, wt, msg, "cairnd", "cairnd@bot"); err != nil && err != workspace.ErrNoChanges {
		return githubapp.PullRequest{}, fmt.Errorf("publisher: 提交 catalog manifest: %w", err)
	}
	// 推送的远端身份由冻结的文件/base 确定；已 applied 时复用原提交响应。
	headSHA, err := p.ws.HeadSHA(ctx, wt)
	if err != nil {
		return githubapp.PullRequest{}, err
	}
	headSHA, err = Effect(ctx, p.journal, "push", p.catalogRef.Owner+"/"+p.catalogRef.Name, struct{ Branch, Base, Digest string }{branch, baseSHA, spec.Bundle.Manifest.BundleDigest}, func() (string, error) {
		return headSHA, p.ws.PushBranch(ctx, "catalog", spec.CatalogRemoteURL, branch, spec.ForgeToken)
	})
	if err != nil {
		return githubapp.PullRequest{}, err
	}
	// 创建 PR。Body 优先使用调用方构建的丰富版本（含 LLM 摘要），为空则回退机械模板。
	prBody := spec.PRBody
	if prBody == "" {
		prBody = fmt.Sprintf("%s\n\nbundle_digest: %s\nconfig_digest: %s\nfingerprint: %s",
			runMarker, spec.Bundle.Manifest.BundleDigest, spec.Bundle.Manifest.ConfigDigest, spec.Fingerprint.Digest())
	}
	prSpec := githubapp.PullRequestSpec{
		Owner:      p.catalogRef.Owner,
		Repo:       p.catalogRef.Name,
		HeadBranch: branch,
		BaseBranch: spec.Catalog.Branch,
		Title:      fmt.Sprintf("chore(kb): publish %s stable @ %s", spec.Repo.KGGroup, shortSHA(spec.SourceSHA)),
		Body:       prBody,
		RunMarker:  runMarker,
	}
	pr, err := Effect(ctx, p.journal, "create_pr", p.catalogRef.Owner+"/"+p.catalogRef.Name, struct{ Branch, Base, Head, Digest string }{branch, spec.Catalog.Branch, headSHA, spec.Bundle.Manifest.BundleDigest}, func() (githubapp.PullRequest, error) { return p.forge.EnsurePullRequest(ctx, prSpec) })
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

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func releaseIdentity(spec githubapp.ReleaseSpec) any {
	// 本地文件路径不属于远端请求身份；内容摘要和目标字段才决定不可变动作。
	spec.AssetFilePath = ""
	return spec
}
