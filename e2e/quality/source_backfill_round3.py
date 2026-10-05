#!/usr/bin/env python3
"""冻结 DEV 原文回填实验；offline replay and deferred paid preflight only.

The Go adapter alone selects/ranks nodes. This wrapper may replace each returned
node with one verified, file-scoped original chunk before the existing evidence
budget and native trace driver run. No command in this module calls a provider.
"""
from __future__ import annotations

import argparse
import collections
import datetime as dt
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import random
import re
import sqlite3
import subprocess
import sys

from agent_eval import EvalConfig
from retrieval import RetrievalEngine, json_text, normalized_block, sha256_file, text_chunks
from retrieval_round2 import replay_trace, write_new_json
from semantic_gate_round2 import decode, quote_segments, validate_fact, visible_evidence


PROTOCOL_SHA256 = "151ceb8608853cdace7f6d19bdb0d184af94c0b9a30f6967551e2fc8fff76504"
INVENTORY_SHA256 = "e5e8e26d8adf961dbf1645c76f2d829ef505188a754c96de52def5bf0ea2f164"
FACTORS = ("node-only", "source-backfill")
ARMS = ("fts", "graph")
KIND = "source_backfill"
PROVENANCE = "verified_original_source_file_scope"
HERE = Path(__file__).resolve().parent
IMPLEMENTATION = ("source_backfill_round3.py", "test_source_backfill_round3.py",
                  "retrieval.py", "retrieval_round2.py", "semantic_gate_round2.py",
                  "agent_eval.py", "agent_eval_native.py", "durable_ledger.py",
                  "grade.py", "audit_gold.py", "round3/README.md")


def read_json(path: Path):
    return decode(path.read_bytes().decode("utf-8"))


def checked_path(path: Path, directory: bool = False) -> Path:
    """连根目录一起拒绝符号链接，避免校验与读取指向不同对象 / no symlinks."""
    if ".." in path.parts:
        raise ValueError(f"source_backfill: unsafe path: {path}")
    absolute = path.absolute()
    if absolute.resolve(strict=True) != absolute:
        raise ValueError(f"source_backfill: unsafe or symlink path: {path}")
    if not (absolute.is_dir() if directory else absolute.is_file()):
        raise ValueError(f"source_backfill: wrong path type: {path}")
    return absolute


def safe_relative(value: str) -> bool:
    return (isinstance(value, str) and bool(value) and "\\" not in value and "\0" not in value
            and not PurePosixPath(value).is_absolute() and not re.match(r"^[A-Za-z]:", value)
            and all(part not in ("", ".", "..") for part in value.split("/")))


def checked_hash(path: Path, expected: str) -> str:
    actual = sha256_file(checked_path(path))
    if actual != expected:
        raise ValueError(f"source_backfill: SHA-256 mismatch: {path}: {actual} != {expected}")
    return actual


def database_identity(path: Path, expected: str) -> str:
    for suffix in ("-wal", "-journal", "-shm"):
        sidecar = Path(str(path) + suffix)
        if sidecar.is_symlink():
            raise ValueError(f"source_backfill: symlink SQLite sidecar: {sidecar}")
        if sidecar.exists():
            checked_path(sidecar)
            if suffix != "-shm" and sidecar.stat().st_size:
                raise ValueError(f"source_backfill: nonempty SQLite sidecar: {sidecar}")
    return checked_hash(path, expected)


