# Cairn

> 把散落在 Skill 文档里的领域知识，持续构建成一张可查询、可演化、可分发的知识图谱，
> 并通过 MCP 交付给 AI Agent。
>
> Continuously turn knowledge scattered across skill documents into a queryable,
> evolvable knowledge graph — and serve it to AI agents over MCP.

一套开源三件套：**构建端（DK Build）+ 查询端（MCP Service）+ 可视化（Viewer）**。

---

## 它解决什么问题

AI Agent 消费文档时面临两难：全量塞进上下文太贵且噪声大；用向量检索又丢失结构关系。

Cairn 走第三条路：**先把文档蒸馏成结构化知识图谱**（领域 → 子域 → 实体/概念 +
语义关系），Agent 按需精准检索子图。同时解决三个工程问题：

| 问题 | 做法 |
| --- | --- |
| 文档改了，图谱怎么跟上？ | **增量更新**：只重算受影响的子域，人的手工编辑被识别并保留 |
| 人能不能直接改图谱？ | **结构化回写**：图谱以 Markdown 块 + sidecar 形式写回源仓库，人改文档即改图谱 |
| 图谱怎么分发给团队？ | **不可变 Bundle + Catalog**：GitHub Release 分发，消费端自动热更新、零中断 |

---

## 架构

```
                          ┌──────────────────────────────────────┐
    你的 Skill 仓库  ────▶ │  build（构建端）                  │
    SKILL.md +            │  ├ cairnd          持续构建守护进程  │
    references/*.md       │  ├ cairn-ingest      全量提取流水线     │
                          │  ├ cairn-incremental 增量更新           │
                          │  └ cairn-rebalance   结构重整           │
                          └───────────────┬──────────────────────┘
                                          │ knowledge.db（SQLite + FTS5）
                                          │ → 打包为不可变 Bundle
                                          ▼
                          ┌──────────────────────────────────────┐
                          │  Catalog 仓库（GitHub）              │
                          │  stable.json 指针 + Release assets   │
                          └───────────────┬──────────────────────┘
                                          │ 轮询 + 原子热切换
                       ┌──────────────────┴───────────────────┐
                       ▼                                      ▼
        ┌──────────────────────────────┐      ┌───────────────────────────────┐
        │ service（查询端）         │      │ graph-viewer（可视化）        │
        │ ├ cairn-mcp                 │      │ 浏览器内 SQLite（sql.js）     │
        │ │   stdio / SSE / REST API   │      │ 3D 力导向图 + 演化时间线      │
        │ └ cairn  命令行只读查询      │      └───────────────────────────────┘
        └──────────────┬───────────────┘
                       ▼
          Claude Desktop / 任意 MCP client
```

构建端与查询端**严格分离**：查询端只读、零 LLM、零 embedding（由 `make verify` 的架构不变量检查强制保证）。

---

## 快速开始

### 环境要求

- Go 1.22+
- Node 18+（仅 graph-viewer 需要）
- 一个 OpenAI/Anthropic 兼容的 LLM 端点

### 1. 构建

```bash
make build          # 产出 8 个二进制到 bin/
```

### 2. 跑一次全量提取

构建端的三个流水线二进制走命令行参数（不读配置文件）：

```bash
export CAIRN_LLM_API_KEY="your-key"          # Key 只走环境变量，绝不写进配置

./bin/cairn-ingest \
  --repo /path/to/your-skills-repo \
  --db   ./knowledge.db
```

默认模型与端点是 DeepSeek（`--model` / `--endpoint` 可改）。完整参数见 `./bin/cairn-ingest -h`。

产出 `knowledge.db`（SQLite）。用 CLI 查一下（`--db-path` 是全局标志，须置于子命令之前）：

```bash
./bin/cairn --db-path ./knowledge.db find "订单聚合根"     # 按名称搜索实体及其入边
./bin/cairn --db-path ./knowledge.db impact "支付网关"     # 前向 BFS，看影响范围
./bin/cairn --db-path ./knowledge.db status                # 图谱概览
```

> **关于中文检索**：`nodes_fts` 使用 FTS5 默认的 `unicode61` 分词器，连续汉字是
> 单个 token，因此请传**完整节点名**而非子串（搜「订单聚合根」可命中，只搜「订单」不行）。
> 命中一个入口节点后，图遍历会把周边子图带出来。这是当前的既定设计，
> 原因与后续改法记录在 `service/internal/service/query_rewriter.go` 的文档注释里。

### 3. 挂给 AI Agent（MCP）

```bash
cp configs/mcp-local.example.yaml configs/mcp-local.yaml   # 改 path 指向你的 .db
./bin/cairn-mcp --config configs/mcp-local.yaml           # stdio 传输
```

需要团队共享（HTTP/SSE + 免鉴权 REST API）：

```bash
cp configs/mcp-http.example.yaml configs/mcp-http.yaml
./bin/cairn-mcp --config configs/mcp-http.yaml --listen :8080

curl 'http://localhost:8080/api/search?keyword=aggregate&kg=my-skills&limit=5'
```

想确认 MCP 链路是否通，跑一次冒烟测试：

```bash
scripts/dev/smoke-mcp.sh configs/mcp-local.yaml my-skills
```

