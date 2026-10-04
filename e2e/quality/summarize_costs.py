#!/usr/bin/env python3
"""Offline provider metering audit. No network, credentials, prompts or CoT output.

Reads an explicit allowlist of real-run ledgers. Every attempt, including failed
validation and retries, is charged once by its attempt ID. Reconciled baseline
replaces its incomplete original; copied results/receipts are never extra calls.
Run with --final only after the coordinator confirms the active judges stopped.
"""
import argparse
import collections
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import tempfile

STAGES = (
    ("solver-pilot-json", "current-quality", "quality-pilot/requests.jsonl", "solver"),
    ("solver-pilot-native", "current-quality", "quality-native-pilot/requests.jsonl", "solver"),
    ("solver-pilot-batch", "current-quality", "quality-batch-pilot/requests.jsonl", "solver"),
    ("solver-baseline-reconciled", "current-quality", "quality-baseline-reconciled/requests.jsonl", "solver"),
    ("solver-rankfix", "current-quality", "quality-rankfix/requests.jsonl", "solver"),
    ("judge-pilot-v1", "current-quality", "quality-pilot/judge-usage.jsonl", "judge"),
    ("judge-pilot-v2", "current-quality", "quality-judge-v2-pilot/judge-usage.jsonl", "judge"),
    ("judge-pilot-v3", "current-quality", "quality-judge-v3-pilot/judge-usage.jsonl", "judge"),
    ("judge-baseline", "current-quality", "quality-baseline/judge-usage.jsonl", "judge"),
    ("judge-rankfix", "current-quality", "quality-rankfix/judge-usage.jsonl", "judge"),
    ("lifecycle-initial", "current-lifecycle", "lifecycle-live/llm-usage.jsonl", "flat"),
    ("lifecycle-recheck-64k", "current-lifecycle", "lifecycle-recheck-64k/llm-usage.jsonl", "flat"),
    ("provider-preflight", "current-quality", "eval-preflight.json", "preflight"),
    ("historical-full-build", "historical-excluded-from-current", "final-real/llm-usage.jsonl", "flat"),
)
EMPTY_SHA = hashlib.sha256(b"").hexdigest()
TOKEN_FIELDS = ("input_tokens", "output_tokens", "total_tokens", "cache_hit_tokens", "cache_miss_tokens")
COUNTERS = ("http_requests", "http_responses_confirmed", "completed_attempts", "unresolved_requests",
            "request_events", "response_events", "failed_attempts", "retry_attempts",
            "attempts_without_any_usage", "attempts_missing_input_or_output", "estimated_accounting_events",
            "finish_length", "finish_reason_missing", "duplicate_identical_events", "reconstructed_request_events",
            "grade_records_without_ledger_response", "judge_responses_without_grade_record", "requests_without_retry_metadata")


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def number(value):
    return value if type(value) is int and value >= 0 else None


def read_source(root, relative, final=False, single=False):
    path = root / relative
    if not path.exists():
        return [], {"path": relative, "missing": True}
    with path.open("rb") as stream:
        before = os.fstat(stream.fileno())
        raw = stream.read()
        after = os.fstat(stream.fileno())
    changed = (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns)
    prefix, trailing = raw, 0
    if not single and raw and not raw.endswith(b"\n"):
        boundary = raw.rfind(b"\n") + 1
        prefix, trailing = raw[:boundary], len(raw) - boundary
    if final and (changed or trailing):
        raise ValueError("final input changed or has incomplete trailing record: " + relative)
    try:
        rows = [json.loads(prefix)] if single else [json.loads(line) for line in prefix.splitlines() if line.strip()]
    except (ValueError, UnicodeDecodeError):
        raise ValueError("invalid complete JSON record in " + relative) from None
    if any(not isinstance(row, dict) for row in rows):
        raise ValueError("non-object metering record in " + relative)
    return rows, {"path": relative, "sha256": sha(raw), "bytes_read": len(raw),
                  "processed_prefix_sha256": sha(prefix), "ignored_trailing_bytes": trailing,
                  "changed_during_read": changed, "records": len(rows)}


