// Package incremental 实现第二块地基：增量流水线。
//
// 本文件实现 I-1 变化检测的核心——uuid 成对注释块解析与「四分类」块级 diff：
// 给定一篇回写过的结构化文档的当前内容与它的 sidecar baseline，把人的编辑分类为：
//
//   - C1 块内改：块内 summary/description 当前文本（经 writeback.NormalizeEditable
//     规范化后）的 hash ≠ sidecar 的 summary_hash/description_hash（坑点 4：hash 函数
//     与 sidecar 写入完全同一套——writeback.HashEditable，绝不各写一份）；
//   - C2 块删除：sidecar 有某 uuid、当前 md 无该 uuid 块；
//   - C3 只读区改：块内 name/tag/归属/shared 与 sidecar 只读字段不一致；镜像块
//     （mirror=true）内的任何编辑也一律视为 C3（真相源在 _shared primary）；
//   - C4 块外新增：出现在任何 uuid 成对块之外的非空文本（排除文档头部说明注释）。
//
// 铁律映射：
//   - R7：人只能改 summary/description；本文件只负责"检测与分类"，不做任何 KG 修改；
//   - 块边界：uuid 块是成对注释 <!-- kg:uuid=X ... --> … <!-- /kg:uuid=X -->，
//     用行级状态机解析；未闭合的块按结构篡改记 C3，块内容不落入 C4。
//
// This file implements I-1 change detection: a line-level state machine parses
// uuid paired-comment blocks, and a block-level diff against the sidecar baseline
// classifies human edits into C1 (editable-area change), C2 (block deletion),
// C3 (read-only-area tampering, incl. any edit inside mirror blocks), and C4
// (new prose outside all blocks). Detection/classification only — no KG mutation.
package incremental

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/writeback"
)

// ——————————————————————————————————————————————————————————————————————————————
// 解析产物类型 / Parsed-artifact types
// ——————————————————————————————————————————————————————————————————————————————

// ParsedBlock 是从当前 md 解析出的一个 uuid 成对注释块（完整块或镜像块）。
// Summary/Description 已经过 writeback.NormalizeEditable 规范化（镜像块无 Description）。
//
// ParsedBlock is one uuid paired-comment block parsed from the current md
// (full editable block or read-only mirror block).
type ParsedBlock struct {
	UUID        string // 开注释里的 uuid / uuid from the opening comment
	Tag         string // 开注释 tag 属性（小写）/ tag attr (lower-case)
	Shared      bool   // 开注释 shared 属性 / shared attr
	Mirror      bool   // 开注释 mirror 属性 / mirror attr
	Source      string // 镜像块 source 属性（primary 路径）/ mirror source attr
	Name        string // 完整块 ## 标题 / full-block heading
	Domain      string // 归属行 domain（展示名）/ ownership-line domain
	Subdomain   string // 归属行 subdomain（展示名）/ ownership-line subdomain
	Summary     string // 规范化后的摘要正文 / normalized summary text
	Description string // 规范化后的详述正文 / normalized description text
	// HasSummaryMarker / HasDescMarker 记录「**摘要**（可编辑）」「**详述**（可编辑）」
	// 标记行是否存在：标记被删时正文解析不可信，C1 判定必须跳过该字段（防止把
	// 结构篡改误判成"人清空了 summary"而污染 KG——R7 结构篡改只还原不写入）。
	// HasSummaryMarker / HasDescMarker record whether the editable-area marker
	// lines survived; a deleted marker makes the parsed text untrustworthy, so C1
	// detection skips that field (structural tampering is restored, never written).
	HasSummaryMarker bool
	HasDescMarker    bool
	OpenComment      string   // canonical 校验用的开注释原始行 / raw opening-comment line
	CloseComment     string   // canonical 校验用的闭注释原始行 / raw closing-comment line
	BodyLines        []string // 开闭注释之间的原始行（镜像逐字只读校验）/ raw body lines
	StartLine        int      // 开注释所在行（1-based）/ opening-comment line (1-based)
	EndLine          int      // 闭注释所在行（1-based）；未闭合时为文档末行 / closing-comment line
	Closed           bool     // 闭注释是否找到 / whether the closing comment was found
}

// TextSegment 是一段块外非空文本（C4 候选），行号为 1-based 闭区间。
//
// TextSegment is a run of non-empty text outside all uuid blocks (C4 candidate),
// with 1-based inclusive line numbers in the current document.
type TextSegment struct {
	StartLine int
	EndLine   int
	Text      string
}

// BlockEdit 是 C1 变更：某 uuid 块的可编辑区（summary/description）被改。
// 只记录实际变化的字段；New* 是规范化后的当前值（直接可作为 KG 权威写入）。
//
// BlockEdit is a C1 change: the block's editable area was modified. Only the
// actually-changed fields are set; New* are normalized current values.
type BlockEdit struct {
	UUID               string
	SummaryChanged     bool
	NewSummary         string
	DescriptionChanged bool
	NewDescription     string
}

// C3Violation 是一次只读区篡改（忽略 + 告警 + 回写阶段用 KG 权威值还原）。
//
// C3Violation records one read-only-area tampering (ignored + warned + restored
// from KG values at write-back time).
type C3Violation struct {
	UUID  string // 涉事块 uuid；未知块为开注释里的原始字符串 / block uuid
	Field string // name | tag | domain | subdomain | shared | mirror | structure | unknown_block
	Want  string // baseline 期望值 / expected baseline value
	Got   string // md 中实际值 / actual value in md
}