### 4. 可视化

```bash
cd graph-viewer && npm install && npm run dev
# 浏览器打开后，直接把你的 knowledge.db 拖进页面
```

### 无凭证试跑整条流水线

`cairnd` 支持 `--fake` 模式，用内存版 Forge 替代 GitHub，无需任何凭证即可走完
「构建 → PR → Catalog → stable」全流程：

```bash
cp configs/cairnd.example.yaml configs/cairnd.yaml
./bin/cairnd run --once --fake --config configs/dkd.yaml
```

---

## 组件

| 二进制 | 归属 | 职责 |
| --- | --- | --- |
| `cairnd` | build | 持续构建守护进程：轮询源仓库 → 构建 → 开 PR → 发布 Bundle → 提升 stable |
| `cairnctl` | build | dkd 的运维 CLI（查看 run 状态、重试、解除阻塞） |
| `cairn-ingest` | build | 全量提取流水线（首次建库 / 指纹变更后重建） |
| `cairn-incremental` | build | 增量更新（文档改动后最小化重算） |
| `cairn-rebalance` | build | 结构重整（漂移达阈值时全局化简） |
| `cairn-evolve` | build | 演化数据 HTTP 服务（供 Viewer 的时间线视图） |
| `cairn-mcp` | service | MCP Server：stdio / SSE / REST 三种传输，支持 catalog 自动热更新 |
| `cairn` | service | 命令行只读查询：`find` `impact` `show` `status` `why` `timeline` `sentinel` |

### 目录结构

```
core/           共享库：storage(SQLite+FTS5) / kbbundle(打包分发) / githubapp(鉴权)
                       metrics(图质量哨兵) / evolve+observe(演化) / dktypes / dkconfig
build/       构建端：cmd/* 入口 + internal/{pipeline,extract,incremental,rebalance,
                       writeback,controller,...}
service/     查询端：cmd/{dk,cairn-mcp} + internal/service（只读、零 LLM）
graph-viewer/   前端：Vite + React + TypeScript，浏览器内 SQLite
configs/        配置示例（*.example.yaml）
deploy/         Dockerfile / docker-compose / systemd unit
scripts/        单机流水线编排与冒烟测试
```

---

## 核心设计

**双层知识图谱** — `Domain → Subdomain` 为结构层，`Entity / Concept` 为内容层，
两层用 `composes` 层级边连接，语义关系（`depends_on` / `references` / `generalizes` 等
6 种枚举）只在内容层之间。

**结构化回写** — 提取结果以带 UUID 锚点的 Markdown 块 + `.kg.yaml` sidecar 写回源仓库。
块内分「可编辑区」与「只读区」：人改可编辑区会被识别为内容编辑并写入图谱（provenance
升级为 `human_curated`，此后 LLM 不再覆盖）；只读区被篡改则忽略并还原。

**增量的最小扰动** — 变更被分为四类：C1 内容编辑（旁路，零 LLM）、C2 块删除、
C3 只读区篡改、C4 新增内容（走 LLM 管道）。脏集封闭保证「未涉及对象零改动」：
新增一个节点只会重算它所在的那一个子域。

**LLM 提议、代码裁决** — 节点 UUID 由 member 集合确定性派生（不是随机，保证幂等）；
LLM 只见 alias、绝不见 UUID；输出必过校验（枚举、端点存在性、alias 命中、自环过滤），
不合法则降级跳过而非写坏数据。

**不可变 Bundle 分发** — Release tag 内嵌 `bundle_digest`，同一 commit 的不同构建产出
不同 tag、互不覆盖，历史版本可回滚。消费端解包后校验 digest，再原子热切换。

---

## 配置

配置文件**不含任何密钥**：LLM Key 走环境变量（`api_key_env` 指定变量名），
GitHub App 私钥走文件路径或环境变量。

`.gitignore` 已设为「只跟踪 `*.example.yaml`」，防止误提交含私钥路径或私有仓库名的本地配置。

| 示例配置 | 用途 |
| --- | --- |
| `configs/cairnd.example.yaml` | cairnd 守护进程：GitHub App、源仓库、catalog、阈值 |
| `configs/mcp-local.example.yaml` | MCP stdio + 本地静态 db（最简） |
| `configs/mcp-catalog-watch.example.yaml` | MCP stdio + catalog 自动热更新 |
| `configs/mcp-http.example.yaml` | MCP HTTP/SSE + REST API |
| `configs/abbreviations.yaml` | 可选的缩写映射表（查询时把缩写展开为全称） |

---

## 开发

```bash
make build      # 构建全部二进制
make fmt        # goimports + gofmt
make vet        # go vet
make verify     # 完整门禁：vet + 构建 + 架构不变量检查
make help       # 列出全部命令
```

`make verify` 会强制检查若干架构不变量，例如：查询端不得 import LLM 包、
查询端不得写入 KG 结构、演化层不得引入 embedding/向量库。这些是设计约束，
不是风格偏好。

> **关于测试**：本仓库当前未包含单元测试文件，`make test` 会输出一片
> `no test files`——这是已知状态而非故障。欢迎贡献测试，约定见
> [CONTRIBUTING.md](CONTRIBUTING.md)。

## License

[MIT](LICENSE)
