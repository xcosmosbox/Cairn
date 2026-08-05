package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	coregh "github.com/xcosmosbox/cairn/core/githubapp"
)

// HTTPForge 通过 GitHub REST API 实现 Forge（生产用）。
// 需配合 core/githubapp.AppAuth 提供 installation token。
type HTTPForge struct {
	auth          *coregh.AppAuth
	installationID int64
	http          *http.Client
}

// NewHTTPForge 构造一个指向真实 GitHub（或兼容 API）的 Forge。
// installationID 由 AppAuth.ResolveInstallation 获取。
func NewHTTPForge(auth *coregh.AppAuth, installationID int64) *HTTPForge {
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
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
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

func (h *HTTPForge) EnsurePullRequest(ctx context.Context, spec PullRequestSpec) (PullRequest, error) {
	// 幂等：先按 head/base 查找已存在 PR。
	resp, err := h.do(ctx, "GET", "/repos/"+spec.Owner+"/"+spec.Repo+"/pulls?head="+spec.Owner+":"+spec.HeadBranch+"&state=open", nil)
	if err != nil {
		return PullRequest{}, err
	}
	var existing []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Head   struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&existing); err != nil {
		resp.Body.Close()
		return PullRequest{}, err
	}
	resp.Body.Close()
	if len(existing) > 0 {
		return PullRequest{
			Owner: spec.Owner, Repo: spec.Repo, Number: existing[0].Number,
			HeadBranch: existing[0].Head.Ref, HeadSHA: existing[0].Head.SHA,
			BaseBranch: existing[0].Base.Ref, State: existing[0].State,
			HTMLURL: existing[0].HTMLURL,
		}, nil
	}
	// 创建。
	body := map[string]string{"title": spec.Title, "body": spec.Body, "head": spec.HeadBranch, "base": spec.BaseBranch}
	resp2, err := h.do(ctx, "POST", "/repos/"+spec.Owner+"/"+spec.Repo+"/pulls", body)
	if err != nil {
		return PullRequest{}, err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 201 {
		return PullRequest{}, httpError(resp2)
	}
	var pr PullRequest
	json.NewDecoder(resp2.Body).Decode(&pr)
	pr.Owner = spec.Owner
	pr.Repo = spec.Repo
	return pr, nil
}

func (h *HTTPForge) GetPullRequest(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	resp, err := h.do(ctx, "GET", "/repos/"+owner+"/"+repo+"/pulls/"+fmt.Sprint(number), nil)
	if err != nil {
		return PullRequest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return PullRequest{}, httpError(resp)
	}
	var raw struct {
		Number  int    `json:"number"`
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
		Head    struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		HTMLURL        string `json:"html_url"`
		Mergeable      bool   `json:"mergeable"`
		MergeCommitSHA string `json:"merge_commit_sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return PullRequest{}, err
	}
	return PullRequest{
		Owner: owner, Repo: repo, Number: raw.Number, State: raw.State, Merged: raw.Merged,
		HeadBranch: raw.Head.Ref, HeadSHA: raw.Head.SHA, BaseBranch: raw.Base.Ref,
		Mergeable: raw.Mergeable, HTMLURL: raw.HTMLURL, MergeCommitSHA: raw.MergeCommitSHA,
	}, nil
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

func (h *HTTPForge) EnsureRelease(ctx context.Context, spec ReleaseSpec) (Release, error) {
	// 先查 tag 是否已存在。
	resp, err := h.do(ctx, "GET", "/repos/"+spec.Owner+"/"+spec.Repo+"/releases/tags/"+spec.Tag, nil)
	if err != nil {
		return Release{}, err
	}
	if resp.StatusCode == 200 {
		// tag 已存在。tag 已内嵌 bundle_digest，故「同 tag 即同内容」。
		var full struct {
			Release
			Assets []struct {
				Name string `json:"name"`
			} `json:"assets"`
		}
		json.NewDecoder(resp.Body).Decode(&full)
		resp.Body.Close()
		r := full.Release
		r.Owner = spec.Owner
		r.Repo = spec.Repo
		// 不可变约束：仅当同名 asset 尚不存在时才补传（应对上次上传中断）；
		// 已存在则直接复用，绝不覆盖——保证 Release 一经发布即冻结。
		if spec.AssetName != "" {
			exists := false
			for _, a := range full.Assets {
				if a.Name == spec.AssetName {
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
	resp.Body.Close()
	// 创建。
	body := map[string]any{
		"tag_name":   spec.Tag,
		"target":     spec.Target,
		"name":       spec.Title,
		"body":       spec.Body,
		"prerelease": spec.Prelease,
	}
	resp2, err := h.do(ctx, "POST", "/repos/"+spec.Owner+"/"+spec.Repo+"/releases", body)
	if err != nil {
		return Release{}, err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 201 {
		return Release{}, httpError(resp2)
	}
	var r Release
	json.NewDecoder(resp2.Body).Decode(&r)
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
		uploadBase, spec.Owner, spec.Repo, releaseID, spec.AssetName)
	req, _ := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(data))
	req.Header.Set("Authorization", "token "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("githubapp: 上传 asset 失败 %d: %s", resp.StatusCode, string(body))
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
