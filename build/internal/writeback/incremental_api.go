// Package writeback 的本文件是增量流水线（第二块地基）复用回写能力的公开 API。
//
// 设计约束（决策总账）：
//   - 复用而非另造：增量回写（I-9）复用本包的渲染（renderDoc/renderFullBlock/
//     renderMirrorBlock）与 sidecar 读写（buildSidecar/writeSidecar），不复制实现；
//   - R6 整篇替换：RewriteDoc / RewritePrimary 都是整篇覆盖写；
//   - R7 只读区权威在 KG：调用方传入的 NodeUpdate 必须已是 KG 权威值，
//     本文件不做任何内容判断，只负责渲染 + 落盘 + sidecar baseline 更新；
//   - C1 hash 一致性（坑点 4）：NormalizeEditable / HashEditable 与 sidecar 写入
//     用的是同一套规范化与 hash 函数（sha256Hex + 占位符常量），增量 diff 必须
//     用这两个函数比对，绝不能各写一份。
//
// This file is the public API by which the incremental pipeline (second block)
// reuses the write-back machinery: rendering and sidecar writing are reused, not
// reimplemented. Callers pass KG-authoritative NodeUpdate values; this file only
// renders, writes (whole-file replace, R6), and refreshes sidecar baselines.
package writeback

import (
	"context"
	"errors"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ——————————————————————————————————————————————————————————————————————————————
// 可编辑正文的规范化与 hash（C1 判定的单一事实源）
// Editable-text normalization & hashing (single source of truth for C1)
// ——————————————————————————————————————————————————————————————————————————————

// NormalizeEditable 把从 md 块中提取的 summary/description 正文规范化为 KG 语义值：
//  1. 去掉尾部换行/空行（渲染时正文后固定补 "\n\n" 或 "\n"，人编辑也可能增减尾部空行，
//     这些都不是实质内容变化）；
//  2. 占位符（"（暂无摘要）" / "（暂无详述）"）映射回空串——渲染用 nonEmpty 把空串
//     显示为占位符，sidecar baseline 却是空串的 hash，必须对称映射（坑点 4）。
//
// 注意：不做首部 trim——渲染时正文从标记行下一行原样开始，首部即原文首部。
//
// NormalizeEditable normalizes summary/description text extracted from an md block
// into the KG semantic value: trailing blank lines are stripped, and the empty
// placeholders map back to "" (render shows placeholders for empty strings, but
// the sidecar baseline hashes the empty string).
func NormalizeEditable(s string) string {
	s = strings.TrimRight(s, "\n")
	s = strings.TrimRight(s, " \t\n") // 尾行可能带尾随空白，统一去掉 / strip trailing whitespace-only tail
	if s == emptySummaryPlaceholder || s == emptyDescriptionPlaceholder {
		return ""
	}
	return s
}

// HashEditable 计算可编辑正文的 baseline hash，与 sidecar 写入时完全一致：
// 同一 sha256Hex 函数（坑点 4：diff 侧必须复用本函数，绝不能另写一份）。
// 调用方应先对提取文本做 NormalizeEditable，再算 hash 与 sidecar 的
// summary_hash/description_hash 比对。
//
// HashEditable computes the editable-text baseline hash, identical to what the
// sidecar writer uses. Callers must NormalizeEditable before hashing.
func HashEditable(s string) string {
	return sha256Hex(s)
}

// DisplaySummary 返回 summary 在 md 中的规范展示文本。diff 侧用它校验镜像块的
// canonical 只读行，避免复制空摘要占位符常量。
// DisplaySummary returns the canonical markdown display for a summary.
func DisplaySummary(s string) string {
	return nonEmpty(s, emptySummaryPlaceholder)
}

// ——————————————————————————————————————————————————————————————————————————————
// 原子写 / Atomic writes（问题 5：写失败不得留下半写状态）
// ——————————————————————————————————————————————————————————————————————————————

// atomicWriteFile 原子写入文件：先写同目录临时文件，再 rename 覆盖目标。
// 任一中间步骤失败都不会留下「写了一半」的目标文件——下一轮增量仍可识别旧内容并重试。
//
// atomicWriteFile writes via a temp file in the same directory + rename, so a
// mid-write failure never leaves a half-written target file.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return atomicWriteFileContext(context.Background(), path, data, perm)
}