class RawCorpus:
    """只读验证全部原文与 chunk，之后只在精确 path 集合内挑选 / verified inputs."""

    def __init__(self, database: Path, root: Path, expected_sha256: str):
        self.database, self.root = checked_path(database), checked_path(root, directory=True)
        self.database_sha256 = database_identity(database, expected_sha256)
        with sqlite3.connect(self.database.as_uri() + "?mode=ro&immutable=1", uri=True) as conn:
            conn.execute("PRAGMA query_only=ON")
            conn.row_factory = sqlite3.Row
            metadata = conn.execute("SELECT value FROM metadata WHERE key='manifest'").fetchall()
            if len(metadata) != 1:
                raise ValueError("source_backfill: raw manifest must be unique")
            manifest = decode(metadata[0][0])
            chunks = [dict(row) for row in conn.execute("SELECT * FROM chunks ORDER BY path,start_char")]
        if manifest.get("format") != "cairn-quality-raw-lexical/v1":
            raise ValueError("source_backfill: invalid raw manifest")
        inventory = manifest.get("inventory")
        if not isinstance(inventory, list) or not inventory:
            raise ValueError("source_backfill: empty source inventory")
        paths = [entry["path"] for entry in inventory]
        if len(set(paths)) != len(paths) or paths != sorted(paths) or not all(safe_relative(p) for p in paths):
            raise ValueError("source_backfill: duplicate, unordered, or unsafe inventory path")
        inventory_sha = hashlib.sha256(json_text(inventory).encode()).hexdigest()
        if inventory_sha != manifest.get("source_inventory_sha256"):
            raise ValueError("source_backfill: source inventory digest mismatch")
        if type(manifest.get("chunk_chars")) is not int or type(manifest.get("overlap_chars")) is not int:
            raise ValueError("source_backfill: invalid raw chunking parameters")
        expected_chunks = []
        self.documents = []
        for entry in inventory:
            path = checked_path(self.root / entry["path"])
            # bytes.decode preserves CRLF and Unicode offsets used by build_raw_index.
            data = path.read_bytes()
            source_sha = hashlib.sha256(data).hexdigest()
            if source_sha != entry["source_sha256"]:
                raise ValueError(f"source_backfill: source SHA-256 mismatch: {entry['path']}")
            text = data.decode("utf-8")
            title = next((line.lstrip("#").strip() for line in text.splitlines() if line.startswith("#")), path.stem)
            pieces = list(text_chunks(text, manifest["chunk_chars"], manifest["overlap_chars"]))
            if entry["chars"] != len(text) or entry["chunks"] != len(pieces):
                raise ValueError(f"source_backfill: inventory char/chunk count mismatch: {entry['path']}")
            for chunk in pieces:
                identity = f"{entry['path']}\0{chunk['start_char']}\0{chunk['end_char']}".encode()
                expected_chunks.append({"id": "raw:" + hashlib.sha256(identity).hexdigest()[:20],
                                        "path": entry["path"], "title": title, **chunk})
            self.documents.append({"path": str(path), "sha256": source_sha})
        if chunks != expected_chunks or len({row["id"] for row in chunks}) != len(chunks):
            raise ValueError("source_backfill: raw chunk ID/text/char/line completeness mismatch")
        if manifest.get("documents") != len(inventory) or manifest.get("chunks") != len(chunks):
            raise ValueError("source_backfill: raw document/chunk cardinality mismatch")
        self.by_path = collections.defaultdict(list)
        for chunk in chunks:
            self.by_path[chunk["path"]].append(chunk)
        self.identity = {"raw_sha256": self.database_sha256, "source_root": str(self.root),
                         "source_inventory_sha256": inventory_sha, "documents": len(inventory),
                         "chunks": len(chunks), "source_files": self.documents}
        database_identity(database, expected_sha256)
        check_bindings(self.documents)


def literal_terms(query: str) -> list[str]:
    rewritten = " ".join(query.split())
    terms = list(dict.fromkeys((phrase or word).casefold()
                              for phrase, word in re.findall(r'"([^"]+)"|(\S+)', rewritten)))
    if not terms:
        raise ValueError("source_backfill: empty literal query")
    return terms


def backfill_response(response: dict, query: str, corpus: RawCorpus) -> dict:
    """先选最佳再去重；重复时保留节点，禁止改选次佳 / preserve selection order."""
    terms, used, evidence, audits = literal_terms(query), set(), [], []
    for position, node in enumerate(response.get("evidence") or [], 1):
        audit = {"position": position, "parent_node_id": node["id"], "score": node.get("score"),
                 "sources": node.get("sources"), "original_node": node}
        paths = [source.get("path") for source in (node.get("sources") or [])]
        reason, selected = "selected", None
        if not paths:
            reason = "missing_sources"
        elif not all(safe_relative(path) for path in paths):
            reason = "unsafe_paths"
        elif any(path not in corpus.by_path for path in paths):
            reason = "unknown_paths"
        else:
            ranked = []
            for path in set(paths):
                for chunk in corpus.by_path[path]:
                    body = chunk["text"].casefold()
                    occurrences = [body.count(term) for term in terms]
                    matched = sum(count > 0 for count in occurrences)
                    if matched:
                        density = sum(occurrences) / max(1, len(body))
                        ranked.append(((-matched, -density, path, chunk["start_char"]), chunk))
            if not ranked:
                reason = "no_term_match"
            else:
                rank, selected = min(ranked, key=lambda pair: pair[0])
                audit.update(best_chunk_id=selected["id"], matched_terms=-rank[0], occurrence_density=-rank[1])
                if selected["id"] in used:
                    reason = "duplicate_best_chunk"
        if reason == "selected":
            used.add(selected["id"])
            source = {key: selected[key] for key in ("path", "start_char", "end_char", "start_line", "end_line")}
            block = {"id": selected["id"], "kind": KIND, "provenance": PROVENANCE,
                     "title": "原文(file-scope):" + selected["path"], "text": selected["text"],
                     "sources": [source], **source}
            for key in ("score", "bm25_rank", "bm25_score", "graph_score", "depth"):
                if key in node:
                    block[key] = node[key]
            evidence.append(block)
        else:
            evidence.append(node)
        audits.append({**audit, "reason": reason})
    diagnostics = {**(response.get("diagnostics") or {}), "source_backfill": {
        "query_terms": terms, "scope": "verified original text selected only at node.sources file scope",
        "nodes": audits, "counts": dict(collections.Counter(item["reason"] for item in audits))}}
    return {**response, "evidence": evidence, "diagnostics": diagnostics}


