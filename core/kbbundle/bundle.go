// Package kbbundle 实现不可变 KB Bundle 的打包、校验与安装（产品化 Prompt §11、§12）。
//
// Bundle 是消费者（Viewer / MCP / cairn）读取知识库的唯一不可变载体：
//   - knowledge.db          KG 主库
//   - kb-manifest.json      不可变 provenance（本包的 Manifest）
//   - build-report.json     构建报告（可选）
//   - checksums.sha256      全部文件的 sha256 摘要
//   - evolution.db          演化库（可选）
//   - evolution/manifest.json 演化 manifest（可选）
//
// Bundle 的 manifest 是不可变 provenance；Catalog Repo 中的发布 manifest 额外承载
// stable 指针。禁止为「candidate 变 stable」重新打包或覆盖原 Bundle。
package kbbundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestFormat 是 manifest 的 format 字段固定值。
// ⚠ 值 "dk-kb-bundle/v1" 是历史遗留，**永远不要改成 "cairn-kb-bundle"**：
// 已发布的 Bundle 在 kb-manifest.json 里写死了这个值，改了会让消费端的格式校验
// 全部失败。项目虽已更名为 Cairn，但 manifest 格式标识是不可变的兼容性契约。
const ManifestFormat = "dk-kb-bundle/v1"

// Manifest 是 Bundle 内的不可变 provenance（kb-manifest.json）。
type Manifest struct {
	Format               string `json:"format"`
	KG                   string `json:"kg"`
	SourceRepo           string `json:"source_repo"`
	SourceRef            string `json:"source_ref"`
	SourceCommit         string `json:"source_commit"`
	BuilderVersion       string `json:"builder_version"`
	BuilderCommit        string `json:"builder_commit"`
	SchemaVersion        int    `json:"schema_version"`
	PromptSetVersion     string `json:"prompt_set_version"`
	Model                string `json:"model"`
	ConfigDigest         string `json:"config_digest"`
	BundleDigest         string `json:"bundle_digest"`
	KBVersion            string `json:"kb_version"`
	CreatedAt            string `json:"created_at"`
	MinimumReaderVersion string `json:"minimum_reader_version"`
}

// CatalogManifest 是 Catalog Repo 中的发布 manifest（额外含 channel）。
type CatalogManifest struct {
	Manifest
	Channel     string `json:"channel"` // stable | candidate | preview
	PublishedAt string `json:"published_at"`
	ReleaseTag  string `json:"release_tag,omitempty"`
	AssetName   string `json:"asset_name,omitempty"`
}

// Bundle 是打包后的内存表示。
type Bundle struct {
	Manifest Manifest
	Dir      string   // 临时目录或安装目录
	Files    []string // 相对 Dir 的文件列表（已排序）
}

// DigestFile 计算单个文件的 sha256（hex，不带前缀）。
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DigestBytes 计算字节的 sha256（hex）。
func DigestBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// DigestPrefix 返回带 sha256: 前缀的摘要。
func DigestPrefix(hex string) string {
	if strings.HasPrefix(hex, "sha256:") {
		return hex
	}
	return "sha256:" + hex
}

// PackRequest 是 Pack 的输入。
type PackRequest struct {
	Manifest          Manifest
	KGDBPath          string // 必填
	BuildReport       []byte // 可选（build-report.json）
	EvolutionDBPath   string // 可选
	EvolutionManifest []byte // 可选
	PreparedSnapshots bool   // Only for private immutable snapshots already produced by storage.SnapshotDatabase.
	OutDir            string // 输出目录（Bundle 落盘位置）
}

