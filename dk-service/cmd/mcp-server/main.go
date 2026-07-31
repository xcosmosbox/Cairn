// Package main 是领域知识层（Domain Knowledge Layer）MCP 服务器（MCP Server）的入口。
//
// MCP Server 通过标准输入/输出（stdio）传输层提供 Model Context Protocol 兼容的服务，
// 使外部 MCP 客户端（如 Claude Code）可以通过 JSON-RPC 协议查询知识图谱。
//
// 支持多知识库（multi-DB），通过 kg 参数路由查询到对应的数据库，
// 未指定 kg 时支持联邦查询（fan-out 到所有知识库并合并结果）。
//
// 支持的工具（tools）：
//   - domain_search：按关键词搜索领域实体，返回匹配节点及上下文信息（支持 kg/federated）
//   - domain_status：获取知识库状态摘要（节点数、边数、模式版本等）
//   - domain_impact：对指定实体进行正向 BFS 影响分析
//   - list_knowledge_bases：列出所有已连接的知识库及其统计信息
//   - describe_knowledge_layer：获取知识层完整清单或指定知识库的详细信息
//
// Package main is the entry point for the Domain Knowledge Layer MCP Server.
//
// The MCP Server provides Model Context Protocol-compatible services over the
// standard input/output (stdio) transport layer, enabling external MCP clients
// (such as Claude Code) to query the knowledge graph via JSON-RPC.
//
// Supports multi-DB with kg-based routing; federated queries fan out to all
// knowledge bases and merge results when kg is not specified.
//
// Supported tools:
//   - domain_search: search domain entities by keyword, returning matched nodes
//     with contextual information (supports kg/federated)
//   - domain_status: get knowledge base status summary (node count, edge count,
//     schema version, etc.)
//   - domain_impact: perform forward BFS impact analysis on a specified entity
//   - list_knowledge_bases: list all connected knowledge bases with stats
//   - describe_knowledge_layer: get full knowledge layer manifest or per-KG detail
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // SQLite driver (纯 Go 实现 / pure Go implementation)

	"github.com/xcosmosbox/domain-knowledge-layer/core/kbbundle"
	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
	coregh "github.com/xcosmosbox/domain-knowledge-layer/core/githubapp"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-service/internal/config"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-service/internal/service"
)

// ——————————————————————————————————————————————————————————————————————————————
// JSON-RPC 协议数据结构 / JSON-RPC Protocol Data Structures
// ——————————————————————————————————————————————————————————————————————————————

// jsonRPCRequest 表示一个 JSON-RPC 2.0 请求。
// jsonRPCRequest represents a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse 表示一个 JSON-RPC 2.0 响应。
// jsonRPCResponse represents a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError 表示一个 JSON-RPC 2.0 错误。
// jsonRPCError represents a JSON-RPC 2.0 error.
type jsonRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// toolResult 是 tools/call 的返回结果。
// toolResult is the return value of tools/call.
type toolResult struct {
	Content []toolContent `json:"content"`
}

// toolContent 是工具调用结果的内容项。
// toolContent is a content item in a tool call result.
type toolContent struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// toolDef 是 tools/list 返回的工具定义。
// toolDef is a tool definition returned by tools/list.
type toolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
}

// kbMeta 保存已打开知识库的元数据。
// kbMeta holds metadata for an opened knowledge base.
type kbMeta struct {
	Name        string
	Path        string
	Description string
}

// serverState 保存 MCP Server 的运行时状态。
// 所有字段通过 mu（RWMutex）保护：
//   - 查询路径（getSvc/getSvcs/handlers）持 RLock
//   - 热切换（hotSwapDB）持 Lock，等所有查询完成后才替换
type serverState struct {
	mu           sync.RWMutex                         // 并发访问保护
	svcs         map[string]service.KnowledgeService  // 元能力抽象（一库一实例）
	meta         map[string]kbMeta                    // kgName → metadata
	storages     map[string]*storage.DB               // DB 连接（热切换时关闭旧连接）
	digests      map[string]string                    // kgName → 当前 bundle_digest（检测变更）
	watchConfigs map[string]config.KBConfig           // kgName → catalog watch 配置（仅含配置了 catalog_repo 的 KB）
	appAuth      *coregh.AppAuth                      // GitHub App 鉴权（复用 dkd 的同一 App），nil 时回退 PAT
	catalogInstID int64                               // catalog repo 的 installation ID（GitHub App 模式）
	// rewriter 是共享的查询改写器（无状态只读）。
	// 热切换重建 KnowledgeService 时必须复用它，否则热更新后缩写扩展会静默失效。
	// rewriter must be reused when hot-swapping rebuilds a KnowledgeService,
	// otherwise abbreviation expansion silently stops working after an update.
	rewriter *service.QueryRewriter
}

