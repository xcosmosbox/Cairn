// transport_http.go — HTTP/SSE 传输层（MCP 协议标准 HTTP+SSE transport）。
//
// 协议流程：
//   1. Client 连接 GET /sse（Server-Sent Events 长连接）
//   2. Server 立即发送 endpoint 事件，告知 POST 端点 URL：
//      event: endpoint
//      data: /rpc?session=<session-id>
//   3. Client 通过 POST /rpc?session=<id> 发送 JSON-RPC 请求
//   4. Server 处理后通过 SSE 流推送 JSON-RPC 响应：
//      data: {"jsonrpc":"2.0","id":1,"result":{...}}
//   5. SSE 连接保持开放，支持多次请求/响应
//   6. Client 断开时自动清理 session
//
// 端点：
//   GET  /sse     — SSE 流（每 client 一个 session）
//   POST /rpc     — JSON-RPC 请求（?session=<id>）
//   GET  /health  — 健康检查（Docker/K8s liveness probe）
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// sseSession 表示一个 SSE 客户端连接。
type sseSession struct {
	id       string
	events   chan *jsonRPCResponse // 响应推送通道
	done     chan struct{}         // 连接关闭信号
	created  time.Time
}

// sessionManager 管理所有活跃的 SSE session。
type sessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*sseSession
}

func newSessionManager() *sessionManager {
	return &sessionManager{sessions: make(map[string]*sseSession)}
}

func (m *sessionManager) create() *sseSession {
	s := &sseSession{
		id:      randomID(16),
		events:  make(chan *jsonRPCResponse, 64), // 缓冲 64 条响应
		done:    make(chan struct{}),
		created: time.Now(),
	}
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	return s
}

func (m *sessionManager) get(id string) (*sseSession, bool) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	return s, ok
}

