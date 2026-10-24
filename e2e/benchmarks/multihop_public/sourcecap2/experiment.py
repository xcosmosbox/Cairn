#!/usr/bin/env python3
"""只DEV的单因子来源上限实验；one shared original FTS pool, no tuning."""
from __future__ import annotations

import argparse
import collections
import json
from pathlib import Path
import platform
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
BASE = HERE.parent
sys.path.insert(0, str(BASE))
import benchmark as baseline

ARMS = ("control_rank10", "treatment_sourcecap2")
LOSS_TYPES = ("visible_fact", "source_absent_pool50", "source_in_pool_absent_selected", "source_selected_wrong_chunks", "budget_only_loss")


def verify_registration():
    manifest = baseline.load(HERE / "manifest.json")
    for name, expected in manifest["baseline_file_sha256"].items():
        if baseline.file_hash(BASE / name) != expected:
            raise ValueError(f"immutable baseline changed: {name}")
    if baseline.file_hash(HERE / "baseline-diagnostic.json") != manifest["diagnostic_aggregate_sha256"]:
        raise ValueError("registered diagnostic aggregate changed")
    if manifest["shared_candidate_pool"]["limit"] != 50 or manifest["control"]["final_top_k"] != 10 or manifest["treatment"]["per_doc_cap"] != 2 or manifest["treatment"]["final_top_k"] != 10:
        raise ValueError("registered single-factor parameters changed")
    return manifest


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


def assert_baseline_control(control):
    frozen = baseline.load(BASE / "dev-summary.json")["arms"]["raw_source_fts_sqlite"]
    for section in ("overall", "by_question_type", "by_unique_support_doc_count", "by_evidence_fact_count"):
        pairs = [(control[section], frozen[section])] if section == "overall" else [(control[section][key], frozen[section][key]) for key in frozen[section]]
        for actual, expected in pairs:
            for key, value in expected["macro"].items():
                if value is None:
                    if actual["macro"][key] is not None:
                        raise ValueError("control macro no longer matches frozen baseline")
                elif abs(actual["macro"][key] - value) > 1e-12:
                    raise ValueError(f"control no longer matches frozen baseline: {section}/{key}")


