// Package kbbundle — remote pull from GitHub Release（产品化 Prompt §12）。
//
// 流程：
//  1. 从 Catalog Repo main 读取 stable manifest（GitHub Contents API）。
//  2. 从 manifest 解析 Release tag。
//  3. 获取 Release 的 asset 列表（GitHub Releases API）。
//  4. 下载 Bundle asset 到临时目录。
//  5. 解包完整 tarball，验证全部 provenance 与 catalog 指针一致。
//  6. 调 Install 校验 + 原子安装。
package kbbundle

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PullRemoteSpec 是远程拉取的输入参数。
type PullRemoteSpec struct {
	CatalogOwner  string // Catalog Repo owner（如 "acme"）
	CatalogRepo   string // Catalog Repo name（如 "knowledge-catalog"）
	CatalogBranch string // Catalog Repo 分支（如 "main"）
	ManifestDir   string // manifest 目录（如 "knowledge-bases"）
	KG            string // KG group（如 "payment"）
	Token         string // GitHub PAT 或 installation token
	APIBaseURL    string // GitHub API base（如 "https://api.github.com"）
	InstallDir    string // 本地安装目录
}

// PullResult 是远程拉取的结果。
type PullResult struct {
	Manifest CatalogManifest
	Install  *InstallResult
	TempDir  string // 临时下载目录（调用方负责清理）
}

// PullRemote 从 Catalog Repo 的 GitHub Release 下载 stable Bundle 并安装。
//
// 用法：
//
//	res, err := kbbundle.PullRemote(kbbundle.PullRemoteSpec{
//	    CatalogOwner: "acme", CatalogRepo: "knowledge-catalog",
//	    CatalogBranch: "main", ManifestDir: "knowledge-bases",
//	    KG: "payment", Token: tok, APIBaseURL: "https://api.github.com",
//	    InstallDir: "~/.cairn/kbs/payment",
//	})
//	defer os.RemoveAll(res.TempDir)
func PullRemote(spec PullRemoteSpec) (*PullResult, error) {
	return PullRemoteContext(context.Background(), spec)
}

// PullRemoteContext 将取消/超时传递到所有 HTTP 请求，并在安装前再次检查。
// PullRemoteContext bounds the full network operation and never begins install after cancellation.
func PullRemoteContext(parent context.Context, spec PullRemoteSpec) (*PullResult, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.APIBaseURL == "" {
		spec.APIBaseURL = "https://api.github.com"
	}
	if spec.Token == "" {
		return nil, fmt.Errorf("kbbundle.PullRemote: token 不可为空")
	}

	// 1. 读 stable manifest。
	manifestPath := fmt.Sprintf("%s/%s/stable.json", spec.ManifestDir, spec.KG)
	cm, err := FetchCatalogManifestContext(ctx, spec.APIBaseURL, spec.CatalogOwner, spec.CatalogRepo, spec.CatalogBranch, manifestPath, spec.Token)
	if err != nil {
		return nil, fmt.Errorf("kbbundle.PullRemote: 读 catalog manifest: %w", err)
	}
	if err := validateStableCatalog(cm, spec.KG); err != nil {
		return nil, fmt.Errorf("kbbundle.PullRemote: %w", err)
	}

	// 2. 下载到私有临时目录后验证完整不可变产物。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "dkb-pull-*")
	if err != nil {
		return nil, fmt.Errorf("kbbundle.PullRemote: 创建临时目录: %w", err)
	}

	// 3. Catalog fixes both the exact Release and asset; missing pointers fail closed.
	assetURL, assetName, err := getReleaseAssetURLContext(ctx, spec.APIBaseURL, spec.CatalogOwner, spec.CatalogRepo, cm.ReleaseTag, spec.Token, cm.AssetName)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: 获取 Release asset: %w", err)
	}
	tempAsset := filepath.Join(tmpDir, "asset.bin")
	if err := downloadFileContext(ctx, assetURL, tempAsset, spec.Token); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: 下载 %s: %w", assetName, err)
	}

	if err := ctx.Err(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}

	// 4. Only a complete artifact can prove catalog identity. Bare DB releases
	// must be republished rather than synthesizing missing provenance locally.
	bundleDir := filepath.Join(tmpDir, "bundle")
	isGz, err := IsGzip(tempAsset)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}
	if !isGz {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: raw DB cannot prove catalog identity; republish a complete Bundle")
	}
	if err := ExtractTarball(tempAsset, bundleDir); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: unpack Bundle: %w", err)
	}
	actual, err := Verify(bundleDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: verify Bundle: %w", err)
	}
	if *actual != cm.Manifest {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: verified artifact provenance differs from stable catalog pointer")
	}

	if err := ctx.Err(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}

	// 6. Install（Verify 会校验 checksums + bundle_digest）。
	inst, err := Install(bundleDir, spec.InstallDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: 安装: %w", err)
	}

	return &PullResult{
		Manifest: *cm,
		Install:  inst,
		TempDir:  tmpDir,
	}, nil
}

// FetchCatalogManifest 从 Catalog Repo 的 Contents API 读取 stable.json。
// 导出供 MCP Server 等 external caller 直接调用（无需完整 PullRemote）。
func FetchCatalogManifest(apiBase, owner, repo, branch, path, token string) (*CatalogManifest, error) {
	return FetchCatalogManifestContext(context.Background(), apiBase, owner, repo, branch, path, token)
}

