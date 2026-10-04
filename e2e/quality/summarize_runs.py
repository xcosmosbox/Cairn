#!/usr/bin/env python3
"""Aggregate frozen solver runs by arm and dataset split without grading answers.

Provenance-path overlap measures declared source-path coverage only. It is not
semantic recall, factual correctness, citation support, or an answer-quality score.
This command does not call a model, inspect original documents, or change data.
"""
from __future__ import annotations

import argparse
import collections
import hashlib
import json
import math
from pathlib import Path
from typing import Any


def file_sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def read_jsonl(path: Path) -> tuple[list[dict], int]:
    """An interrupted last write is reported, not confused with a complete row."""
    if not path.exists():
        return [], 0
    lines = path.read_text(encoding="utf-8").splitlines()
    output, partial = [], 0
    for index, line in enumerate(lines):
        if not line.strip():
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            if index != len(lines) - 1:
                raise ValueError(f"interior malformed JSONL row in {path.name}")
            partial += 1
            continue
        if not isinstance(record, dict):
            raise ValueError(f"non-object JSONL row in {path.name}")
        output.append(record)
    return output, partial


def dataset_rows(path: Path) -> list[dict]:
    if path.suffix == ".jsonl":
        rows, partial = read_jsonl(path)
        if partial:
            raise ValueError("dataset is incomplete")
        return rows
    data = json.loads(path.read_text(encoding="utf-8"))
    if isinstance(data, dict):
        data = data.get("questions", data.get("items"))
    if not isinstance(data, list):
        raise ValueError("dataset must be a list or questions/items object")
    return data


def question_id(item: dict) -> str:
    identity = item.get("question_id", item.get("id"))
    if identity is None:
        raise ValueError("question id is required for a fixed-cohort summary")
    return str(identity)


def path_set(value: Any) -> set[str]:
    paths = set()
    if isinstance(value, dict):
        for key, child in value.items():
            if key in ("path", "file_path", "source_path", "file") and isinstance(child, str):
                paths.add(child.removeprefix("./").replace("\\", "/"))
            elif key in ("evidence", "sources", "evidence_paths", "source_paths"):
                paths.update(path_set(child))
    elif isinstance(value, list):
        for child in value:
            paths.update(path_set(child))
    elif isinstance(value, str) and ("/" in value or value.endswith((".md", ".txt"))):
        paths.add(value.removeprefix("./").replace("\\", "/"))
    return paths


def quantiles(values: list[float]) -> dict:
    finite = sorted(float(value) for value in values if isinstance(value, (int, float)) and math.isfinite(value))
    def percentile(p):
        if not finite:
            return None
        position = (len(finite) - 1) * p
        lo, hi = math.floor(position), math.ceil(position)
        return round(finite[lo] + (finite[hi] - finite[lo]) * (position - lo), 6)
    return {"n": len(finite), "median": percentile(0.5), "p95": percentile(0.95)}


def average(values):
    return round(sum(values) / len(values), 6) if values else None


def split_for(identity: str, dataset: dict[str, dict]) -> str:
    return str(dataset.get(identity, {}).get("split", "unspecified"))


def overlap_metrics(result: dict, item: dict) -> dict | None:
    expected = path_set(item.get("evidence", [])) | path_set(item.get("evidence_paths", []))
    if not expected:
        return None
    blocks = result.get("retrieved_evidence") or []
    retrieved = set().union(*(path_set(block) for block in blocks)) if blocks else set()
    cited_ids = set(result.get("citations") or [])
    cited_blocks = [block for block in blocks if block.get("id") in cited_ids]
    cited = set().union(*(path_set(block) for block in cited_blocks)) if cited_blocks else set()
    return {"visible_fraction": len(expected & retrieved) / len(expected),
            "cited_fraction": len(expected & cited) / len(expected),
            "any_visible": bool(expected & retrieved), "any_cited": bool(expected & cited),
            "expected_path_count": len(expected)}