// DocChangeSet 是一篇文档的四分类变更集（I-1 产出）。
//
// DocChangeSet is the four-way classified change set of one document (I-1 output).
type DocChangeSet struct {
	Path    string        // 相对 repo 根的 md 路径 / repo-relative md path
	Skill   string        // 所属 skill（C4 标注用；由编排器解析）/ owning skill
	Deleted bool          // 整篇删除：C2 含全部 baseline uuid / whole doc deleted
	IsNew   bool          // 全新文档（无 sidecar）：C4 含全文 / brand-new doc
	C1      []BlockEdit   // 块内改 / editable-area changes
	C2      []string      // 被删块的 uuid / deleted-block uuids
	C3      []C3Violation // 只读区篡改 / read-only violations
	C4      []TextSegment // 块外新增 / outside-block additions
	// C4Text 是「块区域置空」后的文档文本（行数与原档一致，块行与头注释替换为空行）：
	// I-3 只标注块外新增片段时的 LLM 输入——标注产出的 SourceSpan 与原档行号 1:1 对齐。
	// C4Text is the doc with all block regions and the header comment blanked out
	// (line count preserved): the I-3 annotation input, so SourceSpan lines map 1:1
	// onto the current document.
	C4Text  string
	Sidecar *writeback.SidecarFile  // baseline（全新文档为 nil）/ baseline (nil for new docs)
	Blocks  map[string]*ParsedBlock // 当前解析出的块（uuid → block）/ current blocks
	Changed bool                    // 是否存在任何变更（粗筛通过≠细判有变更，如仅尾部空行）/ any real change
}

// HasChanges 报告该文档是否存在任何实质变更（C1/C2/C3/C4 之一非空）。
// HasChanges reports whether the doc has any substantive change.
func (cs *DocChangeSet) HasChanges() bool {
	return len(cs.C1) > 0 || len(cs.C2) > 0 || len(cs.C3) > 0 || len(cs.C4) > 0
}

// ——————————————————————————————————————————————————————————————————————————————
// 块解析（行级状态机）/ Block parsing (line-level state machine)
// ——————————————————————————————————————————————————————————————————————————————

// kgOpenPrefix 与 kgClosePrefix 是 uuid 成对注释的前后缀。
// kgOpenPrefix / kgClosePrefix delimit uuid paired-comment blocks.
const (
	kgOpenPrefix  = "<!-- kg:uuid="
	kgClosePrefix = "<!-- /kg:uuid="
	commentSuffix = "-->"
)

// docHeaderLine 是回写文档头部说明注释（与 writeback 渲染的一致）；
// 块外检测时排除它（它不是人写的新增内容）。
// docHeaderLine is the system header comment excluded from C4 detection.
const docHeaderLine = "<!-- 本文档由领域知识层结构化生成。仅「摘要/详述」可编辑；其余为系统维护，请勿修改。 -->"

// parseKGBlocks 把当前 md 内容解析为 uuid 块序列 + 块外文本段 + 结构异常列表。
// 状态机：块外行遇到 kg 开注释进入块；块内行遇到匹配的 kg 闭注释出块。
//
// 结构异常（只读身份歧义，触发 fail-closed，问题 3）：
//   - 未知 UUID（sidecar 中不存在）；
//   - 重复 UUID（同一文档两个块共用同一 uuid）；
//   - 孤立闭注释（无对应开注释）；
//   - 开闭注释 uuid 不匹配；
//   - 嵌套块（块内又出现开注释）；
//   - 未闭合块（闭注释被删）。
//
// 任一异常出现时，调用方必须 fail-closed：整篇只产生 C3，抑制 C1/C2/C4——
// 只读身份存在歧义时优先保护数据，禁止推断式删除。
//
// parseKGBlocks parses md content into uuid blocks, outside-block segments, and
// structural anomalies. Any anomaly forces fail-closed handling (C3-only).
func parseKGBlocks(content string) (blocks []*ParsedBlock, segments []TextSegment, anomalies []C3Violation) {
	lines := strings.Split(content, "\n")
	var cur *ParsedBlock
	var segStart int
	var segLines []string
	seenUUID := make(map[string]int) // uuid → 首次出现的开注释行号（重复检测）

	flushSegment := func(endLine int) {
		if len(segLines) == 0 {
			return
		}
		segments = append(segments, TextSegment{
			StartLine: segStart,
			EndLine:   endLine,
			Text:      strings.Join(segLines, "\n"),
		})
		segLines = nil
	}

	for i, raw := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(raw)

		if cur == nil {
			// —— 块外区域 ——
			if uuid, ok := parseOpenComment(trimmed); ok {
				flushSegment(lineNo - 1)
				cur = &ParsedBlock{UUID: uuid, StartLine: lineNo, OpenComment: raw}
				parseOpenAttrs(cur, trimmed)
				if first, dup := seenUUID[uuid]; dup {
					anomalies = append(anomalies, C3Violation{
						UUID: uuid, Field: "duplicate",
						Want: fmt.Sprintf("uuid 唯一（首次出现于第 %d 行）", first),
						Got:  fmt.Sprintf("第 %d 行再次出现", lineNo),
					})
				} else {
					seenUUID[uuid] = lineNo
				}
				continue
			}
			// 孤立闭注释：没有进行中的块 → 身份歧义。
			if closeUUID, ok := parseCloseComment(trimmed); ok {
				flushSegment(lineNo - 1)
				anomalies = append(anomalies, C3Violation{
					UUID: closeUUID, Field: "structure",
					Want: "成对开闭注释", Got: fmt.Sprintf("第 %d 行孤立闭注释", lineNo),
				})
				continue
			}
			// 看起来像 KG 锚点却无法被严格 parser 接受：必须按结构异常处理，
			// 不能在开闭锚同时损坏时把原块退化成 C2 + C4。
			if looksLikeMalformedKGAnchor(trimmed) {
				flushSegment(lineNo - 1)
				anomalies = append(anomalies, C3Violation{
					Field: "structure", Want: "合法 kg:uuid 成对注释",
					Got: fmt.Sprintf("第 %d 行损坏锚点: %s", lineNo, trimmed),
				})
				continue
			}
			// 块外非空行（排除头注释）累积为 C4 段；空行切断当前段。
			if trimmed == "" || trimmed == docHeaderLine {
				flushSegment(lineNo - 1)
				continue
			}
			if len(segLines) == 0 {
				segStart = lineNo
			}
			segLines = append(segLines, raw)
			continue
		}

		// —— 块内区域 ——
		// 嵌套开注释：块内又出现开注释 → 身份歧义（作为块内容继续，但已记异常）。
		if uuid, ok := parseOpenComment(trimmed); ok {
			anomalies = append(anomalies, C3Violation{
				UUID: cur.UUID, Field: "structure",
				Want: "块内不得嵌套开注释", Got: fmt.Sprintf("第 %d 行嵌套 kg:uuid=%s", lineNo, uuid),
			})
			continue
		}
		if closeUUID, ok := parseCloseComment(trimmed); ok {
			if closeUUID != cur.UUID {
				// 开闭不匹配 → 身份歧义；按关闭当前块收尾（fail-closed 由调用方统一处理）。
				anomalies = append(anomalies, C3Violation{
					UUID: cur.UUID, Field: "structure",
					Want: fmt.Sprintf("闭注释 uuid=%s", cur.UUID),
					Got:  fmt.Sprintf("第 %d 行闭注释 uuid=%s", lineNo, closeUUID),
				})
			}
			cur.EndLine = lineNo
			cur.CloseComment = raw
			cur.Closed = true
			finalizeBlock(cur, lines)
			blocks = append(blocks, cur)
			cur = nil
			continue
		}
		if looksLikeMalformedKGAnchor(trimmed) {
			anomalies = append(anomalies, C3Violation{
				UUID: cur.UUID, Field: "structure", Want: "合法 kg:uuid 成对注释",
				Got: fmt.Sprintf("第 %d 行损坏锚点: %s", lineNo, trimmed),
			})
			continue
		}
		// 普通块内容行： finalizeBlock 统一解析。
	}

	// EOF：未闭合块按结构异常收尾；块外段收尾。
	if cur != nil {
		cur.EndLine = len(lines)
		cur.Closed = false
		finalizeBlock(cur, lines)
		blocks = append(blocks, cur)
		anomalies = append(anomalies, C3Violation{
			UUID: cur.UUID, Field: "structure",
			Want: "closed block", Got: "unclosed",
		})
	}
	flushSegment(len(lines))
	return blocks, segments, anomalies
}

