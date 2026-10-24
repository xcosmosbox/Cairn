# 免费公开演示 / Free public demo

从空目录开始，不需要模型 key、GitHub App 或私有 archive。
本例复用既有 `cairnd --fake`：固定合成知识 → 本地模拟 Source/Catalog PR →
不可变 Bundle → 只读 CLI/MCP → 已存来源元数据。

**两类材料要分开：** [SKILL.md](SKILL.md) 与两个 [reference 文档](references/queue.md)
展示一个有跨文档链接的合成输入。当前 fake **不读取这些文件**，只生成程序内固定的
“阻塞队列”概念。本次不运行真实 LLM 抽取，也不声称生成了输入中的跨文档语义关系。
预期值与未执行项在 [expected-queries.json](expected-queries.json) 中分别列出。

## 从干净目录运行

需要 Git、Go 1.22+、Python 3（仅标准库）。首次构建可能需要下载 Go modules；不需要
Node/Viewer 或模型凭据。Go 不在 PATH 时，可参考已验收 [#10 的官方工具链恢复步骤](../../docs/coordination/reports/issue10-restore-20261024-env/README.md#environment-and-reproducible-activation)。

```bash
git clone https://github.com/xcosmosbox/Cairn.git cairn-public-demo
cd cairn-public-demo
go version
python3 --version
python3 examples/public-demo/demo.py
```

[demo.py](demo.py) 是小型示例验证器，不是新的构建器：它调用 `make build` 和现有二进制，
找到这次实际创建的 fake session 与 stable manifest，校验文件摘要，生成仅指向这个 DB
的本地 MCP 配置，再用真实 CLI 与 stdio MCP 会话检查内容。它不会将示例文档灌入数据库。
运行可重复；每次创建独立 session 与 `.local/public-demo-*` 回执，不覆盖此前结果。

成功后打印三行结论和实际 `Receipt: /.../.local/public-demo-.../receipt.json` 路径。
回执保留基线 SHA、命令/退出码/输出、实际 Bundle manifest、MCP 配置路径、完整 JSON-RPC
请求响应与查询前后 SHA256。`mcp-stderr.log` 保存实际 server 日志。二者是公开合成输出，
位于忽略的本地目录，不是凭据配置或私有业务数据。

## 应看到什么

| 操作 | fixed fake 的实际预期 |
| --- | --- |
| fake 控制器 | 最终 `Stable`，模拟 source/catalog 两次 PR 合并 |
| stable Bundle | `kg=demo`；实际 `kb-manifest.json`、`checksums.sha256`、`knowledge.db` |
| `cairn status` | 4 个节点、3 条边 |
| CLI/MCP 查 `阻塞队列` | 一个已知概念；摘要 `Bounded queue blocks when full or empty` |
| `resolve_node` 默认 | 一个原有文本 content item |
| 显式 `include_sources: true` | 第二个 JSON 文本 item；`source_refs` 包含 `fake`，相对路径 `SKILL.md`，span 6–8 |
| 所有查询后 | stable Bundle 三个文件字节保持不变 |

固定 fixture 的概念 UUID 是 `ccd9e2cf-1848-51c3-b013-229fbcef88b2`。验证器仍从实际 DB
读取并核对 UUID，没有仅凭 transport 成功或工具退出 0 判为语义通过。
这里的来源路径指向 fake 自己生成的 `SKILL.md`，**不是本目录的 SKILL.md**。
来源元数据不等于原文内容；本接口不读取文件或推断 repository URL/commit。

## MCP 客户端如何请求来源

使用回执中 `mcp_config` 的实际路径启动 `bin/cairn-mcp --config ...`，或让客户端用
同样的 command/args 配置启动它。先完成握手并收到 initialize 响应，再发 initialized 通知：

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"public-demo","version":"1"}}}
```

```json
{"jsonrpc":"2.0","method":"notifications/initialized"}
```

然后调用：

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"resolve_node","arguments":{"kg":"demo","uuid":"ccd9e2cf-1848-51c3-b013-229fbcef88b2","include_sources":true}}}
```

读取 `result.content[1].text` 中的 JSON；它带有 `requested_uuid`、
`scope=resolved_node_only` 与 `source_metadata.node_uuid`，便于核对当前存活节点归属。
省略选项或传 false 不返回该附加块。[完整契约 ADR](../../docs/decisions/0002-mcp-source-citations.md)
说明 merge/split、缺失来源、非法路径与现有 KG 访问边界。

## 负例也属于验收

验证器实际执行这些负例并断言不同的结果类型：

| 负例 | 预期 |
| --- | --- |
| CLI 指向一个不存在的 DB | 退出 1；不得创建 DB |
| CLI/MCP 查 `重试预算` | 退出/协议成功，但明确零命中；新增 reference 文件未被 fake 消费 |
| MCP resolve `public-demo-missing-uuid`，include_sources=true | 来源 `status=unknown`，没有捏造 UUID/文件位置 |

不能把“零结果但协议成功”写成已找到知识，也不能把合成输入的预期关系写成真实抽取结果。
实际复跑与 #16 原验收条件的逐项边界见本目录的验证回执说明（随验收 PR 归档）。

## 还没有证明的内容

跨文档材料意图是 `重试预算 depends_on 阻塞队列`，见 [timeout.md](references/timeout.md)。
其真实消费、关系抽取与检索效果未执行。此免费路径验证控制器/Bundle/查询/来源元数据，
不验证 LLM 质量、GraphRAG 收益、生产 GitHub App 鉴权或付费 pilot。它不会自动关闭 #16：
若验收要求免费流程实际消费多文档，此项仍须保留为 partial。