// FetchCatalogManifestContext 中止已取消的请求，并给旧调用方设置有界网络等待。
// FetchCatalogManifestContext propagates cancellation and bounds network waits.
func FetchCatalogManifestContext(parent context.Context, apiBase, owner, repo, branch, path, token string) (*CatalogManifest, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", apiBase, owner, repo, path, branch)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.raw") // 直接返回文件内容
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var cm CatalogManifest
	if err := json.Unmarshal(body, &cm); err != nil {
		return nil, fmt.Errorf("解析 manifest JSON: %w", err)
	}
	return &cm, nil
}

// ghRelease 是 GitHub Release API 响应（只取需要的字段）。
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Body    string    `json:"body"`
	Assets  []ghAsset `json:"assets"`
}
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int    `json:"size"`
}

// getReleaseAssetURL 获取 Release 中第一个 asset 的下载 URL。
func getReleaseAssetURL(apiBase, owner, repo, tag, token string, desiredAssets ...string) (url, name string, err error) {
	return getReleaseAssetURLContext(context.Background(), apiBase, owner, repo, tag, token, desiredAssets...)
}
func getReleaseAssetURLContext(ctx context.Context, apiBase, owner, repo, tag, token string, desiredAssets ...string) (url, name string, err error) {
	releaseURL := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", apiBase, owner, repo, tag)
	req, err := http.NewRequestWithContext(ctx, "GET", releaseURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", err
	}
	if len(rel.Assets) == 0 {
		return "", "", fmt.Errorf("Release %s 无 asset", tag)
	}
	if len(desiredAssets) > 0 && desiredAssets[0] != "" {
		for _, asset := range rel.Assets {
			if asset.Name == desiredAssets[0] {
				return asset.BrowserDownloadURL, asset.Name, nil
			}
		}
		return "", "", fmt.Errorf("Release %s missing catalog asset %s", tag, desiredAssets[0])
	}
	// 优先选 knowledge.db，否则取第一个。
	for _, a := range rel.Assets {
		if a.Name == "knowledge.db" || a.Name == "bundle.tar.gz" {
			return a.BrowserDownloadURL, a.Name, nil
		}
	}
	return rel.Assets[0].BrowserDownloadURL, rel.Assets[0].Name, nil
}

// findReleaseByDigest 列出所有 Release，按 bundle_digest 在 body 中搜索匹配的 Release。
// 用于 release_tag 为空（旧版 stable.json Bug）或 tag 查不到时的 fallback。
func findReleaseByDigest(apiBase, owner, repo, bundleDigest, token string, desiredAssets ...string) (string, string, error) {
	return findReleaseByDigestContext(context.Background(), apiBase, owner, repo, bundleDigest, token, desiredAssets...)
}
func findReleaseByDigestContext(ctx context.Context, apiBase, owner, repo, bundleDigest, token string, desiredAssets ...string) (string, string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=30", apiBase, owner, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", "", err
	}
	for _, rel := range releases {
		// 在 body 中搜索 bundle_digest。
		if strings.Contains(rel.Body, bundleDigest) {
			if len(rel.Assets) == 0 {
				continue // 该 Release 无 asset，跳过
			}
			if len(desiredAssets) > 0 && desiredAssets[0] != "" {
				for _, asset := range rel.Assets {
					if asset.Name == desiredAssets[0] {
						return asset.BrowserDownloadURL, asset.Name, nil
					}
				}
				continue
			}
			for _, a := range rel.Assets {
				if a.Name == "knowledge.db" || a.Name == "bundle.tar.gz" {
					return a.BrowserDownloadURL, a.Name, nil
				}
			}
			return rel.Assets[0].BrowserDownloadURL, rel.Assets[0].Name, nil
		}
	}
	return "", "", fmt.Errorf("未找到 bundle_digest=%s 对应的 Release", bundleDigest)
}

// downloadFile 下载 URL 到本地文件。
func downloadFile(url, destPath, token string) error {
	return downloadFileContext(context.Background(), url, destPath, token)
}
func downloadFileContext(ctx context.Context, url, destPath, token string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func validateStableCatalog(cm *CatalogManifest, expectedKG string) error {
	if cm == nil || cm.Channel != "stable" {
		return fmt.Errorf("catalog channel is not stable")
	}
	if cm.Format != ManifestFormat || cm.KG == "" || cm.KG != expectedKG {
		return fmt.Errorf("catalog format or KG identity differs")
	}
	if cm.SourceRepo == "" || cm.SourceRef == "" || cm.BuilderVersion == "" || cm.BuilderCommit == "" || cm.PromptSetVersion == "" || cm.Model == "" || cm.KBVersion == "" || cm.SchemaVersion < 4 {
		return fmt.Errorf("catalog provenance is incomplete; republish a complete Bundle")
	}
	if len(cm.SourceCommit) != 40 && len(cm.SourceCommit) != 64 {
		return fmt.Errorf("catalog source commit is invalid")
	}
	if _, err := hex.DecodeString(cm.SourceCommit); err != nil {
		return fmt.Errorf("catalog source commit is invalid")
	}
	for _, digest := range []string{cm.BundleDigest, cm.ConfigDigest} {
		if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
			return fmt.Errorf("catalog digest is missing or invalid")
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")); err != nil {
			return fmt.Errorf("catalog digest is invalid")
		}
	}
	if _, err := time.Parse(time.RFC3339, cm.CreatedAt); err != nil {
		return fmt.Errorf("catalog creation time is invalid")
	}
	if _, err := time.Parse(time.RFC3339, cm.PublishedAt); err != nil {
		return fmt.Errorf("catalog publication time is invalid")
	}
	if strings.TrimSpace(cm.ReleaseTag) == "" || strings.TrimSpace(cm.AssetName) == "" {
		return fmt.Errorf("catalog Release or asset pointer is missing; republish a complete Bundle")
	}
	return nil
}
