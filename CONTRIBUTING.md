# 贡献指南 / Contributing

感谢你对 Cairn 的兴趣。本文档说明开发约定与不可违背的架构约束。

多人或多 Agent 协作请先读 [AGENTS.md](AGENTS.md) 和
[协作协议](docs/coordination/PROTOCOL.md)。[看板 #7](https://github.com/xcosmosbox/Cairn/issues/7)
管理任务，Issue 评论记录 session、领取/续期与交接，PR 承载实现与评审。独立分支可 provisional
推进，重叠写入和接管需协调；不要依赖共享 GitHub 账号的 Assignee 区分 agent。

## 开发环境

```bash
go version      # 需要 1.22+
node -v         # 需要 22+（仅 graph-viewer，与 CI 一致）

make help       # 列出全部命令
make build      # 构建全部二进制到 bin/
make fmt        # goimports + gofmt -s
make vet        # go vet
make verify     # 可选本地完整检查；CI 保留正常检查
```

## 架构铁律（不可违背）

以下约束由 `make verify` 的 `check-invariants` 目标以 grep 自查强制执行，
违反会导致门禁失败。它们是**设计约束，不是风格偏好**：

| 约束 | 含义 |
| --- | --- |
| 查询端零 LLM | `service/internal/service` 不得 import LLM 包 |
| 查询端只读 | 查询端不得对 KG 结构做 Insert / Update / Delete / 重建 |
| 演化层不写 KG | `core/evolve` 不得写 node / edge |
| 零 embedding | 查询端、哨兵、演化层不得引入 embedding / faiss / hnsw |
| 热更新链路存活 | `ResolveLatest` 必须有生产调用点（否则 catalog 热更新已断） |

此外，代码中还有一批以编号形式标注的运行时铁律（`R1`、`R2`、`R8`、`R9` 等），
它们写在相关函数的文档注释里，例如：

- **R1** — 机器身份使用本次请求 alias；原文中的 UUID 字面量保留
- **R2** — 融入既有节点时 UUID 钉死，绝不重新派生
- **R8** — `human_curated` 的内容不被 LLM 覆盖
- **R9** — 脏集封闭：未涉及对象零改动

修改相关代码前请先读懂对应注释。约束变更须在任务决策与相关设计文档中明确说明。

## 代码约定

**注释双语** — 关键类型、导出函数、复杂逻辑用「中文在前、英文补充」的双语注释。
中文讲清「为什么这样做」，英文给出简洁的行为描述。

**注释解释动机，而非复述代码** — 优先记录「为什么」，尤其是那些不这样写就会出错的原因。
修复缺陷时，把根因与失败模式写进注释，这是防止回归的第一道防线。

**错误信息带上下文前缀** — 形如 `kbbundle.PullRemote: 下载 asset: %w`，
便于从日志直接定位调用链。

**降级优于写坏数据** — 面对 LLM 的非法输出（枚举越界、引用不存在的 alias、字段缺失），
一律记录告警并跳过，绝不把可疑数据写入图谱。

**可观测性是硬要求** — 任何「跳过 / 降级 / 删除」都必须留下日志，且日志要能区分
「按设计跳过」与「异常失败」。只报计数不报明细的日志会让缺陷长期潜伏。

## 测试

仓库包含缺陷回归测试。提交前运行受影响的测试与二进制构建；正常 CI 保留。
只有变更触及并发或查看器部署时，才运行相应 race 或 `npm run test:subpath`。
`make verify` 与全仓 race 可按变更需要运行，不是每次 PR 或小实验的前置条件。约定：

- 新增测试文件命名为 `zz_<主题>_test.go`，与被测源码同目录（Go 的包内测试要求）
- 修复缺陷时，测试注释里写清**原本的失败模式**，而不只是断言正确行为
- 涉及 SQLite 的测试用 `t.TempDir()`，不要依赖仓库内的固定路径
- 可并行的测试加 `t.Parallel()`

## 提交与 PR

- Commit message 用 `<type>(<scope>): <subject>`，例如 `fix(incremental): 补回被 LLM 遗漏的既有边`
- 一个 PR 只做一件事；重构与功能变更请分开提
- PR 记录实际运行的相关测试、构建及退出结果
- 若改动涉及架构约束或数据格式（尤其是 Bundle 结构、FTS 索引形态），请在 PR 描述里
  明确说明兼容性影响——这类改动会让既有 Bundle 失效

## 报告缺陷

请尽量附上：

1. 复现步骤或触发条件
2. **完整日志**（本项目的阶段日志信息量很大，往往能直接定位根因）
3. 涉及的知识库规模（节点数 / 边数 / 文档数）——很多问题只在特定规模下出现

如果你怀疑是 LLM 输出导致的问题，请一并说明使用的模型与端点。

## 小规模真实实验

执行 [#105 采用的简化计划](docs/coordination/reports/2026-10-26-simplification-plan.md)：
最小相关检查通过后直接运行已声明实验。原始输入、请求、响应、错误与全部计划分母保留；
缺失 usage/费用记 unknown，上传失败不阻塞。真实污染只使其依赖范围失败，独立问题继续。
不把历史归档、全609、恢复演练、重复技术 GO 或发布清单当作小实验前置条件。
