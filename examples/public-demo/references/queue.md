# 阻塞队列 / Blocking queue

**Synthetic input — not consumed by the fixed fake runner.**

这个虚构队列容量为 8。满时生产者等待，空时消费者等待；这解释了 `阻塞队列` 的基本含义。
Bounded queue blocks when full or empty.

当生产者不能无限等待时，调用方使用[重试预算](timeout.md#重试预算--retry-budget)。
跨文档意图是：重试预算依赖队列的阻塞行为，而不是与队列无关的独立概念。
此语义是合成输入中的说明，不是已观测的图谱输出。