func main() {
	// 解析命令行参数 / Parse command-line flags
	configPath := flag.String("config", "config.yaml", "配置文件路径 / Path to config YAML file")
	listenAddr := flag.String("listen", "", "HTTP/SSE 监听地址（空=stdio 模式，如 :8080）")
	flag.Parse()

	// listen 优先级：命令行 > 配置文件
	// listen priority: CLI flag > config file

	fmt.Fprintln(os.Stderr, "Domain Knowledge Layer MCP Server v2.0.0 (multi-DB)")

	// 加载配置 / Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败，使用默认配置 / Failed to load config, using defaults: %v\n", err)
		cfg = config.DefaultConfig()
	}

	// 加载缩写映射表（可选增强项）。
	// 失败只告警：缩写表缺失不影响检索主路径，不该让 server 起不来。
	// Load the optional abbreviation map; failures only warn.
	var abbr map[string]string
	if p := cfg.MCP.AbbreviationsFile; p != "" {
		loaded, err := config.LoadAbbreviations(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "加载缩写表失败，跳过缩写扩展 / failed to load abbreviations, skipping: %v\n", err)
		} else {
			abbr = loaded
			fmt.Fprintf(os.Stderr, "已加载 %d 条缩写映射 / loaded %d abbreviations\n", len(abbr), len(abbr))
		}
	}
	rewriter := service.NewQueryRewriter(abbr)

	// 打开所有配置的知识库 / Open all configured knowledge bases
	// 通过 storage.OpenReadOnly 以只读模式打开（无写副作用），并为每个库装配一个
	// service.KnowledgeService 元能力实例（一库一实例）。
	svcs := make(map[string]service.KnowledgeService)
	meta := make(map[string]kbMeta)
	storages := make(map[string]*storage.DB)               // 本地：仅用于 defer 关闭
	digests := make(map[string]string)                     // kgName → bundle_digest
	watchConfigs := make(map[string]config.KBConfig)       // kgName → catalog watch 配置

	// openKB 打开一个知识库并装配 KnowledgeService（复用同一底层连接）。
	// rewriter 为无状态只读，多库共享同一实例。
	openKB := func(name, path, desc string) error {
		sdb, err := storage.OpenReadOnly(path)
		if err != nil {
			return err
		}
		storages[name] = sdb
		svcs[name] = service.NewKnowledgeService(sdb, nil, rewriter)
		if desc == "" {
			desc = fmt.Sprintf("知识库 %s / Knowledge base %s", name, name)
		}
		meta[name] = kbMeta{Name: name, Path: path, Description: desc}
		digests[name] = "" // 初始无 digest（首次启动不知道当前 bundle 版本）
		return nil
	}

	if len(cfg.MCP.KnowledgeBases) == 0 {
		// 向后兼容：未配置知识库列表时回退到默认单库
		// Backward compat: fall back to default single DB when no KBs configured
		defaultPath := "./domain-knowledge.db"
		if err := openKB("default", defaultPath, "默认知识库 / Default knowledge base"); err != nil {
			fmt.Fprintf(os.Stderr, "打开默认数据库失败 / Failed to open default database: %v\n", err)
			os.Exit(1)
		}
	} else {
		for _, kb := range cfg.MCP.KnowledgeBases {
			name := kb.Name
			if name == "" {
				name = "unnamed"
			}

			// 检查文件是否存在 / Check if file exists
			if _, statErr := os.Stat(kb.Path); os.IsNotExist(statErr) {
				if kb.CatalogRepo != "" {
					// DB 不存在但配了 catalog 自动更新：加入 watchConfigs，启动后自动拉取
					fmt.Fprintf(os.Stderr, "知识库文件不存在，将自动拉取 / KB file not found, will auto-pull: %s (%s)\n", name, kb.Path)
					watchConfigs[name] = kb
					continue
				}
				fmt.Fprintf(os.Stderr, "知识库文件不存在，跳过 / KB file not found, skipping: %s (%s)\n", name, kb.Path)
				continue
			}

		if err := openKB(name, kb.Path, kb.Description); err != nil {
			fmt.Fprintf(os.Stderr, "打开知识库失败 / Failed to open KB: %s (%s): %v\n", name, kb.Path, err)
			continue
		}
		// 收集 catalog watch 配置（仅含配置了 catalog_repo 的 KB）。
		if kb.CatalogRepo != "" {
			watchConfigs[name] = kb
		}
	}

		if len(storages) == 0 && len(watchConfigs) == 0 {
			fmt.Fprintln(os.Stderr, "没有可用的知识库，退出 / No knowledge bases available, exiting")
			os.Exit(1)
		}
	}

	// 关闭所有数据库连接 / Close all database connections on exit
	defer func() {
		for name, sdb := range storages {
			if err := sdb.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "关闭数据库失败 / Failed to close DB %s: %v\n", name, err)
			}
		}
	}()

	// 创建 GitHub App 鉴权（复用 dkd 的同一 App），用于 catalog 自动更新。
	var appAuth *coregh.AppAuth
	var catalogInstID int64
	if cfg.GitHub.AppID != 0 {
		key, err := cfg.GitHub.ResolvePrivateKey()
		if err != nil {
			fmt.Fprintf(os.Stderr, "GitHub App private key 加载失败 / Failed to load GitHub App key: %v\n", err)
		} else {
			appAuth, err = coregh.NewAppAuth(cfg.GitHub.AppID, key, cfg.GitHub.APIBaseURL)
			if err != nil {
				fmt.Fprintf(os.Stderr, "GitHub App 初始化失败 / Failed to init GitHub App: %v\n", err)
			} else {
				// 解析 catalog repo 的 installation ID（用第一个 watchConfig 的 catalog_repo）。
				for _, wcfg := range watchConfigs {
					parts := strings.SplitN(wcfg.CatalogRepo, "/", 2)
					if len(parts) == 2 {
						catalogInstID, err = appAuth.ResolveInstallation(context.Background(), parts[0], parts[1])
						if err != nil {
							fmt.Fprintf(os.Stderr, "GitHub App installation 解析失败 / Failed to resolve installation: %v\n", err)
							appAuth = nil // 降级到 PAT
						}
						break
					}
				}
			}
		}
	}
	if appAuth != nil {
		fmt.Fprintf(os.Stderr, "GitHub App 鉴权就绪（app_id=%d, installation=%d）\n", cfg.GitHub.AppID, catalogInstID)
	}

	state := &serverState{
		svcs: svcs, meta: meta,
		storages: storages, digests: digests, watchConfigs: watchConfigs,
		appAuth: appAuth, catalogInstID: catalogInstID,
		rewriter: rewriter,
	}

	// 启动 catalog 自动更新 watcher（后台 goroutine，仅对配置了 catalog_repo 的 KB 生效）。
	state.startCatalogWatcher()

	// 选择传输模式：--listen 非空 → HTTP/SSE，否则 → stdio
	transport := *listenAddr
	if transport == "" {
		transport = cfg.MCP.Listen
	}

	if transport != "" {
		// HTTP/SSE 模式
		fmt.Fprintf(os.Stderr, "MCP Server 就绪 — %d 个知识库 / %d KBs loaded", len(svcs), len(svcs))
		if len(watchConfigs) > 0 {
			fmt.Fprintf(os.Stderr, "（%d 个 KB 启用 catalog 自动更新）", len(watchConfigs))
		}
		fmt.Fprintln(os.Stderr)
		runHTTPServer(state, transport)
	} else {
		// stdio 模式（默认）
		fmt.Fprintf(os.Stderr, "MCP Server 就绪（stdio 传输）/ MCP Server ready (stdio transport) — %d 个知识库 / %d KBs loaded", len(svcs), len(svcs))
		if len(watchConfigs) > 0 {
			fmt.Fprintf(os.Stderr, "（%d 个 KB 启用 catalog 自动更新）", len(watchConfigs))
		}
		fmt.Fprintln(os.Stderr)
		runStdioLoop(state)
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// KB 路由辅助函数 / KB Routing Helpers
// ——————————————————————————————————————————————————————————————————————————————

// getSvc 根据 kg 名称返回对应的 KnowledgeService。
// 如果 kg 为空且只有一个库，返回该库；否则要求显式指定 kg。
// 线程安全：持 RLock，与 hotSwapDB 的 Lock 互斥。
func (s *serverState) getSvc(kg string) (service.KnowledgeService, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if kg == "" {
		if len(s.svcs) == 1 {
			for name, svc := range s.svcs {
				return svc, name, nil // only one KB, return it
			}
		}
		return nil, "", fmt.Errorf("kg parameter required (multiple KBs available: %d)", len(s.svcs))
	}
	svc, ok := s.svcs[kg]
	if !ok {
		return nil, "", fmt.Errorf("未找到知识库 / Knowledge base not found: %s", kg)
	}
	return svc, kg, nil
}

// getSvcs 返回需要查询的 KnowledgeService 集合。
// 返回 map 的副本（安全迭代，不受 hotSwapDB 影响）。
func (s *serverState) getSvcs(kg string) map[string]service.KnowledgeService {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if kg != "" {
		if svc, ok := s.svcs[kg]; ok {
			return map[string]service.KnowledgeService{kg: svc}
		}
		return nil
	}
	// 返回副本，调用方可安全迭代。
	out := make(map[string]service.KnowledgeService, len(s.svcs))
	for k, v := range s.svcs {
		out[k] = v
	}
	return out
}

// svcsSnapshot 返回 svcs map 的副本（供 handler 安全迭代）。
func (s *serverState) svcsSnapshot() map[string]service.KnowledgeService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]service.KnowledgeService, len(s.svcs))
	for k, v := range s.svcs {
		out[k] = v
	}
	return out
}

// svcsCount 返回当前知识库数量。
func (s *serverState) svcsCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.svcs)
}

// metaLookup 返回指定 kg 的元数据。
func (s *serverState) metaLookup(kg string) (kbMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.meta[kg]
	return m, ok
}

// ──────────────────────────────────────────────────────────────────────────────
// Catalog 自动更新 / Catalog Auto-Update
// ──────────────────────────────────────────────────────────────────────────────

