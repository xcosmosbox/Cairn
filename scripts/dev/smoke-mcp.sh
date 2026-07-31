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
# 依赖：已 make build（需要 bin/mcp-server）、python3（仅用于美化 JSON 输出）
#
# 提示：中文检索请传「完整节点名」。索引侧使用 FTS5 unicode61 且不做分词，
# 连续汉字是单个 token，因此子串（如只传「订单」）不会命中——这是既定设计，
# 详见 dk-service/internal/service/query_rewriter.go 的文档注释。
# ═══════════════════════════════════════════════════════════════════
set -euo pipefail

CFG="${1:?usage: smoke-mcp.sh <config> [kg] [keyword]}"
KG="${2:-}"
KEYWORD="${3:-aggregate}"

# 定位仓库根（本脚本位于 scripts/dev/）
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

BIN=./bin/mcp-server
# 变量后紧跟全角标点时必须写 ${BIN}：某些 locale 下 bash 会把全角字符的
# 首字节并入变量名，导致 set -u 报 "unbound variable"。
[ -x "$BIN" ] || { echo "✗ 未找到 ${BIN}，请先 make build" >&2; exit 1; }
[ -f "$CFG" ] || { echo "✗ 配置文件不存在: ${CFG}" >&2; exit 1; }

# 构造工具参数：指定 kg 则限定单库，否则联邦检索
if [ -n "$KG" ]; then
  search_args="{\"keyword\":\"$KEYWORD\",\"kg\":\"$KG\",\"limit\":5}"
  status_args="{\"kg\":\"$KG\"}"
else
  search_args="{\"keyword\":\"$KEYWORD\",\"limit\":5}"
  status_args="{}"
fi

echo "── MCP 冒烟测试 / smoke test ──"
echo "   config=$CFG  kg=${KG:-<联邦检索>}  keyword=$KEYWORD"
echo

# 响应解析器写入临时文件：内嵌 heredoc 会占用 stdin，与下游管道冲突。
PARSER="$(mktemp -t smoke-mcp-parser)"
trap 'rm -f "$PARSER"' EXIT
cat > "$PARSER" <<'PYEOF'
import sys, json

LABEL = {2: "tools/list", 3: "list_knowledge_bases", 4: "domain_status", 5: "domain_search"}
seen = set()

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        d = json.loads(line)
    except json.JSONDecodeError:
        continue

    rid = d.get("id")
    if rid not in LABEL:
        continue
    seen.add(rid)
    print("=" * 60)
    print("  [%s] %s" % (rid, LABEL[rid]))
    print("=" * 60)

    if "error" in d:
        print("  x error: %s" % (d["error"],))
        continue

    result = d.get("result", {})
    if "tools" in result:
        for t in result["tools"]:
            print("  - %s" % (t["name"],))
    else:
        for c in result.get("content", []):
            if c.get("type") == "text":
                print(c["text"][:1200])
    print()

missing = sorted(set(LABEL) - seen)
if missing:
    sys.stderr.write("! 未收到响应: %s\n" % ", ".join(LABEL[m] for m in missing))
    sys.exit(1)
print("OK 冒烟测试通过 / smoke test passed")
PYEOF

# 一次 stdio 会话内按序发送全部请求。
# initialize 必须最先发送；catalog watch 模式下首次拉取需要时间，故留 3s。
{
  echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"1.0"}}}'
  sleep 3
  echo '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
  sleep 0.5
  echo '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_knowledge_bases","arguments":{}}}'
  sleep 0.5
  echo "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"domain_status\",\"arguments\":$status_args}}"
  sleep 0.5
  echo "{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"tools/call\",\"params\":{\"name\":\"domain_search\",\"arguments\":$search_args}}"
  sleep 0.5
} | "$BIN" --config "$CFG" 2>/dev/null | python3 "$PARSER"