def load_protocol(path: Path) -> dict:
    checked_hash(path, PROTOCOL_SHA256)
    return read_json(path)


def adapter_main(factor: str, protocol_path: Path, argv: list[str]) -> int:
    """固定键值协议保留 -- 开头的 query；do not reinterpret search text as flags."""
    try:
        if factor not in FACTORS or len(argv) != 8 or argv[::2] != ["--db", "--mode", "--query", "--limit"]:
            raise ValueError("source_backfill: unexpected RetrievalEngine argv")
        args = dict(zip(argv[::2], argv[1::2]))
        protocol = load_protocol(protocol_path)
        inputs = protocol["inputs"]
        if args["--db"] != inputs["han_kg"]["path"] or args["--mode"] not in ARMS or args["--limit"] != "10":
            raise ValueError("source_backfill: adapter controls differ from frozen protocol")
        command = [inputs["adapter"]["path"], "--query-syntax", "text", "--text-profile", "han-v1",
                   "--index-manifest", inputs["index_manifest"]["path"], *argv]
        process = subprocess.run(command, capture_output=True, text=True, timeout=30)
        if process.returncode or factor == "node-only":
            sys.stdout.write(process.stdout)
            sys.stderr.write(process.stderr)
            return process.returncode
        response = decode(process.stdout)
        if response.get("error"):
            sys.stdout.write(process.stdout)
        else:
            corpus = RawCorpus(Path(inputs["raw_index"]["path"]), Path(protocol["source_root"]), inputs["raw_index"]["sha256"])
            sys.stdout.write(json_text(backfill_response(response, args["--query"], corpus)))
        sys.stderr.write(process.stderr)
        return 0
    except (ValueError, OSError, KeyError, TypeError, sqlite3.Error, subprocess.SubprocessError) as exc:
        # 旧 engine 在非零退出时读取 stderr；preserve the cause on both channels.
        diagnostic = "source_backfill: " + str(exc)
        sys.stdout.write(json_text({"error": diagnostic, "evidence": []}))
        sys.stderr.write(diagnostic + "\n")
        return 1


