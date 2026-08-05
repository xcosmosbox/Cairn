// Package extract 的本文件提供给增量流水线复用的公开包装：
// 把包内既有的 slugify 与 relation kind 枚举校验暴露出去，避免增量包另造一套
// （DRY：slug 规则与 6 枚举是跨阶段必须一致的单一事实源）。
//
// This file exposes small public wrappers around the package's existing slugify
// and relation-kind validation so the incremental pipeline reuses one source of
// truth instead of re-implementing them.
package extract

// Slugify 把（中文）名称转为 kebab-case 标识（包内 slugify 的公开包装）。
// 增量流水线在「新建 domain/subdomain」时用它派生 slug，与全量流水线完全一致。
//
// Slugify converts a (Chinese) name into a kebab-case identifier; public wrapper
// around the internal slugify so the incremental pipeline derives slugs exactly
// like the full pipeline.
func Slugify(s string) string {
	return slugify(s)
}

// IsValidRelationKind 校验 relation.kind 是否为允许的 6 个语义枚举之一
// （validRelationKinds 的公开包装）。增量 relation 重算（I-7）用它做越界校验，
// 与全量 relate 阶段同一枚举集。
//
// IsValidRelationKind reports whether kind is one of the 6 allowed semantic
// relation kinds; public wrapper so incremental relation recomputation shares
// the full pipeline's enum set.
func IsValidRelationKind(kind string) bool {
	return validRelationKinds[kind]
}