// Pack 把 KG db + manifest + 可选文件打包到 OutDir，生成 checksums.sha256 与
// bundle_digest（manifest 内全部文件摘要的聚合）。返回 Bundle。
//
// bundle_digest = sha256(canonical(文件名:摘要 按文件名升序))——确定性、不可变。
func Pack(req PackRequest) (*Bundle, error) {
	if req.OutDir == "" {
		return nil, fmt.Errorf("kbbundle: OutDir 不可为空")
	}
	if req.KGDBPath == "" {
		return nil, fmt.Errorf("kbbundle: KGDBPath 不可为空")
	}
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("kbbundle: mkdir %s: %w", req.OutDir, err)
	}
	// 复制 knowledge.db。
	copyDB := func(src, dst string) error {
		if req.PreparedSnapshots {
			return copyFile(src, dst)
		}
		return SnapshotSQLite(context.Background(), src, dst)
	}
	if err := copyDB(req.KGDBPath, filepath.Join(req.OutDir, "knowledge.db")); err != nil {
		return nil, fmt.Errorf("kbbundle: 复制 knowledge.db: %w", err)
	}
	files := []string{"knowledge.db"}
	// build-report.json。
	if req.BuildReport != nil {
		if err := os.WriteFile(filepath.Join(req.OutDir, "build-report.json"), req.BuildReport, 0o644); err != nil {
			return nil, fmt.Errorf("kbbundle: 写 build-report.json: %w", err)
		}
		files = append(files, "build-report.json")
	}
	// evolution.db + evolution/manifest.json。
	if req.EvolutionDBPath != "" {
		evDir := filepath.Join(req.OutDir, "evolution")
		if err := os.MkdirAll(evDir, 0o755); err != nil {
			return nil, fmt.Errorf("kbbundle: mkdir evolution: %w", err)
		}
		if err := copyDB(req.EvolutionDBPath, filepath.Join(evDir, "evolution.db")); err != nil {
			return nil, fmt.Errorf("kbbundle: 复制 evolution.db: %w", err)
		}
		files = append(files, "evolution/evolution.db")
	}
	if req.EvolutionManifest != nil {
		evDir := filepath.Join(req.OutDir, "evolution")
		if err := os.MkdirAll(evDir, 0o755); err != nil {
			return nil, fmt.Errorf("kbbundle: mkdir evolution: %w", err)
		}
		if err := os.WriteFile(filepath.Join(evDir, "manifest.json"), req.EvolutionManifest, 0o644); err != nil {
			return nil, fmt.Errorf("kbbundle: 写 evolution/manifest.json: %w", err)
		}
		files = append(files, "evolution/manifest.json")
	}
	actualVersion, err := actualSchema(filepath.Join(req.OutDir, "knowledge.db"))
	if err != nil {
		return nil, err
	}
	if req.Manifest.SchemaVersion != 0 && req.Manifest.SchemaVersion != actualVersion {
		return nil, fmt.Errorf("kbbundle: manifest schema %d != actual DB schema %d", req.Manifest.SchemaVersion, actualVersion)
	}
	req.Manifest.SchemaVersion = actualVersion
	// 计算每个文件摘要。
	digests, err := computeDigests(req.OutDir, files)
	if err != nil {
		return nil, err
	}
	// bundle_digest = sha256(canonical(file digests) + canonical(manifest sans BundleDigest))。
	// 纳入 manifest 字段使「同 KG 内容、不同 source commit/fingerprint」的 Bundle 不碰撞。
	m := req.Manifest
	m.Format = ManifestFormat
	m.BundleDigest = "" // 自身不参与计算，避免循环。
	manifestCanonical, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("kbbundle: 序列化 manifest: %w", err)
	}
	bundleDigest, err := aggregateDigestWithManifest(digests, manifestCanonical)
	if err != nil {
		return nil, err
	}
	m.BundleDigest = DigestPrefix(bundleDigest)
	// 写 kb-manifest.json（manifest 不参与自身摘要计算，避免自引用）。
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("kbbundle: 序列化 manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(req.OutDir, "kb-manifest.json"), mb, 0o644); err != nil {
		return nil, fmt.Errorf("kbbundle: 写 kb-manifest.json: %w", err)
	}
	files = append(files, "kb-manifest.json")
	// checksums.sha256（含 manifest 自身）。
	allDigests, err := computeDigests(req.OutDir, files)
	if err != nil {
		return nil, err
	}
	if err := writeChecksums(req.OutDir, allDigests); err != nil {
		return nil, err
	}
	files = append(files, "checksums.sha256")
	sort.Strings(files)
	return &Bundle{Manifest: m, Dir: req.OutDir, Files: files}, nil
}