// looksLikeMalformedKGAnchor 识别「明显意图为 KG 锚点、但严格 parse 失败」的行。
// 仅 HTML 注释形态且含 kg:uuid 才命中，普通正文提及该字符串不会被误伤。
func looksLikeMalformedKGAnchor(trimmed string) bool {
	line := strings.TrimSpace(trimmed)
	if line == "" || (!strings.HasPrefix(line, "<!--") && !strings.HasSuffix(line, "-->")) {
		return false
	}
	// A damaged anchor still has the characteristic kg/uuid pair, but users
	// commonly alter case or replace ':'/'=' with whitespace. Keep the match
	// deliberately narrow: an HTML-comment boundary plus an actual uuid value
	// and either an anchor attribute, a closing slash, or a UUID-looking token.
	lower := strings.ToLower(line)
	m := malformedAnchorPattern.FindStringSubmatchIndex(lower)
	if len(m) != 4 {
		return false
	}
	value := lower[m[2]:m[3]]
	tail := strings.TrimSpace(lower[m[3]:])
	prefix := lower[:m[0]]
	commentBody := strings.TrimSpace(strings.TrimPrefix(lower, "<!--"))
	strongAttrs := strings.Contains(tail, "tag=") || strings.Contains(tail, "shared=") ||
		strings.Contains(tail, "mirror=") || strings.Contains(tail, "source=") ||
		strings.Contains(tail, "readonly")
	// KG anchors begin immediately after the HTML comment opener (or its '/'
	// closing form). If the token appears in ordinary prose, require explicit
	// anchor metadata before considering it malformed.
	nearCommentStart := strings.HasPrefix(commentBody, "kg") || strings.HasPrefix(commentBody, "/kg")
	if !nearCommentStart && !strongAttrs {
		return false
	}
	if strings.Contains(prefix, "/") && tail == "" {
		return true // damaged closing anchor
	}
	if strongAttrs {
		return true // damaged opening anchor with metadata
	}
	// Avoid treating ordinary prose such as "<!-- note about kg uuid format -->"
	// as an anchor. Real node ids in this format contain a digit or separator;
	// the opening/closing comment evidence above handles ids without either.
	return strings.ContainsAny(value, "0123456789-_.:")
}

var malformedAnchorPattern = regexp.MustCompile(`(?:^|[\s/])kg[\s:_/-]*uuid\s*(?:=|:|\s)\s*([A-Za-z0-9][A-Za-z0-9._:-]*)`)

// parseOpenComment 判定一行是否为 kg 开注释，提取 uuid。
// parseOpenComment reports whether a line is a kg opening comment and extracts uuid.
func parseOpenComment(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, kgOpenPrefix) || !strings.HasSuffix(trimmed, commentSuffix) {
		return "", false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, kgOpenPrefix), commentSuffix)
	// inner 形如 "<uuid> tag=entity shared=false readonly-meta"：uuid 是第一个空白前的段。
	fields := strings.Fields(inner)
	if len(fields) == 0 || fields[0] == "" {
		return "", false
	}
	return fields[0], true
}

// parseCloseComment 判定一行是否为 kg 闭注释，提取 uuid。
// parseCloseComment reports whether a line is a kg closing comment and extracts uuid.
func parseCloseComment(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, kgClosePrefix) || !strings.HasSuffix(trimmed, commentSuffix) {
		return "", false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, kgClosePrefix), commentSuffix)
	fields := strings.Fields(inner)
	if len(fields) == 0 || fields[0] == "" {
		return "", false
	}
	return fields[0], true
}