def validate_inputs(protocol: dict) -> tuple[dict, dict, dict]:
    bindings = []
    for name, item in protocol["inputs"].items():
        path = Path(item["path"])
        function = database_identity if name in ("original_kg", "han_kg", "raw_index") else checked_hash
        bindings.append({"name": name, "path": str(path), "sha256": function(path, item["sha256"])})
    if not os.access(protocol["inputs"]["adapter"]["path"], os.X_OK):
        raise ValueError("source_backfill: Go adapter is not executable")
    corpus = RawCorpus(Path(protocol["inputs"]["raw_index"]["path"]), Path(protocol["source_root"]), protocol["inputs"]["raw_index"]["sha256"])
    if (corpus.identity["documents"], corpus.identity["chunks"], corpus.identity["source_inventory_sha256"]) != (69, 406, INVENTORY_SHA256):
        raise ValueError("source_backfill: frozen inventory identity differs")
    manifest = read_json(Path(protocol["inputs"]["index_manifest"]["path"]))
    if (manifest.get("index_profile") != "han-v1"
            or manifest.get("source_db_sha256") != protocol["inputs"]["original_kg"]["sha256"]
            or manifest.get("output_db_sha256") != protocol["inputs"]["han_kg"]["sha256"]
            or not manifest.get("all_non_fts_tables_unchanged")
            or manifest.get("non_fts_tables_before") != manifest.get("non_fts_tables_after")):
        raise ValueError("source_backfill: han-v1 manifest does not preserve frozen graph/source data")
    dev = read_json(Path(protocol["inputs"]["dev_questions"]["path"]))
    questions = {row["id"]: row["question"] for row in dev}
    roster = read_json(Path(protocol["inputs"]["fixed_roster"]["path"]))
    if len(dev) != 40 or len(questions) != 40 or set(roster["question_ids"]) != set(questions):
        raise ValueError("source_backfill: exact 40-DEV roster required")
    combinations, included, excluded = set(), 0, 0
    for trace in roster["traces"]:
        qid, origin = trace["question_id"], trace["query_origin_arm"]
        key = (qid, origin)
        if key in combinations or qid not in questions or origin not in ARMS or trace["question"] != questions[qid] or trace["trace_id"] != f"{qid}/{origin}":
            raise ValueError("source_backfill: invalid or duplicated frozen DEV trace")
        combinations.add(key)
        for ordinal, event in enumerate(trace["events"], 1):
            if event["search_index"] != ordinal or event["event_id"] != f"{qid}/{origin}/{ordinal}":
                raise ValueError("source_backfill: frozen search order changed")
            if event["include_in_query_metrics"]:
                included += 1
            else:
                excluded += 1
    if combinations != {(qid, arm) for qid in questions for arm in ARMS} or (included, excluded) != (240, 4):
        raise ValueError("source_backfill: frozen trace/query cardinalities changed")
    return {"bindings": bindings, "corpus": corpus.identity}, roster, questions


def binding(path: Path) -> dict:
    return {"path": str(checked_path(path)), "sha256": sha256_file(path)}


def check_bindings(bindings: list[dict]) -> None:
    if len({item["path"] for item in bindings}) != len(bindings):
        raise ValueError("source_backfill: duplicate implementation binding")
    for item in bindings:
        checked_hash(Path(item["path"]), item["sha256"])


def freeze_implementation(protocol_path: Path, output: Path) -> dict:
    protocol = load_protocol(protocol_path)
    inputs, _, _ = validate_inputs(protocol)
    if sha256_file(HERE / "agent_eval_native.py") != protocol["code"]["native_harness_sha256"]:
        raise ValueError("source_backfill: frozen native harness changed")
    output.mkdir(parents=True, exist_ok=True)
    checked_path(output, directory=True)
    freeze_path = output / "implementation-freeze.json"
    if freeze_path.exists():
        raise FileExistsError("source_backfill: freeze already exists; no automatic re-freeze")
    wrappers = output / "adapter-wrappers"
    wrappers.mkdir()
    generated = {}
    for factor in FACTORS:
        path = wrappers / (factor + ".py")
        content = (f"#!{sys.executable}\nimport sys\nfrom pathlib import Path\n"
                   f"sys.path.insert(0, {str(HERE)!r})\nfrom source_backfill_round3 import adapter_main\n"
                   f"sys.exit(adapter_main({factor!r}, Path({str(protocol_path.absolute())!r}), sys.argv[1:]))\n")
        with path.open("x", encoding="utf-8") as stream:
            stream.write(content)
        path.chmod(0o700)
        generated[factor] = binding(path)
    frozen = {"format": "cairn-round3-implementation-freeze/v1", "created_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
              "protocol": binding(protocol_path), "inputs": inputs,
              "implementation": [binding(HERE / name) for name in IMPLEMENTATION],
              "wrappers": generated, "python": binding(Path(sys.executable).resolve()),
              "python_version": sys.version, "fixed_controls": protocol["fixed_controls"],
              "future_paid_pilot": protocol["future_paid_pilot"], "paid_requests": 0}
    write_new_json(freeze_path, frozen)
    return {"freeze": str(freeze_path), "sha256": sha256_file(freeze_path), "paid_requests": 0}