// startCatalogWatcher 启动后台 goroutine，定期轮询 catalog repo 检测新 stable bundle。
// 仅对配置了 catalog_repo 的知识库生效。发现新 digest 时自动拉取并热切换。
func (s *serverState) startCatalogWatcher() {
	if len(s.watchConfigs) == 0 {
		return // 无 catalog watch 配置，跳过
	}

	go func() {
		// 启动时立即检查一次（不等第一个 ticker 周期，确保首次启动快速拉取）。
		for name, cfg := range s.watchConfigs {
			s.checkAndPullUpdate(name, cfg)
		}

		// 每个 KB 独立 ticker（间隔可能不同）。
		type kbTicker struct {
			name   string
			ticker *time.Ticker
			cfg    config.KBConfig
		}
		var tickers []*kbTicker
		for name, cfg := range s.watchConfigs {
			interval := 5 * time.Minute
			if d, err := time.ParseDuration(cfg.PollInterval); err == nil && d > 0 {
				interval = d
			}
			t := &kbTicker{name: name, ticker: time.NewTicker(interval), cfg: cfg}
			tickers = append(tickers, t)
			fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 启动轮询（间隔 %s, catalog=%s）\n", name, interval, cfg.CatalogRepo)
		}
		defer func() {
			for _, t := range tickers {
				t.ticker.Stop()
			}
		}()

		for {
			for _, t := range tickers {
				select {
				case <-t.ticker.C:
					s.checkAndPullUpdate(t.name, t.cfg)
				default:
				}
			}
			time.Sleep(100 * time.Millisecond) // 避免忙等待
		}
	}()
}

// checkAndPullUpdate 检查单个 KB 的 catalog 是否有新 stable，有则拉取并热切换。
func (s *serverState) checkAndPullUpdate(name string, kbCfg config.KBConfig) {
	// 鉴权：优先 GitHub App installation token，回退 PAT（token_env）。
	var token string
	if s.appAuth != nil {
		tok, _, err := s.appAuth.InstallToken(context.Background(), s.catalogInstID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 获取 installation token 失败: %v\n", name, err)
			return
		}
		token = tok
	} else if kbCfg.TokenEnv != "" {
		token = os.Getenv(kbCfg.TokenEnv)
	}
	if token == "" {
		fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 跳过（未配置 GitHub App 或 token_env）\n", name)
		return
	}

	// 解析 catalog repo owner/name。
	parts := strings.SplitN(kbCfg.CatalogRepo, "/", 2)
	if len(parts) != 2 {
		fmt.Fprintf(os.Stderr, "[catalog-watch] %s: catalog_repo 格式无效: %s\n", name, kbCfg.CatalogRepo)
		return
	}
	owner, repo := parts[0], parts[1]

	// 读取 catalog repo 的 stable.json 获取当前 stable digest。
	manifestPath := fmt.Sprintf("%s/%s/stable.json", kbCfg.ManifestDir, name)
	cm, err := kbbundle.FetchCatalogManifest("https://api.github.com", owner, repo, kbCfg.CatalogBranch, manifestPath, token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 读取 catalog manifest 失败: %v\n", name, err)
		return
	}
	if cm.Channel != "stable" {
		return // 非 stable，跳过
	}

	// 与当前 digest 比较。
	s.mu.RLock()
	currentDigest := s.digests[name]
	s.mu.RUnlock()

	if cm.Manifest.BundleDigest == currentDigest {
		return // digest 未变，跳过
	}

	fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 检测到新 stable（%s → %s），开始拉取...\n",
		name, currentDigest, cm.Manifest.BundleDigest)

	// 拉取并安装新 Bundle。
	res, err := kbbundle.PullRemote(kbbundle.PullRemoteSpec{
		CatalogOwner:  owner,
		CatalogRepo:   repo,
		CatalogBranch: kbCfg.CatalogBranch,
		ManifestDir:   kbCfg.ManifestDir,
		KG:            name,
		Token:         token,
		APIBaseURL:    "https://api.github.com",
		InstallDir:    kbCfg.InstallDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 拉取失败: %v\n", name, err)
		return
	}
	defer os.RemoveAll(res.TempDir)

	// 新 DB 路径：installDir/current/knowledge.db。
	newDBPath := filepath.Join(kbCfg.InstallDir, "current", "knowledge.db")
	if _, err := os.Stat(newDBPath); err != nil {
		fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 安装后 DB 文件不存在: %s\n", name, newDBPath)
		return
	}

	// 热切换。
	if err := s.hotSwapDB(name, newDBPath, cm.Manifest.BundleDigest); err != nil {
		fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 热切换失败: %v\n", name, err)
		return
	}

	fmt.Fprintf(os.Stderr, "[catalog-watch] %s: 热切换完成（digest=%s）\n", name, cm.Manifest.BundleDigest)
}

// hotSwapDB 关闭旧 DB 连接，打开新 DB，原子替换 serverState 中的 service。
// 持写锁：等待所有进行中的查询（持读锁）完成后才执行替换。
func (s *serverState) hotSwapDB(name, newDBPath, newDigest string) error {
	// 先在新 DB 上打开（在锁外，避免长时间持锁）。
	newDB, err := storage.OpenReadOnly(newDBPath)
	if err != nil {
		return fmt.Errorf("打开新 DB %s: %w", newDBPath, err)
	}
	// 复用共享 rewriter，保证热切换后缩写扩展依然生效。
	newSvc := service.NewKnowledgeService(newDB, nil, s.rewriter)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 关闭旧 DB 连接（首次拉取时不存在旧连接，跳过）。
	if oldDB, ok := s.storages[name]; ok {
		oldDB.Close()
	}

	// 原子替换（或首次插入）。
	s.storages[name] = newDB
	s.svcs[name] = newSvc
	s.digests[name] = newDigest

	// 更新 meta（首次拉取时创建 meta 条目）。
	if m, ok := s.meta[name]; ok {
		m.Path = newDBPath
		s.meta[name] = m
	} else {
		s.meta[name] = kbMeta{Name: name, Path: newDBPath, Description: name}
	}

	return nil
}

// extractKG 从工具参数中提取 kg 字段。
// extractKG extracts the kg field from tool arguments.
func extractKG(args map[string]interface{}) string {
	if kg, ok := args["kg"].(string); ok && kg != "" {
		return kg
	}
	return ""
}

// ——————————————————————————————————————————————————————————————————————————————
// 联邦查询辅助 / Federated Query Helpers
// ——————————————————————————————————————————————————————————————————————————————

// searchHit 表示联邦搜索中的一个命中结果。
// searchHit represents a hit result in federated search.
type searchHit struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Name       string  `json:"name"`
	Summary    string  `json:"summary"`
	Domain     string  `json:"domain"`
	Subdomain  string  `json:"subdomain"`
	Confidence float64 `json:"confidence"`
	Rank       float64 `json:"rank"`
	KG         string  `json:"kg"`
}