func atomicWriteFileContext(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".kg-atomic-*")
	if err != nil {
		return fmt.Errorf("writeback: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// 任何失败都清理临时文件（rename 成功后 Remove 是 no-op）。
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writeback: write temp %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writeback: close temp %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("writeback: chmod temp %s: %w", tmpName, err)
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writeback: rename %s → %s: %w", tmpName, path, err)
	}
	return syncDir(dir)
}

// ——————————————————————————————————————————————————————————————————————————————
// NodeUpdate — 增量回写的 node 视图（KG 权威值）
// ——————————————————————————————————————————————————————————————————————————————

// NodeUpdate 是增量回写（I-9）渲染单个 node 块所需的全部 KG 权威值。
// 与私有 nodeView 同构；增量编排器从 KG（nodes + node_sources）装配后传入。
//
// NodeUpdate carries all KG-authoritative values needed to render one node block
// during incremental write-back (I-9). Isomorphic to the private nodeView.
type NodeUpdate struct {
	UUID          string   // node UUID（只读锚点）/ node UUID (read-only anchor)
	FileSlug      string   // LLM 生成的可读 slug（用于 _shared 文件名）/ LLM slug for _shared filename
	Tag           string   // "Entity" | "Concept"（大小写不敏感，渲染时归一小写）
	Name          string   // 只读 / read-only
	Domain        string   // 展示用中文名 / display name
	Subdomain     string   // 展示用中文名 / display name
	DomainSlug    string   // _shared 路径用 / for _shared path
	SubdomainSlug string   // 归属展示与 alias 用
	Summary       string   // 可编辑（KG 权威值）/ editable (KG-authoritative)
	Description   string   // 可编辑（KG 权威值）/ editable (KG-authoritative)
	Members       []string // 04 id 血缘（只读）/ 04 id lineage (read-only)
	Shared        bool     // 是否共享（来源文档 ≥2）/ shared (≥2 source docs)
	SourceFiles   []string // distinct 来源文档（镜像渲染）/ distinct source docs
	SpanStart     int      // 排序 span（1-based 闭区间起；0=无）/ ordering span start
	SpanEnd       int      // 排序 span（1-based 闭区间止；0=无）/ ordering span end
	Provenance    string   // sidecar provenance；空串回退 llm_inferred
}

// toNodeView 把公开 NodeUpdate 转为私有 nodeView，复用同一渲染管线。
// toNodeView converts the public NodeUpdate into the private nodeView.
func (u NodeUpdate) toNodeView() nodeView {
	v := nodeView{
		UUID:          u.UUID,
		FileSlug:      u.FileSlug,
		Tag:           u.Tag,
		Name:          u.Name,
		Domain:        u.Domain,
		Subdomain:     u.Subdomain,
		DomainSlug:    u.DomainSlug,
		SubdomainSlug: u.SubdomainSlug,
		Summary:       u.Summary,
		Description:   u.Description,
		Members:       append([]string(nil), u.Members...),
		Shared:        u.Shared,
		SourceFiles:   append([]string(nil), u.SourceFiles...),
		Provenance:    u.Provenance,
	}
	if u.SpanStart > 0 {
		v.Span = &span{StartLine: u.SpanStart, EndLine: u.SpanEnd}
	}
	return v
}

// ——————————————————————————————————————————————————————————————————————————————
// RewriteDoc / RewritePrimary — 增量回写落盘入口
// ——————————————————————————————————————————————————————————————————————————————

// RewriteDoc 对单篇受影响文档做增量回写：把该文档应包含的全部 node 渲染为
// 完整块（非 shared）/ 镜像块（shared），整篇覆盖写 md（R6）+ 重写 sidecar baseline。
// nodes 为空时渲染为仅含头注释的空文档（该文档所有块都被删的场景）。
// docRelPath 相对 repoRoot；返回写盘错误（调用方按部分失败降级处理）。
//
// RewriteDoc incrementally rewrites one affected doc: renders every node that
// belongs in it (full blocks for non-shared, mirror blocks for shared), replaces
// the whole md (R6), and rewrites the sidecar baseline.
func RewriteDoc(repoRoot, docRelPath string, nodes []NodeUpdate, generatedAt time.Time) error {
	views := make([]nodeView, 0, len(nodes))
	for _, u := range nodes {
		views = append(views, u.toNodeView())
	}
	mdContent := renderDoc(views, isMirrorForDoc)

	mdAbsPath, pathErr := safeRepoPath(repoRoot, docRelPath)
	if pathErr != nil {
		return pathErr
	}
	if err := os.MkdirAll(filepath.Dir(mdAbsPath), 0o755); err != nil {
		return fmt.Errorf("writeback: mkdir %s: %w", filepath.Dir(mdAbsPath), err)
	}
	sf := buildSidecar(docRelPath, mdContent, views, generatedAt)
	return writeDocPair(mdAbsPath, []byte(mdContent), sf)
}

