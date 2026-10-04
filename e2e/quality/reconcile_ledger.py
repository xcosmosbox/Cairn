#!/usr/bin/env python3
"""Offline, fail-closed reconciliation of a completed run's metering records.

Result-embedded model_calls are response *records*, not original HTTP bytes.
Missing request events can recover metadata, never proof of pre-request writes.
No provider, dataset, source document, answer grader, or external service is used.
"""
from __future__ import annotations

import argparse
import collections
import datetime as dt
import hashlib
import json
import re
import shutil
from pathlib import Path


SOURCE_NAMES = ("requests.jsonl", "results.jsonl", "run-config.json", "summary.json")
REQUEST_FIELDS = ("question_id", "arm", "turn", "http_attempt", "attempt_for_turn",
                  "started_at", "request_sha256", "request_bytes", "request_max_tokens",
                  "model", "thinking")
OPTIONAL_REQUEST_FIELDS = ("tool_choice", "protocol", "retry_of_http_attempt")
TOKEN_FIELDS = ("prompt_tokens", "completion_tokens", "total_tokens",
                "prompt_cache_hit_tokens", "prompt_cache_miss_tokens")


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def canonical(value) -> str:
    """Unlike Python equality, preserve JSON distinctions such as true/1/1.0."""
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def integer(value, label: str, minimum: int = 0) -> int:
    require(type(value) is int and value >= minimum, f"invalid {label}")
    return value


def strict_json(data: bytes | str):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate JSON object key")
            result[key] = value
        return result
    def constant(_):
        raise ValueError("non-finite JSON constant")
    return json.loads(data, object_pairs_hook=pairs, parse_constant=constant)


def rows(data: bytes, name: str) -> list[dict]:
    result = []
    for line in data.splitlines():
        if line.strip():
            item = strict_json(line)
            require(isinstance(item, dict), f"non-object row in {name}")
            result.append(item)
    return result


def case_key(item: dict) -> tuple[str, str]:
    require(isinstance(item, dict), "case must be an object")
    for field in ("question_id", "arm"):
        require(isinstance(item.get(field), str) and bool(item[field]), f"invalid {field}")
    return item["question_id"], item["arm"]


def request_record(response: dict) -> dict:
    record = {field: response[field] for field in REQUEST_FIELDS}
    record.update({field: response[field] for field in OPTIONAL_REQUEST_FIELDS if field in response})
    return {**record, "event": "request", "status": "in_flight"}


def validate_usage(usage) -> None:
    if usage is None:
        return
    require(isinstance(usage, dict), "usage must be an object or absent")
    for field in TOKEN_FIELDS:
        if field in usage and usage[field] is not None:
            integer(usage[field], f"usage.{field}")
    for fields in (("prompt_tokens", "completion_tokens", "total_tokens"),
                   ("prompt_cache_hit_tokens", "prompt_cache_miss_tokens", "prompt_tokens")):
        if all(usage.get(field) is not None for field in fields):
            require(usage[fields[0]] + usage[fields[1]] == usage[fields[2]], "inconsistent usage totals")
    details = usage.get("prompt_tokens_details")
    if details is not None:
        require(isinstance(details, dict), "invalid prompt_tokens_details")
        if details.get("cached_tokens") is not None:
            integer(details["cached_tokens"], "cached_tokens")
            if usage.get("prompt_cache_hit_tokens") is not None:
                require(details["cached_tokens"] == usage["prompt_cache_hit_tokens"], "conflicting cache token counts")