// federatedSearch 在多个知识库上并发执行 KnowledgeService.Search，合并结果并按
// 归一化 FinalScore 排序。联邦/归一化/合并逻辑保留在前端，仅单库查询原语
// 委托给元能力抽象。
//
// federatedSearch performs concurrent KnowledgeService.Search across multiple
// knowledge bases, merges results, and sorts by normalized FinalScore.
func (s *serverState) federatedSearch(keyword string, limit int) ([]searchHit, error) {
	svcs := s.svcsSnapshot()
	if len(svcs) == 0 {
		return nil, fmt.Errorf("没有可用的知识库 / No knowledge bases available")
	}

	type kgResult struct {
		hits []searchHit
		err  error
	}

	// 并发查询所有知识库 / Concurrently query all knowledge bases
	var wg sync.WaitGroup
	ch := make(chan kgResult, len(svcs))

	for kgName, svc := range svcs {
		wg.Add(1)
		go func(name string, ks service.KnowledgeService) {
			defer wg.Done()
			hits, err := searchServiceToHits(ks, keyword, limit, name)
			ch <- kgResult{hits: hits, err: err}
		}(kgName, svc)
	}

	wg.Wait()
	close(ch)

	// 收集所有结果 / Collect all results
	var allHits []searchHit
	for res := range ch {
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "联邦搜索警告 / Federated search warning: %v\n", res.err)
			continue
		}
		allHits = append(allHits, res.hits...)
	}

	if len(allHits) == 0 {
		return nil, nil
	}

	// 按原始 FinalScore 降序排序（大=更相关，排前），分数相同按 ID 升序保证确定性。
	// 注：原 min-max 归一化是保序变换，不影响排序结果，故去掉以与单库口径统一。
	// Sort by raw FinalScore descending; tie-break by ID for determinism.
	// min-max normalization is order-preserving, so dropping it unifies the score scale.
	sort.Slice(allHits, func(i, j int) bool {
		if allHits[i].Rank != allHits[j].Rank {
			return allHits[i].Rank > allHits[j].Rank
		}
		return allHits[i].ID < allHits[j].ID
	})

	// 截断到 limit / Truncate to limit
	if len(allHits) > limit {
		allHits = allHits[:limit]
	}

	return allHits, nil
}

// searchServiceToHits 调用单个 KnowledgeService.Search 并将结构化结果转换为
// 联邦搜索用的 searchHit 切片（Rank = FinalScore）。单库查询亦复用此函数。
//
// searchServiceToHits calls a single KnowledgeService.Search and maps the
// structured result into searchHit slices (Rank = FinalScore).
func searchServiceToHits(svc service.KnowledgeService, keyword string, limit int, kgName string) ([]searchHit, error) {
	res, err := svc.Search(context.Background(), keyword, service.SearchOptions{Limit: limit})
	if err != nil {
		return nil, err
	}
	hits := make([]searchHit, 0, len(res.Hits))
	for _, h := range res.Hits {
		if h == nil || h.Node == nil {
			continue
		}
		hits = append(hits, searchHit{
			ID:         h.Node.ID,
			Label:      string(h.Node.Label),
			Name:       h.Node.Name,
			Summary:    h.Node.Summary,
			Domain:     h.Node.Domain,
			Subdomain:  h.Node.Subdomain,
			Confidence: h.Node.Confidence,
			Rank:       h.FinalScore,
			KG:         kgName,
		})
	}
	return hits, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// KB 统计辅助 / KB Stats Helper
// ——————————————————————————————————————————————————————————————————————————————

// kbStats 保存单个知识库的统计信息。
// kbStats holds statistics for a single knowledge base.
type kbStats struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Nodes       int      `json:"nodes"`
	Edges       int      `json:"edges"`
	Domains     []string `json:"domains"`
	Skills      []string `json:"skills,omitempty"`
}

// ——————————————————————————————————————————————————————————————————————————————
// 清单辅助 / Manifest Helper
// ——————————————————————————————————————————————————————————————————————————————

// manifestEntry 表示知识库清单中的一个条目。
// manifestEntry represents an entry in the knowledge base manifest.
type manifestEntry struct {
	SchemaVersion    int               `json:"schema_version"`
	BuildTimestamp   string            `json:"build_timestamp,omitempty"`
	KGName           string            `json:"kg_name"`
	Description      string            `json:"description"`
	Stats            kbStats           `json:"stats"`
	CrossKGLinks     []crossKGLink     `json:"cross_kg_links,omitempty"`
	Domains          []string          `json:"domains"`
	TopLabels        []labelCount      `json:"top_labels,omitempty"`
	TopRelationships []relationCount   `json:"top_relationships,omitempty"`
}

type crossKGLink struct {
	SourceEntity string `json:"source_entity"`
	TargetEntity string `json:"target_entity"`
	Kind         string `json:"kind"`
	Reason       string `json:"reason,omitempty"`
}

type labelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type relationCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// ——————————————————————————————————————————————————————————————————————————————
// stdio 消息循环 / stdio Message Loop
// ——————————————————————————————————————————————————————————————————————————————

// runStdioLoop 从 stdin 读取 JSON-RPC 请求，处理后写回 stdout。
// runStdioLoop reads JSON-RPC requests from stdin, processes them,
// and writes responses to stdout.
func runStdioLoop(state *serverState) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			sendError(nil, -32700, "解析错误 / Parse error: "+err.Error())
			continue
		}

		resp := handleRequest(state, &req)
		if resp != nil {
			sendResponse(resp)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stdin 读取错误 / stdin read error: %v\n", err)
	}
}

// handleRequest 根据 JSON-RPC 方法名分发处理。
// handleRequest dispatches processing based on the JSON-RPC method name.
func handleRequest(state *serverState, req *jsonRPCRequest) *jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return handleInitialize(req)
	case "tools/list":
		return handleToolsList(req)
	case "tools/call":
		return handleToolsCall(state, req)
	case "notifications/initialized":
		return nil
	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &jsonRPCError{
				Code:    -32601,
				Message: fmt.Sprintf("未知方法 / Method not found: %s", req.Method),
			},
		}
	}
}

// handleInitialize 处理初始化握手。
// handleInitialize handles the initialization handshake.
func handleInitialize(req *jsonRPCRequest) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "0.1",
			"serverInfo": map[string]interface{}{
				"name":    "domain-knowledge-layer",
				"version": "2.0.0",
			},
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
		},
	}
}

