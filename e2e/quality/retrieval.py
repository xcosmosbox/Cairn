#!/usr/bin/env python3
"""Auditable three-arm retrieval for the constrained quality evaluation.

raw: case-insensitive literal OR/phrase matching over original-source chunks.
fts / graph: the Go adapter calls Cairn's actual production retrieval APIs.
Python does not reproduce Cairn graph traversal, ranking or query semantics.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sqlite3
import subprocess
import time
from pathlib import Path
from typing import Any


def json_text(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def source_text(root: Path, relative: str, expected_sha256: str) -> str:
    path = root / relative
    if Path(relative).is_absolute() or ".." in Path(relative).parts or path.resolve() != path.absolute():
        raise ValueError(f"unsafe or symlink source path: {relative}")
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != expected_sha256:
        raise ValueError(f"source SHA-256 mismatch: {relative}")
    return data.decode("utf-8")


def text_chunks(text: str, chunk_chars: int = 1800, overlap_chars: int = 200):
    if chunk_chars < 100 or not 0 <= overlap_chars < chunk_chars:
        raise ValueError("invalid original-text chunking parameters")
    start = 0
    while start < len(text):
        end = min(len(text), start + chunk_chars)
        yield {
            "text": text[start:end], "start_char": start, "end_char": end,
            "start_line": text.count("\n", 0, start) + 1,
            "end_line": text.count("\n", 0, max(start, end - 1)) + 1,
        }
        if end == len(text):
            break
        start = end - overlap_chars


def build_raw_index(source_root: Path, accepted_proof: Path, output: Path,
                    chunk_chars: int = 1800, overlap_chars: int = 200) -> dict:
    if output.exists():
        raise FileExistsError(f"refusing to overwrite raw index: {output}")
    proof = json.loads(accepted_proof.read_text())
    entries = proof["accepted_sources"]
    if not entries or len({entry["path"] for entry in entries}) != len(entries):
        raise ValueError("accepted-source proof must contain a unique nonempty file set")
    inventory = []
    prepared = []
    for entry in sorted(entries, key=lambda item: item["path"]):
        relative = entry["path"]
        text = source_text(source_root.absolute(), relative, entry["source_sha256"])
        title = next((line.lstrip("#").strip() for line in text.splitlines() if line.startswith("#")), Path(relative).stem)
        count = 0
        for chunk in text_chunks(text, chunk_chars, overlap_chars):
            identity = f"{relative}\0{chunk['start_char']}\0{chunk['end_char']}".encode()
            prepared.append({"id": "raw:" + hashlib.sha256(identity).hexdigest()[:20],
                             "path": relative, "title": title, **chunk})
            count += 1
        inventory.append({"path": relative, "source_sha256": entry["source_sha256"],
                          "chars": len(text), "chunks": count})
    metadata = {
        "format": "cairn-quality-raw-lexical/v1", "scope": "original source chunks; no KG content or graph algorithms",
        "retrieval": "literal substring OR; matched-term count then occurrence density then path/start offset",
        "experimental_fts_tokenizer": "unicode61", "experimental_fts_columns": ["title", "path", "text"],
        "chunk_chars": chunk_chars, "overlap_chars": overlap_chars,
        "accepted_proof_sha256": sha256_file(accepted_proof),
        "source_commit": proof.get("source_commit"), "documents": len(entries), "chunks": len(prepared),
        "source_inventory_sha256": hashlib.sha256(json_text(inventory).encode()).hexdigest(), "inventory": inventory,
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        with sqlite3.connect(output) as conn:
            conn.execute("CREATE TABLE chunks(id TEXT PRIMARY KEY,path TEXT,title TEXT,text TEXT,start_line INTEGER,end_line INTEGER,start_char INTEGER,end_char INTEGER)")
            conn.execute("CREATE VIRTUAL TABLE chunks_fts USING fts5(title,path,text,tokenize='unicode61')")
            conn.execute("CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT)")
            for block in prepared:
                cursor = conn.execute("INSERT INTO chunks VALUES(?,?,?,?,?,?,?,?)", tuple(block[k] for k in ("id", "path", "title", "text", "start_line", "end_line", "start_char", "end_char")))
                conn.execute("INSERT INTO chunks_fts(rowid,title,path,text) VALUES(?,?,?,?)", (cursor.lastrowid, block["title"], block["path"], block["text"]))
            conn.execute("INSERT INTO metadata VALUES('manifest',?)", (json_text(metadata),))
    except BaseException:
        output.unlink(missing_ok=True)
        raise
    return {**metadata, "index_sha256": sha256_file(output)}


def raw_search(database: Path, query: str, limit: int) -> dict:
    rewritten = " ".join(query.split())
    if not rewritten:
        raise ValueError("empty search query")
    terms = list(dict.fromkeys((phrase or word).casefold() for phrase, word in re.findall(r'"([^"]+)"|(\S+)', rewritten)))
    if not terms:
        raise ValueError("empty literal query")
    with sqlite3.connect(database.absolute().as_uri() + "?mode=ro&immutable=1", uri=True) as conn:
        conn.row_factory = sqlite3.Row
        candidates = conn.execute("SELECT * FROM chunks ORDER BY path,start_char").fetchall()
    ranked = []
    for row in candidates:
        body = row["text"].casefold()
        occurrences = [body.count(term) for term in terms]
        matched = sum(count > 0 for count in occurrences)
        if matched:
            density = sum(occurrences) / max(1, len(body))
            ranked.append(((-matched, -density, row["path"], row["start_char"]), row))
    ranked.sort(key=lambda pair: pair[0])
    evidence = []
    for rank, row in ranked[:limit]:
        source = {key: row[key] for key in ("path", "start_line", "end_line", "start_char", "end_char")}
        evidence.append({"id": row["id"], "title": row["title"], "text": row["text"],
                         "kind": "original_text", "sources": [source], **source,
                         "provenance": "original_source", "matched_terms": -rank[0], "occurrence_density": -rank[1]})
    return {"evidence": evidence, "diagnostics": {"backend": "original-text literal substring OR (not BM25)",
            "ranking": "matched-term count DESC, occurrence density DESC, path ASC, start_char ASC",
            "query_terms": terms, "rewritten_query": rewritten, "hits": len(evidence), "total_matching_chunks": len(ranked)}}


def normalized_block(block: dict) -> dict:
    sources = block.get("sources") or []
    if not sources and block.get("path"):
        sources = [{key: block[key] for key in ("path", "start_line", "end_line") if key in block}]
    safe_sources = [{key: source[key] for key in ("path", "start_line", "end_line", "start_char", "end_char") if key in source}
                    for source in sources[:10]]
    result = {
        "id": "e:" + hashlib.sha256(str(block["id"]).encode()).hexdigest()[:20], "title": str(block.get("title", block.get("name", ""))),
        "text": str(block.get("text", block.get("body", ""))),
        "kind": str(block.get("kind", block.get("label", "knowledge"))),
        "provenance": str(block.get("provenance", "unknown")), "sources": safe_sources,
    }
    if safe_sources:
        result.update({key: safe_sources[0][key] for key in ("path", "start_line", "end_line") if key in safe_sources[0]})
    if len(sources) > len(safe_sources):
        result["sources_truncated"] = True
    relations = block.get("relations", block.get("edges"))
    if relations:
        result["relations"] = relations[:20]
        if len(relations) > 20:
            result["relations_truncated"] = True
    return result


def evidence_budget(blocks: list[dict], max_chars: int, top_k: int = 10) -> dict:
    """Count the exact JSON shown to the solver, including all metadata."""
    if max_chars < len(json_text({"evidence": [], "truncated": True})):
        return {"evidence": [], "serialized": "", "visible_chars": 0, "truncated": bool(blocks), "budget_exhausted": True}
    selected = []
    truncated = len(blocks) > top_k
    for source in blocks[:top_k]:
        block = normalized_block(source)
        candidate = {"evidence": selected + [block], "truncated": truncated}
        if len(json_text(candidate)) <= max_chars:
            selected.append(block)
            continue
        truncated = True
        text = block["text"]
        left, right, best = 0, len(text), None
        while left <= right:
            middle = (left + right) // 2
            clipped = {**block, "text": text[:middle], "text_truncated": True}
            if len(json_text({"evidence": selected + [clipped], "truncated": True})) <= max_chars:
                best, left = clipped, middle + 1
            else:
                right = middle - 1
        if best is not None and best["text"]:
            selected.append(best)
        break
    payload = {"evidence": selected, "truncated": truncated}
    serialized = json_text(payload)
    return {**payload, "serialized": serialized, "visible_chars": len(serialized), "budget_exhausted": False}


class RetrievalEngine:
    def __init__(self, arm: str, kg_db: Path | None = None, raw_db: Path | None = None,
                 adapter: Path | None = None, top_k: int = 10, timeout: float = 30):
        if arm not in ("raw", "fts", "graph") or not 1 <= top_k <= 10:
            raise ValueError("invalid retrieval arm or top-k")
        self.arm, self.kg_db, self.raw_db, self.adapter = arm, kg_db, raw_db, adapter
        self.top_k, self.timeout = top_k, timeout

    def search(self, query: str, max_chars: int = 10000) -> dict:
        started = time.monotonic()
        try:
            if not isinstance(query, str) or not query.strip() or len(query) > 2000:
                raise ValueError("search query must contain 1..2000 characters")
            if self.arm == "raw":
                if self.raw_db is None:
                    raise ValueError("raw database is required")
                result = raw_search(self.raw_db, query, self.top_k)
            else:
                if self.adapter is None or self.kg_db is None:
                    raise ValueError("Go adapter and knowledge database are required")
                process = subprocess.run([str(self.adapter), "--db", str(self.kg_db), "--mode", self.arm,
                                          "--query", query, "--limit", str(self.top_k)], capture_output=True, text=True, timeout=self.timeout)
                if process.returncode:
                    raise RuntimeError(f"Cairn adapter exit {process.returncode}: {process.stderr[-1500:]}")
                result = json.loads(process.stdout)
                if result.get("error"):
                    raise RuntimeError(str(result["error"]))
            limited = evidence_budget(result.get("evidence") or [], max_chars, self.top_k)
            diagnostics = dict(result.get("diagnostics", {}))
            diagnostics["evidence_id_map"] = {normalized_block(block)["id"]: str(block["id"]) for block in (result.get("evidence") or [])}
            return {"status": "ok", "query": query, **limited,
                    "diagnostics": diagnostics, "latency_ms": round((time.monotonic() - started) * 1000, 3)}
        except (ValueError, OSError, RuntimeError, sqlite3.Error, subprocess.SubprocessError) as exc:
            payload = json_text({"error": str(exc)[:1000], "evidence": []})
            if len(payload) > max_chars:
                payload = json_text({"error": "retrieval_failed", "evidence": []}) if max_chars >= 50 else ""
            return {"status": "error", "query": query, "error": str(exc)[:1500], "evidence": [],
                    "serialized": payload, "visible_chars": len(payload), "truncated": False,
                    "latency_ms": round((time.monotonic() - started) * 1000, 3)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    build = sub.add_parser("build-raw-index")
    build.add_argument("--source-root", type=Path, required=True)
    build.add_argument("--accepted-proof", type=Path, required=True)
    build.add_argument("--output", type=Path, required=True)
    build.add_argument("--chunk-chars", type=int, default=1800)
    build.add_argument("--overlap-chars", type=int, default=200)
    search = sub.add_parser("search")
    search.add_argument("--arm", choices=("raw", "fts", "graph"), required=True)
    search.add_argument("--query", required=True)
    search.add_argument("--raw-db", type=Path)
    search.add_argument("--kg-db", type=Path)
    search.add_argument("--adapter", type=Path)
    search.add_argument("--max-chars", type=int, default=10000)
    args = parser.parse_args()
    if args.command == "build-raw-index":
        result = build_raw_index(args.source_root, args.accepted_proof, args.output, args.chunk_chars, args.overlap_chars)
    else:
        result = RetrievalEngine(args.arm, args.kg_db, args.raw_db, args.adapter).search(args.query, args.max_chars)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
