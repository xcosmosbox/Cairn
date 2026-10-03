// Package writeback 的本文件实现 shared 判定与文档分组逻辑。
//
// shared 判定（决策总账 §4.3）：一个 node 的来源文档（经 MemberSources 的 distinct
// file_path）跨越 ≥2 个不同 file_path → shared=true。
//   - shared node 的可编辑副本（primary）写到 _shared/<domain-slug>/<uuid>.md；
//   - 在每个来源文档中，该 shared node 渲染为只读镜像块。
//   - 非 shared node 直接在其唯一来源文档写完整块。
//
// This file implements shared detection and per-document grouping. A node is
// shared when its distinct source file_paths ≥ 2; shared nodes get a primary in
// _shared/<domain-slug>/<uuid>.md and read-only mirror blocks in each source doc.
package writeback

import "fmt"

// IsSharedSourceFiles 使用来源账本中不同且非空的路径判定 shared，不按来源行或
// member 个数计数；渲染与发布验证必须使用同一个规则。
// IsSharedSourceFiles reports whether at least two distinct source paths exist.
func IsSharedSourceFiles(paths []string) bool {
	seen := make(map[string]bool)
	for _, path := range paths {
		if path != "" {
			seen[path] = true
			if len(seen) >= 2 {
				return true
			}
		}
	}
	return false
}

// docGroup 是按 file_path 聚合的回写分组：该文档需要渲染的 node 视图集合。
// 一个 nodeView 可能同时出现在多个 docGroup（shared node 的镜像块在每个来源文档一份）。
//
// docGroup is a per-file_path write-back group: the node views to render in that doc.
type docGroup struct {
	FilePath string
	Nodes    []nodeView // 该文档要渲染的 node（非 shared 完整块 / shared 镜像块）
}

// groupByDoc 把全部 nodeView 按来源文档分组，返回每个文档要渲染的 node 集合。
//   - 非 shared node：加入其唯一来源文档（完整块）。
//   - shared node：加入其每个来源文档（镜像块），并记录为需写 primary。
//
// groupByDoc groups all node views by source document for rendering.
func groupByDoc(views []nodeView) (groups []docGroup, primaries []nodeView) {
	byPath := make(map[string]*docGroup)
	var pathOrder []string

	// getOrCreate 按路径取或建分组，保持首次出现顺序。
	// getOrCreate fetches or creates a group by path, preserving first-seen order.
	getOrCreate := func(fp string) *docGroup {
		if g, ok := byPath[fp]; ok {
			return g
		}
		g := &docGroup{FilePath: fp}
		byPath[fp] = g
		pathOrder = append(pathOrder, fp)
		return g
	}

	for i := range views {
		v := views[i]
		if len(v.SourceFiles) == 0 {
			// 无来源文档：跳过（不应发生；防御 repair/supplement 新建节点无来源的边界）。
			// No source: skip (defensive).
			continue
		}
		if v.Shared {
			// shared：在每个来源文档放镜像块；并收集为 primary。
			for _, fp := range v.SourceFiles {
				g := getOrCreate(fp)
				g.Nodes = append(g.Nodes, v)
			}
			primaries = append(primaries, v)
		} else {
			// 非 shared：唯一来源文档放完整块。
			// Non-shared: full block in its single source doc.
			fp := v.SourceFiles[0]
			g := getOrCreate(fp)
			g.Nodes = append(g.Nodes, v)
		}
	}

	for _, fp := range pathOrder {
		groups = append(groups, *byPath[fp])
	}
	return groups, primaries
}

// isMirrorForDoc 判断一个 nodeView 在来源文档中是否应渲染为镜像块。
// shared node 在所有来源文档中都是镜像块（文档参数无意义，保持签名简单）。
//
// isMirrorForDoc reports whether a node view should be a mirror in a source doc.
func isMirrorForDoc(v nodeView) bool {
	return v.Shared
}

// primaryAbsPath 返回 shared node primary 文件的绝对路径。
// primaryAbsPath returns the absolute path of a shared node's primary file.
func primaryAbsPath(repoRoot string, v nodeView) string {
	return fmt.Sprintf("%s/%s", repoRoot, primaryFilePath(v))
}