// handleToolsList 返回可用工具列表（含新增工具）。
// handleToolsList returns the list of available tools (including new tools).
func handleToolsList(req *jsonRPCRequest) *jsonRPCResponse {
	tools := []toolDef{
		{
			Name:        "domain_search",
			Description: "在领域知识图谱中按关键词搜索实体。使用FTS5全文索引进行模糊匹配，返回匹配节点及其上下文。支持通过kg参数指定知识库，未指定时在所有知识库中联邦搜索。Search domain entities by keyword in the knowledge graph. Uses FTS5 full-text index for fuzzy matching. Supports kg parameter to specify a knowledge base; federated search across all KBs when not specified.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"keyword": map[string]interface{}{
						"type":        "string",
						"description": "搜索关键词 / Search keyword",
					},
					"kg": map[string]interface{}{
						"type":        "string",
						"description": "可选：指定知识库名称，不填则联邦搜索所有已连接的知识库 / Optional: knowledge base name; federated search across all connected KBs if not specified",
					},
					"limit": map[string]interface{}{
						"type":        "number",
						"description": "可选：返回结果数量上限，默认10，最大50 / Optional: max result count, default 10, max 50",
					},
				},
				"required": []string{"keyword"},
			},
		},
		{
			Name:        "domain_status",
			Description: "获取知识库状态摘要。返回节点总数、边总数、域分布、模式版本等信息。支持kg参数指定知识库。Get knowledge base status summary. Returns total node count, edge count, domain distribution, schema version, etc. Supports kg parameter.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"kg": map[string]interface{}{
						"type":        "string",
						"description": "可选：指定知识库名称；仅一个知识库时可省略，多个知识库时必须指定 / Optional: knowledge base name; can be omitted when only one KB exists, required when multiple KBs are configured",
					},
				},
			},
		},
		{
			Name:        "domain_impact",
			Description: "对指定实体执行正向BFS影响分析。从起始实体出发，按层级遍历所有可达节点，展示变更影响范围。Perform forward BFS impact analysis on a specified entity. Traverses all reachable nodes from the starting entity by level, showing change impact scope.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"entity_name": map[string]interface{}{
						"type":        "string",
						"description": "起始实体名称 / Starting entity name",
					},
					"kg": map[string]interface{}{
						"type":        "string",
						"description": "可选：指定知识库名称 / Optional: knowledge base name",
					},
					"depth": map[string]interface{}{
						"type":        "number",
						"description": "可选：BFS遍历深度，默认2，范围1-5 / Optional: BFS traversal depth, default 2, range 1-5",
					},
				},
				"required": []string{"entity_name"},
			},
		},
		{
			Name:        "list_knowledge_bases",
			Description: "列出所有已连接的知识库及其统计信息。返回每个知识库的名称、描述、节点数、边数、域分布等信息。List all connected knowledge bases with statistics. Returns name, description, node count, edge count, domain distribution for each KB.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "describe_knowledge_layer",
			Description: "获取知识层完整清单信息。指定kg时返回该知识库的详细manifest（模式版本、构建时间、统计信息、跨KG链接等）；未指定kg时返回所有知识库概览及跨KG链接摘要。Get full knowledge layer manifest. When kg is specified, returns detailed manifest for that KB; when not specified, returns overview of all KBs with cross-KG links summary.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"kg": map[string]interface{}{
						"type":        "string",
						"description": "可选：指定知识库名称，不填则返回所有KB概览 / Optional: knowledge base name; returns all KBs overview if not specified",
					},
					"verbose": map[string]interface{}{
						"type":        "boolean",
						"description": "可选：是否输出详细信息，默认false / Optional: whether to output verbose details, default false",
					},
				},
			},
		},
		{
			Name:        "resolve_node",
			Description: "解析一个可能已被重整（合并/拆分）的旧节点UUID：返回其最新存活节点并如实标注重定向；若已被拆分则附全部存活后继。外部长期持有旧UUID（历史快照、审计引用、书签）的正式解析入口。Resolve a possibly-restructured node UUID to its latest living node; explicitly marks the redirect and lists all living successors if it was split.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"uuid": map[string]interface{}{
						"type":        "string",
						"description": "待解析的节点UUID / Node UUID to resolve",
					},
					"kg": map[string]interface{}{
						"type":        "string",
						"description": "可选：指定知识库名称 / Optional: knowledge base name",
					},
				},
				"required": []string{"uuid"},
			},
		},
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"tools": tools},
	}
}

// handleToolsCall 处理工具调用，根据工具名称分发到具体处理器。
// 在分发前提取 kg 参数用于知识库路由。
//
// handleToolsCall handles tool calls, dispatching to specific handlers by tool name.
// Extracts kg parameter before dispatch for knowledge base routing.
func handleToolsCall(state *serverState, req *jsonRPCRequest) *jsonRPCResponse {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: "参数解析失败 / Invalid params: " + err.Error()},
		}
	}

	var result *toolResult
	var errMsg string

	switch params.Name {
	case "domain_search":
		result, errMsg = toolDomainSearch(state, params.Arguments)
	case "domain_status":
		result, errMsg = toolDomainStatus(state, params.Arguments)
	case "domain_impact":
		result, errMsg = toolDomainImpact(state, params.Arguments)
	case "list_knowledge_bases":
		result, errMsg = toolListKnowledgeBases(state, params.Arguments)
	case "describe_knowledge_layer":
		result, errMsg = toolDescribeKnowledgeLayer(state, params.Arguments)
	case "resolve_node":
		result, errMsg = toolResolveNode(state, params.Arguments)
	default:
		errMsg = fmt.Sprintf("未知工具 / Unknown tool: %s", params.Name)
	}

	if errMsg != "" {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: &toolResult{
				Content: []toolContent{
					{Type: "text", Text: fmt.Sprintf("错误 / Error: %s", errMsg)},
				},
			},
		}
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// 工具实现 / Tool Implementations
// ——————————————————————————————————————————————————————————————————————————————

// toolDomainSearch 按关键词搜索实体，支持单库查询和联邦搜索。
// toolDomainSearch searches entities by keyword, supporting single-KB and federated search.
func toolDomainSearch(state *serverState, args map[string]interface{}) (*toolResult, string) {
	keyword, _ := args["keyword"].(string)
	if keyword == "" {
		return nil, "keyword 参数不能为空 / keyword parameter must not be empty"
	}

	limit := 10
	if l, ok := args["limit"].(float64); ok && l > 0 && l <= 50 {
		limit = int(l)
	}

	kg := extractKG(args)

	if kg != "" {
		// 单库查询 / Single-KB query
		svc, dbName, err := state.getSvc(kg)
		if err != nil {
			return nil, err.Error()
		}

		hits, qErr := searchServiceToHits(svc, keyword, limit, dbName)
		if qErr != nil {
			return nil, fmt.Sprintf("搜索失败 / Search failed: %v", qErr)
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("搜索关键词: %s（知识库: %s）\n\n", keyword, kg))
		if len(hits) == 0 {
			sb.WriteString("未找到匹配结果 / No matching results found.\n")
		} else {
			formatSearchHits(&sb, hits)
		}
		return &toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}, ""
	}

	// 联邦搜索 / Federated search
	hits, err := state.federatedSearch(keyword, limit)
	if err != nil {
		return nil, fmt.Sprintf("联邦搜索失败 / Federated search failed: %v", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("联邦搜索关键词: %s（%d 个知识库）\n\n", keyword, state.svcsCount()))
	if len(hits) == 0 {
		sb.WriteString("未找到匹配结果 / No matching results found.\n")
	} else {
		formatSearchHitsFed(&sb, hits)
	}

	return &toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}, ""
}

// formatSearchHits 格式化单库搜索结果。
func formatSearchHits(sb *strings.Builder, hits []searchHit) {
	for i, h := range hits {
		sb.WriteString(fmt.Sprintf("## %d. %s (%.2f)\n", i+1, h.Name, h.Rank))
		sb.WriteString(fmt.Sprintf("- ID: %s\n", h.ID))
		sb.WriteString(fmt.Sprintf("- 类型/Type: %s\n", h.Label))
		sb.WriteString(fmt.Sprintf("- 域/Domain: %s/%s\n", h.Domain, h.Subdomain))
		sb.WriteString(fmt.Sprintf("- 摘要: %s\n", h.Summary))
		sb.WriteString(fmt.Sprintf("- 置信度/Confidence: %.2f\n\n", h.Confidence))
	}
}

// formatSearchHitsFed 格式化联邦搜索结果（含知识库来源）。
func formatSearchHitsFed(sb *strings.Builder, hits []searchHit) {
	for i, h := range hits {
		sb.WriteString(fmt.Sprintf("## %d. %s (分数/Score: %.2f) [知识库/KG: %s]\n", i+1, h.Name, h.Rank, h.KG))
		sb.WriteString(fmt.Sprintf("- ID: %s\n", h.ID))
		sb.WriteString(fmt.Sprintf("- 类型/Type: %s\n", h.Label))
		sb.WriteString(fmt.Sprintf("- 域/Domain: %s/%s\n", h.Domain, h.Subdomain))
		sb.WriteString(fmt.Sprintf("- 摘要: %s\n", h.Summary))
		sb.WriteString(fmt.Sprintf("- 置信度/Confidence: %.2f\n\n", h.Confidence))
	}
}