// Verify 校验目录中的 Bundle：重算所有文件摘要与 checksums.sha256 比对，
// 并校验 manifest 中的 bundle_digest 与文件摘要聚合一致。返回 manifest。
func Verify(dir string) (*Manifest, error) {
	manifestPath := filepath.Join(dir, "kb-manifest.json")
	mb, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("kbbundle: 读取 manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return nil, fmt.Errorf("kbbundle: 解析 manifest: %w", err)
	}
	if m.Format != ManifestFormat {
		return nil, fmt.Errorf("kbbundle: manifest format 非法 %q", m.Format)
	}
	// 读 checksums.sha256。
	stored, err := readChecksums(dir)
	if err != nil {
		return nil, err
	}
	for _, required := range []string{"knowledge.db", "kb-manifest.json"} {
		if _, ok := stored[required]; !ok {
			return nil, fmt.Errorf("kbbundle: required artifact %s is not covered by checksums", required)
		}
	}
	// 重算并比对（checksums 中的文件 + manifest 自身）。
	for name, expected := range stored {
		actual, err := DigestFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("kbbundle: 校验 %s: %w", name, err)
		}
		if actual != expected {
			return nil, fmt.Errorf("kbbundle: %s 摘要不匹配（期望 %.12s 实际 %.12s）", name, expected, actual)
		}
	}
	// 校验 bundle_digest（不含 manifest 自身与 checksums）。
	nonManifest := make([]string, 0, len(stored))
	for name := range stored {
		if name != "kb-manifest.json" && name != "checksums.sha256" {
			nonManifest = append(nonManifest, name)
		}
	}
	digests := map[string]string{}
	for _, name := range nonManifest {
		digests[name] = stored[name]
	}
	// 重构 manifest（sans BundleDigest）参与聚合。
	mNoDigest := m
	mNoDigest.BundleDigest = ""
	manifestCanonical, err := json.Marshal(mNoDigest)
	if err != nil {
		return nil, fmt.Errorf("kbbundle: 序列化 manifest: %w", err)
	}
	agg, err := aggregateDigestWithManifest(digests, manifestCanonical)
	if err != nil {
		return nil, err
	}
	if DigestPrefix(agg) != m.BundleDigest {
		return nil, fmt.Errorf("kbbundle: bundle_digest 不匹配（manifest %s 实际 %s）", m.BundleDigest, DigestPrefix(agg))
	}
	version, err := actualSchema(filepath.Join(dir, "knowledge.db"))
	if err != nil {
		return nil, fmt.Errorf("kbbundle: actual database: %w", err)
	}
	if version != m.SchemaVersion {
		return nil, fmt.Errorf("kbbundle: manifest schema %d != actual DB schema %d", m.SchemaVersion, version)
	}
	return &m, nil
}

// InstallResult 是安装结果。
type InstallResult struct {
	InstallDir string
	Current    string // current 指向的版本目录（绝对路径）
	Previous   string // 上一版目录（用于回滚；空表示首次）
	Digest     string
}