// parseOpenAttrs 从开注释行解析 tag/shared/mirror/source 属性到块。
// parseOpenAttrs fills tag/shared/mirror/source attrs from the opening comment.
func parseOpenAttrs(b *ParsedBlock, trimmed string) {
	for _, f := range strings.Fields(trimmed) {
		switch {
		case strings.HasPrefix(f, "tag="):
			b.Tag = strings.ToLower(strings.TrimPrefix(f, "tag="))
		case strings.HasPrefix(f, "shared="):
			b.Shared = strings.TrimPrefix(f, "shared=") == "true"
		case strings.HasPrefix(f, "mirror="):
			b.Mirror = strings.TrimPrefix(f, "mirror=") == "true"
		case strings.HasPrefix(f, "source="):
			b.Source = strings.TrimPrefix(f, "source=")
		}
	}
}

// finalizeBlock 在块闭合（或 EOF）后解析块体：完整块提取 name/归属/summary/description；
// 镜像块提取 > **name** — summary 行的 name 与 summary。
// lines 是全文行切片（0-based），块体为 (StartLine, EndLine) 开区间内的行。
//
// finalizeBlock parses the block body once the block closes (or at EOF).
func finalizeBlock(b *ParsedBlock, lines []string) {
	// 块体行：开注释之后、闭注释之前（未闭合则到文末）。
	bodyStart := b.StartLine // 0-based 下标 = StartLine（开注释行的下一行）
	bodyEnd := b.EndLine - 1 // 0-based 下标 = 闭注释行
	if !b.Closed {
		bodyEnd = len(lines)
	}
	if bodyStart > len(lines) {
		bodyStart = len(lines)
	}
	if bodyEnd > len(lines) {
		bodyEnd = len(lines)
	}
	body := lines[bodyStart:bodyEnd]
	b.BodyLines = append([]string(nil), body...)

	if b.Mirror {
		parseMirrorBody(b, body)
		return
	}
	parseFullBody(b, body)
}

// parseFullBody 解析完整可编辑块的块体：
//
//	## <name>
//	- **归属**：<domain> / <subdomain>
//
//	**摘要**（可编辑）
//	<summary 行…>
//
//	**详述**（可编辑）
//	<description 行…>
//
// summary/description 经 NormalizeEditable 规范化（去尾部空行、占位符→空串）。
// 标记行缺失时保持零值（结构篡改由 C3 兜底，回写阶段还原）。
//
// parseFullBody parses a full editable block's body into name/ownership/summary/
// description, normalizing the editable texts.
func parseFullBody(b *ParsedBlock, body []string) {
	const (
		stageHead = iota // 摘要标记之前 / before the summary marker
		stageSummary
		stageDescription
	)
	stage := stageHead
	var summaryLines, descLines []string
	for _, raw := range body {
		trimmed := strings.TrimSpace(raw)
		switch {
		case trimmed == "**摘要**（可编辑）":
			stage = stageSummary
			b.HasSummaryMarker = true
			continue
		case trimmed == "**详述**（可编辑）":
			stage = stageDescription
			b.HasDescMarker = true
			continue
		}
		switch stage {
		case stageHead:
			if strings.HasPrefix(trimmed, "## ") {
				b.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			} else if strings.HasPrefix(trimmed, "- **归属**：") {
				own := strings.TrimPrefix(trimmed, "- **归属**：")
				// 渲染格式为 "<domain> / <subdomain>"（空格-斜杠-空格）：
				// 按 " / " 切分，容忍展示名内含 "/"。
				parts := strings.SplitN(own, " / ", 2)
				if len(parts) == 2 {
					b.Domain = strings.TrimSpace(parts[0])
					b.Subdomain = strings.TrimSpace(parts[1])
				}
			}
		case stageSummary:
			summaryLines = append(summaryLines, raw)
		case stageDescription:
			descLines = append(descLines, raw)
		}
	}
	b.Summary = writeback.NormalizeEditable(joinTrimTrailingBlank(summaryLines))
	b.Description = writeback.NormalizeEditable(joinTrimTrailingBlank(descLines))
}

// parseMirrorBody 解析镜像块块体：期望恰好两行（🔒 说明行 + > **name** — summary 行）。
// 只提取 name 与 summary（供 C3 比对）；多余/缺失行由调用方按 C3 处理。
//
// parseMirrorBody parses a mirror block body: the 🔒 notice line and the
// "> **name** — summary" line. Extra/missing lines are left for C3 detection.
func parseMirrorBody(b *ParsedBlock, body []string) {
	var summaryLines []string
	seenSummary := false
	for _, raw := range body {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "> 🔒") {
			continue
		}
		if !seenSummary && strings.HasPrefix(raw, "> **") {
			rest := strings.TrimPrefix(raw, "> **")
			if idx := strings.Index(rest, "**"); idx >= 0 {
				b.Name = rest[:idx]
				tail := rest[idx+2:]
				if strings.HasPrefix(tail, " — ") {
					tail = strings.TrimPrefix(tail, " — ")
				} else {
					// 非 canonical 分隔符仍尽量抽取正文供 C3 诊断；逐行精确比较会拒绝它。
					tail = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(tail), "—"))
				}
				summaryLines = append(summaryLines, tail)
				seenSummary = true
			}
			continue
		}
		if seenSummary && strings.HasPrefix(raw, "> ") {
			summaryLines = append(summaryLines, strings.TrimPrefix(raw, "> "))
		}
	}
	b.Summary = writeback.NormalizeEditable(strings.Join(summaryLines, "\n"))
}

