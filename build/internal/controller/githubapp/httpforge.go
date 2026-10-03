package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	coregh "github.com/xcosmosbox/cairn/core/githubapp"
)

// InstallationTokenSource 让 HTTP 传输只依赖凭证契约；生产入口仍使用 AppAuth。
// Decouple REST transport from token minting without adding a production auth bypass.
type InstallationTokenSource interface {
	InstallToken(context.Context, int64) (string, time.Time, error)
	HTTPBaseURL() string
}

var _ InstallationTokenSource = (*coregh.AppAuth)(nil)

// HTTPForge 通过 GitHub REST API 实现 Forge；生产入口使用 AppAuth。
// HTTPForge accepts the token-source contract while the daemon retains App authentication.
type HTTPForge struct {
	auth           InstallationTokenSource
	installationID int64
	http           *http.Client
}

// NewHTTPForge 构造一个指向真实 GitHub（或兼容 API）的 Forge。
// installationID 由 AppAuth.ResolveInstallation 获取。
func NewHTTPForge(auth InstallationTokenSource, installationID int64) *HTTPForge {
	return &HTTPForge{
		auth:           auth,
		installationID: installationID,
		http:           &http.Client{Timeout: 60 * time.Second},
	}
}

func (h *HTTPForge) token(ctx context.Context) (string, error) {
	tok, _, err := h.auth.InstallToken(ctx, h.installationID)
	return tok, err
}

func (h *HTTPForge) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	if err := store.CheckLease(ctx); err != nil {
		return nil, err
	}
	tok, err := h.token(ctx)
	if err != nil {
		return nil, err
	}
	var br io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		br = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.auth.HTTPBaseURL()+path, br)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := store.CheckLease(ctx); err != nil {
		return nil, err
	}
	return h.http.Do(req)
}

func (h *HTTPForge) GetBranchSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	resp, err := h.do(ctx, "GET", "/repos/"+owner+"/"+repo+"/branches/"+branch, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", httpError(resp)
	}
	var b struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := decodeAPIJSON(resp.Body, &b); err != nil {
		return "", err
	}
	if err := ValidateBranchSHA(b.Commit.SHA); err != nil {
		return "", err
	}
	return b.Commit.SHA, nil
}

func (h *HTTPForge) CreateBranch(ctx context.Context, owner, repo, branch, baseSHA string) error {
	body := map[string]string{"ref": "refs/heads/" + branch, "sha": baseSHA}
	resp, err := h.do(ctx, "POST", "/repos/"+owner+"/"+repo+"/git/refs", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 422 { // 已存在。
		return nil
	}
	if resp.StatusCode != 201 {
		return httpError(resp)
	}
	return nil
}

// apiPullRequest 保留 GitHub 的嵌套响应，所有入口统一转换，不能把 head/base 丢成零值。
// Decode the wire format once rather than treating nested API fields as the flat domain model.
type apiPullRequest struct {
	Number   int        `json:"number"`
	State    string     `json:"state"`
	Body     string     `json:"body"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	Head     struct {
		SHA   string `json:"sha"`
		Ref   string `json:"ref"`
		Label string `json:"label"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	HTMLURL        string `json:"html_url"`
	Mergeable      bool   `json:"mergeable"`
	MergeCommitSHA string `json:"merge_commit_sha"`
}

func (raw apiPullRequest) convert(owner, repo string) (PullRequest, error) {
	if raw.Number <= 0 || raw.Head.SHA == "" || raw.Head.Ref == "" || raw.Base.Ref == "" {
		return PullRequest{}, fmt.Errorf("githubapp: incomplete pull request receipt: number=%d head=%q base=%q", raw.Number, raw.Head.Ref, raw.Base.Ref)
	}
	if raw.State != "open" && raw.State != "closed" && raw.State != "merged" {
		return PullRequest{}, fmt.Errorf("githubapp: invalid pull request state: %q", raw.State)
	}
	if raw.Base.Repo != nil && raw.Base.Repo.FullName != "" && !strings.EqualFold(raw.Base.Repo.FullName, owner+"/"+repo) {
		return PullRequest{}, fmt.Errorf("githubapp: pull request base repository mismatch")
	}
	return PullRequest{Owner: owner, Repo: repo, Number: raw.Number, State: raw.State, Merged: raw.Merged || raw.MergedAt != nil || raw.State == "merged", HeadBranch: raw.Head.Ref, HeadSHA: raw.Head.SHA, BaseBranch: raw.Base.Ref, Mergeable: raw.Mergeable, HTMLURL: raw.HTMLURL, MergeCommitSHA: raw.MergeCommitSHA}, nil
}

// exactReplayMarker 按完整机器标记匹配，不能用子串把 run-1 误认成 run-10。
// Match whole marker lines/comments, including the dedicated comment added to every new proposal.
func exactReplayMarker(body, marker string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<!--") && strings.HasSuffix(line, "-->") {
			line = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "<!--"), "-->"))
		}
		if line == marker || line == "cairn-replay-marker:"+marker {
			return true
		}
	}
	return false
}