// RenderDoc 只渲染不写盘：返回该文档（含头注释）的整篇 md 文本。
// 增量回写（I-9）用它做「渲染结果 vs 现文件」字节比对——内容不变则不写盘
// （R9 最小改动：未涉及文档的字节、未变化文档的 mtime 都保持不动）。
//
// RenderDoc renders the whole md content without writing, so the incremental
// write-back can byte-compare and skip no-op writes (R9 minimality).
func RenderDoc(nodes []NodeUpdate) string {
	views := make([]nodeView, 0, len(nodes))
	for _, u := range nodes {
		views = append(views, u.toNodeView())
	}
	return renderDoc(views, isMirrorForDoc)
}

// RenderPrimary 只渲染一个 shared node 的 primary 文档内容（不写盘），
// 供 I-9 字节比对后决定是否写盘。
// RenderPrimary renders a shared node's primary doc content without writing.
func RenderPrimary(u NodeUpdate) string {
	return docHeaderComment + "\n\n" + renderFullBlock(u.toNodeView()) + "\n"
}

// WriteDocContent 把「已渲染的 md 内容」整篇覆盖写入并同步重写 sidecar
// （与 RewriteDoc 的区别：内容已由 RenderDoc 算出并完成字节比对，避免重复渲染）。
// WriteDocContent writes pre-rendered md content and its sidecar (used after a
// byte-compare decided the write is necessary).
func WriteDocContent(repoRoot, docRelPath, mdContent string, nodes []NodeUpdate, generatedAt time.Time) error {
	return WriteDocContentContext(context.Background(), repoRoot, docRelPath, mdContent, nodes, generatedAt)
}

func WriteDocContentContext(ctx context.Context, repoRoot, docRelPath, mdContent string, nodes []NodeUpdate, generatedAt time.Time) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	views := make([]nodeView, 0, len(nodes))
	for _, u := range nodes {
		views = append(views, u.toNodeView())
	}
	mdAbsPath, pathErr := safeRepoPath(repoRoot, docRelPath)
	if pathErr != nil {
		return pathErr
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(mdAbsPath), 0o755); err != nil {
		return fmt.Errorf("writeback: mkdir %s: %w", filepath.Dir(mdAbsPath), err)
	}
	sf := buildSidecar(docRelPath, mdContent, views, generatedAt)
	return writeDocPairContext(ctx, mdAbsPath, []byte(mdContent), sf)
}

// WritePrimaryContent 把「已渲染的 primary 内容」整篇覆盖写入并同步重写 sidecar。
// WritePrimaryContent writes pre-rendered primary content and its sidecar.
func WritePrimaryContent(repoRoot string, u NodeUpdate, primaryContent string, generatedAt time.Time) error {
	return WritePrimaryContentContext(context.Background(), repoRoot, u, primaryContent, generatedAt)
}

func WritePrimaryContentContext(ctx context.Context, repoRoot string, u NodeUpdate, primaryContent string, generatedAt time.Time) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	v := u.toNodeView()
	primaryRel := primaryFilePath(v)
	primaryAbs, pathErr := safeRepoPath(repoRoot, primaryRel)
	if pathErr != nil {
		return pathErr
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(primaryAbs), 0o755); err != nil {
		return fmt.Errorf("writeback: mkdir primary dir %s: %w", filepath.Dir(primaryAbs), err)
	}
	sf := buildSidecar(primaryRel, primaryContent, []nodeView{v}, generatedAt)
	return writeDocPairContext(ctx, primaryAbs, []byte(primaryContent), sf)
}

// SidecarStale 报告一篇文档的 sidecar 是否与给定 md 内容/节点视图不一致
// （缺失、损坏、doc_hash 或任一 node 元数据不匹配 → true）。增量回写的字节比对短路
// （md 未变 → 跳过写盘）必须同时确认 sidecar 也是新鲜的——否则「md 已写好但
// sidecar 写失败」的现场会永远跳过重试；members/shared/provenance/span 等只读
// 元数据变化但 md 字节不变时，也必须刷新 baseline。
//
// SidecarStale reports whether a doc's sidecar is missing, corrupt, or differs
// from the complete expected baseline (document hash plus all node metadata).
func SidecarStale(repoRoot, docRelPath, mdContent string, nodes []NodeUpdate) bool {
	sc, err := ReadSidecar(filepath.Join(repoRoot, docRelPath))
	if err != nil {
		return true
	}
	views := make([]nodeView, 0, len(nodes))
	for _, u := range nodes {
		views = append(views, u.toNodeView())
	}
	expected := buildSidecar(docRelPath, mdContent, views, time.Time{})
	return sc.SchemaVersion != expected.SchemaVersion ||
		sc.Doc != expected.Doc ||
		sc.DocHash != expected.DocHash ||
		!sidecarNodesEqual(sc.Nodes, expected.Nodes)
}