def validate_response(record: dict, owner: tuple[str, str], config: dict) -> None:
    require(isinstance(record, dict), "model_calls item must be an object")
    require(case_key(record) == owner, "model call belongs to another result case")
    require(record.get("event") == "response", "model_calls must contain response events")
    require(record.get("status") in ("ok", "error"), "invalid response status")
    require(all(field in record for field in REQUEST_FIELDS), "response lacks request metadata")
    integer(record["http_attempt"], "http_attempt", 1)
    integer(record["turn"], "turn", 1)
    integer(record["attempt_for_turn"], "attempt_for_turn", 1)
    integer(record["request_bytes"], "request_bytes", 1)
    integer(record["request_max_tokens"], "request_max_tokens", 1)
    require(isinstance(record["started_at"], str), "invalid started_at")
    require(dt.datetime.fromisoformat(record["started_at"]).tzinfo is not None, "started_at lacks timezone")
    require(isinstance(record["request_sha256"], str) and re.fullmatch(r"[0-9a-f]{64}", record["request_sha256"]) is not None, "invalid request SHA256")
    has_response = "response_sha256" in record or "response_bytes" in record
    if record["status"] == "ok" or has_response:
        require(isinstance(record.get("response_sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", record["response_sha256"]) is not None, "invalid response SHA256")
        integer(record.get("response_bytes"), "response_bytes", 1)
    for source, target in (("model", "model"), ("thinking", "thinking"), ("max_tokens", "request_max_tokens")):
        require(source in config and record[target] == config[source], f"response/config mismatch: {target}")
    require(record["turn"] <= config["max_model_turns"], "turn exceeds configured budget")
    require(record["attempt_for_turn"] <= config["max_retries"] + 1, "retry exceeds configured budget")
    validate_usage(record.get("usage"))


def validate(sources: dict[str, bytes]) -> dict:
    results = rows(sources["results.jsonl"], "results.jsonl")
    events = rows(sources["requests.jsonl"], "requests.jsonl")
    config = strict_json(sources["run-config.json"])
    original_summary = strict_json(sources["summary.json"])
    require(isinstance(config, dict) and isinstance(original_summary, dict), "config/summary must be objects")
    jobs = config.get("job_order")
    require(isinstance(jobs, list) and bool(jobs), "nonempty configured job_order required")
    scheduled = [case_key(job) for job in jobs]
    require(len(scheduled) == len(set(scheduled)), "duplicate configured case")
    settings = config.get("config")
    require(isinstance(settings, dict), "run config settings required")
    integer(settings.get("max_model_turns"), "max_model_turns", 1)
    integer(settings.get("max_retries"), "max_retries")
    integer(config.get("max_http_attempts"), "max_http_attempts", 1)
    by_case, responses, origins = {}, {}, {}
    for row_number, result in enumerate(results, 1):
        owner = case_key(result)
        require(owner not in by_case, "duplicate result case")
        require(result.get("status") in ("ok", "error"), "invalid result status")
        by_case[owner] = result
        calls = result.get("model_calls")
        require(isinstance(calls, list), "result requires model_calls list")
        per_turn = collections.defaultdict(list)
        previous_attempt, previous_turn = 0, 0
        for index, response in enumerate(calls):
            validate_response(response, owner, settings)
            attempt = response["http_attempt"]
            require(attempt not in responses, "duplicate model_calls HTTP attempt")
            require(attempt > previous_attempt and response["turn"] >= previous_turn, "model_calls order is inconsistent")
            previous_attempt, previous_turn = attempt, response["turn"]
            responses[attempt] = response
            origins[attempt] = {"file": "results.jsonl", "line": row_number, "field": "model_calls", "index": index}
            per_turn[response["turn"]].append(response)
        require(sorted(per_turn) == list(range(1, len(per_turn) + 1)), "model call turns are incomplete")
        for turn_calls in per_turn.values():
            for index, response in enumerate(turn_calls):
                require(response["attempt_for_turn"] == index + 1, "retry sequence is incomplete")
                if index:
                    integer(response.get("retry_of_http_attempt"), "retry predecessor", 1)
                    require(response.get("retry_of_http_attempt") == turn_calls[index - 1]["http_attempt"], "invalid retry predecessor")
                    require(request_record(response)["request_sha256"] == turn_calls[index - 1]["request_sha256"], "retry changed request hash")
                else:
                    require("retry_of_http_attempt" not in response, "first attempt has retry predecessor")
    require(set(by_case) == set(scheduled), "results do not exactly cover configured tasks")
    require(sorted(responses) == list(range(1, len(responses) + 1)), "HTTP attempts are not contiguous from one")
    require(len(responses) <= config["max_http_attempts"], "HTTP attempts exceed configured maximum")
    require(integer(original_summary.get("http_attempts"), "summary HTTP attempts") == len(responses), "model_calls do not match summary HTTP attempt count")
    for field, expected in (("completed_jobs", len(results)), ("questions", len({key[0] for key in scheduled})),
                            ("successful_jobs", sum(result["status"] == "ok" for result in results))):
        require(integer(original_summary.get(field), f"summary {field}") == expected, f"original summary case-count mismatch: {field}")
    require(isinstance(original_summary.get("arms"), list) and set(original_summary["arms"]) == {key[1] for key in scheduled}, "original summary arm mismatch")
    requests, surviving_responses = {}, {}
    for event in events:
        attempt = integer(event.get("http_attempt"), "ledger http_attempt", 1)
        kind = event.get("event")
        require(kind in ("request", "response"), "unknown ledger event type")
        target = requests if kind == "request" else surviving_responses
        require(attempt not in target, f"duplicate original {kind} event")
        require(attempt in responses, "original ledger attempt absent from model_calls")
        expected = request_record(responses[attempt]) if kind == "request" else responses[attempt]
        require(canonical(event) == canonical(expected), f"original {kind} conflicts with model_calls record")
        target[attempt] = event
    return {"results": results, "config": config, "original_summary": original_summary,
            "responses": responses, "requests": requests, "surviving_responses": surviving_responses,
            "origins": origins, "scheduled": scheduled}


def usage_summary(responses: dict[int, dict]) -> dict:
    totals = {}
    reported, missing = {}, {}
    for field in TOKEN_FIELDS:
        values = [(record.get("usage") or {}).get(field) for record in responses.values()]
        present = [value for value in values if value is not None]
        totals[field] = sum(present) if present else None
        reported[field], missing[field] = len(present), len(values) - len(present)
    without = sum(not isinstance(record.get("usage"), dict) for record in responses.values())
    return {"reported_token_totals": totals, "attempts_reporting_field": reported,
            "attempts_missing_field": missing, "attempts_without_usage": without,
            "usage_complete": all(missing[field] == 0 for field in ("prompt_tokens", "completion_tokens", "total_tokens")),
            "cache_usage_complete": all(missing[field] == 0 for field in ("prompt_cache_hit_tokens", "prompt_cache_miss_tokens")),
            "policy": "Only explicitly reported fields are summed; missing values are not inferred or filled. Totals are partial when their missing count is nonzero."}


def json_bytes(value) -> bytes:
    return (json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode()


def jsonl_bytes(values) -> bytes:
    return b"".join((json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False) + "\n").encode() for value in values)


def reconcile(run_dir: Path, output_dir: Path) -> dict:
    run_dir, output_dir = run_dir.resolve(), output_dir.resolve()
    require(run_dir.is_dir(), "source run directory does not exist")
    require(not output_dir.exists(), "output directory must be new")
    require(not output_dir.is_relative_to(run_dir), "output directory must be outside the original run")
    sources = {name: (run_dir / name).read_bytes() for name in SOURCE_NAMES}
    stat_observed_at = dt.datetime.now(dt.timezone.utc).isoformat()
    observed_stats = {}
    for name in SOURCE_NAMES:
        stat = (run_dir / name).stat()
        observed_stats[name] = {"bytes": stat.st_size, "inode": stat.st_ino,
                                "mtime_ns": stat.st_mtime_ns, "ctime_ns": stat.st_ctime_ns}
    state = validate(sources)  # Every mismatch fails before creating any output.
    responses, requests = state["responses"], state["requests"]
    source_manifest = {name: {"sha256": sha256(data), "bytes": len(data)} for name, data in sources.items()}
    reconstructed_ids = sorted(responses.keys() - requests.keys())
    recovered_response_ids = sorted(responses.keys() - state["surviving_responses"].keys())
    derived_events = []
    for attempt in sorted(responses):
        if attempt in requests:
            request = requests[attempt]
        else:
            request = {**request_record(responses[attempt]), "reconstructed": True,
                       "pre_request_persistence_verified": False,
                       "reconstruction_source": {**state["origins"][attempt],
                           "source_sha256": source_manifest["results.jsonl"]["sha256"]}}
        derived_events.extend((request, responses[attempt]))
    counts = {"http_attempts": len(responses), "original_request_events": len(requests),
              "original_response_events": len(state["surviving_responses"]),
              "reconstructed_request_events": len(reconstructed_ids),
              "response_records_recovered_from_results": len(recovered_response_ids),
              "pre_request_persistence_verified_attempts": None}
    usage = usage_summary(responses)
    totals = usage["reported_token_totals"]
    summary = {"format": "cairn-reconciled-metering-summary/v1", "derived": True,
               "questions": len({key[0] for key in state["scheduled"]}),
               "arms": state["original_summary"]["arms"], "completed_jobs": len(state["results"]),
               "successful_jobs": sum(result["status"] == "ok" for result in state["results"]),
               "http_attempts": len(responses),
               "failed_http_attempts": sum(record["status"] != "ok" for record in responses.values()),
               "retries": sum(record["attempt_for_turn"] > 1 for record in responses.values()),
               "input_tokens": totals["prompt_tokens"], "output_tokens": totals["completion_tokens"],
               "total_tokens": totals["total_tokens"], "cache_hit_tokens": totals["prompt_cache_hit_tokens"],
               "cache_miss_tokens": totals["prompt_cache_miss_tokens"],
               "attempts_without_usage": usage["attempts_without_usage"], "usage_complete": usage["usage_complete"],
               "unmatched_request_attempts": [], "metering": usage, "reconstruction": counts,
               "receipt": "reconciliation-receipt.json",
               "kg_claims_copied_from_original_summary_not_revalidated": {
                   key: state["original_summary"][key] for key in ("kg_sha256_before", "kg_sha256_after", "kg_unchanged")
                   if key in state["original_summary"]}}
    receipt = {"format": "cairn-ledger-reconciliation-receipt/v1", "derived": True,
               "source_directory": str(run_dir), "source_files": source_manifest,
               "source_stat_observations": {"observed_at": stat_observed_at, "files": observed_stats,
                   "interpretation": "These are observations at reconciliation time, not an inode history or mutation audit. Absence of a mutation audit prevents attribution of missing events."},
               "source_loss_root_cause": "unknown", "counts": counts,
               "reconstructed_request_http_attempts": reconstructed_ids,
               "response_http_attempts_recovered_from_results": recovered_response_ids,
               "original_request_http_attempts_without_original_response": sorted(requests.keys() - state["surviving_responses"].keys()),
               "method": [
                   "Require exact configured task coverage, globally unique contiguous HTTP attempt IDs, per-case turn/retry consistency, request hashes, response hashes where a response exists, and internally consistent reported usage.",
                   "Require every surviving original request to equal the request metadata projected from its result-embedded response; require every surviving original response to equal the complete corresponding model_calls object, comparing canonical JSON so true, 1 and 1.0 remain distinct.",
                   "Copy results, run-config and original-summary bytes unchanged. Preserve original-requests bytes separately. Derived response objects are unchanged copies of result-embedded model_calls, serialized anew.",
                   "Reconstruct only missing request metadata from its response record. Mark each reconstructed event and identify its exact results line/model_calls index and source-file hash.",
                   "Sort derived requests.jsonl by HTTP attempt, with request then response. This is a normalized presentation, not the original event order. started_at is copied metadata, not new timing evidence.",
                   "Neither surviving request records nor reconstructed ones establish that a request was persisted before the HTTP call. Pre-request persistence is unverified for this run.",
                   "Response SHA256 and byte counts describe reported original HTTP responses; raw HTTP response bodies are unavailable here and were not reconstructed or rehashed.",
                   "No missing usage field is inferred. Summary token totals include all available response records, including failed calls and retries; per-field missing counts remain explicit.",
                   "This receipt does not establish the cause of lost ledger events or cryptographically authenticate the provider's usage. It establishes consistency of the surviving local records."],
               "response_record_origins": {str(key): value for key, value in state["origins"].items()},
               "metering": usage}
    artifacts = {"results.jsonl": sources["results.jsonl"], "run-config.json": sources["run-config.json"],
                 "original-summary.json": sources["summary.json"], "original-requests.jsonl": sources["requests.jsonl"],
                 "requests.jsonl": jsonl_bytes(derived_events), "summary.json": json_bytes(summary),
                 "reconciliation-receipt.json": json_bytes(receipt)}
    manifest = {"format": "cairn-ledger-reconciliation-manifest/v1", "derived": True,
                "source_files": source_manifest,
                "artifact_files": {name: {"sha256": sha256(data), "bytes": len(data)} for name, data in artifacts.items()},
                "reconciliation_script": {"sha256": sha256(Path(__file__).read_bytes())},
                "manifest_self_hash": "excluded to avoid a circular digest"}
    artifacts["manifest.json"] = json_bytes(manifest)
    require(all((run_dir / name).read_bytes() == data for name, data in sources.items()), "source files changed during reconciliation")
    output_dir.mkdir(parents=True, exist_ok=False)
    try:
        for name, data in artifacts.items():
            with (output_dir / name).open("xb") as stream:
                stream.write(data)
    except BaseException:
        shutil.rmtree(output_dir)
        raise
    return summary


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    summary = reconcile(args.run_dir, args.output_dir)
    print(json.dumps({"output_directory": str(args.output_dir.resolve()), "derived": True,
                      "http_attempts": summary["http_attempts"], "input_tokens": summary["input_tokens"],
                      "output_tokens": summary["output_tokens"], "usage_complete": summary["usage_complete"],
                      "reconstruction": summary["reconstruction"]}, ensure_ascii=False))


if __name__ == "__main__":
    main()
