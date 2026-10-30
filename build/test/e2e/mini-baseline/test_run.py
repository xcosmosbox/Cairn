"""免费验收器控制；手造数据不冒充真实模型端到端证据。 / Offline checker controls."""
import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import yaml

spec = importlib.util.spec_from_file_location("mini_run", Path(__file__).with_name("run.py"))
mini = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mini)


def persisted_control(root, version_layout=False):
    """Minimal local artifact control, not a provider response or production run."""
    phase = root / "phase"
    phase.mkdir()
    oracle = mini.read_json(mini.HERE / "oracle.json")
    originals = [{"path": s["path"], "skill": "paperhall-mini", "content": (mini.SOURCES / s["path"]).read_text(), "sha256": s["sha256"]} for s in oracle["sources"]]
    graph = {"nodes": [], "edges": [], "node_sources": []}
    for i, expected in enumerate(oracle["nodes"]):
        graph["nodes"].append({"id": str(i), "name": expected["name"], "label": "Concept", "summary": expected["name"], "description": {"夜场入厅": "只有票签有效时入厅，过期不接受。", "霜羽票签": "2026年1月版有效期9分钟。", "檐铃清点": "清点4只纸鹤。"}[expected["name"]]})
        for j, path in enumerate(expected["sources"]):
            graph["node_sources"].append({"node_uuid": str(i), "member_id": f"member-{i}-{j}", "skill": "paperhall-mini", "file_path": path, "start_line": 0, "end_line": 0})
    original = next(s for s in originals if s["path"] == "mini/SKILL.md")
    quote = "夜场入厅依赖霜羽票签。"
    start = original["content"].encode().index(quote.encode())
    source = {"id": "s1", "path": original["path"], "skill": original["skill"], "sha256": original["sha256"]}
    evidence = [{"endpoint": role, "source_id": "s1", "member_id": f"member-{i}-0", "quote": quote, "start_byte": start, "end_byte": start + len(quote.encode()), "start_line": 6, "end_line": 6} for role, i in (("source", 0), ("target", 1))]
    graph["edges"].append({"id": 1, "source_id": "0", "target_id": "1", "kind": "depends_on", "bidirectional": 0, "properties": None, "source_refs": None, "cardinality": None, "description": json.dumps({"cairn_relation_assertions_v1": [{"classification": "explicit", "description": "有效票签是入厅前提", "sources": [source], "evidence": evidence}]}, ensure_ascii=False)})
    if version_layout:
        graph["nodes"][1]["description"] = "纸岚展馆夜场使用的票签。"
        graph["nodes"].append({"id": "3", "name": "2026年1月版霜羽票签", "label": "Entity", "summary": "2026年1月版霜羽票签", "description": "2026年1月版有效期为9分钟。"})
        graph["node_sources"].append({"node_uuid": "3", "member_id": "member-version", "skill": "paperhall-mini", "file_path": "mini/references/pass.md", "start_line": 0, "end_line": 0})
        original = next(s for s in originals if s["path"] == "mini/references/pass.md")
        raw = original["content"].encode()
        sentences = original["content"].splitlines()[1].split("。")
        evidence = []
        for role, member, quote in (("source", "member-1-1", sentences[0] + "。"), ("target", "member-version", sentences[1] + "。")):
            start = raw.index(quote.encode()); end = start + len(quote.encode())
            evidence.append({"endpoint": role, "member_id": member, "source_id": "s3", "quote": quote, "start_byte": start, "end_byte": end, "start_line": raw[:start].count(b"\n") + 1, "end_line": raw[:end - 1].count(b"\n") + 1})
        source = {"id": "s3", "path": original["path"], "skill": original["skill"], "sha256": original["sha256"]}
        edge = dict(graph["edges"][0], id=2, source_id="1", target_id="3", kind="generalizes")
        edge["description"] = json.dumps({"cairn_relation_assertions_v1": [{"classification": "explicit", "description": "该票签的2026年1月版本", "sources": [source], "evidence": evidence}]}, ensure_ascii=False)
        graph["edges"].append(edge)
    with sqlite3.connect(phase / "knowledge.db") as conn:
        for table, rows in graph.items():
            conn.execute("CREATE TABLE " + table + " (" + ",".join(k + (" INTEGER" if isinstance(v, int) else " TEXT") for k, v in rows[0].items()) + ")")
            for row in rows:
                conn.execute("INSERT INTO " + table + " VALUES (" + ",".join("?" for _ in row) + ")", list(row.values()))
    mini.write_json(phase / "caller-frozen-sources.json", originals)
    mini.write_json(phase / "phase-finish.json", {"status": "complete", "aggregate_status": "complete"})
    mini.write_json(phase / "build-report.json", {"DocsChanged": 0, "Documents": [{"file_path": s["path"], "outcome": "complete"} for s in oracle["sources"]], "Coverage": {"Outcome": "complete"}, "Describe": {"Outcomes": [{"outcome": "complete"}]}, "Ingest": {"control": True}, "Writeback": {"control": True}, "FinalCoverage": {"outcome": "complete"}, "Relations": {"scopes": [{"outcome": "complete"}]}})
    (phase / "requests.jsonl").write_text("")
    for node in graph["nodes"]:
        rel = "_shared/mini/" + node["id"] + ".md"
        path = phase / "source" / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        text = f'<!-- kg:uuid={node["id"]} tag=concept shared=true readonly-meta -->\n## {node["name"]}\n**摘要**（可编辑）\n{node["summary"]}\n\n**详述**（可编辑）\n{node["description"]}\n<!-- /kg:uuid={node["id"]} -->\n'
        path.write_text(text)
        sources = [{k: v for k, v in s.items() if k != "node_uuid"} | {"coordinate_space": "source_document"} for s in graph["node_sources"] if s["node_uuid"] == node["id"]]
        edges = [{k: "" if v is None else v for k, v in e.items()} for e in graph["edges"] if node["id"] in (e["source_id"], e["target_id"])]
        entry = {"uuid": node["id"], "name": node["name"], "members": [s["member_id"] for s in sources], "summary_hash": "sha256:" + mini.digest(node["summary"].encode()), "description_hash": "sha256:" + mini.digest(node["description"].encode()), "graph_metadata": {"sources_known": True, "sources": sources, "relations_known": True, "relations": edges}}
        Path(str(path) + ".kg.yaml").write_text(yaml.safe_dump({"doc": rel, "doc_hash": "sha256:" + mini.digest(path.read_bytes()), "nodes": [entry]}, allow_unicode=True))
    for original in originals:
        path = phase / "source" / original["path"]
        path.parent.mkdir(parents=True, exist_ok=True)
        text, entries = "", []
        for node in graph["nodes"]:
            if not any(s["node_uuid"] == node["id"] and s["file_path"] == original["path"] for s in graph["node_sources"]):
                continue
            primary = f'_shared/mini/{node["id"]}.md'
            entry = yaml.safe_load((phase / "source" / (primary + ".kg.yaml")).read_text())["nodes"][0]
            entries.append(entry)
            text += f'<!-- kg:uuid={node["id"]} shared=true mirror=true source={primary} -->\n## {node["name"]}\n<!-- /kg:uuid={node["id"]} -->\n'
        path.write_text(text)
        Path(str(path) + ".kg.yaml").write_text(yaml.safe_dump({"doc": original["path"], "doc_hash": "sha256:" + mini.digest(path.read_bytes()), "nodes": entries}, allow_unicode=True))
    return phase, oracle


