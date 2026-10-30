#!/usr/bin/env python3
"""小样本只编排生产入口与独立验收；不向模型提供 oracle。
Run ordinary production phases and inspect persisted artifacts, never model answers.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

import yaml

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
SOURCES = HERE / "fixtures/sources"


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def read_json(path):
    return json.loads(Path(path).read_text())


def write_json(path, value):
    with Path(path).open("x") as stream:
        os.chmod(path, 0o600)
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write("\n")


def roster(tree):
    return [{"path": str(p.relative_to(tree)), "sha256": digest(p.read_bytes()),
             "bytes": p.stat().st_size} for p in sorted(tree.rglob("*")) if p.is_file()]


def fixture_identity(oracle):
    actual = roster(SOURCES)
    if any(p.is_symlink() for p in SOURCES.rglob("*")) or actual != oracle["sources"]:
        raise ValueError("fixed synthetic source roster differs from independent oracle")
    change = HERE / oracle["change"]["file"]
    if digest(change.read_bytes()) != oracle["change"]["sha256"]:
        raise ValueError("fixed synthetic addition differs from independent oracle")
    return {"sources": actual, "change": oracle["change"],
            "oracle_sha256": digest((HERE / "oracle.json").read_bytes())}


def graph_rows(phase):
    # 验收只读，不跑迁移；书签/运行统计不属于语义比较。
    # Read existing tables only; file-state bookkeeping is not graph semantics.
    uri = (phase / "knowledge.db").resolve().as_uri() + "?mode=ro"
    with sqlite3.connect(uri, uri=True) as conn:
        conn.row_factory = sqlite3.Row
        graph = {table: [dict(row) for row in conn.execute("SELECT * FROM " + table)]
                 for table in ("nodes", "edges", "node_sources")}
    # Go repositories decode nullable TEXT to empty strings; sidecars serialize
    # those DTO values. Preserve the fields, applying that exact readback contract.
    nullable = {"nodes": ("synonyms", "description", "properties", "tags", "related_entities", "source_refs", "file_slug"),
                "edges": ("description", "properties", "source_refs", "cardinality")}
    for table, fields in nullable.items():
        for row in graph[table]:
            for field in fields:
                if field in row and row[field] is None:
                    row[field] = ""
    return graph


def canonical(rows, ignored=()):
    # yaml.v3 may render float64(1) as YAML 1, while SQLite reads REAL 1.0.
    # Normalize equal numeric scalars without dropping confidence or other fields.
    return sorted(json.dumps({k: int(v) if isinstance(v, float) and v.is_integer() else v for k, v in row.items() if k not in ignored},
                             ensure_ascii=False, sort_keys=True, default=str) for row in rows)


def normalized(graph):
    return {"nodes": canonical(graph["nodes"], ("created_at", "updated_at")),
            "edges": canonical(graph["edges"], ("id", "created_at")),
            "node_sources": canonical(graph["node_sources"])}


def file_hashes(phase, include_db=False):
    result = {str(p.relative_to(phase)): digest(p.read_bytes())
              for p in sorted((phase / "source").rglob("*")) if p.is_file()}
    if include_db:
        for p in sorted(phase.glob("knowledge.db*")):
            if p.is_file():
                result[p.name] = digest(p.read_bytes())
    return result


def trace_rows(phase):
    path = phase / "requests.jsonl"
    if not path.exists():
        raise ValueError("provider trace unavailable; zero calls cannot be inferred")
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def resolve_nodes(graph, oracle):
    selected = {}
    for expected in oracle["nodes"]:
        names = {expected["name"], *expected["aliases"]}
        matches = [n for n in graph["nodes"] if n["name"] in names and
                   n["label"] in ("Entity", "Concept")]
        if len(matches) != 1:
            raise ValueError("oracle_unrecognized: missing or ambiguous semantic node: " + expected["name"])
        selected[expected["name"]] = matches[0]
    return selected


def validate_frozen(frozen, oracle, changed):
    expected = {s["path"]: (SOURCES / s["path"]).read_bytes() for s in oracle["sources"]}
    if changed:
        expected[oracle["change"]["path"]] += b"\n\n" + (HERE / oracle["change"]["file"]).read_bytes()
    require(len(frozen) == len(expected) and {s["path"] for s in frozen} == set(expected), "frozen source denominator differs from fixture")
    for source in frozen:
        require(source["content"].encode() == expected[source["path"]] and source["sha256"] == digest(expected[source["path"]]), "frozen source differs from fixed synthetic bytes")


def validate_sources(graph, selected, oracle, frozen):
    by_path = {s["path"]: s for s in frozen}
    if len(by_path) != len(frozen):
        raise ValueError("duplicate frozen source path")
    for source in frozen:
        if digest(source["content"].encode()) != source["sha256"]:
            raise ValueError("frozen source hash mismatch")
    for expected in oracle["nodes"]:
        node = selected[expected["name"]]
        rows = [s for s in graph["node_sources"] if s["node_uuid"] == node["id"]]
        if not set(expected["sources"]) <= {s["file_path"] for s in rows}:
            raise ValueError("missing source incidence: " + expected["name"])
        for row in rows:
            source = by_path.get(row["file_path"])
            if source is None or row["skill"] != source["skill"] or not row["member_id"]:
                raise ValueError("node source ownership/path mismatch")
            start, end = row["start_line"] or 0, row["end_line"] or 0
            if (start or end) and not (1 <= start <= end <= len(source["content"].splitlines())):
                raise ValueError("node source span outside original document")


def validate_relation(graph, selected, oracle, frozen):
    expected = oracle["relation"]
    source, target = (selected[expected[k]]["id"] for k in ("source", "target"))
    relevant = [e for e in graph["edges"] if e["kind"] == expected["kind"]]
    if any(e["source_id"] == target and e["target_id"] == source for e in relevant):
        raise ValueError("required relation reversed")
    matches = [e for e in relevant if e["source_id"] == source and e["target_id"] == target]
    if len(matches) != 1 or matches[0]["bidirectional"] != 0:
        raise ValueError("required directed relation missing/duplicated")
    assertions = json.loads(matches[0]["description"]).get("cairn_relation_assertions_v1", [])
    frozen_by_path = {s["path"]: s for s in frozen}
    valid = False
    relation_quote = False
    for assertion in assertions:
        if assertion.get("classification") != "explicit" or not assertion.get("description"):
            continue
        sources = {s["id"]: s for s in assertion.get("sources", [])}
        roles = set()
        for evidence in assertion.get("evidence", []):
            source_ref = sources.get(evidence["source_id"], {})
            original = frozen_by_path.get(source_ref.get("path"))
            if not original or source_ref.get("sha256") != original["sha256"] or source_ref.get("skill") != original["skill"]:
                raise ValueError("relation source hash/path mismatch")
            raw = original["content"].encode()
            start, end = evidence["start_byte"], evidence["end_byte"]
            quote = evidence["quote"]
            if not quote or start < 0 or end <= start or raw[start:end] != quote.encode():
                raise ValueError("relation quote is not exact original byte span")
            relation_quote |= any(fragment in quote for fragment in oracle["relation"]["source_quote_fragments"])
            if (evidence["start_line"] != raw[:start].count(b"\n") + 1 or
                    evidence["end_line"] != raw[:end - 1].count(b"\n") + 1):
                raise ValueError("relation quote line coordinates mismatch")
            role = evidence["endpoint"]
            owner = {"source": source, "target": target}.get(role)
            if not any(row["node_uuid"] == owner and row["member_id"] == evidence["member_id"] and
                       row["file_path"] == original["path"] for row in graph["node_sources"]):
                raise ValueError("relation evidence member is not owned by endpoint")
            roles.add(role)
        valid |= roles == {"source", "target"}
    if not valid or not relation_quote:
        raise ValueError("required relation lacks explicit source-bound endpoint evidence")


def values_ok(text, patterns):
    return all(re.search(pattern, finite_numbers(text), re.S) for pattern in patterns)


def finite_numbers(text):
    # 此 fixture 只含 1/2 月及 4/6/9 数值；不做通用自然语言判定。
    # Accept the finite equivalent Chinese numerals without a generic NLP parser.
    for chinese, arabic in (("一", "1"), ("二", "2"), ("四", "4"), ("六", "6"), ("九", "9")):
        text = re.sub(r"(?<![零〇一二三四五六七八九十百千万两\d])" + chinese + r"(?=\s*(?:月|分钟|只))", arabic, text)
    return text


def version_values(text, changed):
    pairs = set()
    years = re.findall(r"(?<!\d)(\d{4})\s*年", text)
    require(all(year == "2026" for year in years), "incorrect explicit version year")
    require("2026" in years, "oracle_expression_unresolved: version year")
    for clause in re.split(r"[。；;，,\n]", finite_numbers(text)):
        versions = list(re.finditer(r"(?<!\d)(?:2026\s*年\s*)?0?([12])\s*月", clause))
        if len(versions) > 1:
            raise ValueError("oracle_expression_unresolved: multiple versions in one clause")
        for duration in re.finditer(r"(?<!\d)(\d+)\s*分钟", clause):
            if not versions:
                continue
            if re.search(r"(?:不是|并非|不为|非)\s*$", clause[max(0, duration.start() - 8):duration.start()]):
                raise ValueError("target duration explicitly negated")
            preceding = [v for v in versions if v.start() < duration.start()]
            version = preceding[-1] if preceding else versions[0]
            pairs.add((int(version[1]), int(duration[1])))
    expected = {(1, 9), (2, 6)} if changed else {(1, 9)}
    require(all((month, value) in expected for month, value in pairs), "incorrect month-duration scope")
    require(expected <= pairs, "oracle_expression_unresolved: month-duration scope")


def validate_values(selected, oracle, changed):
    for expected in oracle["values"]:
        node = selected[expected["name"]]
        text = node["summary"] + "\n" + node["description"]
        patterns = list(expected["patterns"])
        if changed and expected["name"] == oracle["relation"]["target"]:
            patterns += oracle["changed_patterns"]
        if expected["name"] == oracle["relation"]["target"]:
            version_values(text, changed)
            continue
        if not values_ok(text, patterns):
            raise ValueError("oracle_expression_unresolved: target-scoped key value: " + expected["name"])


def validate_writeback(phase, graph, selected):
    found = set()
    primaries, occurrences = {}, {}
    for path in sorted((phase / "source").rglob("*.md.kg.yaml")):
        sidecar = yaml.safe_load(path.read_text())
        md_path = Path(str(path)[:-len(".kg.yaml")])
        raw = md_path.read_bytes()
        if sidecar["doc"] != str(md_path.relative_to(phase / "source")) or sidecar["doc_hash"] != "sha256:" + digest(raw):
            raise ValueError("writeback document identity/hash mismatch")
        text = raw.decode()
        for entry in sidecar.get("nodes", []):
            matches = [n for n in selected.values() if n["id"] == entry["uuid"]]
            if not matches:
                continue
            node = matches[0]
            graph_meta = entry.get("graph_metadata", {})
            expected_sources = [{**s, "coordinate_space": "source_document"} for s in graph["node_sources"] if s["node_uuid"] == node["id"]]
            for row in expected_sources:
                del row["node_uuid"]
                row["start_line"], row["end_line"] = row["start_line"] or 0, row["end_line"] or 0
            if not graph_meta.get("sources_known") or canonical(graph_meta.get("sources", [])) != canonical(expected_sources):
                raise ValueError("sidecar source rows differ from persisted UUID")
            incident = [e for e in graph["edges"] if node["id"] in (e["source_id"], e["target_id"])]
            if not graph_meta.get("relations_known") or canonical(graph_meta.get("relations", []), ("created_at",)) != canonical(incident, ("created_at",)):
                raise ValueError("sidecar incident relations differ from persisted UUID")
            if entry["name"] != node["name"]:
                raise ValueError("sidecar name differs from persisted UUID")
            block = re.search(r"<!-- kg:uuid=" + re.escape(node["id"]) + r" ([^\n]*)-->\n(.*?)<!-- /kg:uuid=" + re.escape(node["id"]) + r" -->", text, re.S)
            if not block:
                raise ValueError("generated UUID block missing")
            rel = str(md_path.relative_to(phase / "source"))
            occurrences[(rel, node["id"])] = (entry, block[1])
            if "mirror=true" in block[1]:
                continue
            if entry["summary_hash"] != "sha256:" + digest(node["summary"].encode()) or entry["description_hash"] != "sha256:" + digest(node["description"].encode()):
                raise ValueError("sidecar editable hash differs from persisted node")
            expected = "**摘要**（可编辑）\n" + node["summary"] + "\n\n**详述**（可编辑）\n" + node["description"] + "\n"
            if expected not in block[2]:
                raise ValueError("generated primary body differs from persisted UUID")
            found.add(node["id"])
            primaries[node["id"]] = rel
    if found != {n["id"] for n in selected.values()}:
        raise ValueError("one or more target nodes has no verified primary writeback")
    for row in graph["node_sources"]:
        if row["node_uuid"] not in found:
            continue
        occurrence = occurrences.get((row["file_path"], row["node_uuid"]))
        if not occurrence or row["member_id"] not in occurrence[0].get("members", []):
            raise ValueError("source document pair/UUID/member writeback missing")
        if "mirror=true" in occurrence[1]:
            match = re.search(r"(?:^| )source=([^ ]+)", occurrence[1])
            if not match or match[1] != primaries[row["node_uuid"]]:
                raise ValueError("source mirror does not point to verified UUID primary")


def audit_phase(phase, oracle, changed=False, previous=None, unchanged=False):
    checks = []
    def check(name, fn):
        try:
            fn()
            checks.append({"check": name, "status": "PASS"})
        except (ValueError, KeyError, TypeError, OSError, sqlite3.Error) as exc:
            checks.append({"check": name, "status": "UNKNOWN" if str(exc).startswith("oracle_") else "FAIL", "reason": str(exc)})
    graph = graph_rows(phase)
    frozen = read_json(phase / "caller-frozen-sources.json")
    selected = resolve_nodes(graph, oracle)
    def completion():
        finish = read_json(phase / "phase-finish.json")
        if finish.get("status") != "complete" or finish.get("aggregate_status") != "complete" or finish.get("error") or finish.get("snapshot_error"):
            raise ValueError("production phase is not complete")
    check("production-completion", completion)
    def stages():
        report = read_json(phase / "build-report.json")
        if unchanged:
            return
        documents = report.get("Documents") or []
        require(documents and all(d["outcome"] == "complete" for d in documents), "annotation stage missing/incomplete")
        if not changed:
            require({d["file_path"] for d in documents} == {s["path"] for s in oracle["sources"]}, "annotation source denominator differs")
            require(report.get("Coverage") and report["Coverage"]["Outcome"] == "complete", "mapping stage missing/incomplete")
            outcomes = (report.get("Describe") or {}).get("Outcomes") or []
            require(report.get("Ingest") and report.get("Writeback") and report.get("FinalCoverage") and report["FinalCoverage"]["outcome"] == "complete", "materialization/writeback/final coverage stage missing/incomplete")
        else:
            require(report.get("C4Segments", 0) > 0, "declared C4 addition was not processed")
            outcomes = report.get("NodeOutcomes") or []
        require(outcomes and all(o["outcome"] == "complete" for o in outcomes), "node fusion stage missing/incomplete")
        scopes = (report.get("Relations") or {}).get("scopes") or []
        require(scopes and all(s["outcome"] == "complete" for s in scopes), "relation stage missing/incomplete")
    check("production-stages", stages)
    check("fixed-frozen-inputs", lambda: validate_frozen(frozen, oracle, changed))
    check("source-incidences", lambda: validate_sources(graph, selected, oracle, frozen))
    check("target-key-values", lambda: validate_values(selected, oracle, changed))
    check("explicit-directed-relation", lambda: validate_relation(graph, selected, oracle, frozen))
    check("sqlite-markdown-sidecar", lambda: validate_writeback(phase, graph, selected))
    if previous:
        prior = resolve_nodes(graph_rows(previous), oracle)
        check("stable-target-uuid", lambda: require({k: n["id"] for k, n in prior.items()} == {k: n["id"] for k, n in selected.items()}, "target UUID changed"))
        if changed and not unchanged:
            check("unrelated-node-preserved", lambda: require(prior["檐铃清点"] == selected["檐铃清点"], "C4 modified unrelated semantic node"))
    if unchanged:
        check("unchanged-zero-model-requests", lambda: require(not any(row["event"] == "request" for row in trace_rows(phase)), "unchanged invoked model"))
        check("unchanged-zero-doc-changes", lambda: require(read_json(phase / "build-report.json")["DocsChanged"] == 0, "unchanged reports changed documents"))
        check("unchanged-graph", lambda: require(normalized(graph) == normalized(graph_rows(previous)), "unchanged semantic graph differs"))
        check("unchanged-files", lambda: require(file_hashes(phase) == file_hashes(previous), "unchanged source/writeback files differ"))
    return checks, selected


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def query_checks(phase, bins, selected):
    before = file_hashes(phase, True)
    env = {k: v for k, v in os.environ.items() if not ("API_KEY" in k or k.endswith("TOKEN"))}
    db = str(phase / "knowledge.db")
    outputs = []
    graph = graph_rows(phase)
    for name, node in selected.items():
        for args in (["find", name], ["why", node["id"]]):
            result = subprocess.run([str(bins / "cairn"), "--db-path", db, *args], env=env, text=True, capture_output=True)
            validate_cli(result.returncode, result.stdout, args[0], node, graph)
            outputs.append({"args": args, "stdout": result.stdout})
    config = phase / "mcp-query.yaml"
    config.write_text(yaml.safe_dump({"mcp": {"knowledge_bases": [{"name": "mini", "path": db}]}}))
    requests = [{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2024-11-05"}},
                {"jsonrpc": "2.0", "method": "notifications/initialized"}]
    for i, node in enumerate(selected.values(), 2):
        requests.append({"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": {"name": "resolve_node", "arguments": {"uuid": node["id"], "kg": "mini", "include_sources": True}}})
    result = subprocess.run([str(bins / "cairn-mcp"), "--config", str(config)], input="\n".join(json.dumps(x) for x in requests) + "\n", capture_output=True, text=True, env=env)
    require(result.returncode == 0, "MCP stdio process failed")
    responses = {r["id"]: r for r in map(json.loads, result.stdout.splitlines())}
    for i, node in enumerate(selected.values(), 2):
        response = responses[i]
        require(not response.get("error") and not response["result"].get("isError"), "MCP returned an error")
        content = response["result"]["content"]
        validate_mcp(content, node, graph)
    write_json(phase / "query-results.json", {"mode": "real-cli-and-mcp-no-api-credential", "cli": outputs, "mcp": responses})
    require(before == file_hashes(phase, True), "query mutated database or source/writeback files")
    return {"check": "real-cli-mcp-readonly", "status": "PASS"}


def validate_cli(code, text, action, node, graph):
    require(code == 0, "CLI query process failed")
    if action == "find":
        blocks = re.split(r"━━━ 匹配 #", text)
        require(any(re.search(r"(?m)^\s*ID:\s*" + re.escape(node["id"]) + r"\s*$", block) and
                    "名称:       " + node["name"] in block and "摘要:       " + node["summary"] in block for block in blocks), "CLI returned no expected UUID/name/summary")
    else:
        require("追溯 / Why: " + node["id"] + " ━━━" in text and "节点: " + node["name"] + " [" in text and "摘要: " + node["summary"] in text, "CLI why returned incorrect node")
        for row in graph["node_sources"]:
            if row["node_uuid"] == node["id"]:
                require("[" + row["skill"] + "] " + row["file_path"] in text and "(member " + row["member_id"] + ")" in text, "CLI why omitted expected source/member")


def validate_mcp(content, node, graph):
    require(len(content) == 2, "MCP source response shape differs")
    text = content[0]["text"]
    require("✓ UUID " + node["id"] + " 存活 / alive。" in text and
            "- 名称/Name: " + node["name"] + "\n" in text and
            "- 摘要/Summary: " + node["summary"] + "\n" in text, "MCP returned incorrect node body")
    envelope = json.loads(content[1]["text"])
    metadata = envelope["source_metadata"]
    require(envelope.get("requested_uuid") == node["id"] and envelope.get("scope") == "resolved_node_only" and metadata["node_uuid"] == node["id"] and metadata["status"] == "available", "MCP resolved wrong UUID/source")
    fields = ("member_id", "skill", "file_path", "start_line", "end_line")
    expected = [{k: (row[k] or 0) if k.endswith("line") else row[k] for k in fields} for row in graph["node_sources"] if row["node_uuid"] == node["id"]]
    actual = [{k: row.get(k, 0 if k.endswith("line") else "") for k in fields} for row in metadata.get("locations", [])]
    require(expected and canonical(expected) == canonical(actual), "MCP source locations differ from persisted UUID")


def negative_controls(phase, oracle):
    # 用实际落库正例离线破坏，不重发模型。Unit fixtures remain local controls.
    # Mutate copies of the actual persisted result; never replay provider calls.
    positive, _ = audit_phase(phase, oracle)
    require(all(c["status"] == "PASS" for c in positive), "negative controls require a passing persisted positive")
    results = []
    for control, check_name in (("delete-edge", "explicit-directed-relation"), ("reverse-edge", "explicit-directed-relation"), ("misplaced-value", "target-key-values"), ("bad-source-path", "source-incidences"), ("bad-relation-hash", "explicit-directed-relation"), ("missing-source-pair", "sqlite-markdown-sidecar")):
        with tempfile.TemporaryDirectory(prefix="cairn-mini-control-") as directory:
            copied = Path(directory) / "phase"
            shutil.copytree(phase, copied)
            graph = graph_rows(copied)
            selected = resolve_nodes(graph, oracle)
            a, b = (selected[oracle["relation"][k]]["id"] for k in ("source", "target"))
            with sqlite3.connect(copied / "knowledge.db") as conn:
                if control == "delete-edge":
                    conn.execute("DELETE FROM edges WHERE source_id=? AND target_id=? AND kind='depends_on'", (a, b))
                elif control == "reverse-edge":
                    conn.execute("UPDATE edges SET source_id=?,target_id=? WHERE source_id=? AND target_id=? AND kind='depends_on'", (b, a, a, b))
                elif control == "misplaced-value":
                    conn.execute("UPDATE nodes SET summary='票签',description='票签' WHERE id=?", (b,))
                    conn.execute("UPDATE nodes SET description=description || ' 2026年1月版9分钟' WHERE id=?", (selected["檐铃清点"]["id"],))
                elif control == "bad-source-path":
                    conn.execute("UPDATE node_sources SET file_path='nonexistent.md' WHERE node_uuid=?", (b,))
                elif control == "bad-relation-hash":
                    edge = next(e for e in graph["edges"] if e["source_id"] == a and e["target_id"] == b and e["kind"] == "depends_on")
                    envelope = json.loads(edge["description"])
                    for assertion in envelope["cairn_relation_assertions_v1"]:
                        for source in assertion.get("sources", []):
                            source["sha256"] = "0" * 64
                    conn.execute("UPDATE edges SET description=? WHERE id=?", (json.dumps(envelope, ensure_ascii=False), edge["id"]))
                else:
                    doc = copied / "source" / oracle["change"]["path"]
                    doc.unlink()
                    Path(str(doc) + ".kg.yaml").unlink()
            checks, _ = audit_phase(copied, oracle)
            require(any(c["check"] == check_name and c["status"] != "PASS" for c in checks), "negative control escaped: " + control)
            results.append({"control": control, "status": "PASS", "detected_by": check_name, "provider_calls": 0})
    return results


def command(args, log):
    with log.open("x") as stream:
        os.chmod(log, 0o600)
        return subprocess.run([str(a) for a in args], cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT).returncode


def code_identity(bins):
    facts = {"head": None, "tree": None, "dirty": None,
             "binary_sha256": {p.name: digest(p.read_bytes()) for p in sorted(bins.iterdir())}}
    for key, args in (("head", ["rev-parse", "HEAD"]), ("tree", ["rev-parse", "HEAD^{tree}"]), ("dirty", ["status", "--porcelain"])):
        result = subprocess.run(["git", *args], cwd=ROOT, text=True, capture_output=True)
        if result.returncode == 0:
            facts[key] = bool(result.stdout.strip()) if key == "dirty" else result.stdout.strip()
    return facts


def repeat_observation(current, first, oracle):
    left, right = graph_rows(first), graph_rows(current)
    nodes_left, nodes_right = resolve_nodes(left, oracle), resolve_nodes(right, oracle)
    result = []
    for name in nodes_left:
        a, b = nodes_left[name], nodes_right[name]
        def signature(graph, node):
            return {"uuid": node["id"], "classification": [node.get("domain"), node.get("subdomain")],
                    "source_members": sorted((s["file_path"], s["member_id"]) for s in graph["node_sources"] if s["node_uuid"] == node["id"])}
        before, after = signature(left, a), signature(right, b)
        result.append({"semantic_node": name, "before": before, "after": after, "identity_drift": before != after})
    return result


def run_campaign(args):
    oracle = read_json(HERE / "oracle.json")
    identity = fixture_identity(oracle)
    out = args.out.resolve()
    out.mkdir(mode=0o700)
    write_json(out / "input-manifest.json", identity)
    bins = out / "bin"
    bins.mkdir()
    for name, source in (("capacity", "./build/test/e2e/corpus-capacity"), ("cairn", "./service/cmd/cairn"), ("cairn-mcp", "./service/cmd/cairn-mcp")):
        require(command([args.go, "build", "-buildvcs=false", "-o", bins / name, source], out / ("build-" + name + ".log")) == 0, "entry build failed: " + name)
    plan = (("full", "full", False, False), ("unchanged", "unchanged", False, True), ("changed", "changed", True, False), ("changed-unchanged", "unchanged", True, True), ("repeat", "repeat", False, False))
    receipt = {"format": "cairn-mini-result/v1", "started_at": datetime.now(timezone.utc).isoformat(), "mode": "live-production-provider", "code": code_identity(bins), "input": identity, "phases": [{"phase": p[0], "status": "UNRUN"} for p in plan], "status": "UNRUN", "full_followup": "UNRUN", "semantic_review": "manual review of condition/negation/version scope remains separate"}
    write_json(out / "run-start.json", receipt)
    previous = None
    for phase_index, (label, phase, changed, unchanged) in enumerate(plan):
        current = out / label
        cmd = [bins / "capacity", "--mode", "v016", "--phase", phase, "--source-tree", SOURCES, "--out", current]
        if phase in ("changed", "unchanged"):
            cmd += ["--previous", previous]
        if phase == "changed":
            cmd += ["--change-path", oracle["change"]["path"], "--change-file", HERE / oracle["change"]["file"]]
        code = command(cmd, out / (label + ".log"))
        item = {"phase": label, "exit_code": code, "checks": []}
        try:
            checks, selected = audit_phase(current, oracle, changed, previous if phase in ("changed", "unchanged") else None, unchanged)
            if phase == "repeat":
                item["identity_observation"] = repeat_observation(current, out / "full", oracle)
            item["checks"] = checks
            rows = trace_rows(current)
            item["provider_requests"] = sum(r["event"] == "request" for r in rows)
            item["returned_models"] = sorted({r["returned_model"] for r in rows if r.get("returned_model")})
            if not unchanged:
                require(item["provider_requests"] > 0 and item["returned_models"], "no observed live model response")
            if code == 0 and all(c["status"] == "PASS" for c in checks):
                item["checks"].append(query_checks(current, bins, selected))
                if label == "full":
                    item["negative_controls"] = negative_controls(current, oracle)
        except (ValueError, KeyError, IndexError, TypeError, OSError, sqlite3.Error) as exc:
            item["checks"].append({"check": "artifact-query-validation", "status": "UNKNOWN" if str(exc).startswith("oracle_") else "FAIL", "reason": str(exc)})
        item["status"] = "PASS" if code == 0 and item["checks"] and all(c["status"] == "PASS" for c in item["checks"]) else "FAIL"
        if any(c["status"] == "UNKNOWN" for c in item["checks"]) and not any(c["status"] == "FAIL" for c in item["checks"]):
            item["status"] = "UNKNOWN"
        finish = current / "phase-finish.json"
        if finish.exists() and read_json(finish).get("aggregate_status") == "partial":
            item["status"] = "PARTIAL"
        elif finish.exists() and read_json(finish).get("status") in ("unavailable", "cancelled"):
            item["status"] = "UNKNOWN"
        receipt["phases"][phase_index] = item
        print(label + ": " + item["status"], flush=True)
        if item["status"] != "PASS":
            receipt["status"] = item["status"]
            break
        previous = current
    else:
        receipt["status"] = "PASS"
    if receipt["status"] == "PASS" and args.source_tree:
        # 只有显式大样本路径才跟进；mini 失败绝不启动它。
        # The caller selects a separate full source tree; mini failure stops here.
        code = command([bins / "capacity", "--mode", "v016", "--phase", "full", "--source-tree", args.source_tree.resolve(), "--out", out / "full-followup"], out / "full-followup.log")
        receipt["full_followup"] = {"exit_code": code, "acceptance": "independent full-corpus review required"}
    write_json(out / "result.json", receipt)
    return 0 if receipt["status"] == "PASS" and (not isinstance(receipt["full_followup"], dict) or receipt["full_followup"]["exit_code"] == 0) else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, help="new local result directory (never reuse)")
    parser.add_argument("--go", default="go", help="Go executable")
    parser.add_argument("--source-tree", type=Path, help="optional explicit FULL follow-up sources after mini PASS")
    parser.add_argument("--manifest", action="store_true", help="print fixed public synthetic input identity; no model call")
    args = parser.parse_args()
    if args.manifest:
        print(json.dumps(fixture_identity(read_json(HERE / "oracle.json")), ensure_ascii=False, indent=2))
        return 0
    if not args.out:
        parser.error("--out required")
    try:
        return run_campaign(args)
    except (ValueError, OSError) as exc:
        print("mini baseline unavailable: " + str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
