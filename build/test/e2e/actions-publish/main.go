// Command actions-publish 在显式测试驱动中使用 Actions 的临时 token；不改变 cairnd 鉴权。
// It publishes a prebuilt real-LLM artifact and exercises the production remote consumer.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/core/repoidentity"
	"github.com/xcosmosbox/cairn/core/storage"
)

type sourceSpec struct {
	ID            string  `json:"id"`
	URL           string  `json:"url"`
	Owner         string  `json:"owner"`
	Name          string  `json:"name"`
	Branch        string  `json:"branch"`
	KGGroup       string  `json:"kg_group"`
	MinConfidence float64 `json:"min_confidence"`
}

type catalogSpec struct {
	Repo              string `json:"repo"`
	Branch            string `json:"branch"`
	ManifestDir       string `json:"manifest_dir"`
	ReleaseTag        string `json:"release_tag_template"`
	ReleasePrerelease bool   `json:"release_prerelease"`
}

// publishSpec 冻结真实构建身份；Actions 只验证/发布，不重新生成 LLM 知识。
// The specification carries the local builder's actual identity, not the publisher's build identity.
type publishSpec struct {
	Repo         sourceSpec            `json:"repo"`
	Catalog      catalogSpec           `json:"catalog"`
	SourceCommit string                `json:"source_commit"`
	CreatedAt    string                `json:"created_at"`
	Fingerprint  publisher.Fingerprint `json:"fingerprint"`
	ConfigDigest string                `json:"config_digest"`
}

type receipt struct {
	Phase                 string                   `json:"phase"`
	Catalog               kbbundle.CatalogManifest `json:"catalog"`
	Validation            runner.ValidationReport  `json:"validation"`
	Release               githubapp.Release        `json:"release"`
	TarballSHA256         string                   `json:"tarball_sha256,omitempty"`
	PublicationReplay     bool                     `json:"publication_replay_verified"`
	RepeatInstallVerified bool                     `json:"repeat_install_verified"`
	InstalledDir          string                   `json:"installed_dir,omitempty"`
	InstalledDB           string                   `json:"installed_db,omitempty"`
}

// envTokenSource 仅属于这个测试命令；每次操作读取当前临时 token，不写入文件。
// The test driver resolves the current job token per request and never persists it.
type envTokenSource struct {
	baseURL string
	envName string
}

