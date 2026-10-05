#!/usr/bin/env python3
"""离线反例覆盖真实 SQLite/原文边界；synthetic sources only, no provider."""
import copy
import hashlib
import io
import json
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from agent_eval import EvalConfig
from retrieval import (RetrievalEngine, build_raw_index, evidence_budget,
                       json_text, normalized_block, sha256_file)
from retrieval_round2 import replay_trace
from source_backfill_round3 import (RawCorpus, adapter_main, backfill_response, check_bindings,
                                   checked_path, database_identity, literal_terms,
                                   make_packets, validate_text_fact)


def node(identity="node-1", paths=("a.md",), text="node summary", score=0.8):
    # Go adapter schema, not already normalized solver evidence.
    return {"id": identity, "name": "Node", "body": text, "label": "Concept",
            "provenance": "extraction", "score": score, "bm25_rank": -0.2,
            "source_refs": [], "domain": "synthetic", "subdomain": "test", "confidence": 0.9,
            "sources": [{"path": path, "start_line": 0, "end_line": 0} for path in paths],
            "edges": [{"type": "depends_on", "target": "other"}]}


class BackfillTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.corpus = self.root / "corpus"
        self.corpus.mkdir()

    def fixture(self, texts=None, chunk_chars=100, overlap_chars=0):
        texts = texts or {"a.md": '# A\r\nalpha "quoted" \\ 中文🙂\r\n' + "x" * 150 + "\nbeta tail\n",
                          "b.md": "alpha beta alpha\n"}
        entries = []
        for path, text in texts.items():
            target = self.corpus / path
            target.parent.mkdir(parents=True, exist_ok=True)
            data = text.encode()
            target.write_bytes(data)
            entries.append({"path": path, "source_sha256": hashlib.sha256(data).hexdigest()})
        proof = self.root / "proof.json"
        proof.write_text(json_text({"accepted_sources": entries}))
        self.db = self.root / "raw.db"
        build_raw_index(self.corpus, proof, self.db, chunk_chars, overlap_chars)
        self.index = RawCorpus(self.db, self.corpus, sha256_file(self.db))
        return self.index

    def reload(self):
        return RawCorpus(self.db, self.corpus, sha256_file(self.db))

    def rewrite_metadata(self, modify):
        with sqlite3.connect(self.db) as conn:
            value = json.loads(conn.execute("SELECT value FROM metadata WHERE key='manifest'").fetchone()[0])
            modify(value)
            conn.execute("UPDATE metadata SET value=? WHERE key='manifest'", (json_text(value),))

    def test_valid_id_text_offsets_and_crlf_unicode(self):
        index = self.fixture()
        source = (self.corpus / "a.md").read_bytes().decode()
        self.assertIn("\r\n", index.by_path["a.md"][0]["text"])
        for block in index.by_path["a.md"]:
            start, end = block["start_char"], block["end_char"]
            self.assertEqual(block["text"], source[start:end])
            self.assertEqual(block["id"], "raw:" + hashlib.sha256(f"a.md\0{start}\0{end}".encode()).hexdigest()[:20])
        for column, value in (("id", "bad"), ("text", "corrupt"), ("start_char", 4), ("end_line", 99)):
            with self.subTest(column=column), sqlite3.connect(self.db) as conn:
                saved = conn.execute(f"SELECT {column} FROM chunks LIMIT 1").fetchone()[0]
                conn.execute(f"UPDATE chunks SET {column}=? WHERE rowid=1", (value,))
            with self.assertRaisesRegex(ValueError, "chunk"):
                self.reload()
            with sqlite3.connect(self.db) as conn:
                conn.execute(f"UPDATE chunks SET {column}=? WHERE rowid=1", (saved,))

    def test_inventory_digest_uniqueness_hash_and_missing_chunk(self):
        self.fixture()
        with self.assertRaisesRegex(ValueError, "SHA-256"):
            RawCorpus(self.db, self.corpus, "0" * 64)
        original = self.db.read_bytes()
        def duplicate_with_correct_digest(manifest):
            manifest["inventory"].append(manifest["inventory"][0])
            manifest["source_inventory_sha256"] = hashlib.sha256(json_text(manifest["inventory"]).encode()).hexdigest()
        for modifier, error in ((lambda m: m.update(source_inventory_sha256="0" * 64), "digest"),
                                (duplicate_with_correct_digest, "duplicate")):
            self.rewrite_metadata(modifier)
            with self.assertRaisesRegex(ValueError, error):
                self.reload()
            self.db.write_bytes(original)
        with sqlite3.connect(self.db) as conn:
            conn.execute("DELETE FROM chunks WHERE rowid=1")
        with self.assertRaisesRegex(ValueError, "chunk"):
            self.reload()
        self.db.write_bytes(original)
        (self.corpus / "a.md").write_text("changed")
        with self.assertRaisesRegex(ValueError, "SHA-256"):
            self.reload()

    def test_unsafe_root_source_and_journal_rejected(self):
        self.fixture()
        root_link = self.root / "linked-root"
        root_link.symlink_to(self.corpus, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            RawCorpus(self.db, root_link, sha256_file(self.db))
        for suffix in ("-wal", "-journal"):
            sidecar = Path(str(self.db) + suffix)
            sidecar.write_bytes(b"dirty")
            with self.assertRaisesRegex(ValueError, "sidecar"):
                database_identity(self.db, sha256_file(self.db))
            sidecar.unlink()
            sidecar.symlink_to(self.root / "absent")
            with self.assertRaisesRegex(ValueError, "symlink"):
                database_identity(self.db, sha256_file(self.db))
            sidecar.unlink()
        source = self.corpus / "a.md"
        saved = self.root / "saved.md"
        source.rename(saved)
        source.symlink_to(saved)
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.reload()
        with self.assertRaises(ValueError):
            checked_path(self.corpus / ".." / "saved.md")

    def test_literal_ranking_exact_paths_and_fallbacks(self):
        index = self.fixture({"a.md": "ALPHA alpha beta\n", "b.md": "alpha\n", "folder/a.md": "elsewhere beta"})
        self.assertEqual(literal_terms(' "Alpha  beta" ALPHA alpha '), ["alpha beta", "alpha"])
        original = {"evidence": [node(paths=("b.md", "a.md")), node("missing", ()),
                                  node("unknown", ("A.md",)), node("nonmatch", ("folder/a.md",))],
                    "diagnostics": {"fts_seeds": ["node-1"], "graph": {"score": 1}}}
        saved = copy.deepcopy(original)
        response = backfill_response(original, "alpha", index)
        self.assertEqual(original, saved)
        chosen = response["evidence"][0]
        self.assertEqual(chosen["path"], "b.md")  # density wins, paths are exact
        self.assertEqual(chosen["title"], "原文(file-scope):b.md")
        self.assertEqual(chosen["text"], "alpha\n")
        self.assertNotIn("edges", chosen)
        self.assertEqual((chosen["score"], chosen["bm25_rank"]), (0.8, -0.2))
        self.assertEqual(response["evidence"][1:], original["evidence"][1:])
        audit = response["diagnostics"]["source_backfill"]["nodes"]
        self.assertEqual([d["reason"] for d in audit], ["selected", "missing_sources", "unknown_paths", "no_term_match"])
        self.assertEqual(audit[0]["original_node"], original["evidence"][0])
        self.assertEqual([d["position"] for d in audit], [1, 2, 3, 4])
        self.assertEqual([d["parent_node_id"] for d in audit], [n["id"] for n in original["evidence"]])
        self.assertEqual(response["diagnostics"]["fts_seeds"], ["node-1"])
        both = backfill_response({"evidence": [node(paths=("b.md", "a.md"))]}, "alpha beta", index)
        self.assertEqual(both["evidence"][0]["path"], "a.md")  # distinct terms win
        unsafe = backfill_response({"evidence": [node(paths=("../a.md",))]}, "alpha", index)
        self.assertEqual(unsafe["diagnostics"]["source_backfill"]["nodes"][0]["reason"], "unsafe_paths")

    def test_duplicate_best_preserves_original_without_second_best(self):
        index = self.fixture({"a.md": "alpha alpha", "b.md": "alpha and much more text"})
        first, second = node("first", ("a.md", "b.md")), node("second", ("a.md", "b.md"))
        response = backfill_response({"evidence": [first, second]}, "alpha", index)
        self.assertEqual(response["evidence"][0]["path"], "a.md")
        self.assertEqual(response["evidence"][1], second)
        self.assertEqual(response["diagnostics"]["source_backfill"]["nodes"][1]["reason"], "duplicate_best_chunk")

    def test_query_specific_chunk_id_and_empty_backend(self):
        index = self.fixture({"a.md": "alpha" + " " * 95 + "beta"})
        original = {"evidence": [node()]}
        first = backfill_response(original, "alpha", index)["evidence"][0]
        second = backfill_response(original, "beta", index)["evidence"][0]
        self.assertNotEqual(first["id"], second["id"])
        self.assertNotEqual(normalized_block(first)["id"], normalized_block(second)["id"])
        self.assertNotEqual(first["id"], original["evidence"][0]["id"])
        empty = backfill_response({"evidence": []}, "alpha", index)
        self.assertEqual(empty["evidence"], [])
        self.assertEqual(empty["diagnostics"]["source_backfill"]["nodes"], [])

    def test_clipping_exact_json_bounds_and_unchanged_other_paths(self):
        index = self.fixture({"a.md": '# A\r\nalpha "quoted" \\ 中文🙂\r\n' + "z\n" * 80}, 500)
        raw = backfill_response({"evidence": [node()]}, "alpha", index)["evidence"][0]
        full = evidence_budget([raw], 10000)
        for budget in range(400, full["visible_chars"], 7):
            limited = evidence_budget([raw], budget)
            self.assertLessEqual(limited["visible_chars"], budget)
            self.assertEqual(limited["visible_chars"], len(limited["serialized"]))
            for block in limited["evidence"]:
                source = block["sources"][0]
                self.assertEqual(source["end_char"], source["start_char"] + len(block["text"]))
                self.assertEqual(source["end_line"], source["start_line"] + block["text"][:-1].count("\n"))
                self.assertEqual(block["end_char"], source["end_char"])
                self.assertEqual(block["end_line"], source["end_line"])
                self.assertTrue(raw["text"].startswith(block["text"]))
        for block in (node(text="x" * 600), {**raw, "kind": "original_text", "provenance": "original_source"}):
            with patch("retrieval._clipped_evidence", side_effect=lambda b, n: {**b, "text": b["text"][:n], "text_truncated": True}):
                before = evidence_budget([block], 450)
            self.assertEqual(evidence_budget([block], 450), before)

    def test_long_source_displaces_node_evidence_and_native_batch_budget(self):
        index = self.fixture({"a.md": "alpha " * 5000}, 40000)
        original = {"evidence": [node(), node("second", (), text="other visible fact")]}
        response = backfill_response(original, "alpha", index)
        self.assertEqual(len(evidence_budget(original["evidence"], 10000)["evidence"]), 2)
        self.assertEqual(len(evidence_budget(response["evidence"], 10000)["evidence"]), 1)
        trace = {"trace_id": "synthetic/fts", "question_id": "synthetic", "question": "Synthetic?",
                 "query_origin_arm": "fts", "events": [
                     {"event_id": f"synthetic/fts/{i}", "search_index": i, "query": "alpha",
                      "tool_call_id": f"call-{i}", "model_turn": 1, "historical_status": "ok" if i < 4 else "search_budget_exhausted",
                      "include_in_query_metrics": i < 4} for i in range(1, 5)]}
        class SyntheticEngine:
            def search(self, query, max_chars=10000):
                return {"status": "ok", "query": query, **evidence_budget(response["evidence"], max_chars), "diagnostics": response["diagnostics"]}
        with patch("urllib.request.urlopen", side_effect=AssertionError("network forbidden")):
            replay = replay_trace(trace, "source-backfill", "fts", SyntheticEngine(), EvalConfig())
        self.assertLessEqual(replay["budgets"]["visible_context_chars_used"], 20000)
        self.assertEqual(replay["budgets"]["visible_context_chars_used"], sum(len(c["content"]) for c in replay["tool_context"]))
        self.assertEqual([s["executed"] for s in replay["searches"]], [True, True, False, False])
        self.assertEqual(replay["actual_token_usage"], 0)

    def test_semantic_quotes_must_occur_in_visible_text_not_title(self):
        packet = {"evidence": {"e:1": [{"id": "e:1", "title": "title-only fact", "text": "visible fact"}]}}
        fact = {"fact_index": 0, "verdict": "fully_supported", "evidence_id": "e:1", "quote": "title-only fact"}
        with self.assertRaisesRegex(ValueError, "visible text"):
            validate_text_fact(fact, packet)
        fact["quote"] = "visible fact"
        self.assertEqual(validate_text_fact(fact, packet)["verified_quotation_segments"], 1)

    def test_changed_binding_and_symlink_rejected(self):
        path = self.root / "bound.py"
        path.write_text("original")
        bindings = [{"path": str(path), "sha256": sha256_file(path)}]
        check_bindings(bindings)
        path.write_text("changed")
        with self.assertRaisesRegex(ValueError, "SHA-256"):
            check_bindings(bindings)

    def test_adapter_keeps_leading_hyphen_query_and_node_only_stdout(self):
        self.fixture({"a.md": "--no-cache flag"})
        protocol = {"source_root": str(self.corpus), "inputs": {
            "han_kg": {"path": "kg.db"}, "adapter": {"path": "go-adapter"},
            "index_manifest": {"path": "manifest.json"},
            "raw_index": {"path": str(self.db), "sha256": sha256_file(self.db)}}}
        argv = ["--db", "kg.db", "--mode", "fts", "--query", "--no-cache", "--limit", "10"]
        original = ' { "evidence": ' + json.dumps([node()]) + ', "diagnostics": {} }\n'
        for factor in ("node-only", "source-backfill"):
            stdout = io.StringIO()
            with patch("source_backfill_round3.load_protocol", return_value=protocol), \
                    patch("source_backfill_round3.subprocess.run", return_value=subprocess.CompletedProcess([], 0, original, "")) as execute, \
                    patch("sys.stdout", stdout):
                self.assertEqual(adapter_main(factor, self.root / "protocol.json", argv), 0)
            self.assertEqual(execute.call_args.args[0][-8:], argv)
            if factor == "node-only":
                self.assertEqual(stdout.getvalue(), original)
            else:
                self.assertEqual(json.loads(stdout.getvalue())["evidence"][0]["text"], "--no-cache flag")

    def test_packets_only_contain_native_visible_context(self):
        content = json_text({"evidence": [{"id": "e:only", "title": "t", "text": "native visible"}], "truncated": False})
        traces = [{"question_id": "synthetic", "factor": factor, "arm": arm, "query_origin_arm": origin,
                   "trace_id": "synthetic/" + origin,
                   "tool_context": [{"search_index": 1, "content": content}],
                   "budgets": {"visible_context_chars_used": len(content)},
                   "standalone": "MUST NOT LEAK"}
                  for factor in ("node-only", "source-backfill") for arm in ("fts", "graph") for origin in ("fts", "graph")]
        gold = {"synthetic": {"question": "Question?", "required_facts": ["native visible"], "evidence": "GOLD MUST NOT LEAK"}}
        counts = make_packets(self.root, traces, gold, ["synthetic"])
        self.assertEqual(counts, {"synthetic": 8})
        packet_path = self.root / "review-packets/synthetic.json"
        document = json.loads(packet_path.read_text())
        self.assertNotIn("MUST NOT LEAK", packet_path.read_text())
        self.assertNotIn('"factor"', packet_path.read_text())
        self.assertEqual(len(document["packets"]), 8)
        self.assertTrue(all(p["tool_context"][0]["content"] == content for p in document["packets"]))

    def test_wrapper_exception_reaches_unchanged_engine_error(self):
        # 原本 wrapper 只写 stdout，非零退出令旧 engine 丢失校验失败原因。
        # Real child process exercises the wrapper/engine boundary, before any Go query.
        protocol = self.root / "invalid-protocol.json"
        protocol.write_text("{}")
        wrapper = self.root / "wrapper.py"
        wrapper.write_text(
            f"#!{sys.executable}\nimport sys\nfrom pathlib import Path\n"
            f"sys.path.insert(0, {str(Path(__file__).resolve().parent)!r})\n"
            "from source_backfill_round3 import adapter_main\n"
            f"sys.exit(adapter_main('source-backfill', Path({str(protocol)!r}), sys.argv[1:]))\n")
        wrapper.chmod(0o700)
        database = self.root / "unused.db"
        argv = [str(wrapper), "--db", str(database), "--mode", "fts", "--query", "synthetic", "--limit", "10"]
        process = subprocess.run(argv, capture_output=True, text=True, check=False)
        self.assertEqual(process.returncode, 1)
        payload = json.loads(process.stdout)
        diagnostic = payload["error"]
        self.assertIn("SHA-256 mismatch", diagnostic)
        self.assertIn(str(protocol), diagnostic)
        self.assertEqual(payload["evidence"], [])
        self.assertEqual(process.stderr, diagnostic + "\n")
        result = RetrievalEngine("fts", kg_db=database, adapter=wrapper).search("synthetic")
        self.assertEqual(result["status"], "error")
        self.assertIn(diagnostic, result["error"])
        self.assertIn(diagnostic, json.loads(result["serialized"])["error"])


if __name__ == "__main__":
    unittest.main()
