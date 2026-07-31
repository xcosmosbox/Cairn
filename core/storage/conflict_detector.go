// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含 ConflictDetector 结构体，在入库时检测同名概念冲突。
// 使用 Jaro-Winkler 相似度算法（对中文更友好），阈值 0.85。
//
// Package storage implements the persistence layer for the Cairn,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the ConflictDetector struct, which detects name conflicts
// between concepts during ingest. Uses the Jaro-Winkler similarity algorithm
// (more Chinese-friendly), with a threshold of 0.85.
package storage

import (
	"context"
	"fmt"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// ConflictDetector 在入库时检测同名概念冲突。
// 使用 NodeRepo.SearchByNameSimilarity 进行初筛（Levenshtein 距离），
// 再通过 Jaro-Winkler 相似度算法进行二次精筛，阈值 0.85。
//
// ConflictDetector detects name conflicts between concepts during ingest.
// Uses NodeRepo.SearchByNameSimilarity for initial filtering (Levenshtein distance),
// then applies the Jaro-Winkler similarity algorithm for refined filtering
// with a threshold of 0.85.
type ConflictDetector struct {
	nodeRepo *NodeRepo
}

// NewConflictDetector 创建新的 ConflictDetector 实例。
//
// NewConflictDetector creates a new ConflictDetector instance.
func NewConflictDetector(db *DB) *ConflictDetector {
	return &ConflictDetector{nodeRepo: NewNodeRepo(db)}
}

// DetectConflicts 检测新增 entities 与现有 entities 之间的名称冲突。
// 使用 Levenshtein 距离进行初筛，再以 Jaro-Winkler 相似度 > 0.85 进行精筛。
//
// DetectConflicts detects name conflicts between new entities and existing entities.
// Uses Levenshtein distance for initial filtering, then Jaro-Winkler similarity > 0.85
// for refined filtering.
func (d *ConflictDetector) DetectConflicts(ctx context.Context, newEntities []*dktypes.Node) ([]*dktypes.ConflictReport, error) {
	var reports []*dktypes.ConflictReport
	for _, ne := range newEntities {
		// 按名称相似度搜索已有的节点（Levenshtein 距离 ≤ 3）
		// Search existing nodes with similar names (Levenshtein distance ≤ 3)
		existing, err := d.nodeRepo.SearchByNameSimilarity(ctx, ne.Name, 3)
		if err != nil {
			return nil, fmt.Errorf("search similar for %s: %w", ne.Name, err)
		}
		for _, ex := range existing {
			if ex.ID == ne.ID {
				continue
			}
			sim := jaroWinklerSimilarity(ne.Name, ex.Name)
			if sim > 0.85 {
				reports = append(reports, &dktypes.ConflictReport{
					EntityName:         ne.Name,
					ConflictingSources: fmt.Sprintf(`["%s","%s"]`, ne.SourceRefs, ex.SourceRefs),
					ResolutionStatus:   dktypes.ResolutionUnresolved,
				})
			}
		}
	}
	return reports, nil
}

// jaroWinklerSimilarity 计算两个字符串的 Jaro-Winkler 相似度（对中文更友好）。
// 返回值范围 [0.0, 1.0]，1.0 表示完全匹配。
//
// jaroWinklerSimilarity computes the Jaro-Winkler similarity between two strings
// (more friendly for Chinese text). Returns a value in the range [0.0, 1.0],
// where 1.0 indicates an exact match.
func jaroWinklerSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 || len(br) == 0 {
		return 0.0
	}

	// 匹配窗口 / Matching window
	maxDist := max(len(ar), len(br))/2 - 1
	if maxDist < 0 {
		maxDist = 0
	}

	// 查找匹配的字符 / Find matching characters
	matches := 0
	am := make([]bool, len(ar))
	bm := make([]bool, len(br))
	for i := range ar {
		start := i - maxDist
		if start < 0 {
			start = 0
		}
		end := i + maxDist + 1
		if end > len(br) {
			end = len(br)
		}
		for j := start; j < end; j++ {
			if bm[j] || ar[i] != br[j] {
				continue
			}
			am[i] = true
			bm[j] = true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0.0
	}

	// 计算换位次数 / Count transpositions
	var t float64
	k := 0
	for i := range ar {
		if !am[i] {
			continue
		}
		for !bm[k] {
			k++
		}
		if ar[i] != br[k] {
			t += 0.5
		}
		k++
	}

	// Jaro 相似度 / Jaro similarity
	sim := (float64(matches)/float64(len(ar)) +
		float64(matches)/float64(len(br)) +
		(float64(matches)-t)/float64(matches)) / 3.0

	// Winkler 前缀奖励（取前 4 个字符的公共前缀）
	// Winkler prefix bonus (common prefix of up to 4 characters)
	prefix := 0
	for i := 0; i < min(4, min(len(ar), len(br))); i++ {
		if ar[i] == br[i] {
			prefix++
		} else {
			break
		}
	}
	return sim + 0.1*float64(prefix)*(1.0-sim)
}

// 注意：levenshteinDistance 已在 node_repo.go 中定义，此处不再重复。
// Note: levenshteinDistance is already defined in node_repo.go; not redefined here.