def verify_freeze(path: Path) -> tuple[dict, dict, dict]:
    frozen = read_json(checked_path(path))
    if frozen.get("format") != "cairn-round3-implementation-freeze/v1":
        raise ValueError("source_backfill: unknown implementation freeze")
    protocol = load_protocol(Path(frozen["protocol"]["path"]))
    if frozen["protocol"]["sha256"] != PROTOCOL_SHA256:
        raise ValueError("source_backfill: protocol binding mismatch")
    expected_paths = {str(HERE / name) for name in IMPLEMENTATION}
    if {item["path"] for item in frozen["implementation"]} != expected_paths or set(frozen["wrappers"]) != set(FACTORS):
        raise ValueError("source_backfill: incomplete implementation/wrapper bindings")
    check_bindings(frozen["implementation"] + list(frozen["wrappers"].values()) + [frozen["python"]])
    if frozen["python"]["path"] != str(Path(sys.executable).resolve()) or frozen["python_version"] != sys.version:
        raise ValueError("source_backfill: Python runtime changed")
    inputs, roster, _ = validate_inputs(protocol)
    if inputs != frozen["inputs"] or frozen["fixed_controls"] != protocol["fixed_controls"] or frozen["future_paid_pilot"] != protocol["future_paid_pilot"]:
        raise ValueError("source_backfill: frozen data/budget bindings changed")
    return frozen, protocol, roster


def validate_text_fact(fact: dict, packet: dict) -> dict:
    # 复用 round2 校验，但去掉 title 支持通道 / visible body text alone supports facts.
    result = validate_fact(fact, packet)
    for segment in quote_segments(fact):
        if not any(segment["quote"] in block["text"] for block in packet["evidence"].get(segment["evidence_id"], [])):
            raise ValueError("source_backfill: quotation must occur in actual visible text, not only title/path")
    return result


def make_packets(output: Path, traces: list[dict], gold: dict, pilot_ids: list[str]) -> dict:
    directory = output / "review-packets"
    directory.mkdir()
    mapping, counts = [], {}
    for qid in pilot_ids:
        candidates = [trace for trace in traces if trace["question_id"] == qid]
        if len(candidates) != 8 or {(t["factor"], t["query_origin_arm"], t["arm"]) for t in candidates} != {(f, o, a) for f in FACTORS for o in ARMS for a in ARMS}:
            raise ValueError("source_backfill: semantic packets require all eight native traces per DEV question")
        random.SystemRandom().shuffle(candidates)
        packets = []
        for number, trace in enumerate(candidates, 1):
            pid = f"packet-{qid}-{number:02d}"
            packet = {"packet_id": pid, "tool_context": [{"search_index": c["search_index"], "content": c["content"]} for c in trace["tool_context"]],
                      "visible_context_chars": trace["budgets"]["visible_context_chars_used"]}
            visible_evidence(packet)
            packets.append(packet)
            mapping.append({"packet_id": pid, "question_id": qid, "factor": trace["factor"],
                            "query_origin_arm": trace["query_origin_arm"], "arm": trace["arm"], "trace_id": trace["trace_id"]})
        write_new_json(directory / (qid + ".json"), {
            "format": "cairn-round3-blinded-evidence-review/v1", "question_id": qid,
            "question": gold[qid]["question"], "required_facts": gold[qid]["required_facts"],
            "packets": packets,
            "instructions": ["Review each packet independently, using only its actual visible text; never combine packets.",
                             "For every zero-based fact_index return fully_supported, partly_supported, contradicted, or absent, with a short reason.",
                             "Every supported verdict requires exact quote(s) and their visible evidence_id(s). Title, path, provenance and source lines alone cannot support a fact.",
                             "Gold facts and question text are the targets of review, never retrieved evidence. No standalone fixed-query cell is included here.",
                             "Return question_id, reviewer and packets:[{packet_id,facts:[{fact_index,verdict,quotes:[{quote,evidence_id}],reason}]}]. This is AI review, not human gold."]})
        counts[qid] = len(packets)
    private = output / "private-review-mapping.json"
    write_new_json(private, {"format": "cairn-round3-review-mapping/v1", "mapping": mapping,
                             "rule": "withhold mapping until independent verdicts are frozen"})
    private.chmod(0o600)
    return counts


def result_metrics(result: dict) -> dict:
    diagnostics = result.get("diagnostics", {})
    original_ids = diagnostics.get("evidence_id_map", {})
    return {"status": result["status"], "executed": result.get("executed", True),
            "backend_blocks": len(original_ids), "empty_success": result["status"] == "ok" and not original_ids,
            "visible_empty_success": result["status"] == "ok" and not result.get("evidence"),
            "visible_chars": result["visible_chars"], "truncated": result.get("truncated", False),
            "visible_blocks": len(result.get("evidence", [])),
            "selection_counts": diagnostics.get("source_backfill", {}).get("counts", {})}