def meter_events(events: list[dict]) -> dict:
    requests, responses = {}, {}
    legacy = 0
    for record in events:
        identity = record.get("http_attempt")
        if identity is None:
            raise ValueError("HTTP ledger record is missing http_attempt")
        kind = record.get("event", "response")
        if "event" not in record:
            legacy += 1
        destination = requests if kind == "request" else responses if kind == "response" else None
        if destination is None:
            raise ValueError(f"unknown HTTP event kind: {kind}")
        if identity in destination:
            raise ValueError(f"duplicate {kind} event for one HTTP attempt")
        destination[identity] = record
    all_ids = requests.keys() | responses.keys()
    missing_response = requests.keys() - responses.keys()
    matched = requests.keys() & responses.keys()
    for identity in matched:
        for field in ("question_id", "arm", "turn", "request_sha256"):
            if requests[identity].get(field) != responses[identity].get(field):
                raise ValueError(f"mismatched HTTP request/response identity field: {field}")
    usage_rows = [r["usage"] for r in responses.values() if isinstance(r.get("usage"), dict)]
    hit_values, miss_values, reasoning_values = [], [], []
    for usage in usage_rows:
        hit = usage.get("prompt_cache_hit_tokens")
        if hit is None:
            hit = (usage.get("prompt_tokens_details") or {}).get("cached_tokens")
        if isinstance(hit, (int, float)):
            hit_values.append(hit)
        miss = usage.get("prompt_cache_miss_tokens")
        if isinstance(miss, (int, float)):
            miss_values.append(miss)
        reasoning = (usage.get("completion_tokens_details") or {}).get("reasoning_tokens")
        if isinstance(reasoning, (int, float)):
            reasoning_values.append(reasoning)
    attempts = [requests.get(identity, responses.get(identity)) for identity in all_ids]
    return {
        "http_attempts": len(all_ids), "request_events": len(requests), "response_events": len(responses),
        "successful_http_attempts": sum(r.get("status") == "ok" for r in responses.values()),
        "failed_http_attempts": sum(r.get("status") != "ok" for r in responses.values()),
        "retry_attempts": sum(r.get("attempt_for_turn", 1) > 1 for r in attempts),
        "unmatched_request_attempts": len(missing_response), "response_events_without_request": len(responses.keys() - requests.keys()),
        "legacy_response_only_events": legacy,
        "attempts_without_usage": len(missing_response) + len(responses) - len(usage_rows),
        "usage_complete": not missing_response and len(usage_rows) == len(responses),
        "prompt_tokens": sum(u.get("prompt_tokens", 0) for u in usage_rows),
        "completion_tokens": sum(u.get("completion_tokens", 0) for u in usage_rows),
        "cache_hit_tokens_reported": sum(hit_values), "attempts_reporting_cache_hit": len(hit_values),
        "cache_miss_tokens_reported": sum(miss_values), "attempts_reporting_cache_miss": len(miss_values),
        "reasoning_tokens_reported": sum(reasoning_values), "attempts_reporting_reasoning_tokens": len(reasoning_values),
        "finish_reason_counts": dict(collections.Counter(str(r.get("finish_reason", "missing")) for r in responses.values())),
        "http_status_counts": dict(collections.Counter(str(r.get("http_status", "missing")) for r in responses.values())),
        "http_attempt_latency_ms": quantiles([r.get("latency_ms") for r in responses.values()]),
    }