def token_values(row, flat=False):
    usage = row if flat else row.get("usage")
    usage = usage if isinstance(usage, dict) else {}
    values = {
        "input_tokens": number(usage.get("input_tokens" if flat else "prompt_tokens")),
        "output_tokens": number(usage.get("output_tokens" if flat else "completion_tokens")),
        "total_tokens": number(usage.get("total_tokens")),
        "cache_hit_tokens": number(usage.get("prompt_cache_hit_tokens")),
        "cache_miss_tokens": number(usage.get("prompt_cache_miss_tokens")),
    }
    details = usage.get("prompt_tokens_details")
    nested_hit = number(details.get("cached_tokens")) if isinstance(details, dict) else None
    if values["cache_hit_tokens"] is None:
        values["cache_hit_tokens"] = nested_hit
    elif nested_hit is not None and nested_hit != values["cache_hit_tokens"]:
        raise ValueError("conflicting reported cache-hit counters")
    for left, right, total in (("input_tokens", "output_tokens", "total_tokens"),
                               ("cache_hit_tokens", "cache_miss_tokens", "input_tokens")):
        if all(values[k] is not None for k in (left, right, total)) and values[left] + values[right] != values[total]:
            raise ValueError("inconsistent reported token totals")
    return values


def unique(rows, key, event=None):
    mapped, duplicates = {}, 0
    for row in rows:
        if event is not None and row.get("event") != event:
            continue
        identity = row.get(key)
        if identity is None:
            raise ValueError("metering attempt lacks " + key)
        if key == "http_attempt" and (number(identity) is None or identity < 1):
            raise ValueError("HTTP attempt ID must be a positive integer")
        if key == "request_id" and (not isinstance(identity, str) or not identity):
            raise ValueError("judge request ID must be a nonempty string")
        if identity in mapped:
            if json.dumps(row, sort_keys=True) != json.dumps(mapped[identity], sort_keys=True):
                raise ValueError("conflicting duplicate metering attempt")
            duplicates += 1
        else:
            mapped[identity] = row
    return mapped, duplicates


def summarize_rows(rows, kind, grades=()):
    stats = {name: 0 for name in COUNTERS}
    stats["known_token_sums"] = {name: 0 for name in TOKEN_FIELDS}
    stats["missing_token_fields"] = {name: 0 for name in TOKEN_FIELDS}
    finishes, failures, statuses = collections.Counter(), collections.Counter(), collections.Counter()
    if kind in ("solver", "judge"):
        if any(row.get("event") not in ("request", "response") for row in rows):
            raise ValueError("unknown request/response ledger event")
        key = "http_attempt" if kind == "solver" else "request_id"
        requests, d1 = unique(rows, key, "request")
        responses, d2 = unique(rows, key, "response")
        stats["request_events"], stats["response_events"] = len(requests), len(responses)
        stats["http_requests"] = len(requests.keys() | responses.keys())
        stats["unresolved_requests"] = len(requests.keys() - responses.keys())
        stats["reconstructed_request_events"] = sum(row.get("reconstructed") is True for row in requests.values())
        stats["duplicate_identical_events"] = d1 + d2
        for identity in requests.keys() & responses.keys():
            a, b = requests[identity], responses[identity]
            if a.get("request_sha256") != b.get("request_sha256"):
                raise ValueError("request/response request hash mismatch")
        retry_field = "attempt_for_turn" if kind == "solver" else "attempt"
        attempt_rows = {**responses, **requests}.values()
        stats["retry_attempts"] = sum((number(row.get(retry_field)) or 1) > 1 for row in attempt_rows)
        stats["requests_without_retry_metadata"] = sum(number(row.get(retry_field)) is None for row in attempt_rows)
    elif kind == "flat":
        responses, duplicates = unique(rows, "http_attempt")
        stats["http_requests"] = len(responses)
        stats["requests_without_retry_metadata"] = len(responses)
        stats["duplicate_identical_events"] = duplicates
    else:
        if len(rows) > 1:
            raise ValueError("preflight receipt must describe one observed request")
        responses = dict(enumerate(rows))
        stats["http_requests"] = len(rows)
        stats["requests_without_retry_metadata"] = len(rows)
    grade_map, _ = unique(grades, "request_id") if grades else ({}, 0)
    if kind == "judge":
        stats["grade_records_without_ledger_response"] = len(grade_map.keys() - responses.keys())
        stats["judge_responses_without_grade_record"] = len(responses.keys() - grade_map.keys())
    stats["completed_attempts"] = len(responses)
    stats["responses_without_request_event"] = len(responses.keys() - requests.keys()) if kind in ("solver", "judge") else None
    stats["models"] = sorted({str(row.get("model", row.get("judge_model", "unspecified"))) for row in rows})
    for identity, row in responses.items():
        values = token_values(row, flat=kind == "flat")
        for field, value in values.items():
            if value is None:
                stats["missing_token_fields"][field] += 1
            else:
                stats["known_token_sums"][field] += value
        stats["attempts_without_any_usage"] += all(values[k] is None for k in ("input_tokens", "output_tokens", "total_tokens"))
        stats["attempts_missing_input_or_output"] += values["input_tokens"] is None or values["output_tokens"] is None
        stats["estimated_accounting_events"] += row.get("usage_is_estimate") is True
        http_status = number(row.get("http_status"))
        has_response = http_status is not None or (row.get("response_sha256") not in (None, "", EMPTY_SHA)) or any(v is not None for v in values.values())
        stats["http_responses_confirmed"] += has_response
        finish = row.get("finish_reason")
        if kind == "judge" and identity in grade_map:
            grade = grade_map[identity]
            for field in ("request_sha256", "response_sha256", "usage", "status"):
                if json.dumps(grade.get(field), sort_keys=True) != json.dumps(row.get(field), sort_keys=True):
                    raise ValueError("judge grade metadata disagrees with usage ledger: " + field)
            finish = grade.get("finish_reason", finish)
        finishes[str(finish) if finish is not None else "unrecorded"] += 1
        stats["finish_reason_missing"] += finish is None
        length = finish in ("length", "max_tokens") or row.get("failure_kind") == "completion_length"
        stats["finish_length"] += length
        failed = row.get("status") == "error" or row.get("transport_error") or row.get("response_read_error") or (http_status is not None and http_status >= 400) or length
        stats["failed_attempts"] += bool(failed)
        if row.get("failure_kind"):
            failures[str(row["failure_kind"])] += 1
        statuses[str(http_status) if http_status is not None else "unrecorded"] += 1
    stats["finish_reason_counts"] = dict(finishes)
    stats["failure_kind_counts"] = dict(failures)
    stats["http_status_counts"] = dict(statuses)
    stats["observed_usage_complete"] = all(stats["missing_token_fields"][k] == 0 for k in ("input_tokens", "output_tokens", "total_tokens"))
    stats["all_attempts_have_complete_usage"] = stats["observed_usage_complete"] and stats["unresolved_requests"] == 0
    stats["cache_usage_complete"] = stats["missing_token_fields"]["cache_hit_tokens"] == 0 and stats["missing_token_fields"]["cache_miss_tokens"] == 0 and stats["unresolved_requests"] == 0
    return stats


