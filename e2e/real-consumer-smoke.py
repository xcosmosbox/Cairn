#!/usr/bin/env python3
"""Read-only CLI/MCP acceptance against an existing, standalone Cairn KG.

The five corpus themes select actual FTS-searchable names, respecting Cairn's
whole-token Chinese FTS contract. Missing themes are reported, never invented.
Only bounded node/source metadata and response hashes enter the JSON receipt.
"""

import argparse
from contextlib import closing
import hashlib
import http.client
import json
import os
from pathlib import Path, PurePosixPath
import re
import selectors
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlencode, urlsplit
from urllib.request import Request, urlopen

TOOLS = {"domain_search", "domain_status", "domain_impact",
         "list_knowledge_bases", "describe_knowledge_layer", "resolve_node"}
ALIASES = ("consumer-primary", "consumer-alias")
THEMES = (
    ("Flink反压", (("flink",), ("反压", "backpressure", "back pressure"))),
    ("积压", (("积压", "backlog", "consumer lag", "消费延迟", "lag"),)),
    ("GPU OnlineEval", (("gpu",), ("onlineeval", "online eval", "online-eval", "在线评估"))),
    ("datahub datasvr", (("datahub", "datasvr"),)),
    ("checkpoint", (("checkpoint", "检查点", "chkpt"),)),
)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def snapshot(path):
    require(path.is_file(), "knowledge DB does not exist")
    sidecars = [str(path) + suffix for suffix in ("-wal", "-shm")
                if Path(str(path) + suffix).exists()]
    return {"sha256": digest(path.read_bytes()), "bytes": path.stat().st_size,
            "sidecars": sidecars}


def proof(text):
    data = text.encode("utf-8")
    return {"response_sha256": digest(data), "response_bytes": len(data)}


def cli(binary, db, command, *arguments):
    result = subprocess.run([str(binary), "--db-path", str(db), command, *arguments],
                            capture_output=True, text=True, timeout=60)
    require(result.returncode == 0, "CLI %s failed (exit %d)" % (command, result.returncode))
    require(result.stdout.strip(), "CLI %s returned no business output" % command)
    return result.stdout


class MCP:
    def __init__(self, binary, config):
        self.stderr = tempfile.TemporaryFile()
        self.proc = subprocess.Popen([str(binary), "--config", str(config)],
                                     stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=self.stderr, bufsize=0)
        self.selector = selectors.DefaultSelector()
        self.selector.register(self.proc.stdout, selectors.EVENT_READ)
        self.buffer = b""
        self.next_id = 0
        self.calls = []
        self.closed = False

    def request(self, method, params=None, notification=False):
        self.next_id += 1
        rid = self.next_id
        request = {"jsonrpc": "2.0", "method": method}
        if not notification:
            request["id"] = rid
        if params is not None:
            request["params"] = params
        self.proc.stdin.write((json.dumps(request, ensure_ascii=False) + "\n").encode())
        self.proc.stdin.flush()
        if notification:
            return None
        deadline = time.monotonic() + 60
        while True:
            if b"\n" not in self.buffer:
                remaining = deadline - time.monotonic()
                require(remaining > 0 and self.selector.select(remaining), "MCP %s timed out" % method)
                chunk = self.proc.stdout.read(65536)
                require(chunk, "MCP exited before %s response" % method)
                self.buffer += chunk
                require(len(self.buffer) <= 8 * 1024 * 1024, "MCP response exceeds bounded smoke limit")
                continue
            line, self.buffer = self.buffer.split(b"\n", 1)
            message = json.loads(line)
            require(message.get("jsonrpc") == "2.0", "invalid MCP JSON-RPC envelope")
            if "id" not in message and "method" in message:
                continue
            require(type(message.get("id")) is int and message["id"] == rid, "MCP response ID mismatch")
            require("error" not in message and "result" in message, "MCP %s returned protocol error" % method)
            return message["result"]

    def call(self, name, arguments, expect_error=False):
        result = self.request("tools/call", {"name": name, "arguments": arguments})
        require(isinstance(result, dict), "MCP %s result is not an object" % name)
        require(result.get("isError", False) is expect_error,
                "MCP %s business error status differs from expectation" % name)
        content = result.get("content")
        require(isinstance(content, list) and content, "MCP %s lacks content" % name)
        texts = [item["text"] for item in content
                 if item.get("type") == "text" and isinstance(item.get("text"), str)]
        text = "\n".join(texts)
        require(text.strip(), "MCP %s lacks text business payload" % name)
        if not expect_error:
            require(not text.startswith("错误 / Error:"), "MCP %s hides a text error" % name)
        self.calls.append({"tool": name, "arguments": arguments,
                           "expected_error": expect_error, **proof(text)})
        return text

    def close(self, success):
        if self.closed:
            return
        try:
            if success:
                self.proc.stdin.close()
                require(self.proc.wait(timeout=10) == 0, "MCP exited nonzero")
                require(not self.buffer.strip() and not self.proc.stdout.read().strip(),
                        "MCP emitted unexpected trailing messages")
        finally:
            if self.proc.poll() is None:
                self.proc.terminate()
                try:
                    self.proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.proc.kill()
                    self.proc.wait()
            self.selector.close()
            self.proc.stdout.close()
            if not self.proc.stdin.closed:
                self.proc.stdin.close()
            self.stderr.close()
            self.closed = True