// Install 把 srcDir 中的 Bundle 校验后原子安装到 installDir/<digest>，
// 并把 installDir/current 符号链接切换过去，保留上一版用于回滚。
func Install(srcDir, installDir string) (*InstallResult, error) {
	var err error
	installDir, err = filepath.Abs(installDir)
	if err != nil {
		return nil, err
	}
	m, err := Verify(srcDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return nil, fmt.Errorf("kbbundle: mkdir install %s: %w", installDir, err)
	}
	// 版本目录 = installDir/<digest 短形>。
	short := strings.TrimPrefix(m.BundleDigest, "sha256:")[:16]
	verDir := filepath.Join(installDir, short)

	// Published generations are immutable: readers may retain an older directory
	// while current moves forward or back. Never rewrite an existing generation.
	if info, err := os.Lstat(verDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("kbbundle: existing generation is not an ordinary directory")
		}
		existing, err := Verify(verDir)
		if err != nil {
			return nil, fmt.Errorf("kbbundle: existing generation is invalid: %w", err)
		}
		if *existing != *m {
			return nil, fmt.Errorf("kbbundle: existing generation identity differs")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		stage, err := os.MkdirTemp(installDir, ".install-generation-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stage)
		for _, name := range []string{"knowledge.db", "kb-manifest.json", "build-report.json", "checksums.sha256"} {
			src := filepath.Join(srcDir, name)
			if _, err := os.Stat(src); err == nil {
				if err := copyFile(src, filepath.Join(stage, name)); err != nil {
					return nil, fmt.Errorf("kbbundle: install %s: %w", name, err)
				}
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		evSrc := filepath.Join(srcDir, "evolution")
		if _, err := os.Stat(evSrc); err == nil {
			if err := copyTree(evSrc, filepath.Join(stage, "evolution")); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		staged, err := Verify(stage)
		if err != nil {
			return nil, err
		}
		if *staged != *m {
			return nil, fmt.Errorf("kbbundle: source changed while staging installation")
		}
		if err := syncDirectoryTree(stage); err != nil {
			return nil, err
		}
		if err := os.Rename(stage, verDir); err != nil {
			// A concurrent installer may already have published the same generation.
			existing, verifyErr := Verify(verDir)
			if verifyErr != nil || *existing != *m {
				return nil, fmt.Errorf("kbbundle: publish generation: %w", err)
			}
		}
	}
	if err := syncDirectory(installDir); err != nil {
		return nil, err
	}
	currentLink := filepath.Join(installDir, "current")
	prev, _ := os.Readlink(currentLink)
	linkStage, err := os.MkdirTemp(installDir, ".install-current-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(linkStage)
	tmpLink := filepath.Join(linkStage, "current")
	if err := os.Symlink(verDir, tmpLink); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpLink, currentLink); err != nil {
		return nil, fmt.Errorf("kbbundle: switch current: %w", err)
	}
	directory, err := os.Open(installDir)
	if err != nil {
		return nil, err
	}
	err = directory.Sync()
	directory.Close()
	if err != nil {
		return nil, err
	}
	return &InstallResult{
		InstallDir: installDir,
		Current:    verDir,
		Previous:   prev,
		Digest:     m.BundleDigest,
	}, nil
}

// ─── 内部工具 ────────────────────────────────────────────────────

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

func computeDigests(base string, files []string) (map[string]string, error) {
	out := make(map[string]string, len(files))
	for _, name := range files {
		d, err := DigestFile(filepath.Join(base, name))
		if err != nil {
			return nil, fmt.Errorf("kbbundle: 摘要 %s: %w", name, err)
		}
		out[name] = d
	}
	return out, nil
}

// aggregateDigest 对 (文件名 \n 摘要) 按文件名升序拼接后取 sha256。
func aggregateDigest(digests map[string]string) (string, error) {
	names := make([]string, 0, len(digests))
	for n := range digests {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\n%s\n", n, digests[n])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// aggregateDigestWithManifest 在文件摘要聚合基础上追加 manifest 规范序列化，
// 使 manifest 元数据差异反映到 bundle_digest（避免同内容碰撞）。
func aggregateDigestWithManifest(digests map[string]string, manifestCanonical []byte) (string, error) {
	names := make([]string, 0, len(digests))
	for n := range digests {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\n%s\n", n, digests[n])
	}
	h.Write(manifestCanonical)
	h.Write([]byte("\n"))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeChecksums(base string, digests map[string]string) error {
	names := make([]string, 0, len(digests))
	for n := range digests {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", digests[n], n)
	}
	return os.WriteFile(filepath.Join(base, "checksums.sha256"), []byte(b.String()), 0o644)
}

func readChecksums(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "checksums.sha256"))
	if err != nil {
		return nil, fmt.Errorf("kbbundle: 读取 checksums.sha256: %w", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 格式: <hex>  <filename>
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("kbbundle: checksums 行格式非法 %q", line)
		}
		name := parts[1]
		if name == "." || filepath.IsAbs(name) || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(name)) != name || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("kbbundle: unsafe checksum path %q", name)
		}
		if _, duplicate := out[name]; duplicate {
			return nil, fmt.Errorf("kbbundle: duplicate checksum path %q", name)
		}
		if len(parts[0]) != 64 {
			return nil, fmt.Errorf("kbbundle: invalid checksum for %s", name)
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return nil, err
		}
		path := dir
		for _, component := range strings.Split(name, "/") {
			path = filepath.Join(path, component)
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("kbbundle: checksum path traverses symlink: %s", name)
			}
		}
		out[name] = parts[0]
	}
	return out, nil
}

// ─── Tarball 打包 / 解包（完整 Bundle 分发，产品化 Prompt §11）────────────

// PackTarball 把 Pack 产出的 Bundle 目录（srcDir）打包成 gzip 压缩的 tar，写到 outPath。
// 包含目录下全部文件（knowledge.db / kb-manifest.json / build-report.json /
// checksums.sha256 / evolution/*），tar 内路径为相对 srcDir 的形式并按名升序，
// 保证确定性打包。
//
// 若 outPath 位于 srcDir 内，会自动跳过自身，避免把 tarball 打进它自己。
func PackTarball(srcDir, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("kbbundle: mkdir tarball 输出目录: %w", err)
	}
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return err
	}
	// 收集文件并排序，保证确定性打包。
	var files []string
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if abs == outAbs {
			return nil // 跳过 tarball 自身
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("kbbundle: 遍历 bundle 目录: %w", err)
	}
	sort.Strings(files)

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("kbbundle: 创建 tarball: %w", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, path := range files {
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel) // tar 内统一用 / 分隔
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:    rel,
			Mode:    0o644,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("kbbundle: 写 tar header %s: %w", rel, err)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, in); err != nil {
			in.Close()
			return fmt.Errorf("kbbundle: 写 tar 内容 %s: %w", rel, err)
		}
		in.Close()
	}
	return nil
}