func (s envTokenSource) HTTPBaseURL() string { return s.baseURL }
func (s envTokenSource) InstallToken(ctx context.Context, _ int64) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	token := strings.TrimSpace(os.Getenv(s.envName))
	if token == "" {
		return "", time.Time{}, fmt.Errorf("actions-publish: %s is empty", s.envName)
	}
	return token, time.Time{}, nil // Job expiry is controlled by Actions; no invented expiry.
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 20*time.Minute)
	defer timeout()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("actions-publish", flag.ContinueOnError)
	phase := fs.String("phase", "publish", "publish | consume")
	specPath := fs.String("spec", "", "frozen local builder publication specification (JSON)")
	sourceDir := fs.String("source-dir", "", "clean checkout of the reviewed source commit")
	dbPath := fs.String("kg-db", "", "prebuilt real-LLM knowledge.db")
	reportPath := fs.String("build-report", "", "original local build report JSON")
	evolutionPath := fs.String("evolution-db", "", "optional prebuilt evolution.db")
	output := fs.String("output-dir", "", "private publication staging directory")
	receiptPath := fs.String("receipt", "", "result JSON path (contains no token)")
	expectedPath := fs.String("expected-receipt", "", "publish receipt required for consume")
	installDir := fs.String("install-dir", "", "remote consumer installation directory")
	apiBase := fs.String("api-base", "https://api.github.com", "GitHub REST API base")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *specPath == "" || *receiptPath == "" {
		return fmt.Errorf("actions-publish: --spec and --receipt are required, without positional arguments")
	}
	var spec publishSpec
	if err := readJSON(*specPath, &spec); err != nil {
		return err
	}
	repo, catalog, created, err := spec.validate()
	if err != nil {
		return err
	}
	spec.CreatedAt = created.UTC().Format(time.RFC3339)
	auth := envTokenSource{baseURL: strings.TrimRight(*apiBase, "/"), envName: "GITHUB_TOKEN"}
	if _, _, err := auth.InstallToken(ctx, 0); err != nil {
		return err
	}
	forge := githubapp.NewHTTPForge(auth, 0)
	var result receipt
	switch *phase {
	case "publish":
		if *sourceDir == "" || *dbPath == "" || *reportPath == "" || *output == "" {
			return fmt.Errorf("actions-publish: publish requires --source-dir, --kg-db, --build-report and --output-dir")
		}
		result, err = publish(ctx, forge, spec, repo, catalog, created, *sourceDir, *dbPath, *reportPath, *evolutionPath, *output)
	case "consume":
		if *expectedPath == "" || *installDir == "" {
			return fmt.Errorf("actions-publish: consume requires --expected-receipt and --install-dir")
		}
		var expected receipt
		if err = readJSON(*expectedPath, &expected); err == nil {
			result, err = consume(ctx, auth, spec, repo, catalog, expected, *installDir)
		}
	default:
		return fmt.Errorf("actions-publish: unsupported phase %q", *phase)
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeJSON(*receiptPath, result)
}

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (s publishSpec) validate() (dkconfig.RepoConfig, dkconfig.CatalogConfig, time.Time, error) {
	r := dkconfig.RepoConfig{ID: s.Repo.ID, URL: s.Repo.URL, Owner: s.Repo.Owner, Name: s.Repo.Name, Branch: s.Repo.Branch, KGGroup: s.Repo.KGGroup, Rewrite: dkconfig.RewriteConfig{Mode: "pr", MinConfidence: s.Repo.MinConfidence}}
	c := dkconfig.CatalogConfig{Repo: s.Catalog.Repo, Branch: s.Catalog.Branch, ManifestDir: s.Catalog.ManifestDir, ReleaseTagTemplate: s.Catalog.ReleaseTag, ReleasePrerelease: s.Catalog.ReleasePrerelease}
	config := dkconfig.Config{Service: dkconfig.ServiceConfig{Mode: dkconfig.ModeOnce, StateDB: "unused-test-state.db"}, LLM: dkconfig.LLMConfig{Model: s.Fingerprint.Model, Provider: s.Fingerprint.Provider}, Repos: []dkconfig.RepoConfig{r}, Catalog: c}
	if err := config.Validate(); err != nil {
		return r, c, time.Time{}, err
	}
	created, err := time.Parse(time.RFC3339, s.CreatedAt)
	if err != nil || !commitPattern.MatchString(s.SourceCommit) || s.Catalog.Repo == "" || s.Repo.MinConfidence < 0 || s.Repo.MinConfidence > 1 {
		return r, c, created, fmt.Errorf("actions-publish: invalid source commit, created_at, catalog repo or confidence")
	}
	fp := s.Fingerprint
	if fp.Model == "" || fp.Provider == "" || fp.BuilderVersion == "" || fp.BuilderCommit == "" || fp.PromptSetVersion == "" || fp.IdentityAlgorithmVer == "" || fp.DiscoveryRulesDigest == "" || fp.ControllerSchemaVer <= 0 || fp.AnnotationSchemaVer <= 0 || fp.DBSchemaVersion != storage.LatestSchemaVersion() || !digestPattern.MatchString(s.ConfigDigest) || fp.ConfigDigest != s.ConfigDigest {
		return r, c, created, fmt.Errorf("actions-publish: incomplete or inconsistent frozen builder fingerprint")
	}
	identity, err := repoidentity.CanonicalGitURL(s.Repo.URL)
	want := "git:github.com/" + strings.ToLower(s.Repo.Owner+"/"+s.Repo.Name)
	if err != nil || identity != want {
		return r, c, created, fmt.Errorf("actions-publish: source URL differs from declared GitHub repository")
	}
	return config.Repos[0], config.Catalog, created, nil
}

func checkSource(ctx context.Context, root string, spec publishSpec, dbPath string) error {
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-C", root}, args...)...)
		data, err := cmd.Output()
		return strings.TrimSpace(string(data)), err
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil || head != spec.SourceCommit {
		return fmt.Errorf("actions-publish: source checkout is not the reviewed commit")
	}
	status, err := git("status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return fmt.Errorf("actions-publish: source checkout has uncommitted changes")
	}
	identity, err := repoidentity.Resolve(ctx, root, "")
	if err != nil {
		return err
	}
	want, err := repoidentity.CanonicalGitURL(spec.Repo.URL)
	if err != nil || identity != want {
		return fmt.Errorf("actions-publish: source checkout belongs to a different repository")
	}
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	owner, err := storage.NewRepositoryIdentityRepo(db).Get(ctx)
	if err != nil || owner != want {
		return fmt.Errorf("actions-publish: knowledge DB belongs to a different or unstamped repository")
	}
	return nil
}

