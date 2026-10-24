# 超时与重试 / Timeout and retry

**Synthetic input — not consumed by the fixed fake runner.**

## 重试预算 / Retry budget

在这个虚构例子中，生产者最多等待 50 毫秒，最多重试 2 次；预算用尽后返回可见错误。
这些数字仅供输入结构演示，不是调优建议。

`重试预算` 依赖[阻塞队列](queue.md#阻塞队列--blocking-queue)的容量与等待行为。
若未来用真实 LLM 构建这些材料，可以检验是否保留这条跨文档关系；本次没有运行该抽取。
免费 fixed fake 中不存在 `重试预算`，查询它应明确得到零结果。