// joinTrimTrailingBlank 拼接行并去掉尾部空行（渲染在正文后固定补的空行、
// 人编辑增删的尾部空行都不是实质内容变化）。
// joinTrimTrailingBlank joins lines and drops trailing blank lines.
func joinTrimTrailingBlank(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// buildC4Text 构造「块区域置空」的标注输入文本：行数与原档一致，
// 所有 uuid 块占据的行（开注释到闭注释，含块体）与头注释行替换为空行。
// 这样 I-3 标注产出的 SourceSpan 行号与原档 1:1 对齐，无需偏移换算。
//
// buildC4Text blanks out all block-occupied lines and the header comment while
// preserving line count, so annotation SourceSpans map 1:1 onto the document.
func buildC4Text(content string, blocks []*ParsedBlock) string {
	lines := strings.Split(content, "\n")
	for _, b := range blocks {
		start := b.StartLine - 1 // 0-based 开注释行 / 0-based opening line
		end := b.EndLine         // 0-based 闭注释行的下一行 / one past closing line
		if b.Closed {
			end = b.EndLine // EndLine 是闭注释行号（1-based）→ 0-based 区间为 [start, end)
		} else {
			end = len(lines)
		}
		for i := start; i < end && i < len(lines); i++ {
			lines[i] = ""
		}
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == docHeaderLine {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// ——————————————————————————————————————————————————————————————————————————————
// 四分类 diff / Four-way classification diff
// ——————————————————————————————————————————————————————————————————————————————

// DiffDoc 对一篇「已回写过」的文档做块级 diff（细判）：
// 以 sidecar 为 baseline，把当前 md 的人编辑分类为 C1/C2/C3/C4。
// sidecar 为 nil（全新文档）时整篇为 C4；content 为空字符串且 Deleted=true 时
// 全部 baseline uuid 为 C2（整篇删除——调用方在文件系统层判定后传入）。
//
// DiffDoc performs the block-level diff for one previously written-back doc:
// against the sidecar baseline it classifies human edits into C1/C2/C3/C4.
func DiffDoc(path string, sidecar *writeback.SidecarFile, content string, deleted bool) *DocChangeSet {
	cs := &DocChangeSet{Path: path, Sidecar: sidecar}
	if sidecar != nil {
		if err := writeback.ValidateSidecar(*sidecar); err != nil {
			// A syntactically decoded but semantically invalid baseline must never
			// be allowed to manufacture C1/C2 decisions.  Treat the whole doc as
			// a read-only corruption and let I-9 rebuild it from KG.
			cs.C3 = []C3Violation{{UUID: "(doc)", Field: "sidecar_corrupt", Want: "valid sidecar", Got: err.Error()}}
			cs.Changed = true
			return cs
		}
	}

	// 整篇删除：所有 baseline uuid 视为 C2（坑点 3 的整篇删除分支）。
	// 删除本身即变更（即使 sidecar 无节点——也要驱动 sidecar/file_states 清理）。
	if deleted {
		cs.Deleted = true
		if sidecar != nil {
			for _, n := range sidecar.Nodes {
				cs.C2 = append(cs.C2, n.UUID)
			}
		}
		cs.Changed = true
		return cs
	}

	// 全新文档（无 sidecar）：整篇视为 C4（坑点 3 的全新文档分支）。
	if sidecar == nil {
		cs.IsNew = true
		cs.C4Text = content
		if strings.TrimSpace(content) != "" {
			lines := strings.Split(content, "\n")
			cs.C4 = append(cs.C4, TextSegment{StartLine: 1, EndLine: len(lines), Text: content})
		}
		cs.Changed = len(cs.C4) > 0
		return cs
	}

	blocks, segments, parseAnomalies := parseKGBlocks(content)
	anomalies := validateManagedHeader(content)
	anomalies = append(anomalies, parseAnomalies...)
	cs.Blocks = make(map[string]*ParsedBlock, len(blocks))
	cs.C4 = segments
	cs.C4Text = buildC4Text(content, blocks)

	// baseline 索引：uuid → sidecarNode。
	base := make(map[string]writeback.SidecarNode, len(sidecar.Nodes))
	for _, n := range sidecar.Nodes {
		base[n.UUID] = n
	}
	// 文档角色：_shared/<domain>/<uuid>.md 是 shared node 的完整可编辑 primary；
	// 来源文档中的 shared 块才是整体只读镜像。
	isPrimaryDoc := IsSharedPrimaryPath(path)

	// 未知 UUID（sidecar 中不存在）也是身份歧义（问题 3：优先保护数据，禁止推断式删除）。
	for _, b := range blocks {
		if _, ok := base[b.UUID]; !ok {
			anomalies = append(anomalies, C3Violation{
				UUID: b.UUID, Field: "unknown_block", Want: "(not in sidecar)", Got: b.UUID,
			})
		}
	}
	// 旧版 shared sidecar 没有 domain_slug，无法建立固定 primary 路径。
	// 无论当前文档是镜像还是 primary，都一次性 fail-closed，触发 KG
	// 权威回写升级 baseline；否则已有 file_state + 相同 md hash 会永久跳过。
	for _, n := range sidecar.Nodes {
		if n.Shared && n.DomainSlug == "" {
			anomalies = append(anomalies, C3Violation{
				UUID: n.UUID, Field: "sidecar_metadata",
				Want: "domain_slug for exact primary path", Got: "missing",
			})
		}
	}

	// 已知 UUID 也可能被重新绑定：例如删掉原 B 块，再把 A 块的开闭 UUID 改成 B，
	// 或 A/B UUID 互换。若当前块的只读身份与另一个 baseline node 精确吻合，说明
	// UUID→内容的绑定存在歧义，必须整篇 fail-closed，不能产生 C1/C2/C4。
	anomalies = append(anomalies, detectKnownUUIDRebinding(blocks, base, isPrimaryDoc)...)

	// primary 只有完整块内 summary/description 可编辑；块外散文没有 skill 归属且不是
	// C4 知识创建入口，统一按结构越界 C3 处理并由 KG 权威内容恢复。
	if isPrimaryDoc && len(segments) > 0 {
		for _, seg := range segments {
			anomalies = append(anomalies, C3Violation{
				UUID: primaryUUIDFromPath(path), Field: "structure",
				Want: "primary 块外无正文", Got: fmt.Sprintf("第 %d-%d 行存在块外文本", seg.StartLine, seg.EndLine),
			})
		}
	}

	// fail-closed：任一身份/结构异常 → 整篇只产生 C3，抑制 C1/C2/C4。
	// 只读身份存在歧义时，唯一安全动作是告警 + 从 KG 权威状态重渲染（I-9）。
	if len(anomalies) > 0 {
		for _, b := range blocks {
			cs.Blocks[b.UUID] = b
		}
		cs.C3 = anomalies
		cs.C4 = nil    // 块外文本在结构歧义下不可信 / untrustworthy under ambiguity
		cs.C4Text = "" // 不送标注 / do not annotate
		cs.Changed = true
		return cs
	}

	// 逐当前块：C1（块内改）/ C3（只读区改）。
	for _, b := range blocks {
		cs.Blocks[b.UUID] = b
		sn := base[b.UUID]
		if isPrimaryDoc {
			diffFullBlock(cs, b, sn)
			continue
		}
		if sn.Shared || b.Mirror {
			// 镜像块：内部任何编辑一律 C3（坑点 6：真相源在 primary，不当 C1）。
			diffMirrorBlock(cs, b, sn)
			continue
		}
		diffFullBlock(cs, b, sn)
	}

	// C2：baseline 有、当前 md 无的 uuid——只有「干净消失」才算有意删除
	// （身份异常已在上方 fail-closed，走到这里说明文档结构完整）。
	for _, n := range sidecar.Nodes {
		if _, ok := cs.Blocks[n.UUID]; !ok {
			cs.C2 = append(cs.C2, n.UUID)
		}
	}

	cs.Changed = cs.HasChanges()
	return cs
}

// validateManagedHeader enforces the one canonical system header for every
// sidecar-managed document. DiffDoc invokes it only after the nil-sidecar/new
// document branch, so genuinely new prose remains C4.
func validateManagedHeader(content string) []C3Violation {
	lines := strings.Split(content, "\n")
	first := ""
	if len(lines) > 0 {
		first = lines[0]
	}
	var out []C3Violation
	if first != docHeaderLine {
		out = append(out, C3Violation{
			UUID: "(doc)", Field: "structure",
			Want: docHeaderLine + " (line 1, exactly once)", Got: first,
		})
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == docHeaderLine {
			out = append(out, C3Violation{
				UUID: "(doc)", Field: "structure",
				Want: "system header only at line 1", Got: fmt.Sprintf("duplicate/moved header at line %d", i+1),
			})
		}
	}
	return out
}

func managedHeaderCanonical(content string) bool {
	return len(validateManagedHeader(content)) == 0
}

// IsSharedPrimaryPath 判定一个相对路径是否是 _shared primary 文档
// （_shared/<domain-slug>/<uuid>.md）。primary 只走 syncPrimaries 通道，
// 绝不进入普通文档回写通道（问题 1）。
//
// IsSharedPrimaryPath reports whether a repo-relative path is a _shared primary doc.
func IsSharedPrimaryPath(relPath string) bool {
	clean := path.Clean(relPath)
	parts := strings.Split(clean, "/")
	return clean == relPath && len(parts) == 3 && parts[0] == "_shared" &&
		parts[1] != "" && strings.HasSuffix(parts[2], ".md") && parts[2] != ".md"
}

// detectKnownUUIDRebinding 检测「UUID 本身合法且唯一，但被绑定到了另一个 baseline
// node 的块内容」的情形。普通只读字段篡改仍由 diffFullBlock/diffMirrorBlock 逐字段
// 报 C3；只有块身份精确匹配另一个 node 时才升级为整篇 fail-closed。
func detectKnownUUIDRebinding(blocks []*ParsedBlock, base map[string]writeback.SidecarNode, isPrimaryDoc bool) []C3Violation {
	var out []C3Violation
	present := make(map[string]bool, len(blocks))
	var mismatched []*ParsedBlock
	for _, b := range blocks {
		present[b.UUID] = true
		own, ok := base[b.UUID]
		if !ok || blockMatchesBaselineIdentity(b, own, isPrimaryDoc) {
			continue
		}
		mismatched = append(mismatched, b)
		for otherUUID, candidate := range base {
			if otherUUID == b.UUID {
				continue
			}
			if blockMatchesBaselineIdentity(b, candidate, isPrimaryDoc) {
				out = append(out, C3Violation{
					UUID: b.UUID, Field: "uuid_binding", Want: b.UUID,
					Got: fmt.Sprintf("块只读身份属于 %s", otherUUID),
				})
				break
			}
		}
	}

	// 即使人同时改了块的其它只读字段，导致无法精确匹配另一个 baseline，也不能
	// 放过「有 baseline UUID 消失 + 仍存块身份错位」或「多个仍存块同时身份错位」：
	// 前者覆盖 A→B 后删原 B，后者覆盖 A/B swap。保守 fail-closed 比推断式删除安全。
	missing := 0
	for uuid := range base {
		if !present[uuid] {
			missing++
		}
	}
	if (missing > 0 && len(mismatched) > 0) || len(mismatched) > 1 {
		already := make(map[string]bool)
		for _, v := range out {
			already[v.UUID] = true
		}
		for _, b := range mismatched {
			if already[b.UUID] {
				continue
			}
			out = append(out, C3Violation{
				UUID: b.UUID, Field: "uuid_binding", Want: "baseline UUID 与只读身份保持绑定",
				Got: "UUID 缺失/多块错位导致身份歧义",
			})
		}
	}
	return out
}

// blockMatchesBaselineIdentity 只比较足以标识块归属的只读字段。可编辑正文不参与
// full-block 身份匹配；镜像正文整体只读，因此 summary hash 可作为额外身份信号。
func blockMatchesBaselineIdentity(b *ParsedBlock, sn writeback.SidecarNode, isPrimaryDoc bool) bool {
	expectMirror := !isPrimaryDoc && sn.Shared
	if expectMirror {
		return b.Mirror && b.Shared && b.Name == sn.Name &&
			writeback.HashEditable(b.Summary) == sn.SummaryHash &&
			mirrorSourceMatches(b.Source, sn, b.UUID)
	}
	return !b.Mirror && b.Name == sn.Name &&
		b.Tag == strings.ToLower(sn.Tag) &&
		b.Domain == sn.Domain && b.Subdomain == sn.Subdomain &&
		b.Shared == sn.Shared
}

// expectedMirrorSource 返回 sidecar 能确定的精确 primary 路径。
//
// 只有 domain_slug 与 file_slug 都已持久化时才能断定精确路径：引入 file_slug 后
// primary 文件名是可读 slug，缺 file_slug 的旧 sidecar 无从推断文件名。此时必须
// 返回空、交由 mirrorSourceMatches 做形态兜底——否则会把期望值算成 UUID 命名，
// 与磁盘上的 slug 命名不符而误报 C3（进而 fail-closed 吞掉同块的可编辑区编辑）。
// sidecar freshness 会在 I-9 用 KG NodeUpdate 补齐这两个字段，后续轮次即精确比较。
func expectedMirrorSource(sn writeback.SidecarNode, uuid string) string {
	if sn.DomainSlug == "" || sn.FileSlug == "" {
		return ""
	}
	return writeback.PrimaryRelPath(sn.DomainSlug, sn.FileSlug, uuid)
}

// mirrorSourceMatches 校验镜像块的 source 是否指向本 node 的 primary 文档。
// 精确期望可得时严格比较；否则做形态兜底：须为 `_shared/<domain>/<name>.md`，
// domain_slug 已知时必须一致，文件名接受 UUID 命名（旧数据）或 file_slug 命名（新）。
func mirrorSourceMatches(source string, sn writeback.SidecarNode, uuid string) bool {
	if expected := expectedMirrorSource(sn, uuid); expected != "" {
		return source == expected
	}
	clean := path.Clean(source)
	if clean != source {
		return false
	}
	parts := strings.Split(clean, "/")
	if len(parts) != 3 || parts[0] != "_shared" || parts[1] == "" {
		return false
	}
	// domain 级校验强度保留：跨 domain 的 source 篡改仍必须被抓出。
	if sn.DomainSlug != "" && parts[1] != sn.DomainSlug {
		return false
	}
	name := strings.TrimSuffix(parts[2], ".md")
	if name == parts[2] || name == "" || name == "." || name == ".." {
		return false // 无 .md 后缀或非法文件名
	}
	if name == uuid {
		return true // UUID 命名：旧数据或未生成 slug 的节点
	}
	if sn.FileSlug != "" {
		return name == sn.FileSlug // slug 已知（仅 domain_slug 缺失）：必须一致
	}
	// slug 未知：只能校验形态，避免误判健康的 slug 命名 primary。
	return true
}

// diffFullBlock 对一个完整可编辑块做 C1（可编辑区）+ C3（只读区）判定。
// diffFullBlock classifies a full editable block into C1 / C3.
func diffFullBlock(cs *DocChangeSet, b *ParsedBlock, sn writeback.SidecarNode) {
	// Collect all read-only violations first.  A block with any structural or
	// identity mismatch is fail-closed: even a simultaneous summary edit must
	// not become a C1 write against an ambiguous baseline.
	var violations []C3Violation
	addC3 := func(field, want, got string) {
		violations = append(violations, C3Violation{UUID: b.UUID, Field: field, Want: want, Got: got})
	}
	if want := writeback.CanonicalFullOpenComment(b.UUID, sn.Tag, sn.Shared); b.OpenComment != want {
		addC3("structure", want, b.OpenComment)
	}
	if !b.Closed || b.CloseComment != writeback.CanonicalFullCloseComment(b.UUID) {
		addC3("structure", writeback.CanonicalFullCloseComment(b.UUID), b.CloseComment)
	}

	// Read-only semantic fields (name/tag/ownership/shared).  Empty fields are
	// also mismatches because the renderer always emits them.
	if b.Name != sn.Name {
		addC3("name", sn.Name, b.Name)
	}
	if b.Tag != strings.ToLower(sn.Tag) {
		addC3("tag", strings.ToLower(sn.Tag), b.Tag)
	}
	if b.Domain != sn.Domain {
		addC3("domain", sn.Domain, b.Domain)
	}
	if b.Subdomain != sn.Subdomain {
		addC3("subdomain", sn.Subdomain, b.Subdomain)
	}
	if b.Shared != sn.Shared {
		addC3("shared", boolStr(sn.Shared), boolStr(b.Shared))
	}
	if b.Mirror {
		addC3("mirror", "false", "true")
	}
	// Validate the remaining immutable body skeleton after semantic identity
	// fields so callers get the most actionable field first (for example name
	// tampering reports name before the derived heading-line mismatch).
	checkFullReadonlyBody(b, sn, addC3)

	// Marker deletion makes the corresponding parsed text untrustworthy.  The
	// canonical-body check usually reports this too, but retain field-specific
	// diagnostics for callers and older malformed blocks.
	if !b.HasSummaryMarker {
		addC3("structure", writeback.FullSummaryMarker, "(marker deleted)")
	}
	if !b.HasDescMarker {
		addC3("structure", writeback.FullDescriptionMarker, "(marker deleted)")
	}

	// —— C1：editable hashes (only after read-only validation) ——
	var edit *BlockEdit
	if len(violations) == 0 {
		if b.HasSummaryMarker && writeback.HashEditable(b.Summary) != sn.SummaryHash {
			edit = &BlockEdit{UUID: b.UUID, SummaryChanged: true, NewSummary: b.Summary}
		}
		if b.HasDescMarker && writeback.HashEditable(b.Description) != sn.DescriptionHash {
			if edit == nil {
				edit = &BlockEdit{UUID: b.UUID}
			}
			edit.DescriptionChanged = true
			edit.NewDescription = b.Description
		}
	}
	cs.C3 = append(cs.C3, violations...)
	if edit != nil {
		cs.C1 = append(cs.C1, *edit)
	}
}

// checkFullReadonlyBody validates the immutable skeleton inside a full block.
// The two editable bodies are intentionally treated as opaque ranges, so a
// legitimate multi-line summary/description remains C1 rather than C3.
func checkFullReadonlyBody(b *ParsedBlock, sn writeback.SidecarNode, add func(field, want, got string)) {
	body := b.BodyLines
	prefix := writeback.CanonicalFullReadonlyPrefix(sn.Name, sn.Domain, sn.Subdomain)
	if len(body) < len(prefix) {
		add("structure", fmt.Sprintf("至少 %d 行 canonical 只读前缀", len(prefix)), fmt.Sprintf("%d 行", len(body)))
		return
	}
	for i, want := range prefix {
		if body[i] != want {
			add("structure", want, body[i])
		}
	}
	// Exactly one description marker is required.  A marker-looking line in an
	// editable body is reserved syntax, not free-form prose.
	descIdx := -1
	descCount := 0
	for i := len(prefix); i < len(body); i++ {
		if body[i] == writeback.FullDescriptionMarker {
			descCount++
			if descIdx < 0 {
				descIdx = i
			}
		}
	}
	if descCount != 1 {
		add("structure", writeback.FullDescriptionMarker, fmt.Sprintf("出现 %d 次", descCount))
		return
	}
	// The blank immediately before the description marker is the canonical
	// separator.  Earlier trailing blanks belong to the editable summary and
	// are normalized by HashEditable; this preserves the documented tolerance
	// for harmless trailing blank edits while protecting read-only layout.
	if descIdx <= len(prefix) || body[descIdx-1] != "" {
		got := "(missing separator)"
		if descIdx > 0 {
			got = body[descIdx-1]
		}
		add("structure", "", got)
	}
}

// diffMirrorBlock 对一个镜像块做 C3 判定：镜像整体只读，任何编辑（name/summary/
// 属性/结构/块类型转换）都记 C3，忽略 + 告警 + 回写阶段从 primary 还原（坑点 6）。
// diffMirrorBlock classifies a mirror block: any edit is a C3 violation.
func diffMirrorBlock(cs *DocChangeSet, b *ParsedBlock, sn writeback.SidecarNode) {
	addC3 := func(field, want, got string) {
		cs.C3 = append(cs.C3, C3Violation{UUID: b.UUID, Field: field, Want: want, Got: got})
	}
	// 块类型/属性被改：baseline 非 shared 却出现镜像块、或 canonical 属性有任何变化。
	if !sn.Shared {
		addC3("mirror", "full block", "converted to mirror")
		return // 块类型已错，内容比对无意义 / block type wrong; skip content checks
	}
	expectedSource := expectedMirrorSource(sn, b.UUID)
	if expectedSource == "" && mirrorSourceMatches(b.Source, sn, b.UUID) {
		// 旧 sidecar 无 domain_slug：只能从当前合法路径取值；sidecar freshness 会在
		// I-9 用 KG NodeUpdate 补齐 domain_slug，后续轮次即执行精确比较。
		expectedSource = b.Source
	}
	expectedOpen := fmt.Sprintf("<!-- kg:uuid=%s shared=true mirror=true source=%s -->", b.UUID, expectedSource)
	expectedClose := fmt.Sprintf("<!-- /kg:uuid=%s -->", b.UUID)
	expectedNotice := fmt.Sprintf("> 🔒 **[共享镜像 · 只读]** 本内容由 `%s` 维护，请勿在此编辑。", expectedSource)
	expectedBody := append([]string{expectedNotice}, writeback.MirrorSummaryLines(sn.Name, b.Summary)...)

	if !b.Shared {
		addC3("shared", "true", "false")
	}
	if !b.Mirror {
		addC3("mirror", "true", "false")
	}
	if !mirrorSourceMatches(b.Source, sn, b.UUID) {
		addC3("source", expectedSource, b.Source)
	}
	if b.OpenComment != expectedOpen {
		addC3("structure", expectedOpen, b.OpenComment)
	}
	if !b.Closed || b.CloseComment != expectedClose {
		addC3("structure", expectedClose, b.CloseComment)
	}
	if len(b.BodyLines) != len(expectedBody) {
		addC3("structure", fmt.Sprintf("镜像块 %d 行 canonical 只读正文", len(expectedBody)), fmt.Sprintf("%d 行", len(b.BodyLines)))
	} else {
		for i := range expectedBody {
			if b.BodyLines[i] != expectedBody[i] {
				addC3("mirror", expectedBody[i], b.BodyLines[i])
			}
		}
	}
	// 镜像行内容必须可解析为 "> **name** — summary"；不可解析即结构篡改。
	if b.Name == "" {
		addC3("mirror", sn.Name, "(unparseable mirror line)")
		return
	}
	// 镜像行内容被改：name 或 summary（hash 比对）不一致 → C3。
	if b.Name != sn.Name {
		addC3("name", sn.Name, b.Name)
	}
	if writeback.HashEditable(b.Summary) != sn.SummaryHash {
		addC3("mirror", sn.SummaryHash, writeback.HashEditable(b.Summary))
	}
}

// boolStr 渲染布尔为 "true"/"false"（C3 记录用）。
// boolStr renders a bool for C3 records.
func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