def json_tail(text):
    require("\n---\n" in text, "missing structured business payload")
    return json.loads(text.rpartition("\n---\n")[2])


class HTTPMCP(MCP):
    """The same tool assertions over Cairn's real HTTP/SSE transport."""

    def __init__(self, binary, config):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        self.base = "http://127.0.0.1:%d" % port
        self.stderr = tempfile.TemporaryFile()
        self.proc = subprocess.Popen([str(binary), "--config", str(config),
                                      "--listen", "127.0.0.1:%d" % port],
                                     stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                     stderr=self.stderr)
        self.stream = None
        self.connection = None
        self.next_id = 0
        self.calls = []
        self.closed = False
        try:
            deadline = time.monotonic() + 15
            while True:
                require(self.proc.poll() is None, "HTTP MCP exited during startup")
                try:
                    health = self.get("/health")
                    require(health.get("status") == "ok", "HTTP health is not healthy")
                    break
                except OSError:
                    require(time.monotonic() < deadline, "HTTP MCP startup timed out")
                    time.sleep(0.05)
            self.connection = http.client.HTTPConnection("127.0.0.1", port, timeout=15)
            self.connection.request("GET", "/sse", headers={"Accept": "text/event-stream"})
            self.stream = self.connection.getresponse()
            require(self.stream.status == 200 and
                    self.stream.getheader("Content-Type", "").startswith("text/event-stream"),
                    "SSE endpoint lacks successful event-stream response")
            event, self.endpoint = self.event()
            endpoint = urlsplit(self.endpoint)
            require(event == "endpoint" and not endpoint.scheme and not endpoint.netloc
                    and endpoint.path == "/rpc" and endpoint.query.startswith("session="),
                    "SSE did not announce a local RPC session endpoint")
        except Exception:
            self.close(False)
            raise

    def get(self, path, arguments=None):
        if arguments:
            path += "?" + urlencode(arguments)
        with urlopen(self.base + path, timeout=15) as response:
            require(response.status == 200, "REST %s failed" % path.split("?")[0])
            data = response.read(8 * 1024 * 1024 + 1)
            require(len(data) <= 8 * 1024 * 1024, "REST response exceeds smoke limit")
            return json.loads(data)

    def event(self):
        kind, data, total = "message", [], 0
        while True:
            line = self.stream.readline(8 * 1024 * 1024 + 1)
            require(line, "SSE closed before response")
            total += len(line)
            require(total <= 8 * 1024 * 1024, "SSE response exceeds bounded smoke limit")
            line = line.decode("utf-8").rstrip("\r\n")
            if not line:
                if data:
                    return kind, "\n".join(data)
                continue
            if line.startswith("event:"):
                kind = line[6:].strip()
            elif line.startswith("data:"):
                data.append(line[5:].lstrip())

    def request(self, method, params=None, notification=False):
        self.next_id += 1
        rid = self.next_id
        request = {"jsonrpc": "2.0", "method": method}
        if not notification:
            request["id"] = rid
        if params is not None:
            request["params"] = params
        post = Request(self.base + self.endpoint, data=json.dumps(request).encode(),
                       headers={"Content-Type": "application/json"}, method="POST")
        with urlopen(post, timeout=15) as response:
            require(response.status == 202, "SSE RPC request was not accepted")
        if notification:
            return None
        _, data = self.event()
        message = json.loads(data)
        require(message.get("jsonrpc") == "2.0" and type(message.get("id")) is int
                and message["id"] == rid, "SSE response envelope/ID mismatch")
        require("error" not in message and "result" in message,
                "SSE %s returned protocol error" % method)
        return message["result"]

    def close(self, success):
        if self.closed:
            return
        if self.stream is not None:
            self.stream.close()
        if self.connection is not None:
            self.connection.close()
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()
        self.stderr.close()
        self.closed = True