func publish(ctx context.Context, forge *githubapp.HTTPForge, spec publishSpec, repo dkconfig.RepoConfig, catalog dkconfig.CatalogConfig, created time.Time, sourceDir, dbPath, reportPath, evolutionPath, output string) (receipt, error) {
	var result receipt
	if err := checkSource(ctx, sourceDir, spec, dbPath); err != nil {
		return result, err
	}
	// Validate 不调用 client；显式 nil 保证预生成图谱不会被测试发布端重写或再付费。
	// Validation uses the real runner while no LLM client is available in this job.
	r := runner.NewRunnerWithClient(nil, dkconfig.LLMConfig{})
	validation, err := r.Validate(ctx, runner.ValidateRequest{DBPath: dbPath, RepoPath: sourceDir})
	if err != nil {
		return result, err
	}
	if !validation.OK || validation.NodeCount == 0 {
		return result, fmt.Errorf("actions-publish: candidate validation failed: %v", validation.Errors)
	}
	current, err := githubapp.ReadBranchSHA(ctx, forge, repo.Owner, repo.Name, repo.Branch)
	if err != nil {
		return result, fmt.Errorf("actions-publish: read reviewed source branch: %w", err)
	}
	if current != spec.SourceCommit {
		return result, fmt.Errorf("actions-publish: reviewed source branch has advanced")
	}
	report, err := os.ReadFile(reportPath)
	if err != nil || !json.Valid(report) {
		return result, fmt.Errorf("actions-publish: missing or invalid original build report")
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return result, err
	}
	owner, name, _ := strings.Cut(catalog.Repo, "/")
	p := publisher.New(forge, nil, githubapp.RepoRef{Owner: owner, Name: name})
	input := publisher.CandidateSpec{Repo: repo, Catalog: catalog, SourceSHA: spec.SourceCommit, Fingerprint: spec.Fingerprint, ConfigDigest: spec.ConfigDigest, KGDBPath: dbPath, BuildReport: report, EvolutionDBPath: evolutionPath, BundleDir: filepath.Join(output, "bundle"), CreatedAt: created}
	first, err := p.EnsureCandidate(ctx, input)
	if err != nil {
		return result, err
	}
	second, err := p.EnsureCandidate(ctx, input)
	if err != nil {
		return result, err
	}
	if first.Release.ID != second.Release.ID || first.Manifest != second.Manifest || first.Release.Tag != second.Release.Tag {
		return result, fmt.Errorf("actions-publish: publication replay changed immutable identity")
	}
	tarDigest, err := kbbundle.DigestFile(input.BundleDir + ".tar.gz")
	if err != nil {
		return result, err
	}
	cm := kbbundle.CatalogManifest{Manifest: first.Manifest, Channel: "stable", PublishedAt: first.Manifest.CreatedAt, ReleaseTag: first.Release.Tag, AssetName: "bundle.tar.gz"}
	if err := writeJSON(filepath.Join(output, "candidate.json"), cm); err != nil {
		return result, err
	}
	return receipt{Phase: "publish", Catalog: cm, Validation: validation, Release: first.Release, TarballSHA256: kbbundle.DigestPrefix(tarDigest), PublicationReplay: true}, nil
}

func consume(ctx context.Context, auth envTokenSource, spec publishSpec, repo dkconfig.RepoConfig, catalog dkconfig.CatalogConfig, expected receipt, installDir string) (receipt, error) {
	var result receipt
	if expected.Phase != "publish" || !expected.PublicationReplay || !expected.Validation.OK || expected.Catalog.SourceCommit != spec.SourceCommit || expected.Catalog.KG != repo.KGGroup || expected.Catalog.SourceRepo != repo.Owner+"/"+repo.Name || expected.Catalog.Model != spec.Fingerprint.Model || expected.Catalog.ConfigDigest != spec.ConfigDigest || expected.Catalog.CreatedAt != spec.CreatedAt || expected.Release.Tag != expected.Catalog.ReleaseTag || expected.Release.ID <= 0 {
		return result, fmt.Errorf("actions-publish: expected publication receipt does not match specification")
	}
	token, _, err := auth.InstallToken(ctx, 0)
	if err != nil {
		return result, err
	}
	owner, name, _ := strings.Cut(catalog.Repo, "/")
	pulled, err := kbbundle.PullRemoteContext(ctx, kbbundle.PullRemoteSpec{CatalogOwner: owner, CatalogRepo: name, CatalogBranch: catalog.Branch, ManifestDir: catalog.ManifestDir, KG: repo.KGGroup, Token: token, APIBaseURL: auth.HTTPBaseURL(), InstallDir: installDir})
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(pulled.TempDir)
	if pulled.Manifest != expected.Catalog {
		return result, fmt.Errorf("actions-publish: remote catalog differs from reviewed publication receipt")
	}
	verified, err := kbbundle.Verify(pulled.Install.Current)
	if err != nil {
		return result, fmt.Errorf("actions-publish: verify installed artifact: %w", err)
	}
	if *verified != expected.Catalog.Manifest {
		return result, fmt.Errorf("actions-publish: installed artifact differs from reviewed publication")
	}
	dbPath := filepath.Join(pulled.Install.Current, "knowledge.db")
	before, err := kbbundle.DigestFile(dbPath)
	if err != nil {
		return result, err
	}
	reinstalled, err := kbbundle.Install(filepath.Join(pulled.TempDir, "bundle"), installDir)
	if err != nil {
		return result, err
	}
	after, err := kbbundle.DigestFile(dbPath)
	if err != nil || before != after || reinstalled.Current != pulled.Install.Current || reinstalled.Digest != pulled.Install.Digest {
		return result, fmt.Errorf("actions-publish: repeat install modified immutable generation")
	}
	result = expected
	result.Phase = "consume"
	result.RepeatInstallVerified = true
	result.InstalledDir = reinstalled.Current
	result.InstalledDB = dbPath
	return result, nil
}

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("actions-publish: invalid JSON %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("actions-publish: trailing JSON in %s", path)
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".e2e-receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