// toolDomainStatus 返回知识库状态摘要，支持 kg 参数。
// toolDomainStatus returns a knowledge base status summary, supporting kg parameter.
func toolDomainStatus(state *serverState, args map[string]interface{}) (*toolResult, string) {
	kg := extractKG(args)
	svc, dbName, err := state.getSvc(kg)
	if err != nil {
		return nil, err.Error()
	}

	res, err := svc.Status(context.Background())
	if err != nil {
		return nil, fmt.Sprintf("获取状态失败 / Failed to get status: %v", err)
	}

	// 从 StatusResult 的 map 派生排序后的分布文本（与原 SQL GROUP BY ... ORDER BY cnt DESC 对齐）
	domainDist := formatCountMapTop(res.NodesByDomain, 10, "个节点/nodes")
	labelDist := formatCountMapAll(res.NodesByLabel, "个节点/nodes")
	kindDist := formatCountMapAll(res.EdgesByKind, "条边/edges")

	text := fmt.Sprintf(`## 知识库状态 / Knowledge Base Status（知识库/KG: %s）

- 节点总数 / Total Nodes: %d
- 边总数 / Total Edges: %d
- 模式版本 / Schema Version: %d

### 域分布 / Domain Distribution
%s
### 标签分布 / Label Distribution
%s
### 关系类型分布 / Relation Kind Distribution
%s`,
		dbName, res.TotalNodes, res.TotalEdges, res.SchemaVersion,
		domainDist, labelDist, kindDist)

	return &toolResult{
		Content: []toolContent{{Type: "text", Text: text}},
	}, ""
}

// toolResolveNode 解析一个可能已被重整的旧 UUID 到其最新存活节点。
// 内部走 KnowledgeService.GetNode 的血缘兜底（只读，不写 KG 结构），
// 并如实向调用方报告三种现状：存活 / 已重定向（并入或拆分）/ 不存在。
//
// toolResolveNode resolves a possibly-restructured UUID to its latest living
// node via the lineage fallback in GetNode (read-only), reporting one of:
// alive / redirected (merged or split) / not found.
func toolResolveNode(state *serverState, args map[string]interface{}) (*toolResult, string) {
	uuid, _ := args["uuid"].(string)
	if uuid == "" {
		return nil, "uuid 参数不能为空 / uuid parameter must not be empty"
	}

	svc, dbName, err := state.getSvc(extractKG(args))
	if err != nil {
		return nil, err.Error()
	}

	detail, gErr := svc.GetNode(context.Background(), uuid)
	if gErr != nil {
		return nil, fmt.Sprintf("解析失败 / Resolve failed: %v", gErr)
	}

	var sb strings.Builder
	if detail == nil {
		sb.WriteString(fmt.Sprintf("✗ UUID %s 在知识库 %s 中不存在，且无血缘记录（可能从未存在或已被物理清除）。\nUUID %s does not exist in KB %s and has no lineage record.\n",
			uuid, dbName, uuid, dbName))
		return &toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}, ""
	}

	if detail.ResolvedFrom != "" {
		sb.WriteString(fmt.Sprintf("⚠ UUID %s 已被重整重定向 → 最新存活节点 / redirected to latest living node: %s（%s）\n原因 / Reason: %s\n",
			detail.ResolvedFrom, detail.Node.ID, detail.Node.Name, detail.RedirectReason))
		if len(detail.AlsoSplitInto) > 0 {
			sb.WriteString(fmt.Sprintf("该节点曾被拆分，其余存活后继 / other living successors of the split: %s\n",
				strings.Join(detail.AlsoSplitInto, ", ")))
		}
	} else {
		sb.WriteString(fmt.Sprintf("✓ UUID %s 存活 / alive。\n", detail.Node.ID))
	}
	sb.WriteString(fmt.Sprintf("- 名称/Name: %s\n- 类型/Type: %s\n- 域/Domain: %s/%s\n- 摘要/Summary: %s\n",
		detail.Node.Name, detail.Node.Label, detail.Node.Domain, detail.Node.Subdomain, detail.Node.Summary))
	return &toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}, ""
}

// countPair 是计数 map 排序后的键值对。
// countPair is a sorted key-value pair from a count map.
type countPair struct {
	k string
	v int
}

