.PHONY: all build build-build build-service test vet verify check-invariants fmt lint clean help

# 二进制列表（构建端 / 查询端）
BUILD_BINS   := dkd dkctl dk-ingest dk-incremental dk-rebalance dk-evolve-serve
SERVICE_BINS := dk mcp-server

# ═══════════════════════════════════════════════════════════════════
# help — 默认目标：列出常用命令
# ═══════════════════════════════════════════════════════════════════
help:
	@echo "Domain Knowledge Layer — 常用命令 / common targets"
	@echo ""
	@echo "  make build      构建全部 8 个二进制到 bin/"
	@echo "  make all        下载依赖 → 构建 → 门禁检查"
	@echo "  make verify     提 PR 前必跑：vet + 构建 + 架构不变量检查"
	@echo "  make fmt        goimports + gofmt -s"
	@echo "  make vet        go vet"
	@echo "  make test       跑单元测试（当前仓库尚未包含测试文件，见 CONTRIBUTING.md）"
	@echo "  make lint       golangci-lint（需自行安装，仓库未提供 .golangci.yml）"
	@echo "  make clean      删除 bin/ 与覆盖率产物"
	@echo ""

# ═══════════════════════════════════════════════════════════════════
# all — 一键完成：下载依赖 → 构建 → 门禁检查
# ═══════════════════════════════════════════════════════════════════
all:
	go mod download
	$(MAKE) build
	$(MAKE) verify
	@echo "✅ all done"

# ═══════════════════════════════════════════════════════════════════
# build — 构建八个二进制
#
# 构建端（dk-build/）：
#   dkd             持续构建守护进程：轮询源仓库 → 构建 → 开 PR → 发布 Bundle → 提升 stable
#   dkctl           dkd 的运维 CLI（查看 run 状态、重试、解除阻塞）
#   dk-ingest       全量提取流水线（首次建库 / 指纹变更后重建）
#   dk-incremental  增量流水线（人编辑文档后最小化更新）
#   dk-rebalance    全量重整（漂移达阈值时全局化简）
#   dk-evolve-serve 演化数据 HTTP 服务（供 Viewer 的时间线视图）
#
# 查询端（dk-service/）：
#   dk              命令行只读查询
#   mcp-server      MCP Server（stdio / SSE / REST 三种传输）
# ═══════════════════════════════════════════════════════════════════
build: build-build build-service
	@echo "✅ binaries built → bin/ ($(words $(BUILD_BINS) $(SERVICE_BINS)) binaries)"

# 仅构建端 / build-only the build-side binaries
build-build:
	@for b in $(BUILD_BINS); do \
		go build -o bin/$$b ./dk-build/cmd/$$b/ || exit 1; \
	done

# 仅查询端 / build-only the query-side binaries
build-service:
	@for b in $(SERVICE_BINS); do \
		go build -o bin/$$b ./dk-service/cmd/$$b/ || exit 1; \
	done

# ═══════════════════════════════════════════════════════════════════
# 测试
#
# 注意：本仓库当前不包含测试文件，`make test` 会输出一片 "no test files"。
# 这是已知状态而非故障，欢迎贡献测试（约定见 CONTRIBUTING.md）。
# ═══════════════════════════════════════════════════════════════════
test:
	@echo "── go test ./...（当前仓库无测试文件，输出 no test files 属预期）──"
	go test -parallel 8 -count=1 ./...

# ═══════════════════════════════════════════════════════════════════
# 开发辅助
# ═══════════════════════════════════════════════════════════════════
fmt:
	goimports -w .
	gofmt -s -w .

vet:
	go vet ./...

# lint 需要自行安装 golangci-lint；本仓库未提供 .golangci.yml，走工具默认规则集。
lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ coverage.out coverage.html

# ═══════════════════════════════════════════════════════════════════
# verify — 提 PR 前的完整门禁：vet + 构建 + 架构不变量检查
# ═══════════════════════════════════════════════════════════════════
verify: vet build check-invariants
	@echo "✅ verify 全绿 / all checks passed"