func (raw apiPullRequest) matches(spec PullRequestSpec) (bool, error) {
	if raw.Head.Ref != spec.HeadBranch || raw.Base.Ref != spec.BaseBranch || !exactReplayMarker(raw.Body, spec.RunMarker) {
		return false, nil
	}
	// 分支名在不同 fork 里可重复，必须核对响应的仓库身份。
	// Branch names alone do not identify a proposal from another fork.
	if raw.Head.Repo == nil || raw.Head.Repo.FullName == "" || raw.Base.Repo == nil || raw.Base.Repo.FullName == "" {
		return false, fmt.Errorf("githubapp: incomplete pull request repository identity")
	}
	if raw.Head.User.Login != "" && !strings.EqualFold(raw.Head.User.Login, spec.Owner) {
		return false, nil
	}
	if raw.Head.Label != "" {
		owner, branch, ok := strings.Cut(raw.Head.Label, ":")
		if !ok || !strings.EqualFold(owner, spec.Owner) || branch != spec.HeadBranch {
			return false, nil
		}
	}
	return strings.EqualFold(raw.Head.Repo.FullName, spec.Owner+"/"+spec.Repo) && strings.EqualFold(raw.Base.Repo.FullName, spec.Owner+"/"+spec.Repo), nil
}

func decodeAPIJSON(body io.Reader, target any) error {
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("githubapp: response contains multiple JSON values")
	}
	return nil
}

func hasNextPage(link string) bool {
	for _, part := range strings.Split(link, ",") {
		for _, attribute := range strings.Split(part, ";")[1:] {
			if strings.TrimSpace(attribute) == `rel="next"` {
				return true
			}
		}
	}
	return false
}

