#!/usr/bin/env python3
"""固定全库的免费词面基准；labels stay outside the retriever/index.

Only stdlib is required. This is an external source-text baseline, not Cairn
node FTS, a real graph build, the historical paid raw control, or answer grading.
"""
from __future__ import annotations

import argparse
import collections
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import sqlite3
import subprocess
import tempfile
import time
import unicodedata
import urllib.request

HERE = Path(__file__).resolve().parent


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def digest(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def file_hash(path):
    hasher = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(block)
    return hasher.hexdigest()


def load(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def write_new(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("x", encoding="utf-8") as stream:
        stream.write(json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
        stream.flush()
        os.fsync(stream.fileno())


def checked(path, expected):
    path = Path(path)
    if path.is_symlink() or path.stat().st_size != expected["bytes"] or file_hash(path) != expected["sha256"]:
        raise ValueError(f"source identity mismatch: {path.name}")
    return path


def source_url(manifest, name):
    return f"https://huggingface.co/datasets/{manifest['dataset']}/resolve/{manifest['revision']}/{name}"


def download(root):
    """仅下载三个固定文件；never execute upstream dataset/model scripts."""
    manifest = load(HERE / "manifest.json")
    root.mkdir(parents=True, exist_ok=True)
    receipt = {}
    for name, expected in manifest["files"].items():
        target = root / name
        if not target.exists():
            descriptor, temporary_name = tempfile.mkstemp(prefix=name + ".", suffix=".partial", dir=root)
            temporary = Path(temporary_name)
            try:
                with os.fdopen(descriptor, "wb") as out, urllib.request.urlopen(source_url(manifest, name), timeout=120) as response:
                    while block := response.read(1024 * 1024):
                        out.write(block)
                checked(temporary, expected)
                # 不覆盖已有输入；a racing writer must not replace frozen bytes.
                os.link(temporary, target)
            finally:
                temporary.unlink(missing_ok=True)
        checked(target, expected)
        receipt[name] = {**expected, "url": source_url(manifest, name)}
    return receipt


def read_source(root, name):
    manifest = load(HERE / "manifest.json")
    value = load(checked(root / name, manifest["files"][name]))
    if len(value) != manifest["files"][name]["records"]:
        raise ValueError(f"record count mismatch: {name}")
    return value


def normalize(query):
    return " ".join(unicodedata.normalize("NFKC", query).casefold().split())


def query_id(query):
    return digest(normalize(query))


def document_id(url):
    return digest(url)


def validate_corpus(corpus):
    documents = {}
    required = {"title", "author", "source", "published_at", "category", "url", "body"}
    for row in corpus:
        # Exact allowed columns also reject accidentally passing QA/label rows.
        if set(row) != required or not isinstance(row["body"], str) or not row["body"].strip():
            raise ValueError("corpus schema/body mismatch; only original corpus fields allowed")
        if not isinstance(row["url"], str) or not row["url"] or row["url"] in documents:
            raise ValueError("corpus URLs must be nonempty and unique")
        documents[row["url"]] = row
    if not documents:
        raise ValueError("empty corpus")
    return documents


def prepare_questions(questions, documents):
    result = []
    seen = set()
    for row in questions:
        if set(row) != {"query", "answer", "question_type", "evidence_list"}:
            raise ValueError("question schema mismatch")
        qid = query_id(row["query"])
        if qid in seen:
            raise ValueError("duplicate normalized question")
        seen.add(qid)
        evidence = row["evidence_list"]
        is_null = row["question_type"] == "null_query"
        if is_null != (not evidence):
            raise ValueError("null/support mismatch")
        for item in evidence:
            if item["url"] not in documents or not item.get("fact") or item["fact"] not in documents[item["url"]]["body"]:
                raise ValueError("gold source/fact is not an exact original body span")
        urls = sorted({item["url"] for item in evidence})
        key = "null:" + normalize(row["query"]) if is_null else "docs:" + "\n".join(urls)
        result.append({**row, "qid": qid, "group_hash": digest("cairn-mhr-v1\0" + key)})
    return result


def assigned_split(row):
    return "dev" if int(row["group_hash"][:8], 16) % 5 == 0 else "holdout"


def near_duplicate_pairs(rows, threshold=0.85):
    """跨split近重复只报告hash并阻断；never reseed to hide leakage."""
    shingles = []
    for row in rows:
        tokens = re.findall(r"\w+", normalize(row["query"]))
        sets = {tuple(tokens[i:i + 5]) for i in range(len(tokens) - 4)}
        if not sets:
            sets = {tuple(tokens)}
        shingles.append(sets)
    inverted = collections.defaultdict(list)
    pairs = set()
    for i, row in enumerate(rows):
        candidates = set()
        for shingle in shingles[i]:
            candidates.update(inverted[shingle])
        for j in candidates:
            if assigned_split(row) == assigned_split(rows[j]):
                continue
            a, b = shingles[i], shingles[j]
            if len(a & b) / len(a | b) >= threshold:
                pairs.add(tuple(sorted((row["qid"], rows[j]["qid"]))))
        for shingle in shingles[i]:
            inverted[shingle].append(i)
    return sorted(pairs)


def freeze_rows(rows, protocol):
    pairs = near_duplicate_pairs(rows, protocol["near_duplicate_jaccard"])
    if pairs:
        raise ValueError("cross-split near duplicates; quarantine and coordinate, do not reseed: " + canonical(pairs))
    selected = {"dev": [], "holdout": []}
    available = {}
    for split in selected:
        available[split] = {}
        for kind in protocol["types"]:
            pool = sorted((row for row in rows if row["question_type"] == kind and assigned_split(row) == split), key=lambda r: r["qid"])
            count = protocol[split + "_per_type"]
            available[split][kind] = len(pool)
            if len(pool) < count:
                raise ValueError(f"insufficient frozen {split}/{kind}: {len(pool)} < {count}; do not reseed")
            selected[split].extend({k: row[k] for k in ("qid", "question_type", "group_hash")} for row in pool[:count])
    smoke = [row["qid"] for kind in protocol["types"] for row in [r for r in selected["dev"] if r["question_type"] == kind][:2]]
    return {"format": "cairn-mhr-split/v1", "source_manifest_sha256": file_hash(HERE / "manifest.json"),
            "protocol_sha256": file_hash(HERE / "protocol.json"), "available": available,
            "audit": {"questions": len(rows), "groups": len({r["group_hash"] for r in rows}), "gold_facts": sum(len(r["evidence_list"]) for r in rows), "unmapped_sources": 0, "non_exact_body_facts": 0, "cross_split_near_duplicates": pairs},
            "selected": selected, "smoke_qids": smoke, "holdout_status": "frozen_not_evaluated"}


def visible_metadata(document):
    return {key: document[key] for key in ("title", "source", "published_at")}


def indexed_text(metadata, body):
    return canonical(metadata) + "\n" + body


def chunks(document, protocol):
    body = document["body"]
    tokens = list(re.finditer(r"\S+", body))
    body_hash = digest(body)
    doc_id = document_id(document["url"])
    for position in range(0, len(tokens), protocol["stride_tokens"]):
        end_token = min(position + protocol["chunk_tokens"], len(tokens))
        start, end = tokens[position].start(), tokens[end_token - 1].end()
        yield {"chunk_id": digest(f"{doc_id}\0{start}\0{end}\0{body_hash}"), "doc_id": doc_id,
               "start": start, "end": end, "text": body[start:end]}
        if end_token == len(tokens):
            break


def build_index(corpus, output, protocol, corpus_sha256):
    """构建函数只接收corpus；question/gold never enter this process contract."""
    documents = validate_corpus(corpus)
    if output.exists():
        raise FileExistsError(f"refusing to overwrite index: {output}")
    output.parent.mkdir(parents=True, exist_ok=True)
    inventory = []
    try:
        with sqlite3.connect(output) as db:
            db.executescript("CREATE TABLE documents(doc_id TEXT PRIMARY KEY,url TEXT,body_sha256 TEXT,metadata TEXT);"
                             "CREATE TABLE chunks(chunk_id TEXT PRIMARY KEY,doc_id TEXT,start INTEGER,end INTEGER,text TEXT);"
                             "CREATE VIRTUAL TABLE chunks_fts USING fts5(text,tokenize='unicode61');"
                             "CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT);")
            total = 0
            for url in sorted(documents):
                document = documents[url]
                doc_id = document_id(url)
                db.execute("INSERT INTO documents VALUES(?,?,?,?)", (doc_id, url, digest(document["body"]), canonical({k: v for k, v in document.items() if k != "body"})))
                count = 0
                for chunk in chunks(document, protocol):
                    cursor = db.execute("INSERT INTO chunks VALUES(?,?,?,?,?)", tuple(chunk[k] for k in ("chunk_id", "doc_id", "start", "end", "text")))
                    db.execute("INSERT INTO chunks_fts(rowid,text) VALUES(?,?)", (cursor.lastrowid, indexed_text(visible_metadata(document), chunk["text"])))
                    count += 1
                inventory.append({"doc_id": doc_id, "body_sha256": digest(document["body"]), "chunks": count})
                total += count
            metadata = {"format": "cairn-mhr-source-index/v1", "documents": len(documents), "chunks": total,
                        "corpus_sha256": corpus_sha256, "protocol_sha256": file_hash(HERE / "protocol.json"),
                        "source_inventory_sha256": digest(canonical(inventory)), "index_scope": "original title/source/published_at and body only; no questions, answers, gold facts or graph"}
            db.execute("INSERT INTO metadata VALUES('manifest',?)", (canonical(metadata),))
    except BaseException:
        output.unlink(missing_ok=True)
        raise
    return {**metadata, "index_sha256": file_hash(output), "index_bytes": output.stat().st_size}


class Retriever:
    """只接收qid/query；DB以immutable只读打开，不接受label字段。"""
    def __init__(self, database, receipt):
        self.path = database.resolve()
        for suffix in ("-wal", "-journal"):
            if Path(str(self.path) + suffix).exists() and Path(str(self.path) + suffix).stat().st_size:
                raise ValueError("index must be closed/checkpointed without a live journal")
        if file_hash(self.path) != receipt["index_sha256"]:
            raise ValueError("index hash mismatch")
        self.db = sqlite3.connect(self.path.as_uri() + "?mode=ro&immutable=1", uri=True)
        self.db.row_factory = sqlite3.Row
        metadata = json.loads(self.db.execute("SELECT value FROM metadata WHERE key='manifest'").fetchone()[0])
        if any(metadata[k] != receipt[k] for k in metadata):
            self.close()
            raise ValueError("index receipt metadata mismatch")
        self.raw_rows = self.db.execute("SELECT c.*,d.metadata FROM chunks c JOIN documents d USING(doc_id) ORDER BY c.chunk_id").fetchall()

    def close(self):
        self.db.close()

    def search(self, request, arm, top_k):
        if set(request) != {"qid", "query"} or query_id(request["query"]) != request["qid"]:
            raise ValueError("retriever accepts only matching qid/query; labels forbidden")
        query = request["query"]
        if not query.strip():
            raise ValueError("blank query")
        if arm == "raw_source_fts_sqlite":
            expression = " OR ".join('"' + atom.replace('"', '""') + '"' for atom in query.split())
            rows = self.db.execute("SELECT c.*,d.metadata,bm25(chunks_fts) AS rank FROM chunks_fts JOIN chunks c ON c.rowid=chunks_fts.rowid JOIN documents d USING(doc_id) WHERE chunks_fts MATCH ? ORDER BY rank ASC,c.chunk_id ASC LIMIT ?", (expression, top_k)).fetchall()
            return [{**dict(row), "metadata": visible_metadata(json.loads(row["metadata"]))} for row in rows]
        if arm != "raw_lexical_sanity":
            raise ValueError("unsupported arm; no real graph is available")
        terms = list(dict.fromkeys((phrase or word).casefold() for phrase, word in re.findall(r'"([^"]+)"|(\S+)', query)))
        ranked = []
        for row in self.raw_rows:
            metadata = visible_metadata(json.loads(row["metadata"]))
            search_text = indexed_text(metadata, row["text"]).casefold()
            counts = [search_text.count(term) for term in terms]
            matched = sum(count > 0 for count in counts)
            if matched:
                ranked.append(((-matched, -sum(counts) / max(1, len(search_text)), row["chunk_id"]), {**dict(row), "metadata": metadata}))
        return [row for _, row in sorted(ranked, key=lambda pair: pair[0])[:top_k]]


def pack_context(hits, max_chars):
    """预算计算展示JSON；trim final body and preserve its exact visible interval."""
    visible = []
    truncated = False
    for hit in hits:
        block = {key: hit[key] for key in ("chunk_id", "doc_id", "metadata", "start", "end", "text")}
        if len(canonical({"evidence": visible + [block]})) <= max_chars:
            visible.append(block)
            continue
        truncated = True
        low, high, best = 1, len(block["text"]), None
        while low <= high:
            size = (low + high) // 2
            clipped = {**block, "end": block["start"] + size, "text": block["text"][:size]}
            if len(canonical({"evidence": visible + [clipped]})) <= max_chars:
                best, low = clipped, size + 1
            else:
                high = size - 1
        if best:
            visible.append(best)
        break
    payload = {"evidence": visible}
    if len(canonical(payload)) > max_chars:
        raise ValueError("context budget too small for empty payload")
    return visible, truncated, len(canonical(payload))


def interval_covered(start, end, intervals):
    cursor = start
    for left, right in sorted(intervals):
        if left > cursor:
            break
        cursor = max(cursor, right)
        if cursor >= end:
            return True
    return False


def score_question(question, hits, visible, documents):
    gold = question["evidence_list"]
    urls = {item["url"] for item in gold}
    gold_docs = {document_id(url) for url in urls}
    metrics = {"unique_support_doc_count": len(gold_docs), "evidence_fact_count": len(gold), "retrieved_count": len(hits)}
    if not gold:
        return metrics
    for k in (5, 10):
        found = {hit["doc_id"] for hit in hits[:k]} & gold_docs
        metrics[f"doc_hit_at_{k}"] = int(bool(found))
        metrics[f"doc_recall_at_{k}"] = len(found) / len(gold_docs)
        metrics[f"all_gold_docs_at_{k}"] = int(found == gold_docs)
    intervals = collections.defaultdict(list)
    for block in visible:
        intervals[block["doc_id"]].append((block["start"], block["end"]))
    visible_found = set(intervals) & gold_docs
    metrics["visible_doc_recall"] = len(visible_found) / len(gold_docs)
    metrics["visible_all_gold_docs"] = int(visible_found == gold_docs)
    # 同一doc/fact只算一次，任一原文出现范围完整可见即可；metadata never counts.
    facts = {(item["url"], item["fact"]) for item in gold}
    supported = 0
    for url, fact in facts:
        body = documents[url]["body"]
        occurrences = []
        start = body.find(fact)
        while start >= 0:
            occurrences.append((start, start + len(fact)))
            start = body.find(fact, start + 1)
        supported += any(interval_covered(a, b, intervals[document_id(url)]) for a, b in occurrences)
    metrics.update(fact_recall=supported / len(facts), all_gold_facts=int(supported == len(facts)))
    return metrics


def percentile(values, fraction):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * fraction) - 1)] if ordered else None


def summarize(rows):
    positives = [row for row in rows if row["unique_support_doc_count"]]
    nulls = [row for row in rows if not row["unique_support_doc_count"]]
    keys = ("doc_hit_at_5", "doc_hit_at_10", "doc_recall_at_5", "doc_recall_at_10", "all_gold_docs_at_5", "all_gold_docs_at_10", "visible_doc_recall", "visible_all_gold_docs", "fact_recall", "all_gold_facts")
    return {"queries": len(rows), "positive_queries": len(positives), "null_queries": len(nulls),
            "macro": {key: sum(row[key] for row in positives) / len(positives) if positives else None for key in keys},
            "empty_rate": sum(not row["retrieved_count"] for row in rows) / len(rows) if rows else None,
            "error_rate": sum(row["error"] for row in rows) / len(rows) if rows else None,
            "budget_truncation_rate": sum(row["truncated"] for row in rows) / len(rows) if rows else None,
            "null": {"retrieved_count_mean": sum(row["retrieved_count"] for row in nulls) / len(nulls) if nulls else None,
                     "empty_rate": sum(not row["retrieved_count"] for row in nulls) / len(nulls) if nulls else None,
                     "answer_abstention_accuracy": "not_measured"},
            "latency_ms": {"p50": percentile([r["latency_ms"] for r in rows], .5), "p95": percentile([r["latency_ms"] for r in rows], .95)}}


def run_dev(root, database, receipt_path, freeze_path):
    protocol = load(HERE / "protocol.json")
    corpus = read_source(root, "corpus.json")
    documents = validate_corpus(corpus)
    questions = prepare_questions(read_source(root, "MultiHopRAG.json"), documents)
    frozen = load(freeze_path)
    if frozen != freeze_rows(questions, protocol):
        raise ValueError("frozen split/protocol/source identity differs")
    receipt = load(receipt_path)
    if receipt["corpus_sha256"] != file_hash(root / "corpus.json") or receipt["protocol_sha256"] != file_hash(HERE / "protocol.json"):
        raise ValueError("index corpus/protocol binding mismatch")
    dev_ids = {row["qid"] for row in frozen["selected"]["dev"]}
    retriever = Retriever(database, receipt)
    arms = {}
    try:
        for arm in ("raw_lexical_sanity", "raw_source_fts_sqlite"):
            rows = []
            for question in sorted((q for q in questions if q["qid"] in dev_ids), key=lambda q: q["qid"]):
                started = time.perf_counter()
                error = False
                error_detail = None
                try:
                    hits = retriever.search({k: question[k] for k in ("qid", "query")}, arm, protocol["top_k"])
                    visible, truncated, chars = pack_context(hits, protocol["context_chars"])
                except (sqlite3.Error, ValueError) as exc:
                    # 错误占固定分母，绝不冒充空命中成功；raise details in per-query audit.
                    hits, visible, truncated, chars, error = [], [], False, 0, True
                    error_detail = str(exc)
                elapsed = (time.perf_counter() - started) * 1000
                rows.append({"qid": question["qid"], "question_type": question["question_type"], "error": error,
                             "truncated": truncated, "visible_chars": chars, "latency_ms": elapsed, "error_detail": error_detail,
                             "retrieved_doc_ids": [hit["doc_id"] for hit in hits],
                             "visible_intervals": [{k: block[k] for k in ("chunk_id", "doc_id", "start", "end")} for block in visible],
                             **score_question(question, hits, visible, documents)})
            arms[arm] = {"overall": summarize(rows),
                         "by_question_type": {kind: summarize([r for r in rows if r["question_type"] == kind]) for kind in protocol["types"]},
                         "by_unique_support_doc_count": {str(n): summarize([r for r in rows if r["unique_support_doc_count"] == n]) for n in sorted({r["unique_support_doc_count"] for r in rows})},
                         "by_evidence_fact_count": {str(n): summarize([r for r in rows if r["evidence_fact_count"] == n]) for n in sorted({r["evidence_fact_count"] for r in rows})},
                         "query_audit": rows}
    finally:
        retriever.close()
    if file_hash(database) != receipt["index_sha256"]:
        raise ValueError("index changed during read-only evaluation")
    return {"format": "cairn-mhr-dev-results/v1", "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=HERE, text=True).strip(),
            "implementation_sha256": file_hash(Path(__file__)), "protocol_sha256": file_hash(HERE / "protocol.json"),
            "source_manifest_sha256": file_hash(HERE / "manifest.json"), "split_sha256": file_hash(freeze_path),
            "environment": {"python": platform.python_version(), "sqlite": sqlite3.sqlite_version, "platform": platform.platform()},
            "index": receipt, "split": "dev", "holdout": "frozen_not_evaluated", "real_graph": "not_run_no_real_graph",
            "answer_metrics": "not_measured_no_solver", "interpretation": "external source lexical retrieval only; no Cairn or GraphRAG benefit claim", "arms": arms}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("download")
    p.add_argument("--data-root", type=Path, required=True)
    p = sub.add_parser("freeze")
    p.add_argument("--data-root", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    p = sub.add_parser("build-index")
    p.add_argument("--data-root", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--receipt", type=Path, required=True)
    p = sub.add_parser("run-dev")
    p.add_argument("--data-root", type=Path, required=True)
    p.add_argument("--index", type=Path, required=True)
    p.add_argument("--receipt", type=Path, required=True)
    p.add_argument("--freeze", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "download":
        print(canonical(download(args.data_root)))
    elif args.command == "freeze":
        documents = validate_corpus(read_source(args.data_root, "corpus.json"))
        write_new(args.output, freeze_rows(prepare_questions(read_source(args.data_root, "MultiHopRAG.json"), documents), load(HERE / "protocol.json")))
    elif args.command == "build-index":
        write_new(args.receipt, build_index(read_source(args.data_root, "corpus.json"), args.output, load(HERE / "protocol.json"), file_hash(args.data_root / "corpus.json")))
    else:
        write_new(args.output, run_dev(args.data_root, args.index, args.receipt, args.freeze))


if __name__ == "__main__":
    main()
