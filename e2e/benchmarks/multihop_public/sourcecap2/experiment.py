"""历史 DEV 来源上限实验的纯辅助函数；不在当前源码重新执行旧实验。

Pure helpers for the recorded source-cap experiment. Reproduce historical
results from their recorded source_commit, not the evolving current tree.
"""
from __future__ import annotations

import collections
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
BASE = HERE.parent
sys.path.insert(0, str(BASE))
import benchmark as baseline

ARMS = ("control_rank10", "treatment_sourcecap2")
LOSS_TYPES = ("visible_fact", "source_absent_pool50", "source_in_pool_absent_selected", "source_selected_wrong_chunks", "budget_only_loss")




def select_chunks(pool, arm):
    """保留原rank，不补搜；selection cannot see labels or mutate the shared pool."""
    if arm == "control_rank10":
        return pool[:10]
    if arm != "treatment_sourcecap2":
        raise ValueError("unknown experimental arm")
    selected = []
    counts = collections.Counter()
    for hit in pool:
        if counts[hit["doc_id"]] == 2:
            continue
        selected.append(hit)
        counts[hit["doc_id"]] += 1
        if len(selected) == 10:
            break
    return selected


def fact_coverage(question, hits, documents):
    intervals = collections.defaultdict(list)
    for hit in hits:
        intervals[hit["doc_id"]].append((hit["start"], hit["end"]))
    covered = set()
    for url, fact in {(e["url"], e["fact"]) for e in question["evidence_list"]}:
        body = documents[url]["body"]
        start = body.find(fact)
        while start >= 0:
            if baseline.interval_covered(start, start + len(fact), intervals[baseline.document_id(url)]):
                covered.add((url, fact))
                break
            start = body.find(fact, start + 1)
    return covered


def loss_breakdown(question, pool, selected, visible, documents):
    visible_facts = fact_coverage(question, visible, documents)
    selected_facts = fact_coverage(question, selected, documents)
    pool_docs = {hit["doc_id"] for hit in pool}
    selected_docs = {hit["doc_id"] for hit in selected}
    counts = dict.fromkeys(LOSS_TYPES, 0)
    for item in {(e["url"], e["fact"]) for e in question["evidence_list"]}:
        doc_id = baseline.document_id(item[0])
        if item in visible_facts:
            counts["visible_fact"] += 1
        elif doc_id not in pool_docs:
            counts["source_absent_pool50"] += 1
        elif doc_id not in selected_docs:
            counts["source_in_pool_absent_selected"] += 1
        elif item not in selected_facts:
            counts["source_selected_wrong_chunks"] += 1
        else:
            counts["budget_only_loss"] += 1
    return counts, visible_facts, selected_facts


def summarize(rows):
    result = baseline.summarize(rows)
    for key in ("unique_selected_docs", "unique_visible_docs", "visible_chunks", "visible_chars", "visible_body_chars", "serialization_metadata_chars"):
        result[key + "_mean"] = sum(row[key] for row in rows) / len(rows) if rows else None
    result["selected_chunk_count_distribution"] = dict(sorted(collections.Counter(row["retrieved_count"] for row in rows).items()))
    result["fact_loss_counts"] = {key: sum(row["fact_losses"][key] for row in rows) for key in LOSS_TYPES}
    result["selection_packing_ms"] = {"p50": baseline.percentile([r["latency_ms"] for r in rows], .5), "p95": baseline.percentile([r["latency_ms"] for r in rows], .95)}
    del result["latency_ms"]
    return result


def summarize_arm(rows, protocol):
    return {"overall": summarize(rows),
            "by_question_type": {kind: summarize([r for r in rows if r["question_type"] == kind]) for kind in protocol["types"]},
            "by_unique_support_doc_count": {str(n): summarize([r for r in rows if r["unique_support_doc_count"] == n]) for n in sorted({r["unique_support_doc_count"] for r in rows})},
            "by_evidence_fact_count": {str(n): summarize([r for r in rows if r["evidence_fact_count"] == n]) for n in sorted({r["evidence_fact_count"] for r in rows})}}


def paired_summary(rows):
    positives = [row for row in rows if row["unique_support_doc_count"]]
    metrics = ("doc_recall_at_10", "all_gold_docs_at_10", "visible_doc_recall", "visible_all_gold_docs", "fact_recall", "all_gold_facts", "unique_visible_docs")
    return {key: {direction: sum((row["delta"][key] > 0 if direction == "gain" else row["delta"][key] < 0 if direction == "loss" else row["delta"][key] == 0) for row in positives) for direction in ("gain", "loss", "tie")} for key in metrics}