// sortCountMap 按计数降序、key 升序排序计数 map，保证输出确定性。
// sortCountMap sorts a count map by count descending then key ascending.
func sortCountMap(m map[string]int) []countPair {
	pairs := make([]countPair, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, countPair{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	return pairs
}

// formatCountMapTop 将计数 map 按计数降序格式化为文本，取前 limit 条。
// formatCountMapTop formats a count map as text, top limit entries.
func formatCountMapTop(m map[string]int, limit int, unit string) string {
	pairs := sortCountMap(m)
	var sb strings.Builder
	for i, p := range pairs {
		if i >= limit {
			break
		}
		sb.WriteString(fmt.Sprintf("  - %s: %d %s\n", p.k, p.v, unit))
	}
	return sb.String()
}

// formatCountMapAll 同 formatCountMapTop 但不限制条数。
// formatCountMapAll is formatCountMapTop without a limit.
func formatCountMapAll(m map[string]int, unit string) string {
	return formatCountMapTop(m, len(m), unit)
}

// toolDomainImpact 执行正向 BFS 影响分析，支持 kg 参数。
// toolDomainImpact performs forward BFS impact analysis, supporting kg parameter.
func toolDomainImpact(state *serverState, args map[string]interface{}) (*toolResult, string) {
	entityName, _ := args["entity_name"].(string)
	if entityName == "" {
		return nil, "entity_name 参数不能为空 / entity_name parameter must not be empty"
	}

	maxDepth := 2
	if d, ok := args["depth"].(float64); ok && d >= 1 && d <= 5 {
		maxDepth = int(d)
	}

	kg := extractKG(args)
	svc, dbName, err := state.getSvc(kg)
	if err != nil {
		return nil, err.Error()
	}

	// 委托元能力：FTS5 起点定位 + 正向 BFS 遍历全部由 service.Impact 完成。
	res, err := svc.Impact(context.Background(), entityName, service.ImpactOptions{MaxDepth: maxDepth})
	if err != nil {
		return nil, fmt.Sprintf("影响分析失败 / Impact analysis failed: %v", err)
	}
	if len(res.StartNodes) == 0 {
		return nil, fmt.Sprintf("未找到实体 / Entity not found: %s", entityName)
	}

	var sb strings.Builder
	start := res.StartNodes[0]
	sb.WriteString(fmt.Sprintf("## 影响分析: %s (%s, %s) [知识库/KG: %s]\n\n", start.Name, start.ID, start.Label, dbName))
	if len(res.StartNodes) > 1 {
		sb.WriteString(fmt.Sprintf("起点（FTS5 命中 %d 个，取首个为主起点）/ Entry nodes (FTS5 matched %d):\n", len(res.StartNodes), len(res.StartNodes)))
		for _, sn := range res.StartNodes {
			sb.WriteString(fmt.Sprintf("  - %s (%s, %s)\n", sn.Name, sn.ID, sn.Label))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("遍历深度 / Traversal depth: %d\n\n", maxDepth))

	// 构建 nodeID → name 映射，供可读展示。
	nameByID := make(map[string]string, len(res.Nodes))
	for _, in := range res.Nodes {
		if in.Node != nil {
			nameByID[in.Node.ID] = in.Node.Name
		}
	}

	// 按深度分层展示可达节点（depth=0 为起点，已在标题展示）。
	sb.WriteString("可达节点（按深度）/ Reachable nodes (by depth):\n")
	for _, in := range res.Nodes {
		if in.Depth == 0 {
			continue
		}
		indent := strings.Repeat("  ", in.Depth)
		sb.WriteString(fmt.Sprintf("%s- ↳ [d%d] %s (%s, %s)\n", indent, in.Depth, in.Node.Name, in.Node.ID, in.Node.Label))
	}

	// 边关系展示。
	if len(res.Edges) > 0 {
		sb.WriteString("\n关系边 / Edges:\n")
		for _, e := range res.Edges {
			srcName := nameByID[e.SourceID]
			if srcName == "" {
				srcName = e.SourceID
			}
			tgtName := nameByID[e.TargetID]
			if tgtName == "" {
				tgtName = e.TargetID
			}
			desc := ""
			if strings.TrimSpace(e.Description) != "" {
				desc = " — " + e.Description
			}
			sb.WriteString(fmt.Sprintf("  %s --[%s]--> %s%s\n", srcName, e.Kind, tgtName, desc))
		}
	}

	sb.WriteString(fmt.Sprintf("\n### 统计 / Statistics\n"))
	// 仅统计 depth>0 的下游节点，与上方分层展示（skip depth==0）口径一致。
	reachable := 0
	for _, in := range res.Nodes {
		if in.Depth > 0 {
			reachable++
		}
	}
	sb.WriteString(fmt.Sprintf("- 可达节点数 / Reachable nodes: %d\n", reachable))
	sb.WriteString(fmt.Sprintf("- 边总数 / Total edges: %d\n", len(res.Edges)))

	return &toolResult{
		Content: []toolContent{{Type: "text", Text: sb.String()}},
	}, ""
}

// ——————————————————————————————————————————————————————————————————————————————
// 新工具实现 / New Tool Implementations (P0.13)
// ——————————————————————————————————————————————————————————————————————————————

// toolListKnowledgeBases 列出所有已连接的知识库及其统计信息。
// toolListKnowledgeBases lists all connected knowledge bases with statistics.
func toolListKnowledgeBases(state *serverState, args map[string]interface{}) (*toolResult, string) {
	type kbInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Stats       kbStats  `json:"stats"`
		Skills      []string `json:"skills,omitempty"`
	}

	var kbs []kbInfo
	totalNodes, totalEdges := 0, 0

	for name, svc := range state.svcsSnapshot() {
		description := ""
		if m, ok := state.metaLookup(name); ok {
			description = m.Description
		}

		res, err := svc.Status(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "扫描知识库统计失败 / Failed to scan KB stats: %s: %v\n", name, err)
			continue
		}

		// Domains 从 NodesByDomain 派生（排序，排除空域以与原 DISTINCT domain ... WHERE domain != '' 对齐）
		domains := make([]string, 0, len(res.NodesByDomain))
		for d := range res.NodesByDomain {
			if d != "" {
				domains = append(domains, d)
			}
		}
		sort.Strings(domains)

		stats := kbStats{
			Name:        name,
			Description: description,
			Nodes:       res.TotalNodes,
			Edges:       res.TotalEdges,
			Domains:     domains,
			Skills:      make([]string, 0), // skill_references 表不存在，保持空（与原行为一致）
		}

		totalNodes += stats.Nodes
		totalEdges += stats.Edges

		kbs = append(kbs, kbInfo{
			Name:        name,
			Description: description,
			Stats:       stats,
			Skills:      stats.Skills,
		})
	}

	// 排序保证输出确定性 / Sort for deterministic output
	sort.Slice(kbs, func(i, j int) bool { return kbs[i].Name < kbs[j].Name })

	// 构建 JSON 输出 / Build JSON output
	resultMap := map[string]interface{}{
		"knowledge_bases": kbs,
		"summary": map[string]interface{}{
			"total_kbs":   len(kbs),
			"total_nodes": totalNodes,
			"total_edges": totalEdges,
		},
		"tip": "使用 domain_search 或 describe_knowledge_layer 进一步探索 / Use domain_search or describe_knowledge_layer to explore further",
	}

	jsonBytes, err := json.MarshalIndent(resultMap, "", "  ")
	if err != nil {
		return nil, fmt.Sprintf("序列化失败 / Serialization failed: %v", err)
	}

	// 同时提供人类可读的文本版本 / Also provide human-readable text version
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 已连接知识库 / Connected Knowledge Bases（%d 个）\n\n", len(kbs)))
	sb.WriteString(fmt.Sprintf("- 总节点数 / Total Nodes: %d\n", totalNodes))
	sb.WriteString(fmt.Sprintf("- 总边数 / Total Edges: %d\n\n", totalEdges))

	for _, kb := range kbs {
		sb.WriteString(fmt.Sprintf("### %s\n", kb.Name))
		sb.WriteString(fmt.Sprintf("- 描述: %s\n", kb.Description))
		sb.WriteString(fmt.Sprintf("- 节点数 / Nodes: %d\n", kb.Stats.Nodes))
		sb.WriteString(fmt.Sprintf("- 边数 / Edges: %d\n", kb.Stats.Edges))
		sb.WriteString(fmt.Sprintf("- 域 / Domains: %s\n", strings.Join(kb.Stats.Domains, ", ")))
		if len(kb.Skills) > 0 {
			sb.WriteString(fmt.Sprintf("- Skills: %s\n", strings.Join(kb.Skills, ", ")))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n")
	sb.WriteString(string(jsonBytes))

	return &toolResult{
		Content: []toolContent{{Type: "text", Text: sb.String()}},
	}, ""
}

// toolDescribeKnowledgeLayer 获取知识层完整清单信息。
// toolDescribeKnowledgeLayer gets the full knowledge layer manifest.
func toolDescribeKnowledgeLayer(state *serverState, args map[string]interface{}) (*toolResult, string) {
	kg := extractKG(args)
	verbose := false
	if v, ok := args["verbose"].(bool); ok {
		verbose = v
	}

	if kg != "" {
		// 指定 KG：返回该知识库的完整 manifest
		// Specific KG: return full manifest for that KB
		svc, ok := state.svcsSnapshot()[kg]
		if !ok {
			return nil, fmt.Sprintf("未找到知识库 / Knowledge base not found: %s", kg)
		}

		// 委托元能力：Status 提供统计/版本/分布，ListCrossKGLinks 提供跨KG链接。
		res, err := svc.Status(context.Background())
		if err != nil {
			return nil, fmt.Sprintf("读取状态失败 / Failed to read status: %v", err)
		}
		links, lErr := svc.ListCrossKGLinks(context.Background())
		if lErr != nil {
			fmt.Fprintf(os.Stderr, "读取跨KG链接失败 / Failed to read cross-KG links: %v\n", lErr)
			links = nil
		}

		// 域列表（排序，排除空域，与原 DISTINCT domain ... WHERE domain != '' 对齐）
		domains := make([]string, 0, len(res.NodesByDomain))
		for d := range res.NodesByDomain {
			if d != "" {
				domains = append(domains, d)
			}
		}
		sort.Strings(domains)

		// Top labels / Top relationships（计数降序取前 10，与原 GROUP BY ... LIMIT 10 对齐）
		topLabels := make([]labelCount, 0)
		for _, p := range sortCountMap(res.NodesByLabel) {
			if len(topLabels) >= 10 {
				break
			}
			topLabels = append(topLabels, labelCount{Label: p.k, Count: p.v})
		}
		topRelations := make([]relationCount, 0)
		for _, p := range sortCountMap(res.EdgesByKind) {
			if len(topRelations) >= 10 {
				break
			}
			topRelations = append(topRelations, relationCount{Kind: p.k, Count: p.v})
		}

		// 跨 KG 链接（dktypes.CrossKGLink → manifest crossKGLink）
		crossLinks := make([]crossKGLink, 0, len(links))
		for _, l := range links {
			crossLinks = append(crossLinks, crossKGLink{
				SourceEntity: l.FromEntity,
				TargetEntity: l.ToEntity,
				Kind:         string(l.Kind),
				Reason:       l.Reason,
			})
		}

		// description 从 meta 填充（kg_manifest 表为 key-value 结构，原 readManifest 读取失效，保持原行为）
		description := ""
		if m, exists := state.metaLookup(kg); exists {
			description = m.Description
		}

		manifest := &manifestEntry{
			SchemaVersion:    res.SchemaVersion,
			KGName:           kg,
			Description:      description,
			Domains:          domains,
			TopLabels:        topLabels,
			TopRelationships: topRelations,
			CrossKGLinks:     crossLinks,
			Stats:            kbStats{Name: kg, Nodes: res.TotalNodes, Edges: res.TotalEdges, Domains: domains},
		}

		jsonBytes, _ := json.MarshalIndent(manifest, "", "  ")

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("## 知识层清单 / Knowledge Layer Manifest: %s\n\n", kg))
		if manifest.Description != "" {
			sb.WriteString(fmt.Sprintf("**描述/Description**: %s\n\n", manifest.Description))
		}
		sb.WriteString(fmt.Sprintf("- 模式版本 / Schema Version: %d\n", manifest.SchemaVersion))
		if manifest.BuildTimestamp != "" {
			sb.WriteString(fmt.Sprintf("- 构建时间 / Build Time: %s\n", manifest.BuildTimestamp))
		}
		sb.WriteString(fmt.Sprintf("- 节点数 / Nodes: %d\n", manifest.Stats.Nodes))
		sb.WriteString(fmt.Sprintf("- 边数 / Edges: %d\n", manifest.Stats.Edges))
		sb.WriteString(fmt.Sprintf("- 域 / Domains: %s\n", strings.Join(manifest.Domains, ", ")))

		if len(manifest.TopLabels) > 0 {
			sb.WriteString("\n### Top Labels\n")
			for _, l := range manifest.TopLabels {
				sb.WriteString(fmt.Sprintf("  - %s: %d\n", l.Label, l.Count))
			}
		}

		if len(manifest.TopRelationships) > 0 && verbose {
			sb.WriteString("\n### Top Relationships\n")
			for _, r := range manifest.TopRelationships {
				sb.WriteString(fmt.Sprintf("  - %s: %d\n", r.Kind, r.Count))
			}
		}

		if len(manifest.CrossKGLinks) > 0 {
			sb.WriteString(fmt.Sprintf("\n### 跨KG链接 / Cross-KG Links (%d)\n", len(manifest.CrossKGLinks)))
			count := 0
			maxShow := len(manifest.CrossKGLinks)
			if !verbose && maxShow > 10 {
				maxShow = 10
			}
			for _, cl := range manifest.CrossKGLinks[:maxShow] {
				sb.WriteString(fmt.Sprintf("  - %s → %s (%s)\n", cl.SourceEntity, cl.TargetEntity, cl.Kind))
				count++
			}
			if !verbose && len(manifest.CrossKGLinks) > 10 {
				sb.WriteString(fmt.Sprintf("  ... 还有 %d 条链接（使用 verbose=true 查看全部）/ ... and %d more links (use verbose=true to see all)\n",
					len(manifest.CrossKGLinks)-10, len(manifest.CrossKGLinks)-10))
			}
		}

		if verbose {
			sb.WriteString("\n---\n")
			sb.WriteString(string(jsonBytes))
		}

		return &toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}, ""
	}

	// 未指定 KG：返回所有知识库概览
	// No KG specified: return overview of all KBs
	type kbOverview struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Nodes       int      `json:"nodes"`
		Edges       int      `json:"edges"`
		Domains     []string `json:"domains"`
	}

	var overviews []kbOverview
	totalNodes, totalEdges := 0, 0
	crossKGLinksSummary := map[string]int{}

	for name, svc := range state.svcsSnapshot() {
		description := ""
		if m, ok := state.metaLookup(name); ok {
			description = m.Description
		}

		res, err := svc.Status(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "status for %s: %v\n", name, err)
			continue
		}
		totalNodes += res.TotalNodes
		totalEdges += res.TotalEdges

		domains := make([]string, 0, len(res.NodesByDomain))
		for d := range res.NodesByDomain {
			if d != "" {
				domains = append(domains, d)
			}
		}
		sort.Strings(domains)

		overviews = append(overviews, kbOverview{
			Name:        name,
			Description: description,
			Nodes:       res.TotalNodes,
			Edges:       res.TotalEdges,
			Domains:     domains,
		})

		// 统计跨 KG 链接 / Count cross-KG links
		if links, lErr := svc.ListCrossKGLinks(context.Background()); lErr == nil {
			crossKGLinksSummary[name] = len(links)
		}
	}
	sort.Slice(overviews, func(i, j int) bool { return overviews[i].Name < overviews[j].Name })

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 知识层概览 / Knowledge Layer Overview\n\n"))
	sb.WriteString(fmt.Sprintf("- 已连接知识库数 / Connected KBs: %d\n", len(overviews)))
	sb.WriteString(fmt.Sprintf("- 总节点数 / Total Nodes: %d\n", totalNodes))
	sb.WriteString(fmt.Sprintf("- 总边数 / Total Edges: %d\n\n", totalEdges))

	for _, ov := range overviews {
		sb.WriteString(fmt.Sprintf("### %s\n", ov.Name))
		sb.WriteString(fmt.Sprintf("- 描述: %s\n", ov.Description))
		sb.WriteString(fmt.Sprintf("- 节点数 / Nodes: %d\n", ov.Nodes))
		sb.WriteString(fmt.Sprintf("- 边数 / Edges: %d\n", ov.Edges))
		sb.WriteString(fmt.Sprintf("- 域 / Domains: %s\n", strings.Join(ov.Domains, ", ")))
		sb.WriteString("\n")
	}

	// 跨 KG 链接摘要 / Cross-KG links summary
	if len(crossKGLinksSummary) > 0 {
		sb.WriteString("### 跨KG链接摘要 / Cross-KG Links Summary\n")
		for kgName, count := range crossKGLinksSummary {
			if count > 0 {
				sb.WriteString(fmt.Sprintf("  - %s: %d 条链接/links\n", kgName, count))
			}
		}
		sb.WriteString(fmt.Sprintf("\n使用 describe_knowledge_layer kg=<name> 查看特定知识库的详细跨KG链接。\n"))
		sb.WriteString("Use describe_knowledge_layer kg=<name> to see detailed cross-KG links for a specific KB.\n")
	}

	if verbose {
		resultMap := map[string]interface{}{
			"overview":              overviews,
			"cross_kg_links_summary": crossKGLinksSummary,
		}
		jsonBytes, _ := json.MarshalIndent(resultMap, "", "  ")
		sb.WriteString("\n---\n")
		sb.WriteString(string(jsonBytes))
	}

	return &toolResult{Content: []toolContent{{Type: "text", Text: sb.String()}}}, ""
}

// ——————————————————————————————————————————————————————————————————————————————
// 通信辅助函数 / Communication Helpers
// ——————————————————————————————————————————————————————————————————————————————

// sendResponse 将 JSON-RPC 响应序列化后写入 stdout。
// sendResponse serializes a JSON-RPC response and writes it to stdout.
func sendResponse(resp *jsonRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化响应失败 / Failed to marshal response: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

// sendError 发送一个 JSON-RPC 错误响应，不关联任何请求 ID。
// sendError sends a JSON-RPC error response without an associated request ID.
func sendError(id json.RawMessage, code int, message string) {
	resp := &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &jsonRPCError{
			Code:    code,
			Message: message,
		},
	}
	sendResponse(resp)
}