def aggregate(results: list[dict], events: list[dict], scheduled: set[tuple[str, str]], dataset: dict[str, dict]) -> dict:
    statuses = collections.Counter(result.get("status", "missing") for result in results)
    searches = [search for result in results for search in result.get("searches", [])]
    executed = [search for search in searches if search.get("executed", True)]
    empty = [search for search in executed if search.get("status") == "ok" and not search.get("evidence")]
    invalid_citations, citation_cases = 0, 0
    for result in results:
        seen = {block.get("id") for block in result.get("retrieved_evidence", [])}
        invalid = [citation for citation in result.get("citations", []) if citation not in seen]
        invalid_citations += len(invalid)
        citation_cases += bool(invalid)
    blocks = [block for search in searches for block in search.get("evidence", [])]
    overlaps = [value for result in results if (value := overlap_metrics(result, dataset.get(question_id(result), {}))) is not None]
    completed_ids = {(question_id(result), result["arm"]) for result in results}
    return {
        "scheduled_cases": len(scheduled), "cases_with_result": len(results), "missing_results": len(scheduled - completed_ids),
        "status_counts": dict(statuses), "failure_kind_counts": dict(collections.Counter(r.get("failure_kind", "unspecified") for r in results if r.get("status") != "ok")),
        "successful_protocol_rate_completed": statuses["ok"] / len(results) if results else None,
        "successful_protocol_rate_scheduled": statuses["ok"] / len(scheduled) if scheduled else None,
        "search_actions": len(searches), "executed_searches": len(executed),
        "search_errors": sum(s.get("status") == "error" for s in executed), "empty_successful_searches": len(empty),
        "context_budget_exhausted_searches": sum(s.get("status") == "context_budget_exhausted" or s.get("budget_exhausted", False) for s in searches),
        "searches_per_completed_case": average([len(r.get("searches", [])) for r in results]),
        "empty_search_rate_executed": len(empty) / len(executed) if executed else None,
        "invalid_citations": invalid_citations, "cases_with_invalid_citations": citation_cases,
        "truncated_search_responses": sum(bool(s.get("truncated")) for s in searches),
        "text_truncated_blocks": sum(bool(b.get("text_truncated")) for b in blocks),
        "sources_truncated_blocks": sum(bool(b.get("sources_truncated")) for b in blocks),
        "relations_truncated_blocks": sum(bool(b.get("relations_truncated")) for b in blocks),
        "visible_context_chars_total": sum((r.get("budgets") or {}).get("visible_context_chars_used", 0) for r in results),
        "end_to_end_latency_seconds": quantiles([r.get("elapsed_seconds") for r in results]),
        "successful_end_to_end_latency_seconds": quantiles([r.get("elapsed_seconds") for r in results if r.get("status") == "ok"]),
        "search_latency_ms": quantiles([s.get("latency_ms") for s in executed]),
        "provenance_path_overlap": {
            "scope": "declared source paths only; not semantic recall or answer correctness",
            "cases_with_gold_paths": len(overlaps),
            "mean_fraction_of_gold_paths_visible": average([o["visible_fraction"] for o in overlaps]),
            "mean_fraction_of_gold_paths_cited": average([o["cited_fraction"] for o in overlaps]),
            "cases_with_any_gold_path_visible": sum(o["any_visible"] for o in overlaps),
            "cases_with_any_gold_path_cited": sum(o["any_cited"] for o in overlaps),
        },
        "provider_usage_all_attempts_including_retries": meter_events(events),
    }


def summarize_run(root: Path, dataset: dict[str, dict]) -> tuple[dict, dict]:
    results, result_partial = read_jsonl(root / "results.jsonl")
    events, event_partial = read_jsonl(root / "requests.jsonl")
    config_path = root / "run-config.json"
    config = json.loads(config_path.read_text()) if config_path.exists() else {}
    scheduled = {(str(job["question_id"]), str(job["arm"])) for job in config.get("job_order", [])}
    by_case = {}
    for result in results:
        key = (question_id(result), str(result["arm"]))
        if key in by_case:
            raise ValueError("duplicate solver result for question/arm")
        by_case[key] = result
    if not scheduled:
        scheduled = set(by_case)
    arms = sorted({arm for _, arm in scheduled} | {str(e["arm"]) for e in events})
    splits = sorted({split_for(identity, dataset) for identity, _ in scheduled})
    groups = {}
    for split in ["all"] + [s for s in splits if s != "all"]:
        groups[split] = {}
        for arm in arms:
            def belongs(identity, actual_arm):
                return arm == actual_arm and (split == "all" or split_for(identity, dataset) == split)
            groups[split][arm] = aggregate(
                [r for key, r in by_case.items() if belongs(*key)],
                [e for e in events if belongs(question_id(e), str(e["arm"]))],
                {key for key in scheduled if belongs(*key)}, dataset)
    return {"run_directory": str(root), "source_sha256": {name: file_sha(root / name) for name in ("results.jsonl", "requests.jsonl", "run-config.json") if (root / name).exists()},
            "partial_trailing_jsonl_rows": {"results": result_partial, "requests": event_partial},
            "unknown_dataset_ids": len({identity for identity, _ in scheduled if identity not in dataset}),
            "groups": groups}, by_case