def totals(stages):
    result = {name: sum(stage[name] for stage in stages) for name in COUNTERS}
    result["stage_count"] = len(stages)
    result["known_token_sums"] = {name: sum(stage["known_token_sums"][name] for stage in stages) for name in TOKEN_FIELDS}
    result["missing_token_fields"] = {name: sum(stage["missing_token_fields"][name] for stage in stages) for name in TOKEN_FIELDS}
    result["all_attempts_have_complete_usage"] = all(stage["all_attempts_have_complete_usage"] and not stage.get("source_missing") for stage in stages)
    result["cache_usage_complete"] = all(stage["cache_usage_complete"] and not stage.get("source_missing") for stage in stages)
    result["known_sums_are_partial"] = not result["all_attempts_have_complete_usage"] or not result["cache_usage_complete"] or result["grade_records_without_ledger_response"] > 0
    return result


def build_report(root, final=False):
    start = datetime.now(timezone.utc).isoformat()
    stages = []
    for name, group, relative, kind in STAGES:
        rows, source = read_source(root, relative, final, kind == "preflight")
        if final and group.startswith("current-") and source.get("missing"):
            raise ValueError("final current source is missing: " + relative)
        grade_rows, grade_source = [], None
        if kind == "judge":
            grade_rows, grade_source = read_source(root, str(Path(relative).parent / "grades.jsonl"), final)
            if final and grade_source.get("missing"):
                raise ValueError("final judge finish metadata is missing: " + relative)
        stats = summarize_rows(rows, kind, grade_rows)
        if final and stats["grade_records_without_ledger_response"]:
            raise ValueError("final judge grades have unaccounted ledger responses: " + relative)
        if final and group.startswith("current-") and stats["unresolved_requests"]:
            raise ValueError("final current ledger has unresolved requests: " + relative)
        stats.update(stage=name, group=group, source=source, source_missing=bool(source.get("missing")))
        if grade_source:
            stats["finish_metadata_source"] = grade_source
        if name == "solver-baseline-reconciled":
            _, receipt = read_source(root, "quality-baseline-reconciled/reconciliation-receipt.json", final, True)
            if final and receipt.get("missing"):
                raise ValueError("final baseline reconciliation receipt is missing")
            stats["reconciliation_receipt"] = receipt
            stats["note"] = "Recovered response usage from preserved result records; reconstructed request metadata is not proof of pre-request persistence. Original ledger is excluded from totals."
        if kind == "flat":
            stats["note"] = "Observer records completed attempts only, including transport/read failures. Cache counters were not recorded; missing cache is unknown, never inferred as zero."
        stages.append(stats)
    known_paths = {relative for _, _, relative, _ in STAGES}
    excluded = {"quality-baseline/requests.jsonl": "Replaced by reconciled baseline; counting both would duplicate provider calls.",
                "final-materialized/original-llm/llm-usage.jsonl": "Copy of historical full-build ledger; not new calls.",
                "final-materialized/usage-summary.json": "Deterministic materialization, no new provider requests.",
                "judge-retry-test-*/": "Local fault/unit-test fixtures, not real-provider billing."}
    candidates = [str(path.relative_to(root)) for pattern in ("quality-*/requests.jsonl", "quality-*/judge-usage.jsonl") for path in root.glob(pattern)]
    unknown = sorted(set(candidates) - known_paths - set(excluded))
    if final and unknown:
        raise ValueError("final audit has unclassified quality ledgers: " + ", ".join(unknown))
    current = [stage for stage in stages if stage["group"].startswith("current-")]
    return {"format": "cairn-provider-usage-audit/v1", "status": "final" if final else "snapshot",
            "capture_started_at_utc": start, "capture_finished_at_utc": datetime.now(timezone.utc).isoformat(),
            "current_scope": "This evaluation: solver/judge pilots, reconciled baseline, rankfix, lifecycle initial/recheck and 46-token provider preflight.",
            "active_stage_note": "Final mode was explicitly requested after coordinator confirmation." if final else "Formal baseline/rankfix judges may still be appending. Unresolved requests and missing finish metadata are snapshot state; rerun after completion.",
            "current_evaluation_totals": totals(current),
            "current_quality_subtotal": totals([stage for stage in current if stage["group"] == "current-quality"]),
            "current_lifecycle_subtotal": totals([stage for stage in current if stage["group"] == "current-lifecycle"]),
            "historical_build_not_in_current_totals": totals([stage for stage in stages if stage["group"].startswith("historical-")]),
            "stages": stages, "excluded_duplicate_or_nonprovider_inputs": excluded,
            "unclassified_quality_ledgers_not_counted": unknown,
            "method": ["Count each stage/attempt ID once; include failed validation, failures and retries. Never deduplicate by prompt/request hash.",
                       "retry_attempts counts only explicit per-turn/per-case retry metadata. Flat observer ledgers lack that grouping; their every HTTP attempt is still included in request and token totals.",
                       "Token sums use provider-reported usage fields only. Budget reservations/accounted estimates are never substituted for provider usage.",
                       "Missing fields and unresolved requests remain explicit; known sums are not a complete bill when those are nonzero.",
                       "Cache hits are part of prompt tokens, not additional tokens. Thinking/reasoning usage is already in completion tokens; it is not added twice.",
                       "HTTP counts refer to observed chat-completion attempts. Unmetered model-list/balance checks and GitHub traffic are outside this token audit.",
                       "Judge finish reasons are joined only from matching request/hash/usage/status grade metadata. No prompts, verdicts, replies, credentials or chain-of-thought are emitted.",
                       "Local receipts attest consistency, not cryptographic authentication by the provider. No prices, currency totals or USD estimates are computed."]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--e2e-root", type=Path, default=Path(__file__).resolve().parents[3] / "e2e")
    parser.add_argument("--out", type=Path, default=Path(__file__).resolve().parent / "reports/provider-usage.json")
    parser.add_argument("--final", action="store_true", help="coordinator confirms formal judges stopped; freeze final audit")
    args = parser.parse_args()
    report = build_report(args.e2e_root, args.final)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=args.out.parent, delete=False) as stream:
        json.dump(report, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
        temporary = stream.name
    os.replace(temporary, args.out)
    current = report["current_evaluation_totals"]
    print(json.dumps({"status": report["status"], "report": str(args.out),
                      "http_requests": current["http_requests"], "http_responses_confirmed": current["http_responses_confirmed"],
                      "unresolved_requests": current["unresolved_requests"], "known_token_sums": current["known_token_sums"]}))


if __name__ == "__main__":
    main()