# ═══════════════════════════════════════════════════════════════════
# check-invariants — 架构不变量 grep 自查
#
# 这些是**设计约束**而非风格偏好：构建端与查询端严格分离，查询端只读、
# 零 LLM、零 embedding。任一条被违反都意味着架构被破坏，故以门禁形式固化。
# 详见 CONTRIBUTING.md 的「架构铁律」一节。
# ═══════════════════════════════════════════════════════════════════
check-invariants:
	@echo "── 架构不变量检查 / architecture invariant checks ──"
	@# 1. 查询端读侧确实调用了 bundle 版本解析（防止 catalog 热更新退化为死代码）
	@grep -rn "ResolveLatest" dk-service core/storage --include='*.go' | grep -q . \
		&& echo "✓ 查询端已接入 bundle 版本解析（ResolveLatest）" \
		|| (echo "✗ ResolveLatest 无任何调用点：catalog 热更新链路可能已断"; exit 1)
	@# 2. 查询端零 LLM：检索路径不得依赖大模型
	@! grep -rn "internal/llm" dk-service/internal/service --include='*.go' | grep -q . \
		&& echo "✓ 查询端零 LLM" \
		|| (echo "✗ 查询端不应 import llm 包"; exit 1)
	@# 3. 零 embedding：本项目走结构化图谱，不引入向量检索
	@#    只查引号字面量（import 路径等），避免误伤注释里的「零 embedding」声明
	@! grep -rnE "\"[^\"]*(embedding|faiss|hnsw)[^\"]*\"" dk-service core/metrics --include='*.go' | grep -q . \
		&& echo "✓ 查询端与哨兵零 embedding / 向量库" \
		|| (echo "✗ 检出 embedding/向量库引用"; exit 1)
	@# 4. 查询端只读：不得对 KG 结构做写入、删除或重建
	@#    先剔除注释行（^\s*// 与 ^\s*#），否则文档里提到 RebuildAll 等符号会误报
	@! grep -rnE "\.Delete\(|\.Insert|\.Update\(|os\.Remove|RebuildAll" dk-service/internal/service --include='*.go' \
		| grep -vE "^[^:]+:[0-9]+:[[:space:]]*(//|\*|/\*)" | grep -q . \
		&& echo "✓ 查询端只读（无 KG 结构写入）" \
		|| (echo "✗ 查询端检出 KG 结构写入"; \
		    grep -rnE "\.Delete\(|\.Insert|\.Update\(|os\.Remove|RebuildAll" dk-service/internal/service --include='*.go' \
		      | grep -vE "^[^:]+:[0-9]+:[[:space:]]*(//|\*|/\*)"; exit 1)
	@# 5. 演化层不写 KG：只旁路记录，绝不改动图谱本体
	@! grep -rnE "\.Delete\(|\.Insert\(|\.Update\(" core/evolve --include='*.go' \
		| grep -vE "^[^:]+:[0-9]+:[[:space:]]*(//|\*|/\*)" | grep -iE "node|edge" | grep -q . \
		&& echo "✓ 演化层不写 KG（node/edge）" \
		|| (echo "✗ 演化层检出 KG 结构写入"; exit 1)
	@# 6. 演化层零 embedding 零 LLM
	@! grep -rniE "embedding|faiss|hnsw|cosine" core/observe core/evolve dk-build/cmd/dk-evolve-serve --include='*.go' | grep -q . \
		&& echo "✓ 演化层零 embedding / 向量库" \
		|| (echo "✗ 演化层不应引入 embedding/向量库"; exit 1)
	@! grep -rn "internal/llm" core/observe core/evolve dk-build/cmd/dk-evolve-serve --include='*.go' | grep -q . \
		&& echo "✓ 演化层零 LLM" \
		|| (echo "✗ 演化层不应 import LLM 包"; exit 1)