def paired_comparison(before: dict, after: dict, dataset: dict[str, dict]) -> dict:
    common = before.keys() & after.keys()
    arms = sorted({arm for _, arm in before.keys() | after.keys()})
    splits = sorted({split_for(identity, dataset) for identity, _ in before.keys() | after.keys()})
    output = {}
    for split in ["all"] + [s for s in splits if s != "all"]:
        output[split] = {}
        for arm in arms:
            keys = [key for key in common if key[1] == arm and (split == "all" or split_for(key[0], dataset) == split)]
            pairs = [(before[key], after[key]) for key in keys]
            deltas = []
            for key, (left, right) in zip(keys, pairs):
                a, b = overlap_metrics(left, dataset.get(key[0], {})), overlap_metrics(right, dataset.get(key[0], {}))
                if a is not None and b is not None:
                    deltas.append(b["visible_fraction"] - a["visible_fraction"])
            output[split][arm] = {
                "paired_cases": len(keys),
                "protocol_error_to_ok": sum(a.get("status") != "ok" and b.get("status") == "ok" for a, b in pairs),
                "protocol_ok_to_error": sum(a.get("status") == "ok" and b.get("status") != "ok" for a, b in pairs),
                "mean_search_action_delta": average([len(b.get("searches", [])) - len(a.get("searches", [])) for a, b in pairs]),
                "mean_visible_context_char_delta": average([(b.get("budgets") or {}).get("visible_context_chars_used", 0) - (a.get("budgets") or {}).get("visible_context_chars_used", 0) for a, b in pairs]),
                "paired_latency_delta_seconds": quantiles([b["elapsed_seconds"] - a["elapsed_seconds"] for a, b in pairs if isinstance(a.get("elapsed_seconds"), (int, float)) and isinstance(b.get("elapsed_seconds"), (int, float))]),
                "mean_provenance_path_overlap_delta": average(deltas),
            }
    return {"direction": "comparison run minus baseline run", "baseline_only_cases": len(before.keys() - after.keys()),
            "comparison_only_cases": len(after.keys() - before.keys()), "groups": output}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", type=Path, required=True)
    parser.add_argument("--compare-run", type=Path)
    parser.add_argument("--dataset", type=Path, required=True, help="frozen dataset with id, split and optional gold evidence paths")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    items = dataset_rows(args.dataset)
    dataset = {question_id(item): item for item in items}
    if len(dataset) != len(items):
        raise ValueError("duplicate id in fixed dataset")
    baseline, before = summarize_run(args.run, dataset)
    summary = {"format": "cairn-quality-run-summary/v1", "dataset_sha256": file_sha(args.dataset),
               "metric_notes": ["Successful status measures harness protocol completion, not answer correctness.",
                                "Source-path overlap is metadata overlap, not semantic recall.",
                                "HTTP usage includes failed responses and retries where the provider returned usage; missing usage remains explicit.",
                                "Median and p95 use linear interpolation at (n-1)*p; failed cases are included in all-case latency."],
               "baseline": baseline}
    if args.compare_run:
        comparison, after = summarize_run(args.compare_run, dataset)
        summary["comparison"] = comparison
        summary["paired_changes"] = paired_comparison(before, after, dataset)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output), "sha256": file_sha(args.output)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
