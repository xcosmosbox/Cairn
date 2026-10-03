#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════
# smoke-mcp.sh — MCP Server 冒烟测试（stdio 传输）
#
# 走完一次真实的 MCP 会话：initialize 握手 → tools/list 发现能力 →
# tools/call 调用工具，用于确认 server 能起、知识库能开、工具能返回。
#
# 用法 / Usage:
#   scripts/dev/smoke-mcp.sh <config> [kg] [keyword]
#
# 参数 / Args:
#   config   MCP 配置文件路径（必填）
#   kg       知识库名，需与配置里的 name 一致；留空则走联邦检索（跨全部库）
#   keyword  检索关键词，默认 "aggregate"
#
# 示例 / Examples:
#   scripts/dev/smoke-mcp.sh configs/mcp-local.yaml
#   scripts/dev/smoke-mcp.sh configs/mcp-local.yaml my-skills 订单聚合根
#
# 依赖：已 make build（需要 bin/cairn-mcp）、python3（协议交互与语义校验）
#
# 提示：中文检索请传「完整节点名」。索引侧使用 FTS5 unicode61 且不做分词，
# 连续汉字是单个 token，因此子串（如只传「订单」）不会命中——这是既定设计，
# 详见 service/internal/service/query_rewriter.go 的文档注释。
# ═══════════════════════════════════════════════════════════════════
set -euo pipefail

CFG="${1:?usage: smoke-mcp.sh <config> [kg] [keyword]}"
KG="${2:-}"
KEYWORD="${3:-aggregate}"

