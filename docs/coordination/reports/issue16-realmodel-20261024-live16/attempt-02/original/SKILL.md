---
name: public-synthetic-queue
description: Synthetic multi-document input example; never private evaluation data.
---

# 合成队列示例 / Synthetic queue example

所有业务、名称和规则均为合成教学材料。此目录不是历史评测数据，也不代表生产建议。
All content is invented for demonstrating a small linked Skill document layout.

从[队列容量](references/queue.md)理解阻塞行为，再读[超时与重试预算](references/timeout.md)。
两个文档共享 `阻塞队列`，并用显式链接表达 `重试预算 → 阻塞队列` 的依赖意图。

这些文档展示可供真实构建器读取的输入格式；当前 `cairnd --fake` **不会读取这个目录**。
免费演示使用程序内固定的单概念知识，预期输出见 [README](README.md)。
真实 LLM 抽取/跨文档关系是否保留在本示例中未运行、未验证。