func (h *HTTPForge) EnsurePullRequest(ctx context.Context, spec PullRequestSpec) (PullRequest, error) {
	if spec.Owner == "" || spec.Repo == "" || spec.HeadBranch == "" || spec.BaseBranch == "" || strings.TrimSpace(spec.RunMarker) == "" || strings.ContainsAny(spec.RunMarker, "\r\n") {
		return PullRequest{}, fmt.Errorf("githubapp: incomplete pull request identity")
	}
	// 远端成功后丢失本地回执时，PR 可能已合并/关闭；只查 open 会重复提案。
	// Search every state and page before creating, using the immutable branch/base/machine identity.
	var found *PullRequest
	seenPages := map[string]bool{}
	for page := 1; ; page++ {
		q := url.Values{"head": {spec.Owner + ":" + spec.HeadBranch}, "base": {spec.BaseBranch}, "state": {"all"}, "per_page": {"100"}, "page": {strconv.Itoa(page)}, "sort": {"created"}, "direction": {"asc"}}
		resp, err := h.do(ctx, http.MethodGet, "/repos/"+spec.Owner+"/"+spec.Repo+"/pulls?"+q.Encode(), nil)
		if err != nil {
			return PullRequest{}, err
		}
		if resp.StatusCode != http.StatusOK {
			err := httpError(resp)
			resp.Body.Close()
			return PullRequest{}, err
		}
		var existing []apiPullRequest
		err = decodeAPIJSON(resp.Body, &existing)
		next := hasNextPage(resp.Header.Get("Link"))
		resp.Body.Close()
		if err != nil {
			return PullRequest{}, fmt.Errorf("githubapp: decode pull request page %d: %w", page, err)
		}
		if len(existing) > 0 {
			signature := fmt.Sprintf("%d:%d:%d", len(existing), existing[0].Number, existing[len(existing)-1].Number)
			if seenPages[signature] {
				return PullRequest{}, fmt.Errorf("githubapp: repeated pull request page %d", page)
			}
			seenPages[signature] = true
		}
		for _, raw := range existing {
			matches, err := raw.matches(spec)
			if err != nil {
				return PullRequest{}, err
			}
			if !matches {
				continue
			}
			pr, err := raw.convert(spec.Owner, spec.Repo)
			if err != nil {
				return PullRequest{}, err
			}
			if found == nil || pr.Number < found.Number {
				copy := pr
				found = &copy
			}
		}
		if !next && len(existing) < 100 {
			break
		}
	}
	if found != nil {
		return *found, nil
	}
	// 丰富的人类描述可能没有包含 RunMarker；该字段必须实际写入远端才能恢复。
	// Always persist an exact machine marker independently of the human-readable PR body.
	bodyText := spec.Body
	if !exactReplayMarker(bodyText, spec.RunMarker) {
		bodyText += "\n\n<!-- cairn-replay-marker:" + spec.RunMarker + " -->\n"
	}
	body := map[string]string{"title": spec.Title, "body": bodyText, "head": spec.HeadBranch, "base": spec.BaseBranch}
	resp, err := h.do(ctx, http.MethodPost, "/repos/"+spec.Owner+"/"+spec.Repo+"/pulls", body)
	if err != nil {
		return PullRequest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return PullRequest{}, httpError(resp)
	}
	var raw apiPullRequest
	if err := decodeAPIJSON(resp.Body, &raw); err != nil {
		return PullRequest{}, fmt.Errorf("githubapp: decode created pull request: %w", err)
	}
	matches, err := raw.matches(spec)
	if err != nil {
		return PullRequest{}, err
	}
	if !matches {
		return PullRequest{}, fmt.Errorf("githubapp: created pull request identity mismatch")
	}
	return raw.convert(spec.Owner, spec.Repo)
}

func (h *HTTPForge) GetPullRequest(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	if number <= 0 {
		return PullRequest{}, fmt.Errorf("githubapp: invalid pull request number %d", number)
	}
	resp, err := h.do(ctx, http.MethodGet, "/repos/"+owner+"/"+repo+"/pulls/"+strconv.Itoa(number), nil)
	if err != nil {
		return PullRequest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PullRequest{}, httpError(resp)
	}
	var raw apiPullRequest
	if err := decodeAPIJSON(resp.Body, &raw); err != nil {
		return PullRequest{}, fmt.Errorf("githubapp: decode pull request: %w", err)
	}
	if raw.Number != number {
		return PullRequest{}, fmt.Errorf("githubapp: pull request number mismatch: got %d want %d", raw.Number, number)
	}
	return raw.convert(owner, repo)
}