def run(data_root, database):
    manifest = verify_registration()
    protocol = baseline.load(BASE / "protocol.json")
    receipt = baseline.load(BASE / "index-receipt.json")
    if receipt["index_sha256"] != manifest["index_sha256"]:
        raise ValueError("registered index differs")
    documents = baseline.validate_corpus(baseline.read_source(data_root, "corpus.json"))
    frozen = baseline.load(BASE / "split-freeze.json")
    dev_ids = {row["qid"] for row in frozen["selected"]["dev"]}
    # 只为DEV加载评分标签；HOLDOUT只保留冻结ID，never evaluated or returned.
    dev_source_rows = [row for row in baseline.read_source(data_root, "MultiHopRAG.json") if baseline.query_id(row["query"]) in dev_ids]
    questions = baseline.prepare_questions(dev_source_rows, documents)
    if {row["qid"] for row in questions} != dev_ids or len(questions) != 128:
        raise ValueError("DEV roster differs from frozen registration")
    retriever = baseline.Retriever(database, receipt)
    rows_by_arm = {arm: [] for arm in ARMS}
    paired = []
    pool_costs = []
    pool_hashes = []
    try:
        for question in sorted(questions, key=lambda q: q["qid"]):
            started = time.perf_counter()
            pool_error = None
            try:
                pool = retriever.search({key: question[key] for key in ("qid", "query")}, "raw_source_fts_sqlite", 50)
            except (ValueError, baseline.sqlite3.Error) as exc:
                pool, pool_error = [], str(exc)
            pool_costs.append((time.perf_counter() - started) * 1000)
            pool_identity = baseline.digest(baseline.canonical([hit["chunk_id"] for hit in pool]))
            pool_hashes.append({"qid": question["qid"], "pool_sha256": pool_identity, "candidates": len(pool)})
            facts = {}
            selected_facts = {}
            scored = {}
            for arm in ARMS:
                start = time.perf_counter()
                error_detail = pool_error
                try:
                    selected = select_chunks(pool, arm)
                    visible, truncated, chars = baseline.pack_context(selected, protocol["context_chars"])
                except (ValueError, KeyError) as exc:
                    selected, visible, truncated, chars = [], [], False, 0
                    error_detail = str(exc)
                elapsed = (time.perf_counter() - start) * 1000
                losses, facts[arm], selected_facts[arm] = loss_breakdown(question, pool, selected, visible, documents)
                score = baseline.score_question(question, selected, visible, documents)
                row = {"qid": question["qid"], "question_type": question["question_type"], "pool_sha256": pool_identity,
                       "error": error_detail is not None, "error_detail": error_detail, "truncated": truncated, "latency_ms": elapsed,
                       "unique_selected_docs": len({hit["doc_id"] for hit in selected}), "unique_visible_docs": len({hit["doc_id"] for hit in visible}),
                       "visible_chunks": len(visible), "visible_chars": chars, "visible_body_chars": sum(len(hit["text"]) for hit in visible),
                       "serialization_metadata_chars": chars - sum(len(hit["text"]) for hit in visible), "fact_losses": losses, **score}
                rows_by_arm[arm].append(row)
                scored[arm] = row
            positive = bool(question["evidence_list"])
            delta_keys = ("doc_recall_at_10", "all_gold_docs_at_10", "visible_doc_recall", "visible_all_gold_docs", "fact_recall", "all_gold_facts", "unique_visible_docs")
            paired.append({"qid": question["qid"], "question_type": question["question_type"], "unique_support_doc_count": scored[ARMS[0]]["unique_support_doc_count"],
                           "evidence_fact_count": scored[ARMS[0]]["evidence_fact_count"], "pool_sha256": pool_identity,
                           "delta": {key: scored[ARMS[1]][key] - scored[ARMS[0]][key] for key in delta_keys} if positive else {"unique_visible_docs": scored[ARMS[1]]["unique_visible_docs"] - scored[ARMS[0]]["unique_visible_docs"]},
                           "visible_facts_gained": len(facts[ARMS[1]] - facts[ARMS[0]]), "visible_facts_lost": len(facts[ARMS[0]] - facts[ARMS[1]]),
                           "control_visible_fact_removed_from_selected_by_cap": len(facts[ARMS[0]] - selected_facts[ARMS[1]]),
                           "control": scored[ARMS[0]], "treatment": scored[ARMS[1]]})
    finally:
        retriever.close()
    if baseline.file_hash(database) != manifest["index_sha256"]:
        raise ValueError("index changed during read-only experiment")
    summaries = {arm: summarize_arm(rows, protocol) for arm, rows in rows_by_arm.items()}
    assert_baseline_control(summaries[ARMS[0]])
    report = {"format": "cairn-mhr-sourcecap2-results/v1", "manifest_sha256": baseline.file_hash(HERE / "manifest.json"),
              "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=HERE, text=True).strip(),
              "implementation_sha256": baseline.file_hash(Path(__file__)), "baseline_implementation_sha256": baseline.file_hash(BASE / "benchmark.py"),
              "index_sha256_before_after": manifest["index_sha256"], "environment": {"python": platform.python_version(), "sqlite": baseline.sqlite3.sqlite_version},
              "shared_pool_queries": len(pool_costs), "shared_candidate_pool_limit": 50, "shared_pool_identity_sha256": baseline.digest(baseline.canonical(pool_hashes)),
              "shared_pool_retrieval_ms": {"p50": baseline.percentile(pool_costs, .5), "p95": baseline.percentile(pool_costs, .95)},
              "control_matches_frozen_baseline_metrics": True, "arms": summaries, "paired_positive_queries": paired_summary(paired),
              "paired_fact_counts": {"gained": sum(row["visible_facts_gained"] for row in paired), "lost": sum(row["visible_facts_lost"] for row in paired),
                                     "control_visible_fact_removed_from_selected_by_cap": sum(row["control_visible_fact_removed_from_selected_by_cap"] for row in paired)},
              "holdout": "frozen_not_evaluated", "real_graph": "not_run_no_real_graph", "answer_metrics": "not_measured_no_solver",
              "interpretation": "paired DEV source-localization evidence only; not generalization, answer, latency, or GraphRAG benefit"}
    return report, paired


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data-root", type=Path, required=True)
    parser.add_argument("--index", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--paired-output", type=Path, required=True)
    args = parser.parse_args()
    started = time.monotonic()
    report, paired = run(args.data_root, args.index)
    report["execution"] = {"exit_code": 0, "seconds": time.monotonic() - started,
                           "command": "python3 e2e/benchmarks/multihop_public/sourcecap2/experiment.py --data-root <verified-data> --index <baseline-index> --output <new-output> --paired-output <new-paired-output>"}
    baseline.write_new(args.output, report)
    baseline.write_new(args.paired_output, paired)


if __name__ == "__main__":
    main()
