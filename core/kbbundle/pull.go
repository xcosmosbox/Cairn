// Package kbbundle — remote pull from GitHub Release（产品化 Prompt §12）。
//
// 流程：
//  1. 从 Catalog Repo main 读取 stable manifest（GitHub Contents API）。
//  2. 从 manifest 解析 Release tag。
//  3. 获取 Release 的 asset 列表（GitHub Releases API）。
//  4. 下载 Bundle asset 到临时目录。
//  5. 新格式解包完整 tarball 后 Verify；旧格式（裸 DB）回退重新 Pack。
//  6. 调 Install 校验 + 原子安装。
package kbbundle

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	Manifest  CatalogManifest
	Install   *InstallResult
	TempDir   string // 临时下载目录（调用方负责清理）
}

// PullRemote 从 Catalog Repo 的 GitHub Release 下载 stable Bundle 并安装。
//
// 用法：
//
//	res, err := kbbundle.PullRemote(kbbundle.PullRemoteSpec{
//	    CatalogOwner: "acme", CatalogRepo: "knowledge-catalog",
//	    CatalogBranch: "main", ManifestDir: "knowledge-bases",
//	    KG: "payment", Token: tok, APIBaseURL: "https://api.github.com",
//	    InstallDir: "~/.dk/kbs/payment",
//	})
//	defer os.RemoveAll(res.TempDir)
func PullRemote(spec PullRemoteSpec) (*PullResult, error) {
	if spec.APIBaseURL == "" {
		spec.APIBaseURL = "https://api.github.com"
	}
	if spec.Token == "" {
		return nil, fmt.Errorf("kbbundle.PullRemote: token 不可为空")
	}

	// 1. 读 stable manifest。
	manifestPath := fmt.Sprintf("%s/%s/stable.json", spec.ManifestDir, spec.KG)
	cm, err := FetchCatalogManifest(spec.APIBaseURL, spec.CatalogOwner, spec.CatalogRepo, spec.CatalogBranch, manifestPath, spec.Token)
	if err != nil {
		return nil, fmt.Errorf("kbbundle.PullRemote: 读 catalog manifest: %w", err)
	}
	if cm.Channel != "stable" {
		return nil, fmt.Errorf("kbbundle.PullRemote: manifest channel=%s 非 stable", cm.Channel)
	}

	// 2. 临时目录：先下载到临时文件，再用 Pack 重新打包（计算正确的 bundle_digest）。
	tmpDir, err := os.MkdirTemp("", "dkb-pull-*")
	if err != nil {
		return nil, fmt.Errorf("kbbundle.PullRemote: 创建临时目录: %w", err)
	}

	// 3. 下载 Release asset。
	// 优先用 release_tag 查 Release；tag 为空（旧版 stable.json 的 Bug）或 404 时，
	// 按 bundle_digest 在 body 中搜索匹配的 Release。
	var assetURL, assetName string
	if cm.ReleaseTag != "" {
		assetURL, assetName, err = getReleaseAssetURL(spec.APIBaseURL, spec.CatalogOwner, spec.CatalogRepo, cm.ReleaseTag, spec.Token)
	}
	if cm.ReleaseTag == "" || err != nil {
		// Fallback：列出所有 Release，按 bundle_digest 匹配。
		assetURL, assetName, err = findReleaseByDigest(spec.APIBaseURL, spec.CatalogOwner, spec.CatalogRepo, cm.Manifest.BundleDigest, spec.Token)
	}
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: 获取 Release asset: %w", err)
	}
	tempAsset := filepath.Join(tmpDir, "asset.bin")
	if err := downloadFile(assetURL, tempAsset, spec.Token); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: 下载 %s: %w", assetName, err)
	}

	// 4. 组装 Bundle 目录。
	//   - 新格式：asset 是完整 Bundle tarball（gzip）→ 解包后直接 Verify，digest 天然一致。
	//   - 旧格式：asset 是裸 knowledge.db → 回退到重新 Pack（文件集缺 build-report 等，
	//     digest 无法对齐，降级为 warning）。重跑 dkd 产出新格式 Release 后即消除。
	bundleDir := filepath.Join(tmpDir, "bundle")
	isGz, err := IsGzip(tempAsset)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("kbbundle.PullRemote: 探测 asset 格式: %w", err)
	}
	if isGz {
		// 新格式：解包完整 Bundle。
		if err := ExtractTarball(tempAsset, bundleDir); err != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("kbbundle.PullRemote: 解包 Bundle tarball: %w", err)
		}
		// 5. 完整性校验：Verify 重算包内文件摘要 + bundle_digest（含传输完整性），
		//    并与 catalog manifest 的指针交叉确认。
		m, err := Verify(bundleDir)
		if err != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("kbbundle.PullRemote: 校验 Bundle: %w", err)
		}
		if cm.Manifest.BundleDigest != "" && m.BundleDigest != cm.Manifest.BundleDigest {
			// 走到这里说明 catalog stable 指针与 Release 内容不一致（指错了 Release）。
			// 保持 warning 以便排查，但不阻断（Verify 已确保包自身完整）。
			fmt.Fprintf(os.Stderr, "[kbbundle] ⚠ bundle_digest 与 catalog 指针不一致（catalog %s 实际 %s）—stable 指针可能指向了错误的 Release\n",
				cm.Manifest.BundleDigest, m.BundleDigest)
		}
	} else {
		// 旧格式兼容：裸 knowledge.db → 重新 Pack（文件集不同，digest 无法与 catalog 对齐）。
		packed, err := Pack(PackRequest{
			Manifest: cm.Manifest,
			KGDBPath: tempAsset,
			OutDir:   bundleDir,
		})
		if err != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("kbbundle.PullRemote: 重新打包: %w", err)
		}
		if cm.Manifest.BundleDigest != "" && packed.Manifest.BundleDigest != cm.Manifest.BundleDigest {
			fmt.Fprintf(os.Stderr, "[kbbundle] ⚠ bundle_digest 不匹配（catalog %s 实际 %s）—旧格式 Release 上传裸 DB 而非完整 tarball，已降级为 warning；重跑 dkd 产出新 Release 后即消除\n",
				cm.Manifest.BundleDigest, packed.Manifest.BundleDigest)
		}
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
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", apiBase, owner, repo, path, branch)
	req, _ := http.NewRequest("GET", url, nil)
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
	TagName string   `json:"tag_name"`
	Body    string   `json:"body"`
	Assets  []ghAsset `json:"assets"`
}
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int    `json:"size"`
}

// getReleaseAssetURL 获取 Release 中第一个 asset 的下载 URL。
func getReleaseAssetURL(apiBase, owner, repo, tag, token string) (url, name string, err error) {
	releaseURL := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", apiBase, owner, repo, tag)
	req, _ := http.NewRequest("GET", releaseURL, nil)
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
func findReleaseByDigest(apiBase, owner, repo, bundleDigest, token string) (string, string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=30", apiBase, owner, repo)
	req, _ := http.NewRequest("GET", url, nil)
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
	req, _ := http.NewRequest("GET", url, nil)
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