def exercise_http(binary, config, node, query, all_ids, node_count, edge_count, edge_probe):
    http = HTTPMCP(binary, config)
    try:
        tools = http.get("/api/tools")
        require(TOOLS <= {tool["name"] for tool in tools["tools"]}, "REST tools missing")
        catalogue = http.get("/api/kbs")
        require(catalogue["summary"] == {"total_kbs": 2, "total_nodes": node_count * 2,
                                          "total_edges": edge_count * 2}, "REST catalogue mismatch")
        search = http.get("/api/search", {"kg": ALIASES[0], "keyword": query, "limit": 50})
        check_ids(IDs(search.get("markdown", ""), r"^- ID:\s*(\S+)"), all_ids,
                  "REST search", node["id"])
        status = http.get("/api/status", {"kg": ALIASES[0]})
        for label, count in (("Total Nodes", node_count), ("Total Edges", edge_count)):
            require(re.search(label + ": " + str(count) + r"\s*$", status.get("markdown", ""), re.MULTILINE),
                    "REST status counts mismatch")
        failure = http.get("/api/status", {"kg": "consumer-does-not-exist"})
        require(failure.get("isError") is True and "consumer-does-not-exist" in failure.get("error", ""),
                "REST hides unknown-KG business failure")
        initialized = http.request("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                   "clientInfo": {"name": "real-consumer-http-smoke", "version": "1"}})
        require(initialized.get("protocolVersion") == "2024-11-05", "SSE handshake contract failed")
        http.request("notifications/initialized", notification=True)
        require(TOOLS <= {tool["name"] for tool in http.request("tools/list", {})["tools"]},
                "SSE tool discovery missing required tools")
        text = http.call("domain_search", {"kg": ALIASES[0], "keyword": query, "limit": 50})
        check_ids(IDs(text, r"^- ID:\s*(\S+)"), all_ids, "SSE search", node["id"])
        resolved = http.call("resolve_node", {"kg": ALIASES[0], "uuid": node["id"]})
        require("UUID %s 存活 / alive" % node["id"] in resolved, "SSE UUID resolution failed")
        if edge_probe:
            source, target = edge_probe
            traversal = {"kg": ALIASES[0], "entity": phrase(source["name"]), "depth": 2}
            impact = http.get("/api/impact", traversal)
            require(target in IDs(impact.get("markdown", ""), r"\(([^,\s]+), (?:Skill|Domain|Subdomain|Entity|Concept)\)"),
                    "REST BFS omitted actual outgoing neighbor")
            text = http.call("domain_impact", {"kg": ALIASES[0], "entity_name": traversal["entity"], "depth": 2})
            require(target in IDs(text, r"\(([^,\s]+), (?:Skill|Domain|Subdomain|Entity|Concept)\)"),
                    "SSE BFS omitted actual outgoing neighbor")
        http.call("domain_status", {"kg": "consumer-does-not-exist"}, expect_error=True)
        return {"passed": True, "protocol": initialized["protocolVersion"],
                "rest_search": proof(json.dumps(search, ensure_ascii=False, sort_keys=True)),
                "rest_unknown_kg_rejected": True, "sse_calls": http.calls}
    finally:
        http.close(True)


def phrase(name):
    return '"' + name.replace('"', '""') + '"'


def fts_entries(conn, query):
    return [row[0] for row in conn.execute(
        "SELECT n.id FROM nodes_fts JOIN nodes n ON n.rowid=nodes_fts.rowid "
        "WHERE nodes_fts MATCH ? ORDER BY rank LIMIT 3", (query,))]


def score(node, groups):
    name = node["name"].casefold()
    body = " ".join(str(node.get(key) or "") for key in
                    ("name", "summary", "description", "synonyms", "domain", "subdomain")).casefold()
    hits = []
    total = 0
    for group in groups:
        matched = [term for term in group if
                   (re.search(r"\blag\b", body) if term == "lag" else term in body)]
        if not matched:
            return 0, []
        hits.extend(matched)
        total += max(10 if term in name else 1 for term in matched)
    return total, hits


def choose(conn, nodes, groups=None):
    candidates = []
    for node in nodes:
        amount, terms = score(node, groups) if groups else (1, [])
        if amount:
            candidates.append((-amount, node["id"], node, terms))
    for _, _, node, terms in sorted(candidates):
        query = phrase(node["name"])
        if node["id"] in fts_entries(conn, query):
            return node, query, terms
    return None


def IDs(text, pattern):
    return re.findall(pattern, text, re.MULTILINE)


def check_ids(values, all_ids, label, required=None):
    require(values and set(values) <= all_ids, label + " returned absent/unknown node IDs")
    if required:
        require(required in values, label + " did not return the selected actual node")


def exercise_node(conn, node, query, cairn, db, mcp, all_ids):
    nid = node["id"]
    search = cli(cairn, db, "find", "--limit", "50", query)
    find_ids = IDs(search, r"^\s*ID:\s*(\S+)")
    check_ids(find_ids, all_ids, "CLI find", nid)
    impact = cli(cairn, db, "impact", "--depth", "2", query)
    impact_ids = IDs(impact, r"\(([^,\s]+), (?:Skill|Domain|Subdomain|Entity|Concept)\)")
    check_ids(impact_ids, all_ids, "CLI impact", nid)
    require("影响分析起点:" in impact and "统计:" in impact, "CLI impact lacks start/statistics")
    why = cli(cairn, db, "why", nid)
    sources = [dict(row) for row in conn.execute(
        "SELECT node_uuid,member_id,skill,file_path,COALESCE(start_line,0) AS start_line,"
        "COALESCE(end_line,0) AS end_line FROM node_sources WHERE node_uuid=? "
        "ORDER BY skill,file_path,member_id", (nid,))]
    require(sources and "存活" in why and nid in why, "CLI why lacks living source trace")
    for source in sources:
        rel = PurePosixPath(source["file_path"])
        require(not rel.is_absolute() and ".." not in rel.parts and "\\" not in str(rel),
                "source trace has unsafe file path")
        require(source["member_id"] and source["skill"], "source trace lacks member/skill")
        span = ":%d-%d" % (source["start_line"], source["end_line"]) if source["start_line"] else ""
        require(source["start_line"] >= 0 and source["end_line"] >= source["start_line"], "invalid source span")
        require("[%s] %s%s  (member %s)" %
                (source["skill"], source["file_path"], span, source["member_id"]) in why,
                "CLI why trace differs from authoritative source ledger")
    search_text = mcp.call("domain_search", {"kg": ALIASES[0], "keyword": query, "limit": 50})
    check_ids(IDs(search_text, r"^- ID:\s*(\S+)"), all_ids, "MCP search", nid)
    impact_text = mcp.call("domain_impact", {"kg": ALIASES[0], "entity_name": query, "depth": 2})
    check_ids(IDs(impact_text, r"\(([^,\s]+), (?:Skill|Domain|Subdomain|Entity|Concept)\)"),
              all_ids, "MCP impact", nid)
    require(re.search(r"Reachable nodes: [0-9]+", impact_text)
            and re.search(r"Total edges: [0-9]+", impact_text), "MCP impact lacks business statistics")
    resolved = mcp.call("resolve_node", {"kg": ALIASES[0], "uuid": nid})
    require("UUID %s 存活 / alive" % nid in resolved and node["name"] in resolved,
            "MCP resolve_node failed to resolve living authoritative UUID")
    return {"node": {key: node[key] for key in ("id", "name", "label", "domain", "subdomain")},
            "query": query, "cli_find": {"matched_ids": find_ids, **proof(search)},
            "cli_impact": {"node_ids": sorted(set(impact_ids)), **proof(impact)},
            "trace": {"source_count": len(sources), "sources": sources[:8],
                      "source_list_truncated": len(sources) > 8, **proof(why)}}


def run(args, report):
    db = Path(args.db).resolve(strict=True)
    cairn, mcp_bin = Path(args.cairn_bin).resolve(strict=True), Path(args.mcp_bin).resolve(strict=True)
    before = snapshot(db)
    report["database"] = {"path": str(db), "before": before}
    require(not before["sidecars"], "acceptance requires a standalone DB without WAL/SHM")
    mcp = None
    completed = False
    try:
        # mode=ro + immutable prevents Python itself from creating SQLite sidecars.
        # Refuse live WAL inputs above instead of silently ignoring committed WAL.
        with closing(sqlite3.connect(db.as_uri() + "?mode=ro&immutable=1", uri=True)) as conn:
            conn.row_factory = sqlite3.Row
            require(conn.execute("PRAGMA quick_check").fetchone()[0] == "ok", "database quick_check failed")
            schema = conn.execute("PRAGMA user_version").fetchone()[0]
            require(schema in (4, 5), "unsupported query schema")
            node_count = conn.execute("SELECT count(*) FROM nodes").fetchone()[0]
            edge_count = conn.execute("SELECT count(*) FROM edges").fetchone()[0]
            require(node_count > 0, "empty graph is not a consumer acceptance fixture")
            require(conn.execute("SELECT count(*) FROM nodes n WHERE label IN ('Entity','Concept') "
                                 "AND NOT EXISTS(SELECT 1 FROM node_sources s WHERE s.node_uuid=n.id)").fetchone()[0] == 0,
                    "concept/entity has no authoritative source ledger")
            identity = conn.execute("SELECT value FROM kg_manifest WHERE key='repository_identity'").fetchone()
            require(identity and identity[0], "missing repository ownership identity")
            all_nodes = [dict(row) for row in conn.execute("SELECT * FROM nodes ORDER BY id")]
            all_ids = {node["id"] for node in all_nodes}
            nodes = [node for node in all_nodes if node["label"] in ("Entity", "Concept")]
            fallback = choose(conn, nodes)
            require(fallback is not None, "no actual source-backed node is FTS-searchable")
            report["graph"] = {"schema_version": schema, "nodes": node_count, "edges": edge_count,
                               "repository_identity": identity[0]}
            status = cli(cairn, db, "status")
            for label, count in (("节点总数", node_count), ("边总数", edge_count)):
                require(re.search(label + r":\s*" + str(count) + r"\s*$", status, re.MULTILINE),
                        "CLI status count differs from SQLite")
            report["cli_status"] = proof(status)
            with tempfile.TemporaryDirectory(prefix="cairn-consumer-") as temporary:
                config = Path(temporary) / "mcp.yaml"
                # JSON strings are YAML-compatible; avoid interpolating unescaped paths.
                config.write_text("mcp:\n  knowledge_bases:\n" + "".join(
                    "    - name: %s\n      path: %s\n      description: Read-only consumer alias\n" %
                    (alias, json.dumps(str(db))) for alias in ALIASES), encoding="utf-8")
                mcp = MCP(mcp_bin, config)
                initialized = mcp.request("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                         "clientInfo": {"name": "real-consumer-smoke", "version": "1"}})
                require(initialized.get("protocolVersion") == "2024-11-05"
                        and "tools" in initialized.get("capabilities", {}), "MCP handshake contract failed")
                mcp.request("notifications/initialized", notification=True)
                tools = mcp.request("tools/list", {})["tools"]
                definitions = {tool["name"]: tool for tool in tools}
                require(len(definitions) == len(tools) and TOOLS <= set(definitions), "MCP discovery lacks required tools")
                for name in TOOLS:
                    require(definitions[name]["inputSchema"]["type"] == "object", "invalid MCP tool schema")
                for name, required in (("domain_search", "keyword"), ("domain_impact", "entity_name"), ("resolve_node", "uuid")):
                    require(required in definitions[name]["inputSchema"].get("required", []), "MCP required input schema drift")
                report["mcp_handshake"] = {"protocol": initialized["protocolVersion"], "tools": sorted(definitions)}
                catalogue = json_tail(mcp.call("list_knowledge_bases", {}))
                entries = {item["name"]: item for item in catalogue["knowledge_bases"]}
                require(set(entries) == set(ALIASES), "MCP aliases were not both opened")
                require(catalogue["summary"] == {"total_kbs": 2, "total_nodes": node_count * 2, "total_edges": edge_count * 2},
                        "federated catalogue counts are inconsistent")
                for alias in ALIASES:
                    require(entries[alias]["stats"]["nodes"] == node_count
                            and entries[alias]["stats"]["edges"] == edge_count, "alias catalogue stats mismatch")
                    text = mcp.call("domain_status", {"kg": alias})
                    for label, count in (("Total Nodes", node_count), ("Total Edges", edge_count), ("Schema Version", schema)):
                        require(re.search(label + ": " + str(count) + r"\s*$", text, re.MULTILINE), "MCP status counts mismatch")
                    manifest = json_tail(mcp.call("describe_knowledge_layer", {"kg": alias, "verbose": True}))
                    require(manifest["kg_name"] == alias and manifest["schema_version"] == schema
                            and manifest["stats"]["nodes"] == node_count and manifest["stats"]["edges"] == edge_count,
                            "MCP manifest differs from actual graph")
                overview = json_tail(mcp.call("describe_knowledge_layer", {"verbose": True}))
                require({item["name"] for item in overview["overview"]} == set(ALIASES), "MCP global overview misses alias")

                report["themes"] = []
                present = 0
                for theme, groups in THEMES:
                    chosen = choose(conn, nodes, groups)
                    if chosen is None:
                        report["themes"].append({"theme": theme, "status": "not_present_as_searchable_source_node"})
                        continue
                    node, query, terms = chosen
                    probe = exercise_node(conn, node, query, cairn, db, mcp, all_ids)
                    report["themes"].append({"theme": theme, "status": "passed", "matched_terms": terms, **probe})
                    present += 1
                if not present:
                    report["fixture_probe"] = exercise_node(conn, fallback[0], fallback[1], cairn, db, mcp, all_ids)
                report["coverage"] = {"requested_themes": len(THEMES), "present_themes": present,
                                      "absent_themes": len(THEMES) - present}
                node, query, _ = fallback
                federated = mcp.call("domain_search", {"keyword": query, "limit": 50})
                check_ids(IDs(federated, r"^- ID:\s*(\S+)"), all_ids, "federated search", node["id"])
                require(set(IDs(federated, r"\[知识库/KG: ([^\]]+)\]")) == set(ALIASES),
                        "federated search lost one identical-DB alias")
                resolved = mcp.call("resolve_node", {"kg": ALIASES[1], "uuid": node["id"]})
                require("UUID %s 存活 / alive" % node["id"] in resolved, "second alias UUID resolution failed")
                for tool, arguments in (
                    ("domain_search", {"keyword": query}), ("domain_status", {}),
                    ("domain_impact", {"entity_name": query}),
                    ("describe_knowledge_layer", {}), ("resolve_node", {"uuid": node["id"]}),
                ):
                    text = mcp.call(tool, {**arguments, "kg": "consumer-does-not-exist"}, expect_error=True)
                    require("consumer-does-not-exist" in text, "unknown-KG failure did not identify requested scope")
                mcp.call("domain_status", {}, expect_error=True)
                # Ensure graph traversal is nontrivial when a searchable outgoing edge exists.
                edge_probe = None
                for row in conn.execute("SELECT source_id,target_id FROM edges WHERE source_id!=target_id AND confidence>=0 ORDER BY id"):
                    source = next((n for n in all_nodes if n["id"] == row["source_id"]), None)
                    if source and source["id"] in fts_entries(conn, phrase(source["name"])) and row["target_id"] not in fts_entries(conn, phrase(source["name"])):
                        edge_probe = source, row["target_id"]
                        break
                if edge_probe:
                    source, target = edge_probe
                    query = phrase(source["name"])
                    text = cli(cairn, db, "impact", "--depth", "2", query)
                    require(target in IDs(text, r"\(([^,\s]+), (?:Skill|Domain|Subdomain|Entity|Concept)\)"), "CLI BFS omitted actual outgoing neighbor")
                    mtext = mcp.call("domain_impact", {"kg": ALIASES[0], "entity_name": query, "depth": 2})
                    require(target in IDs(mtext, r"\(([^,\s]+), (?:Skill|Domain|Subdomain|Entity|Concept)\)"), "MCP BFS omitted actual outgoing neighbor")
                    report["graph_impact_probe"] = {"source_id": source["id"], "target_id": target,
                                                    "query": query, "cli": proof(text), "mcp": proof(mtext)}
                else:
                    report["graph_impact_probe"] = {"status": "no_searchable_nontrivial_edge"}
                report["mcp_calls"] = mcp.calls
                mcp.close(True)
                mcp = None
                report["http"] = exercise_http(mcp_bin, config, fallback[0], fallback[1], all_ids,
                                               node_count, edge_count, edge_probe)
                completed = True
    finally:
        if mcp is not None:
            mcp.close(False)
        after = snapshot(db)
        report["database"]["after"] = after
        report["database"]["unchanged"] = before == after
        require(before == after and not after["sidecars"], "ordinary consumer queries mutated DB or left WAL/SHM")
    require(completed, "consumer acceptance did not complete")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", required=True)
    parser.add_argument("--cairn-bin", required=True)
    parser.add_argument("--mcp-bin", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    report = {"format": "cairn-real-consumer-smoke/v2", "passed": False,
              "trace_scope": "authoritative node_sources -> CLI why -> MCP living UUID",
              "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    try:
        run(args, report)
        report["passed"] = True
    except (OSError, ValueError, sqlite3.Error, subprocess.SubprocessError, http.client.HTTPException,
            KeyError, TypeError) as exc:
        report["error"] = str(exc)[:500]
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"passed": report["passed"], "out": str(out),
                      "coverage": report.get("coverage"), "error": report.get("error")}, ensure_ascii=False))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