func (h *HTTPForge) MergePullRequest(ctx context.Context, owner, repo string, number int, method string) error {
	body := map[string]string{"merge_method": method}
	resp, err := h.do(ctx, "PUT", "/repos/"+owner+"/"+repo+"/pulls/"+fmt.Sprint(number)+"/merge", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 405 || resp.StatusCode == 409 {
		return fmt.Errorf("githubapp: PR #%d 不可合并（冲突或已合并）", number)
	}
	if resp.StatusCode != 200 {
		return httpError(resp)
	}
	return nil
}

func validateRelease(r Release, spec ReleaseSpec) error {
	if r.ID <= 0 || r.Tag != spec.Tag {
		return fmt.Errorf("githubapp: incomplete or mismatched release receipt: id=%d tag=%q", r.ID, r.Tag)
	}
	return nil
}

func (h *HTTPForge) EnsureRelease(ctx context.Context, spec ReleaseSpec) (Release, error) {
	if spec.Owner == "" || spec.Repo == "" || spec.Tag == "" {
		return Release{}, fmt.Errorf("githubapp: incomplete release identity")
	}
	resp, err := h.do(ctx, http.MethodGet, "/repos/"+spec.Owner+"/"+spec.Repo+"/releases/tags/"+url.PathEscape(spec.Tag), nil)
	if err != nil {
		return Release{}, err
	}
	if resp.StatusCode == http.StatusOK {
		var full struct {
			Release
			Assets []struct {
				Name string `json:"name"`
			} `json:"assets"`
		}
		err := decodeAPIJSON(resp.Body, &full)
		resp.Body.Close()
		if err != nil {
			return Release{}, fmt.Errorf("githubapp: decode existing release: %w", err)
		}
		r := full.Release
		if err := validateRelease(r, spec); err != nil {
			return Release{}, err
		}
		r.Owner = spec.Owner
		r.Repo = spec.Repo
		if spec.AssetName != "" {
			exists := false
			for _, asset := range full.Assets {
				if asset.Name == spec.AssetName {
					exists = true
					break
				}
			}
			if !exists {
				if err := h.uploadAsset(ctx, r.ID, spec); err != nil {
					return Release{}, err
				}
			}
		}
		return r, nil
	}
	if resp.StatusCode != http.StatusNotFound {
		err := httpError(resp)
		resp.Body.Close()
		return Release{}, err
	}
	resp.Body.Close()
	body := map[string]any{"tag_name": spec.Tag, "target_commitish": spec.Target, "name": spec.Title, "body": spec.Body, "prerelease": spec.Prelease}
	resp, err = h.do(ctx, http.MethodPost, "/repos/"+spec.Owner+"/"+spec.Repo+"/releases", body)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Release{}, httpError(resp)
	}
	var r Release
	if err := decodeAPIJSON(resp.Body, &r); err != nil {
		return Release{}, fmt.Errorf("githubapp: decode created release: %w", err)
	}
	if err := validateRelease(r, spec); err != nil {
		return Release{}, err
	}
	r.Owner = spec.Owner
	r.Repo = spec.Repo
	if spec.AssetName != "" {
		if err := h.uploadAsset(ctx, r.ID, spec); err != nil {
			return Release{}, err
		}
	}
	return r, nil
}

func (h *HTTPForge) uploadAsset(ctx context.Context, releaseID int64, spec ReleaseSpec) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	tok, err := h.token(ctx)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(spec.AssetFilePath)
	if err != nil {
		return err
	}
	// GitHub 的 asset 上传端点在 uploads.github.com（不是 api.github.com）。
	// GitHub Enterprise 则为 <host>/api/uploads。
	uploadBase := h.uploadsBaseURL()
	u := fmt.Sprintf("%s/repos/%s/%s/releases/%d/assets?name=%s",
		uploadBase, spec.Owner, spec.Repo, releaseID, url.QueryEscape(spec.AssetName))
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/octet-stream")
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return httpError(resp)
	}
	var asset struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeAPIJSON(resp.Body, &asset); err != nil {
		return fmt.Errorf("githubapp: decode uploaded asset: %w", err)
	}
	if asset.ID <= 0 || asset.Name != spec.AssetName {
		return fmt.Errorf("githubapp: incomplete or mismatched asset receipt")
	}
	return nil
}

// uploadsBaseURL 根据 API base URL 推导 asset 上传端点。
// github.com: api.github.com → uploads.github.com
// GHE: ghe.example.com/api/v3 → ghe.example.com/api/uploads
func (h *HTTPForge) uploadsBaseURL() string {
	apiBase := h.auth.HTTPBaseURL()
	if strings.Contains(apiBase, "api.github.com") {
		return "https://uploads.github.com"
	}
	// GHE: 替换 /api/v3 → /api/uploads
	if strings.Contains(apiBase, "/api/v3") {
		return strings.Replace(apiBase, "/api/v3", "/api/uploads", 1)
	}
	// 未知：回退到 api base（某些兼容平台可能共用）
	return apiBase
}

func (h *HTTPForge) GetInstallToken(ctx context.Context) (string, time.Time, error) {
	return h.auth.InstallToken(ctx, h.installationID)
}

// RemoteURL 返回真实 GitHub HTTPS 远端 URL。
func (h *HTTPForge) RemoteURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
}

func httpError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("githubapp: HTTP %d %s: %s", resp.StatusCode, resp.Request.URL.Path, string(body))
}