# 配置路径按调用者工作目录解释，避免切换仓库根后指向另一文件。
# Resolve a relative config path before entering the repository.
case "$CFG" in
  /*) ;;
  *) CFG="$PWD/$CFG" ;;
esac

# 定位仓库根（本脚本位于 scripts/dev/）
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

BIN=./bin/cairn-mcp
# 变量后紧跟全角标点时必须写 ${BIN}：某些 locale 下 bash 会把全角字符的
# 首字节并入变量名，导致 set -u 报 "unbound variable"。
[ -x "$BIN" ] || { echo "✗ 未找到 ${BIN}，请先 make build" >&2; exit 1; }
[ -f "$CFG" ] || { echo "✗ 配置文件不存在: ${CFG}" >&2; exit 1; }

# 一个 Python 进程掌管请求、握手与校验，避免 shell 拼接破坏引号/换行。
# One client owns the session so initialized follows a successful initialize response.
python3 - "$BIN" "$CFG" "$KG" "$KEYWORD" <<'PYEOF'
import json
import re
import selectors
import subprocess
import sys
import time

binary, config, kg, keyword = sys.argv[1:]
protocol = "2024-11-05"
required_tools = {
    "domain_search", "domain_status", "domain_impact",
    "list_knowledge_bases", "describe_knowledge_layer", "resolve_node",
}
print("── MCP 冒烟测试 / smoke test ──", flush=True)
print("   config=%s  kg=%s  keyword=%s" % (config, kg or "<联邦检索>", keyword), flush=True)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def natural(value):
    return type(value) is int and value >= 0


def tool_text(result):
    require(isinstance(result, dict), "工具结果必须是对象")
    require(result.get("isError", False) is False, "工具返回 isError")
    content = result.get("content")
    require(isinstance(content, list) and content, "工具结果缺少 content")
    texts = []
    for item in content:
        require(isinstance(item, dict), "非法 content 条目")
        if item.get("type") == "text":
            require(isinstance(item.get("text"), str), "text 内容类型错误")
            texts.append(item["text"])
    require(texts, "工具未返回文本")
    text = "\n".join(texts)
    require(not text.lstrip().startswith("错误 / Error:"), "工具以文本返回错误")
    return text


proc = subprocess.Popen([binary, "--config", config], stdin=subprocess.PIPE,
                        stdout=subprocess.PIPE, bufsize=0)
selector = selectors.DefaultSelector()
selector.register(proc.stdout, selectors.EVENT_READ)
buffer = b""


def send(method, params=None, rid=None):
    request = {"jsonrpc": "2.0", "method": method}
    if params is not None:
        request["params"] = params
    if rid is not None:
        request["id"] = rid
    proc.stdin.write((json.dumps(request, ensure_ascii=False) + "\n").encode("utf-8"))
    proc.stdin.flush()


def response(rid, label):
    global buffer
    deadline = time.monotonic() + 60
    while True:
        if b"\n" not in buffer:
            remaining = deadline - time.monotonic()
            require(remaining > 0 and selector.select(remaining), "等待 %s 响应超时" % label)
            chunk = proc.stdout.read(65536)
            require(chunk, "服务器退出，缺少 %s 响应" % label)
            buffer += chunk
            continue
        line, buffer = buffer.split(b"\n", 1)
        require(line.strip(), "stdout 出现空协议消息")
        message = json.loads(line)
        require(isinstance(message, dict) and message.get("jsonrpc") == "2.0", "非法 JSON-RPC 消息")
        # 允许服务器通知；请求的响应 ID 必须精确匹配，不能凭 ID 出现就判通过。
        # Server notifications may interleave; matching responses must actually succeed.
        if "id" not in message and isinstance(message.get("method"), str):
            continue
        require(type(message.get("id")) is int and message["id"] == rid, "响应 ID 不匹配: %s" % label)
        require("error" not in message, "%s 返回 JSON-RPC error: %s" % (label, message.get("error")))
        require("result" in message, "%s 缺少 result" % label)
        print("✓ %s" % label, flush=True)
        return message["result"]


def call(rid, name, arguments):
    send("tools/call", {"name": name, "arguments": arguments}, rid)
    return tool_text(response(rid, name))


try:
    send("initialize", {"protocolVersion": protocol, "capabilities": {},
                        "clientInfo": {"name": "smoke", "version": "1.0"}}, 1)
    initialized = response(1, "initialize")
    require(isinstance(initialized, dict) and initialized.get("protocolVersion") == protocol,
            "服务器未协商请求的 MCP 日期版本")
    require(isinstance(initialized.get("capabilities"), dict)
            and isinstance(initialized["capabilities"].get("tools"), dict), "服务器未声明 tools 能力")
    info = initialized.get("serverInfo")
    require(isinstance(info, dict) and isinstance(info.get("name"), str) and info["name"]
            and isinstance(info.get("version"), str) and info["version"], "serverInfo 不完整")
    send("notifications/initialized")

    send("tools/list", {}, 2)
    listed = response(2, "tools/list")
    require(isinstance(listed, dict) and isinstance(listed.get("tools"), list), "缺少工具定义")
    names = set()
    definitions = {}
    for tool in listed["tools"]:
        require(isinstance(tool, dict) and isinstance(tool.get("name"), str) and tool["name"], "工具名称非法")
        require(tool["name"] not in names, "工具定义重复")
        names.add(tool["name"])
        definitions[tool["name"]] = tool
        schema = tool.get("inputSchema")
        require(isinstance(schema, dict) and schema.get("type") == "object", "工具 inputSchema 非法")
    require(required_tools <= names, "缺少必要工具: %s" % ", ".join(sorted(required_tools - names)))

    search_schema = definitions["domain_search"]["inputSchema"]
    require(isinstance(search_schema.get("properties"), dict)
            and search_schema["properties"].get("keyword", {}).get("type") == "string"
            and "keyword" in search_schema.get("required", []), "搜索工具未定义必填 keyword")

    catalogue_text = call(3, "list_knowledge_bases", {})
    # 现有工具同时返回 Markdown 概览与分隔线后的 JSON；以结构化尾部校验。
    # The current tool appends its machine-readable JSON after a Markdown separator.
    catalogue = json.loads(catalogue_text.rpartition("\n---\n")[2]
                           if "\n---\n" in catalogue_text else catalogue_text)
    require(isinstance(catalogue, dict), "知识库清单必须是对象")
    kbs, summary = catalogue.get("knowledge_bases"), catalogue.get("summary")
    require(isinstance(kbs, list) and kbs, "没有可查询的知识库")
    require(isinstance(summary, dict) and natural(summary.get("total_kbs")) and summary["total_kbs"] == len(kbs), "知识库清单计数不一致")
    counts = {}
    for kb in kbs:
        require(isinstance(kb, dict) and isinstance(kb.get("name"), str) and kb["name"], "知识库名称非法")
        require(kb["name"] not in counts, "知识库名称重复")
        stats = kb.get("stats")
        require(isinstance(stats, dict) and natural(stats.get("nodes")) and natural(stats.get("edges")), "知识库统计非法")
        counts[kb["name"]] = (stats["nodes"], stats["edges"])
    require(natural(summary.get("total_nodes")) and natural(summary.get("total_edges")), "汇总统计非法")
    require(summary["total_nodes"] == sum(v[0] for v in counts.values())
            and summary["total_edges"] == sum(v[1] for v in counts.values()), "汇总统计不一致")
    selected = kg or kbs[0]["name"]
    require(selected in counts, "指定知识库未连接: %s" % selected)
    # 未指定 kg 时搜索保持联邦语义，状态选择已连接库，兼容多库配置。
    # Federated search remains unscoped; status is scoped to a discovered KB.
    status = call(4, "domain_status", {"kg": selected})
    status_header = "## 知识库状态 / Knowledge Base Status（知识库/KG: %s）\n\n" % selected
    require(status.startswith(status_header), "状态返回了错误知识库")
    status = status[len(status_header):]
    for label, expected in zip(("Total Nodes", "Total Edges"), counts[selected]):
        match = re.search(r"^.*" + label + r": ([0-9]+)\s*$", status, re.M)
        require(match and int(match.group(1)) == expected, "状态统计不一致: %s" % label)
    schema = re.search(r"Schema Version: ([0-9]+)\s*$", status, re.M)
    require(schema and int(schema.group(1)) > 0, "缺少有效模式版本")
    arguments = {"keyword": keyword, "limit": 5}
    if kg:
        arguments["kg"] = kg
    search = call(5, "domain_search", arguments)
    if kg:
        search_header = "搜索关键词: %s（知识库: %s）\n\n" % (keyword, kg)
    else:
        search_header = "联邦搜索关键词: %s（%d 个知识库）\n\n" % (keyword, len(kbs))
    require(search.startswith(search_header), "搜索响应未对应输入关键词/范围")
    search = search[len(search_header):]
    require("未找到匹配结果 / No matching results found." in search.splitlines()
            or (re.search(r"^- ID: \S+", search, re.M) and "类型/Type:" in search),
            "搜索结果既无有效命中，也无零结果说明")
    proc.stdin.close()
    require(proc.wait(timeout=10) == 0, "服务器非零退出")
    require(not buffer.strip() and not proc.stdout.read().strip(), "请求完成后出现额外协议输出")
    print("OK 冒烟测试通过 / smoke test passed", flush=True)
except (ValueError, OSError, subprocess.TimeoutExpired) as exc:
    print("✗ MCP 冒烟失败: %s" % exc, file=sys.stderr)
    sys.exit(1)
finally:
    selector.close()
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
PYEOF