def paired_losses(old: dict, new: dict) -> dict:
    before = {block["id"] for block in old.get("evidence", [])}
    after = {block["id"] for block in new.get("evidence", [])}
    represented = set(after)
    for audit in new.get("diagnostics", {}).get("source_backfill", {}).get("nodes", []):
        if audit["reason"] == "selected" and normalized_block({"id": audit["best_chunk_id"]})["id"] in after:
            represented.add(normalized_block({"id": audit["parent_node_id"]})["id"])
    return {"node_only_evidence_ids_absent": sorted(before - after),
            "node_positions_lost_to_budget_or_suppression": sorted(before - represented)}


def run_offline(freeze_path: Path, output: Path) -> dict:
    frozen, protocol, roster = verify_freeze(freeze_path)
    freeze_hash = sha256_file(freeze_path)
    output.mkdir(parents=True, exist_ok=False)
    controls = protocol["fixed_controls"]
    config = EvalConfig(**{key: controls[key] for key in ("top_k", "max_searches", "max_model_turns", "per_search_chars", "total_context_chars")})
    engines = {(factor, arm): RetrievalEngine(arm, kg_db=Path(protocol["inputs"]["han_kg"]["path"]),
                                             adapter=Path(frozen["wrappers"][factor]["path"]), top_k=10)
               for factor in FACTORS for arm in ARMS}
    cells, traces = [], []
    # 逐条持久保存失败与重复 query；standalone cells never enter semantic packets.
    with (output / "comparison-cells.jsonl").open("x", encoding="utf-8") as cell_file, (output / "native-traces.jsonl").open("x", encoding="utf-8") as trace_file:
        for trace in roster["traces"]:
            for event in trace["events"]:
                if not event["include_in_query_metrics"]:
                    continue
                for factor in FACTORS:
                    for arm in ARMS:
                        result = engines[factor, arm].search(event["query"], controls["per_search_chars"])
                        cell = {"factor": factor, "arm": arm, "question_id": trace["question_id"],
                                "query_origin_arm": trace["query_origin_arm"], "event_id": event["event_id"],
                                "scope": "standalone fixed-query cell; not native visible context", "result": result,
                                "metrics": result_metrics(result)}
                        cells.append(cell)
                        cell_file.write(json_text(cell) + "\n")
                        cell_file.flush()
            for factor in FACTORS:
                for arm in ARMS:
                    replay = replay_trace(trace, factor, arm, engines[factor, arm], config)
                    traces.append(replay)
                    trace_file.write(json_text(replay) + "\n")
                    trace_file.flush()
            os.fsync(cell_file.fileno())
            os.fsync(trace_file.fileno())
            print(f"source_backfill: completed {trace['trace_id']} ({len(cells)} cells; {len(traces)} traces)", file=sys.stderr, flush=True)
    if (len(cells), len(traces)) != (960, 320):
        raise ValueError("source_backfill: fixed comparison denominator changed")
    grouped = collections.defaultdict(collections.Counter)
    for cell in cells:
        counter, metrics = grouped[cell["factor"] + "/" + cell["arm"]], cell["metrics"]
        counter["cells"] += 1
        counter["errors"] += metrics["status"] == "error"
        for name in ("empty_success", "visible_empty_success", "visible_chars", "visible_blocks", "truncated"):
            counter[name] += metrics[name]
        counter.update({"selection_" + k: v for k, v in metrics["selection_counts"].items()})
    lookup = {(c["factor"], c["arm"], c["event_id"]): c["result"] for c in cells}
    losses = [{"scope": "standalone", "arm": arm, "event_id": event, **paired_losses(old, lookup["source-backfill", arm, event])}
              for (factor, arm, event), old in lookup.items() if factor == "node-only"]
    native_lookup = {(t["factor"], t["arm"], t["trace_id"]): t for t in traces}
    native_counts = collections.defaultdict(collections.Counter)
    suppressions = []
    for trace in traces:
        counter = native_counts[trace["factor"] + "/" + trace["arm"]]
        counter["traces"] += 1
        counter["visible_chars"] += trace["budgets"]["visible_context_chars_used"]
        counter["native_errors"] += trace["native_trace_status"] != "ok"
        for event in trace["searches"] + trace["unshown_events"]:
            counter[event["status"]] += 1
            counter["truncated_searches"] += event.get("truncated", False)
            counter["visible_empty_successes"] += event["status"] == "ok" and not event.get("evidence")
            if not event["executed"]:
                suppressions.append({"factor": trace["factor"], "arm": trace["arm"], "event_id": event["event_id"],
                                     "status": event["status"], "new_suppression": event["include_in_query_metrics"]})
        if trace["factor"] == "node-only":
            candidate = native_lookup["source-backfill", trace["arm"], trace["trace_id"]]
            new_by_id = {s["event_id"]: s for s in candidate["searches"] + candidate["unshown_events"]}
            for old in trace["searches"] + trace["unshown_events"]:
                losses.append({"scope": "native", "arm": trace["arm"], "event_id": old["event_id"],
                               **paired_losses(old, new_by_id[old["event_id"]])})
    # Select permitted IDs before retaining any gold content; holdout rows are never analyzed.
    ids = set(roster["question_ids"])
    gold = {row["id"]: row for row in read_json(Path(protocol["inputs"]["gold"]["path"]))["questions"] if row["id"] in ids}
    if set(gold) != ids or any(row.get("split") != "dev" for row in gold.values()):
        raise ValueError("source_backfill: gold must be exactly the frozen DEV set")
    packets = make_packets(output, traces, gold, protocol["semantic_gate"]["dev_ids"])
    verify_freeze(freeze_path)
    checked_hash(freeze_path, freeze_hash)
    report = {"format": "cairn-round3-offline-results/v1", "implementation_freeze_sha256": freeze_hash,
              "comparison_cells": len(cells), "budgeted_traces": len(traces), "packet_counts": packets,
              "standalone": dict(grouped), "native": dict(native_counts), "suppressions": suppressions,
              "node_only_evidence_losses": losses, "input_identities_verified_before_and_after": True,
              "paid_deepseek_requests": 0, "semantic_gate": "pending independent review; no self-scoring",
              "limitations": protocol["limitations"],
              "artifacts": [binding(output / name) for name in ("comparison-cells.jsonl", "native-traces.jsonl", "private-review-mapping.json")] + [binding(p) for p in sorted((output / "review-packets").glob("*.json"))]}
    write_new_json(output / "offline-results.json", report)
    return {"output": str(output), "comparison_cells": len(cells), "budgeted_traces": len(traces), "packets": sum(packets.values()), "paid_deepseek_requests": 0}