func (m *sessionManager) remove(id string) {
	m.mu.Lock()
	if s, ok := m.sessions[id]; ok {
		close(s.done)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
}

func (m *sessionManager) count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// runHTTPServer 启动 HTTP/SSE 传输模式的 MCP Server。
func runHTTPServer(state *serverState, addr string) {
	mgr := newSessionManager()

	mux := http.NewServeMux()

	// ── SSE 端点：客户端长连接 ──
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		// SSE 要求 GET + Accept: text/event-stream
		if r.Method != "GET" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		// 创建 session
		sess := mgr.create()
		defer mgr.remove(sess.id)

		log.Printf("[http] SSE session 创建: %s (总活跃: %d)", sess.id, mgr.count())

		// 设置 SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// 1. 发送 endpoint 事件：告知 client POST 端点
		endpointURL := fmt.Sprintf("/rpc?session=%s", sess.id)
		fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURL)
		flusher.Flush()

		// 2. 持续推送响应（直到 client 断开）
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				// Client 断开连接
				log.Printf("[http] SSE session 断开: %s", sess.id)
				return
			case <-sess.done:
				return
			case resp := <-sess.events:
				// 推送 JSON-RPC 响应
				data, _ := json.Marshal(resp)
				fmt.Fprintf(w, "data: %s\n\n", string(data))
				flusher.Flush()
			}
		}
	})

	// ── RPC 端点：接收 JSON-RPC 请求 ──
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		// CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// 查找 session
		sessID := r.URL.Query().Get("session")
		if sessID == "" {
			http.Error(w, `{"error":"missing session parameter"}`, http.StatusBadRequest)
			return
		}
		sess, ok := mgr.get(sessID)
		if !ok {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}

		// 解析 JSON-RPC 请求
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp := &jsonRPCResponse{
				JSONRPC: "2.0",
				Error:   &jsonRPCError{Code: -32700, Message: "Parse error: " + err.Error()},
			}
			sess.events <- resp
			w.WriteHeader(http.StatusAccepted)
			return
		}

		// 处理请求
		resp := handleRequest(state, &req)
		if resp != nil {
			// 通过 SSE 流推送响应
			select {
			case sess.events <- resp:
				w.WriteHeader(http.StatusAccepted)
			default:
				// channel 满（client 太慢），返回错误
				http.Error(w, `{"error":"response buffer full"}`, http.StatusServiceUnavailable)
			}
		} else {
			// notifications（如 initialized）不需要响应
			w.WriteHeader(http.StatusAccepted)
		}
	})

	// ── 健康检查端点 ──
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		state.mu.RLock()
		kbCount := len(state.svcs)
		state.mu.RUnlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":          "ok",
			"knowledge_bases": kbCount,
			"sessions":        mgr.count(),
			"version":         "2.0.0",
		})
	})

	// ── REST API 端点（无鉴权，直接返回 JSON，供 curl 测试 / 前端调用）──
	// 每个 REST 端点内部调用 handleRequest，结果直接写入 HTTP 响应体（不走 SSE）。

	// GET /api/tools — 列出所有可用工具
	mux.HandleFunc("/api/tools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := handleRequest(state, &jsonRPCRequest{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/list", Params: json.RawMessage("{}"),
		})
		writeJSONResponse(w, resp)
	})

	// GET /api/kbs — 列出所有知识库
	mux.HandleFunc("/api/kbs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := handleRequest(state, &jsonRPCRequest{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
			Params: json.RawMessage(`{"name":"list_knowledge_bases","arguments":{}}`),
		})
		writeJSONResponse(w, resp)
	})

	// GET /api/search?keyword=aggregate&kg=my-skills&limit=10 — 搜索知识图谱
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		keyword := r.URL.Query().Get("keyword")
		kg := r.URL.Query().Get("kg")
		limit := 10
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
		}
		args := map[string]interface{}{"keyword": keyword, "limit": limit}
		if kg != "" {
			args["kg"] = kg
		}
		paramsJSON, _ := json.Marshal(map[string]interface{}{"name": "domain_search", "arguments": args})
		resp := handleRequest(state, &jsonRPCRequest{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
			Params: paramsJSON,
		})
		writeJSONResponse(w, resp)
	})

	// GET /api/status?kg=my-skills — 知识库状态
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		kg := r.URL.Query().Get("kg")
		args := map[string]interface{}{}
		if kg != "" {
			args["kg"] = kg
		}
		paramsJSON, _ := json.Marshal(map[string]interface{}{"name": "domain_status", "arguments": args})
		resp := handleRequest(state, &jsonRPCRequest{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
			Params: paramsJSON,
		})
		writeJSONResponse(w, resp)
	})

	// GET /api/impact?entity=order-aggregate&kg=my-skills&depth=3 — 影响分析
	mux.HandleFunc("/api/impact", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		entity := r.URL.Query().Get("entity")
		kg := r.URL.Query().Get("kg")
		depth := 2
		if d := r.URL.Query().Get("depth"); d != "" {
			fmt.Sscanf(d, "%d", &depth)
		}
		args := map[string]interface{}{"entity_name": entity, "depth": depth}
		if kg != "" {
			args["kg"] = kg
		}
		paramsJSON, _ := json.Marshal(map[string]interface{}{"name": "domain_impact", "arguments": args})
		resp := handleRequest(state, &jsonRPCRequest{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
			Params: paramsJSON,
		})
		writeJSONResponse(w, resp)
	})

	// GET /api/describe?kg=my-skills — 知识层完整清单
	mux.HandleFunc("/api/describe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		kg := r.URL.Query().Get("kg")
		args := map[string]interface{}{"verbose": true}
		if kg != "" {
			args["kg"] = kg
		}
		paramsJSON, _ := json.Marshal(map[string]interface{}{"name": "describe_knowledge_layer", "arguments": args})
		resp := handleRequest(state, &jsonRPCRequest{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
			Params: paramsJSON,
		})
		writeJSONResponse(w, resp)
	})

	// ── 根路径：服务信息 ──
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><body>
<h1>Domain Knowledge Layer MCP Server</h1>
<p>Version 2.0.0 (HTTP/SSE transport)</p>
<h2>Endpoints</h2>
<ul>
  <li><code>GET /sse</code> — SSE stream (MCP client connects here)</li>
  <li><code>POST /rpc?session=ID</code> — JSON-RPC request</li>
  <li><code>GET /health</code> — Health check</li>
