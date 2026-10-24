package service

import (
	"context"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xcosmosbox/cairn/core/storage"
)

// NodeSources 区分可定位的来源与 unknown；引用标签不等于已核实的文件位置。
// NodeSources separates stored locations from unknown; reference labels alone are not locations.
type NodeSources struct {
	NodeUUID   string           `json:"node_uuid,omitempty"`
	Status     string           `json:"status"`
	Reason     string           `json:"reason,omitempty"`
	SourceRefs []string         `json:"source_refs,omitempty"`
	Locations  []SourceLocation `json:"locations,omitempty"`
	Omitted    int              `json:"omitted,omitempty"`
}

// SourceLocation 仅描述库中记录的相对路径，不读取文件或推断仓库、提交和原文。
// SourceLocation describes stored relative metadata, never fetched content or inferred Git identity.
type SourceLocation struct {
	MemberID   string `json:"member_id,omitempty"`
	Skill      string `json:"skill,omitempty"`
	FilePath   string `json:"file_path"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
	SpanStatus string `json:"span_status"`
}

// GetNodeSources 的调用者须传入 GetNode 已解析的存活 UUID，避免把旧或 split
// 兄弟节点的来源错配给主后继。只读缺表检查兼容旧库，绝不为查询补表或迁移。
// Callers pass the resolved live UUID; missing legacy tables remain unknown without migration.
func (s *knowledgeService) GetNodeSources(ctx context.Context, id string) (*NodeSources, error) {
	info := &NodeSources{Status: "unknown", Reason: "node_not_found"}
	node, err := s.nodeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("knowledge service node sources: %w", err)
	}
	if node == nil {
		return info, nil
	}
	info.NodeUUID = node.ID
	info.Reason = "no_source_locations"
	seen := map[string]bool{}
	for _, raw := range strings.Split(node.SourceRefs, ",") {
		// 先拒绝控制字符再裁剪分隔空格，避免 TrimSpace 掩盖非法原始引用。
		// Reject controls before trimming delimiter whitespace, preserving validation intent.
		if strings.ContainsFunc(raw, unicode.IsControl) {
			info.Omitted++
			continue
		}
		ref := strings.TrimSpace(raw)
		if ref == "" {
			continue
		}
		if !safeSourceRef(ref) {
			info.Omitted++
			continue
		}
		if !seen[ref] {
			info.SourceRefs = append(info.SourceRefs, ref)
			seen[ref] = true
		}
	}
	var present int
	if err := s.db.Conn().QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='node_sources'`).Scan(&present); err != nil {
		return nil, fmt.Errorf("knowledge service source table: %w", err)
	}
	if present == 0 {
		info.Reason = "source_table_unavailable"
		return info, nil
	}
	rows, err := storage.NewNodeSourceRepo(s.db).ListByNode(ctx, node.ID)
	if err != nil {
		return nil, fmt.Errorf("knowledge service source locations: %w", err)
	}
	for _, row := range rows {
		if !safeSourceRef(row.FilePath) || (row.MemberID != "" && !safeSourceRef(row.MemberID)) ||
			(row.Skill != "" && !safeSourceRef(row.Skill)) {
			info.Omitted++
			continue
		}
		location := SourceLocation{MemberID: row.MemberID, Skill: row.Skill, FilePath: row.FilePath, SpanStatus: "unknown"}
		if row.StartLine > 0 && row.EndLine >= row.StartLine {
			location.StartLine, location.EndLine, location.SpanStatus = row.StartLine, row.EndLine, "available"
		}
		info.Locations = append(info.Locations, location)
	}
	if len(info.Locations) > 0 {
		info.Status, info.Reason = "available", ""
	}
	return info, nil
}

// 保守保留仓库相对标识；拒绝 URL、跨平台绝对路径、穿越、编码分隔符和控制字符。
// Keep canonical repository-relative identifiers; reject URL/absolute/traversal/encoded forms.
func safeSourceRef(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\\:%") || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}
