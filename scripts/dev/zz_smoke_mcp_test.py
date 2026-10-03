#!/usr/bin/env python3
"""Smoke regression harness: python3 scripts/dev/zz_smoke_mcp_test.py.

原脚本只检查响应 ID，错误响应也通过，而且 shell 拼接不能保留引号与换行。
Fake servers test actual process exit status, sequencing, and semantic validation.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


FAKE_SERVER = r'''#!/usr/bin/env python3
import json
import os
import sys

mode = os.environ.get("SMOKE_TEST_MODE", "ok")
kg = os.environ.get("SMOKE_TEST_KG", "")
keyword = os.environ.get("SMOKE_TEST_KEYWORD", "aggregate")
name = kg or "first"
initialized = False
requests = 0

def reply(rid, result):
    print(json.dumps({"jsonrpc": "2.0", "id": rid, "result": result}), flush=True)

for line in sys.stdin:
    d = json.loads(line)
    method, rid = d["method"], d.get("id")
    if method == "notifications/initialized":
        assert requests == 1 and rid is None
        initialized = True
        continue
    requests += 1
    assert rid == requests
    if rid > 1:
        assert initialized, "tools sent before initialized notification"
    if mode == "missing":
        sys.exit(0)
    if mode == "malformed":
        print("invalid-json", flush=True)
        continue
    if mode == "rpc_error":
        print(json.dumps({"jsonrpc": "2.0", "id": rid,
                          "error": {"code": -32603, "message": "broken"}}), flush=True)
        continue
    if rid == 1:
        result = {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}},
                  "serverInfo": {"name": "fake", "version": "1.0"}}
        if mode == "bad_protocol":
            result["protocolVersion"] = "0.1"
        reply(rid, result)
    elif rid == 2:
        names = ["domain_search", "domain_status", "domain_impact", "list_knowledge_bases",
                 "describe_knowledge_layer", "resolve_node"]
        tools = [{"name": n, "inputSchema": {"type": "object", "properties": {}}} for n in names]
        tools[0]["inputSchema"].update({"properties": {"keyword": {"type": "string"}},
                                       "required": ["keyword"]})
        if mode == "bad_tools":
            tools = []
        reply(rid, {"tools": tools})
    else:
        arguments = d["params"]["arguments"]
        if rid == 3:
            assert d["params"]["name"] == "list_knowledge_bases" and arguments == {}
            bases = [{"name": name, "stats": {"nodes": 0, "edges": 0}}]
            if mode == "multi":
                bases.append({"name": "second", "stats": {"nodes": 0, "edges": 0}})
            text = json.dumps({"knowledge_bases": bases,
                               "summary": {"total_kbs": len(bases), "total_nodes": 0, "total_edges": 0}})
            if mode == "empty_kbs":
                text = json.dumps({"knowledge_bases": [], "summary": {"total_kbs": 0}})
            if mode != "bare_json":
                text = "## 已连接知识库 / Connected Knowledge Bases\n\n---\n" + text
        elif rid == 4:
            assert d["params"]["name"] == "domain_status" and arguments == {"kg": name}
            text = ("## 知识库状态 / Knowledge Base Status（知识库/KG: %s）\n\n"
                    "- 节点总数 / Total Nodes: 0\n- 边总数 / Total Edges: 0\n"
                    "- 模式版本 / Schema Version: 5\n") % name
            if mode == "bad_stats":
                text = text.replace("Total Nodes: 0", "Total Nodes: 9")
        else:
            assert d["params"]["name"] == "domain_search"
            expected = {"keyword": keyword, "limit": 5}
            if kg:
                expected["kg"] = kg
            assert arguments == expected
            if kg:
                text = "搜索关键词: %s（知识库: %s）\n\n" % (keyword, kg)
            else:
                text = "联邦搜索关键词: %s（%d 个知识库）\n\n" % (keyword, 2 if mode == "multi" else 1)
            if mode == "hit":
                text += "## 1. matched (1.0)\n- ID: node-1\n- 类型/Type: Concept\n"
            elif mode == "bad_search":
                text += "arbitrary success-looking text"
            else:
                text += "未找到匹配结果 / No matching results found.\n"
        result = {"content": [{"type": "text", "text": text}]}
        if mode == "tool_error":
            result["isError"] = True
        if mode == "error_text":
            result["content"][0]["text"] = "错误 / Error: fake failure"
        reply(rid, result)
'''


class SmokeMCPTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        root = Path(self.directory.name)
        scripts = root / "scripts" / "dev"
        scripts.mkdir(parents=True)
        self.script = scripts / "smoke-mcp.sh"
        shutil.copyfile(Path(__file__).with_name("smoke-mcp.sh"), self.script)
        (root / "bin").mkdir()
        binary = root / "bin" / "cairn-mcp"
        binary.write_text(FAKE_SERVER, encoding="utf-8")
        binary.chmod(0o755)
        self.config = root / "config.yaml"
        self.config.write_text("fake: true\n", encoding="utf-8")
        self.root = root

    def run_smoke(self, mode, kg="", keyword="aggregate"):
        env = dict(os.environ, SMOKE_TEST_MODE=mode, SMOKE_TEST_KG=kg,
                   SMOKE_TEST_KEYWORD=keyword)
        return subprocess.run(["bash", str(self.script), "config.yaml", kg, keyword],
                              cwd=self.root, env=env, capture_output=True, text=True, timeout=10)

    def test_success_including_zero_results_and_multiple_bases(self):
        for mode in ("ok", "hit", "multi", "bare_json"):
            with self.subTest(mode=mode):
                result = self.run_smoke(mode)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("smoke test passed", result.stdout)

    def test_errors_and_invalid_payloads_cannot_pass_by_id(self):
        # 原脚本把 JSON-RPC error 当成“已收到响应”；日期和 content 均未校验。
        for mode in ("rpc_error", "bad_protocol", "bad_tools", "tool_error", "error_text",
                     "bad_stats", "bad_search", "empty_kbs", "malformed", "missing"):
            with self.subTest(mode=mode):
                result = self.run_smoke(mode)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertNotIn("smoke test passed", result.stdout)

    def test_quoted_and_multiline_arguments_round_trip(self):
        result = self.run_smoke("ok", 'kg "quoted"\\line\n下一行',
                                'term "quoted"\\line\nNo matching results found.')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