</ul>
<h2>Test with curl</h2>
<pre>curl http://%s/health</pre>
</body></html>`, addr)
	})

	// 后台定期清理超时 session（1 小时无活动）
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mgr.mu.Lock()
			for id, s := range mgr.sessions {
				if time.Since(s.created) > time.Hour {
					close(s.done)
					delete(mgr.sessions, id)
					log.Printf("[http] 清理超时 session: %s", id)
				}
			}
			mgr.mu.Unlock()
		}
	}()

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
		// 超时配置：SSE 长连接需要较长超时
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("[http] MCP Server 监听 http://%s", addr)
	fmt.Fprintf(os.Stderr, "\nMCP Server 就绪（HTTP/SSE 传输）/ MCP Server ready (HTTP/SSE transport)\n")
	fmt.Fprintf(os.Stderr, "  SSE:  http://%s/sse\n", addr)
	fmt.Fprintf(os.Stderr, "  RPC:  http://%s/rpc?session=ID\n", addr)
	fmt.Fprintf(os.Stderr, "  健康检查: http://%s/health\n\n", addr)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[http] Server 错误: %v", err)
	}
}

// randomID 生成随机 hex 字符串。
func randomID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// writeJSONResponse 将 JSON-RPC 响应直接写入 HTTP 响应体（REST 模式，不走 SSE）。
// 从 MCP 的 content[].text 中提取文本，尝试解析嵌入的 JSON 返回。
func writeJSONResponse(w http.ResponseWriter, resp *jsonRPCResponse) {
	if resp == nil {
		writeJSON(w, map[string]interface{}{"error": "no response"})
		return
	}
	if resp.Error != nil {
		writeJSON(w, map[string]interface{}{"error": resp.Error})
		return
	}

	// JSON round-trip 到 generic map（绕过 *toolResult 类型问题）
	data, _ := json.Marshal(resp)
	var generic map[string]interface{}
	if json.Unmarshal(data, &generic) != nil {
		writeJSON(w, resp.Result)
		return
	}

	result, _ := generic["result"].(map[string]interface{})
	if result == nil {
		writeJSON(w, generic["result"])
		return
	}

	content, _ := result["content"].([]interface{})
	if content == nil {
		writeJSON(w, result)
		return
	}

	// 提取所有 text content
	var texts []string
	for _, c := range content {
		if m, ok := c.(map[string]interface{}); ok && m["type"] == "text" {
			if t, ok := m["text"].(string); ok {
				texts = append(texts, t)
			}
		}
	}

	if len(texts) == 0 {
		writeJSON(w, result)
		return
	}

	// tool 返回格式通常是 "markdown 文本\n---\n{JSON}"。
	// 尝试从文本中提取 JSON 部分（最后一个 --- 之后的 content）。
	text := texts[0]
	if idx := strings.LastIndex(text, "\n---\n"); idx >= 0 {
		jsonPart := strings.TrimSpace(text[idx+len("\n---\n"):])
		var parsed interface{}
		if json.Unmarshal([]byte(jsonPart), &parsed) == nil {
			writeJSON(w, parsed)
			return
		}
	}

	// 整体尝试解析为 JSON
	var parsed interface{}
	if json.Unmarshal([]byte(text), &parsed) == nil {
		writeJSON(w, parsed)
		return
	}

	// 不是 JSON，包装为 {"markdown": "..."} 返回（jq 始终可解析）
	writeJSON(w, map[string]interface{}{"markdown": text})
}

// writeJSON 写入 JSON 响应，不转义 HTML，保持 UTF-8 中文可读。
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}
