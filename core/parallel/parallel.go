// Package parallel 提供受全局并发上限约束的并发执行原语。
//
// 领域知识层所有「打同一个 LLM 模型后端」的并发阶段——文档标注、按 subdomain 的
// description 融合重写、按 subdomain 的 relation 识别——共用同一个全局并发上限
// MaxConcurrency。它们共享同一后端，超过上限会触发模型侧限流，故上限必须全局统一。
//
// Package parallel provides a concurrency primitive bounded by a global cap shared by
// all LLM-fanout stages (they hit the same model backend).
package parallel

import "sync"

// MaxConcurrency 是全局 LLM 并发上限（同一模型后端）。所有 LLM 并发阶段以它为上界。
// MaxConcurrency is the global cap on concurrent LLM calls (same model backend).
const MaxConcurrency = 300

// ForEachIndexed 以不超过 limit 的并发度遍历 items，对每个元素调用 fn（携带下标与元素）。
//
//   - limit ≤ 0 或 > MaxConcurrency 时，一律钳制到 MaxConcurrency（全局上限不可被突破）；
//   - fn 之间必须无共享可变状态竞争（约定：各 fn 只写自己负责的那部分数据）；
//   - 本原语刻意不收集 / 传播 error——单元素失败由 fn 内部处理并记录日志，不阻断其余元素
//     （契合流水线「单元失败降级、不中断整体」的契约）。
//
// ForEachIndexed runs fn over items with at most `limit` concurrent goroutines (clamped
// to MaxConcurrency). It does not collect errors; each fn handles its own failure.
func ForEachIndexed[T any](limit int, items []T, fn func(i int, item T)) {
	if limit <= 0 || limit > MaxConcurrency {
		limit = MaxConcurrency
	}
	if len(items) == 0 {
		return
	}

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		sem <- struct{}{} // 获取令牌（满则阻塞，实现限流）
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }() // 释放令牌
			fn(idx, items[idx])
		}(i)
	}
	wg.Wait()
}