// ExtractTarball 把 gzip 压缩的 tar（tarPath）解包到 destDir。
// 做路径安全校验，防止 tar 条目逃逸出 destDir（zip-slip）。
func ExtractTarball(tarPath, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("kbbundle: mkdir 解包目录: %w", err)
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("kbbundle: 打开 tarball: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("kbbundle: gzip reader: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("kbbundle: 读 tar: %w", err)
		}
		// 路径安全：清理并确保解包目标在 destDir 内。
		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))
		targetAbs, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if targetAbs != destAbs && !strings.HasPrefix(targetAbs, destAbs+string(os.PathSeparator)) {
			return fmt.Errorf("kbbundle: tar 条目越界 %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("kbbundle: 写解包文件 %s: %w", hdr.Name, err)
			}
			out.Close()
		default:
			// 忽略符号链接等其他类型（Bundle 不应包含）。
		}
	}
	return nil
}

// IsGzip 探测文件是否为 gzip（magic 0x1f 0x8b），用于区分新格式（完整 tarball）
// 与旧格式（裸 knowledge.db）asset，实现过渡期兼容。
func IsGzip(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var magic [2]byte
	n, err := io.ReadFull(f, magic[:])
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil // 文件太小，肯定不是 gzip
		}
		return false, err
	}
	return n == 2 && magic[0] == 0x1f && magic[1] == 0x8b, nil
}

// syncDirectoryTree persists newly copied file names before publishing their parent link.
func syncDirectoryTree(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		f, err := os.Open(dirs[i])
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