def future_paid_preflight(freeze_path: Path) -> dict:
    frozen, protocol, _ = verify_freeze(freeze_path)
    # 仅返回具体参数；运行入口仍不可调用 provider / no client, key, or transport here.
    return {"format": "cairn-round3-future-paid-preflight/v1", "bindings_verified": True,
            "implementation_freeze_sha256": sha256_file(freeze_path), "paid_requests": 0,
            "execution_enabled": False, "candidate_factor": "source-backfill",
            "tasks": [{"question_id": qid, "arm": arm} for qid in protocol["future_paid_pilot"]["dev_ids"] for arm in ARMS],
            "parameters": frozen["future_paid_pilot"], "wrapper": frozen["wrappers"]["source-backfill"],
            "required_before_first_http": ["Call verify_freeze again in the future runner immediately before admitting its first HTTP request; never automatically re-freeze.",
                                           "Require independently validated positive semantic net gain; report every loss.",
                                           "Require a dedicated runtime credential and unchanged native durable ledger admission.",
                                           "Reuse the existing six-DEV/two-arm paid baseline; never rerun paid controls."],
            "status": "parameters ready; semantic gate, runtime credential, and separately reviewed paid runner required"}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    freeze = commands.add_parser("freeze")
    freeze.add_argument("--protocol", type=Path, default=HERE / "round3/protocol.json")
    freeze.add_argument("--output", type=Path, required=True)
    run = commands.add_parser("run-offline")
    run.add_argument("--freeze", type=Path, required=True)
    run.add_argument("--output", type=Path, required=True)
    preflight = commands.add_parser("future-paid-preflight")
    preflight.add_argument("--freeze", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "freeze":
        result = freeze_implementation(args.protocol, args.output)
    elif args.command == "run-offline":
        result = run_offline(args.freeze, args.output)
    else:
        result = future_paid_preflight(args.freeze)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
