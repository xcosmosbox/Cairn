# graph-viewer

Domain Knowledge Layer 的知识图谱可视化前端。**纯浏览器端运行**——用
[sql.js](https://sql.js.org/)（SQLite 编译到 WebAssembly）直接在页面里读取
`knowledge.db`，不需要后端。

## 功能

- **3D 力导向图**（`react-force-graph`）与 **2D 大图渲染**（Sigma.js），按图规模切换
- **双层视图**：`Domain → Subdomain` 结构层与 `Entity / Concept` 内容层
- **节点详情**：description、来源文档、member、语义关系
- **演化时间线**：知识图谱历次构建的变更集与指标趋势
- **哨兵趋势**：模块度 Q、单例率、边节点比等图质量指标

## 运行

```bash
npm install
npm run dev        # http://localhost:5173
```

浏览器打开后，**把 `knowledge.db` 拖进页面**即可（或用侧栏的文件选择器）。

### 从哪拿 knowledge.db

本仓库**不包含任何知识库数据**（`.db` 已被 `.gitignore` 排除，因为它可能含私有业务知识）。
自己产出一个：

```bash
cd ..
make build
export DK_LLM_API_KEY="your-key"
./bin/dk-ingest --repo /path/to/your-skills-repo --db ./knowledge.db
```

也可以从 catalog 拉取已发布的 Bundle（见根 README 的 Bundle 分发章节）。

### 演化时间线需要额外服务

时间线与指标趋势视图从 `dk-evolve-serve` 读取演化数据。它读的是**演化产物目录**
（含 `evolution.db` 与 `snapshots/`），默认位于 `.db` 同级的 `evolution/`：

```bash
cd ..
./bin/dk-evolve-serve --dir ./evolution
# 默认监听 127.0.0.1:7801，可用 --addr 改
```

前端默认请求 `http://127.0.0.1:7801`，与上面的默认值一致。若改了 `--addr`，
同步调整 `src/services/evolution-api.ts` 的 `EVOLUTION_DEFAULT_BASE_URL`。

### 可选：启动即加载远端 stable bundle

设置环境变量后，前端启动会自动拉取指定的 `knowledge.db` 作为初始数据库，
省去手动拖文件：

```bash
VITE_STABLE_BUNDLE_URL=http://127.0.0.1:7801/stable/my-skills/knowledge.db \
VITE_STABLE_BUNDLE_NAME=stable \
npm run dev
```

## 构建产物

```bash
npm run build      # 输出到 dist/
npm run preview    # 本地预览生产构建
```

`public/sql-wasm.wasm` 是 sql.js 的 WebAssembly 运行时，必须随产物一起部署。

## 技术栈

Vite · React · TypeScript · Zustand（状态）· sql.js（浏览器内 SQLite）·
react-force-graph（3D）· Sigma.js（2D 大图）