class CheckerControls(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.phase, self.oracle = persisted_control(self.root)

    def checks(self, **kwargs):
        return mini.audit_phase(self.phase, self.oracle, **kwargs)[0]

    def test_local_positive_and_all_negative_controls(self):
        self.assertTrue(all(c["status"] == "PASS" for c in self.checks()))
        self.assertEqual(len(mini.negative_controls(self.phase, self.oracle)), 6)

    def test_missing_source_mirror_fails_even_with_valid_primary(self):
        path = self.phase / "source/mini/references/pass.md"
        path.write_text("")
        pair = Path(str(path) + ".kg.yaml")
        sidecar = yaml.safe_load(pair.read_text())
        sidecar["nodes"] = []
        sidecar["doc_hash"] = "sha256:" + mini.digest(b"")
        pair.write_text(yaml.safe_dump(sidecar))
        checks = self.checks()
        self.assertTrue(any(c["check"] == "sqlite-markdown-sidecar" and c["status"] == "FAIL" for c in checks))

    def test_return_code_zero_cannot_hide_partial(self):
        (self.phase / "phase-finish.json").write_text(json.dumps({"status": "partial", "aggregate_status": "partial"}))
        self.assertEqual(self.checks()[0]["status"], "FAIL")

    def test_names_are_not_global_text_search(self):
        with sqlite3.connect(self.phase / "knowledge.db") as conn:
            conn.execute("UPDATE nodes SET name='unrelated' WHERE id='1'")
        with self.assertRaisesRegex(ValueError, "missing or ambiguous"):
            self.checks()

    def test_source_quote_offsets_are_byte_based(self):
        graph = mini.graph_rows(self.phase)
        envelope = json.loads(graph["edges"][0]["description"])
        envelope["cairn_relation_assertions_v1"][0]["evidence"][0]["start_byte"] += 1
        graph["edges"][0]["description"] = json.dumps(envelope)
        with self.assertRaisesRegex(ValueError, "byte span"):
            mini.validate_relation(graph, mini.resolve_nodes(graph, self.oracle), self.oracle, mini.read_json(self.phase / "caller-frozen-sources.json"))

    def test_fake_zero_call_file_is_not_live_credit(self):
        calls = []
        def fake_command(cmd, log):
            calls.append([str(x) for x in cmd])
            if "--mode" in cmd:
                import shutil
                shutil.copytree(self.phase, self.root / "attempt/full")
            return 0
        args = SimpleNamespace(out=self.root / "attempt", go="go", source_tree=self.root / "full-corpus")
        with patch.object(mini, "command", side_effect=fake_command):
            self.assertEqual(mini.run_campaign(args), 1)
        receipt = mini.read_json(args.out / "result.json")
        self.assertEqual(receipt["status"], "FAIL")
        self.assertEqual(receipt["full_followup"], "UNRUN")
        self.assertFalse(any("full-followup" in " ".join(cmd) for cmd in calls))

    def test_cli_exit_zero_with_no_hit_fails(self):
        selected = mini.resolve_nodes(mini.graph_rows(self.phase), self.oracle)
        no_hit = SimpleNamespace(returncode=0, stdout="No matching entities found", stderr="")
        with patch.object(mini.subprocess, "run", return_value=no_hit):
            with self.assertRaisesRegex(ValueError, "no expected UUID"):
                mini.query_checks(self.phase, self.root, selected)

    def test_fixture_identity_excludes_oracle_and_change(self):
        identity = mini.fixture_identity(self.oracle)
        self.assertEqual(len(identity["sources"]), 3)
        self.assertTrue(all(s["path"].endswith(".md") for s in identity["sources"]))

    def test_finite_numerals_and_month_duration_scope(self):
        selected = mini.resolve_nodes(mini.graph_rows(self.phase), self.oracle)
        target = selected["霜羽票签"]
        for text in ("2026年1月版有效期9分钟。", "2026年一月版有效期九分钟。"):
            target["description"] = text
            mini.validate_values(selected, self.oracle, False)
        for text in ("2026年1月版有效期19分钟。", "2026年1月版有效期十九分钟。", "2026年11月版有效期9分钟。", "2025年1月版有效期9分钟。", "2026年1月版有效期不是9分钟。"):
            target["description"] = text
            with self.assertRaises(ValueError):
                mini.validate_values(selected, self.oracle, False)
        target["description"] = "2026年1月版有效期九分钟；2026年2月版有效期六分钟。"
        mini.validate_values(selected, self.oracle, True)
        target["description"] = "2026年1月版有效期6分钟；2026年2月版有效期9分钟。"
        with self.assertRaisesRegex(ValueError, "scope"):
            mini.validate_values(selected, self.oracle, True)
        for text in ("2026年1月版有效期9分钟，2月版有效期6分钟。", "2026年2月版将有效期改为6分钟，9分钟仅适用于2026年1月版。"):
            target["description"] = text
            mini.validate_values(selected, self.oracle, True)
        for text, changed in (("2026-01版有效期9分钟。", False), ("2026年1月版和2026年2月版分别为9分钟和6分钟。", True)):
            target["description"] = text
            with self.assertRaisesRegex(ValueError, "oracle_expression_unresolved"):
                mini.validate_values(selected, self.oracle, changed)

    def test_exact_name_only_quote_cannot_support_dependency(self):
        graph = mini.graph_rows(self.phase)
        frozen = mini.read_json(self.phase / "caller-frozen-sources.json")
        source = next(s for s in frozen if s["path"] == "mini/SKILL.md")
        quote = "纸岚展馆"
        raw = source["content"].encode()
        start = raw.index(quote.encode())
        envelope = json.loads(graph["edges"][0]["description"])
        for evidence in envelope["cairn_relation_assertions_v1"][0]["evidence"]:
            evidence.update(quote=quote, start_byte=start, end_byte=start + len(quote.encode()), start_line=raw[:start].count(b"\n") + 1, end_line=raw[:start].count(b"\n") + 1)
        graph["edges"][0]["description"] = json.dumps(envelope)
        with self.assertRaisesRegex(ValueError, "source-bound"):
            mini.validate_relation(graph, mini.resolve_nodes(graph, self.oracle), self.oracle, frozen)

    def test_nullable_sqlite_text_matches_go_sidecar_readback(self):
        with sqlite3.connect(self.phase / "knowledge.db") as conn:
            self.assertIsNone(conn.execute("SELECT properties FROM edges").fetchone()[0])
        self.assertEqual(mini.graph_rows(self.phase)["edges"][0]["properties"], "")
        self.assertTrue(all(c["status"] == "PASS" for c in self.checks()))
        self.assertEqual(mini.canonical([{"confidence": 1.0}]), mini.canonical([{"confidence": 1}]))
        self.assertNotEqual(mini.canonical([{"confidence": 0.9}]), mini.canonical([{"confidence": 1}]))

    def test_skipped_stage_cannot_hide_behind_complete_finish(self):
        report = mini.read_json(self.phase / "build-report.json")
        report["Describe"] = None
        (self.phase / "build-report.json").write_text(json.dumps(report))
        self.assertTrue(any(c["check"] == "production-stages" and c["status"] == "FAIL" for c in self.checks()))

    def test_readonly_wal_auxiliary_files_do_not_hide_real_mutation(self):
        # Real read-only SQLite consumers create SHM and an empty WAL. Neither is
        # a KG write, while nonempty WAL, main DB and writeback changes still fail.
        before = mini.file_hashes(self.phase, True)
        (self.phase / "knowledge.db-shm").write_bytes(b"reader lock and index")
        (self.phase / "knowledge.db-wal").write_bytes(b"")
        self.assertEqual(before, mini.file_hashes(self.phase, True))
        (self.phase / "knowledge.db-wal").write_bytes(b"nonempty transaction frame")
        self.assertNotEqual(before, mini.file_hashes(self.phase, True))
        (self.phase / "knowledge.db-wal").unlink()
        source = next((self.phase / "source").rglob("*.md"))
        original = source.read_bytes()
        source.write_bytes(original + b"unexpected edit")
        self.assertNotEqual(before, mini.file_hashes(self.phase, True))
        source.write_bytes(original)
        with (self.phase / "knowledge.db").open("ab") as stream:
            stream.write(b"unexpected main database write")
        self.assertNotEqual(before, mini.file_hashes(self.phase, True))

    def version_control(self):
        root = self.root / "version-layout"
        root.mkdir()
        return persisted_control(root, version_layout=True)

    def test_bound_version_layout_and_five_negative_controls(self):
        phase, oracle = self.version_control()
        self.assertTrue(all(c["status"] == "PASS" for c in mini.audit_phase(phase, oracle)[0]))
        import shutil
        for control in ("missing-edge", "reversed-edge", "wrong-hash", "wrong-version", "wrong-value"):
            copied = self.root / control
            shutil.copytree(phase, copied)
            with sqlite3.connect(copied / "knowledge.db") as conn:
                if control == "missing-edge": conn.execute("DELETE FROM edges WHERE id=2")
                elif control == "reversed-edge": conn.execute("UPDATE edges SET source_id='3',target_id='1' WHERE id=2")
                elif control == "wrong-version": conn.execute("UPDATE nodes SET name='2026年2月版霜羽票签' WHERE id='3'")
                elif control == "wrong-value": conn.execute("UPDATE nodes SET description='2026年1月版有效期19分钟。' WHERE id='3'")
                else:
                    envelope = json.loads(conn.execute("SELECT description FROM edges WHERE id=2").fetchone()[0])
                    envelope["cairn_relation_assertions_v1"][0]["sources"][0]["sha256"] = "0" * 64
                    conn.execute("UPDATE edges SET description=? WHERE id=2", (json.dumps(envelope),))
            checks = mini.audit_phase(copied, oracle)[0]
            self.assertTrue(any(c["check"] == "target-key-values" and c["status"] != "PASS" for c in checks), control)

    def test_correct_child_cannot_rescue_wrong_parent(self):
        phase, oracle = self.version_control()
        for text in ("2026年1月版有效期19分钟。", "2025年1月版有效期9分钟。", "2026年1月版有效期不是9分钟。", "有效期十九分钟。"):
            with sqlite3.connect(phase / "knowledge.db") as conn:
                conn.execute("UPDATE nodes SET description=? WHERE id='1'", (text,))
            checks = mini.audit_phase(phase, oracle)[0]
            self.assertTrue(any(c["check"] == "target-key-values" and c["status"] != "PASS" for c in checks), text)

    def test_ambiguous_bound_version_is_unknown(self):
        phase, oracle = self.version_control()
        with sqlite3.connect(phase / "knowledge.db") as conn:
            conn.row_factory = sqlite3.Row
            for table, old_id, new_id in (("nodes", "3", "4"), ("edges", 2, 3)):
                row = dict(conn.execute("SELECT * FROM " + table + " WHERE id=?", (old_id,)).fetchone())
                row["id"] = new_id
                if table == "edges": row["target_id"] = "4"
                conn.execute("INSERT INTO " + table + " VALUES (" + ",".join("?" for _ in row) + ")", list(row.values()))
        checks = mini.audit_phase(phase, oracle)[0]
        self.assertTrue(any(c["check"] == "target-key-values" and c["status"] == "UNKNOWN" for c in checks))

    def test_mcp_wrong_body_or_source_is_not_a_hit(self):
        graph = mini.graph_rows(self.phase)
        node = mini.resolve_nodes(graph, self.oracle)["霜羽票签"]
        locations = [{k: row[k] for k in ("member_id", "skill", "file_path", "start_line", "end_line")} for row in graph["node_sources"] if row["node_uuid"] == node["id"]]
        body = f'✓ UUID {node["id"]} 存活 / alive。\n- 名称/Name: {node["name"]}\n- 摘要/Summary: {node["summary"]}\n'
        envelope = {"requested_uuid": node["id"], "scope": "resolved_node_only", "source_metadata": {"node_uuid": node["id"], "status": "available", "locations": locations}}
        content = [{"text": body}, {"text": json.dumps(envelope)}]
        mini.validate_mcp(content, node, graph)
        with self.assertRaisesRegex(ValueError, "body"):
            mini.validate_mcp([{"text": "incorrect body"}, content[1]], node, graph)
        envelope["source_metadata"]["locations"][0]["file_path"] = "unrelated.md"
        with self.assertRaisesRegex(ValueError, "locations"):
            mini.validate_mcp([content[0], {"text": json.dumps(envelope)}], node, graph)


if __name__ == "__main__":
    unittest.main()