// SidecarMetadataMatches reports whether the read-only node metadata and the
// editable baseline hashes in a sidecar agree with KG-authoritative views.
//
// The document hash and generated-at timestamp are deliberately excluded.  A
// human may legitimately change the markdown editable areas (which changes
// doc_hash), and an unresolved C4 document may carry preserved prose that KG
// cannot reconstruct.  Callers use this check before trusting a sidecar for
// diff classification; a mismatch must therefore fail closed and trigger a
// KG-authoritative rewrite.
func SidecarMetadataMatches(sf SidecarFile, docRelPath string, nodes []NodeUpdate) bool {
	views := make([]nodeView, 0, len(nodes))
	for _, u := range nodes {
		views = append(views, u.toNodeView())
	}
	expected := buildSidecar(docRelPath, "", views, time.Time{})
	if sf.SchemaVersion != expected.SchemaVersion || sf.Doc != expected.Doc {
		return false
	}
	// Node order is a rendering detail, not an identity signal.  Compare by
	// UUID so a harmless YAML reorder cannot manufacture a false C3.
	if len(sf.Nodes) != len(expected.Nodes) {
		return false
	}
	gotByUUID := make(map[string]sidecarNode, len(sf.Nodes))
	for _, n := range sf.Nodes {
		if _, exists := gotByUUID[n.UUID]; exists {
			return false
		}
		gotByUUID[n.UUID] = n
	}
	for _, want := range expected.Nodes {
		got, ok := gotByUUID[want.UUID]
		if !ok || !sidecarNodeMetadataEqual(got, want) {
			return false
		}
	}
	return true
}

func sidecarNodeMetadataEqual(a, b sidecarNode) bool {
	return a.UUID == b.UUID && a.FileSlug == b.FileSlug && a.Tag == b.Tag && a.Name == b.Name &&
		a.Domain == b.Domain && a.Subdomain == b.Subdomain &&
		a.DomainSlug == b.DomainSlug && a.SubdomainSlug == b.SubdomainSlug &&
		a.Shared == b.Shared && a.SummaryHash == b.SummaryHash &&
		a.DescriptionHash == b.DescriptionHash && a.Provenance == b.Provenance &&
		stringSlicesEqual(a.Members, b.Members) && sidecarSpansEqual(a.Span, b.Span)
}

// sidecarNodesEqual 比较完整 node baseline，同时把 nil/[] 空切片视为等价（YAML
// round-trip 会把 nil slice 解码为空 slice，不能因此造成永久 stale）。
func sidecarNodesEqual(a, b []sidecarNode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.UUID != y.UUID || x.FileSlug != y.FileSlug || x.Tag != y.Tag || x.Name != y.Name ||
			x.Domain != y.Domain || x.Subdomain != y.Subdomain ||
			x.DomainSlug != y.DomainSlug || x.SubdomainSlug != y.SubdomainSlug ||
			x.Shared != y.Shared || x.SummaryHash != y.SummaryHash ||
			x.DescriptionHash != y.DescriptionHash || x.Provenance != y.Provenance ||
			!stringSlicesEqual(x.Members, y.Members) || !sidecarSpansEqual(x.Span, y.Span) {
			return false
		}
	}
	return true
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sidecarSpansEqual(a, b *sidecarSpan) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.StartLine == b.StartLine && a.EndLine == b.EndLine && a.Quote == b.Quote
}

// WriteSidecarForDoc 只重写一篇文档的 sidecar（md 已是最新、sidecar 需要刷新时用）。
// WriteSidecarForDoc rewrites only the sidecar for a doc whose md is current.
func WriteSidecarForDoc(repoRoot, docRelPath, mdContent string, nodes []NodeUpdate, generatedAt time.Time) error {
	return WriteSidecarForDocContext(context.Background(), repoRoot, docRelPath, mdContent, nodes, generatedAt)
}

func WriteSidecarForDocContext(ctx context.Context, repoRoot, docRelPath, mdContent string, nodes []NodeUpdate, generatedAt time.Time) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	views := make([]nodeView, 0, len(nodes))
	for _, u := range nodes {
		views = append(views, u.toNodeView())
	}
	sf := buildSidecar(docRelPath, mdContent, views, generatedAt)
	mdAbsPath, pathErr := safeRepoPath(repoRoot, docRelPath)
	if pathErr != nil {
		return pathErr
	}
	if err := writeSidecarContext(ctx, mdAbsPath, sf); err != nil {
		return fmt.Errorf("writeback: write sidecar %s: %w", docRelPath, err)
	}
	return nil
}

// RewritePrimary 重写一个 shared node 的 _shared primary 文档 + sidecar（R6 整篇覆盖）。
// primary 路径为 _shared/<domain-slug>/<uuid>.md（与全量回写一致）。
//
// RewritePrimary rewrites one shared node's _shared primary doc + sidecar.
func RewritePrimary(repoRoot string, u NodeUpdate, generatedAt time.Time) error {
	v := u.toNodeView()
	primaryRel := primaryFilePath(v)
	primaryAbs, pathErr := safeRepoPath(repoRoot, primaryRel)
	if pathErr != nil {
		return pathErr
	}
	primaryContent := docHeaderComment + "\n\n" + renderFullBlock(v) + "\n"
	if err := os.MkdirAll(filepath.Dir(primaryAbs), 0o755); err != nil {
		return fmt.Errorf("writeback: mkdir primary dir %s: %w", filepath.Dir(primaryAbs), err)
	}
	sf := buildSidecar(primaryRel, primaryContent, []nodeView{v}, generatedAt)
	return writeDocPair(primaryAbs, []byte(primaryContent), sf)
}

// PrimaryRelPath 返回 shared node primary 文档相对 repoRoot 的路径。
// 优先使用 file_slug（LLM 生成的可读 slug），回退到 uuid（兼容旧数据）。
// 路径格式：_shared/<domain-slug>/<file-slug>.md 或 _shared/<domain-slug>/<uuid>.md
//
// PrimaryRelPath returns the repo-relative path of a shared node's primary doc.
// Uses file_slug if available (LLM-generated readable slug), falls back to uuid.
func PrimaryRelPath(domainSlug, fileSlug, uuid string) string {
	name := fileSlug
	if !validPrimaryComponent(name) {
		name = uuid // 回退到 UUID（旧数据或未生成 slug 的节点）
	}
	if !validPrimaryComponent(domainSlug) || !validPrimaryComponent(name) {
		return ""
	}
	return fmt.Sprintf("_shared/%s/%s.md", domainSlug, name)
}

func validPrimaryComponent(s string) bool {
	if strings.TrimSpace(s) == "" || s == "." || s == ".." {
		return false
	}
	return !strings.ContainsAny(s, "/\\")
}

// DeletePrimary 删除一个 shared node 的 _shared primary 文档及其 sidecar
// （node 真删时调用）。文件本就不存在时静默成功（幂等）。
//
// DeletePrimary removes a shared node's primary doc and its sidecar. Missing
// files are ignored (idempotent).
func DeletePrimary(repoRoot, domainSlug, fileSlug, uuid string) error {
	primaryAbs, pathErr := safeRepoPath(repoRoot, PrimaryRelPath(domainSlug, fileSlug, uuid))
	if pathErr != nil {
		return pathErr
	}
	var errs []error
	if err := os.Remove(primaryAbs); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("writeback: remove primary %s: %w", primaryAbs, err))
	}
	if err := os.Remove(primaryAbs + ".kg.yaml"); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("writeback: remove primary sidecar %s: %w", primaryAbs, err))
	}
	return errors.Join(errs...)
}

// DeleteSidecar 删除一篇文档的 sidecar（整篇文档被删除时调用，md 本体由人删除）。
// 文件不存在时静默成功（幂等）。
//
// DeleteSidecar removes a doc's sidecar (the md itself was deleted by the human).
// Missing files are ignored (idempotent).
func DeleteSidecar(repoRoot, docRelPath string) error {
	return DeleteSidecarContext(context.Background(), repoRoot, docRelPath)
}

func DeleteSidecarContext(ctx context.Context, repoRoot, docRelPath string) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	mdPath, pathErr := safeRepoPath(repoRoot, docRelPath)
	if pathErr != nil {
		return pathErr
	}
	p := mdPath + ".kg.yaml"
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("writeback: remove sidecar %s: %w", p, err)
	}
	return nil
}
