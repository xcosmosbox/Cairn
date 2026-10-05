#!/usr/bin/env python3
"""Export reviewable, deduplicated receipts from one frozen solver run.

Usage: export_receipts.py --run-dir RUN --output-dir NEW_DIRECTORY
       [--grades grades.jsonl --arm-map arm-map.json] [--grade-status-only]
       [--split pilot|dev|holdout] [--self-test]

This is a whitelist projection, not a raw HTTP-response archive. Evidence text
is preserved in full as it was visible to the solver, including any original
retrieval truncation. Questions reference shared evidence files in search order.
No model response content, reasoning trace, request headers, or credentials are
copied. Judge verdict explanations are short structured review outputs, not
provider reasoning traces. Allowed narrative text is not a general secret scanner.
Held-out artifacts are archived for audit, never used here to tune the system.
The command is offline and never calls a model or reads gold/source documents.
"""
from __future__ import annotations

import argparse
import collections
import hashlib
import json
import math
import re
import shutil
import tempfile
from pathlib import Path
from typing import Any


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def encoded(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2,
                       allow_nan=False) + "\n").encode("utf-8")


def scalar_fields(value: Any, names: str) -> dict:
    """Never copy an unexamined nested object through a permitted field name."""
    if not isinstance(value, dict):
        return {}
    output = {}
    for key in names.split():
        item = value.get(key)
        if key in value and (item is None or type(item) in (str, int, bool)
                             or type(item) is float and math.isfinite(item)):
            output[key] = item
    return output


def strings(value: Any) -> list[str]:
    return [item for item in value if isinstance(item, str)] if isinstance(value, list) else []


def objects(value: Any) -> list[dict]:
    return [item for item in value if isinstance(item, dict)] if isinstance(value, list) else []


def identity(value: Any, label: str) -> str:
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,119}", value):
        raise ValueError(f"invalid {label}; expected a short safe identifier")
    return value


def usage(value: Any) -> dict:
    result = scalar_fields(value, "prompt_tokens completion_tokens total_tokens input_tokens output_tokens "
                           "prompt_cache_hit_tokens prompt_cache_miss_tokens")
    if isinstance(value, dict):
        for name, keys in (("prompt_tokens_details", "cached_tokens"),
                           ("completion_tokens_details", "reasoning_tokens accepted_prediction_tokens rejected_prediction_tokens")):
            nested = scalar_fields(value.get(name), keys)
            if nested:
                result[name] = nested
    return result


def event_projection(value: dict) -> dict:
    result = scalar_fields(value, "event http_attempt question_id arm turn attempt_for_turn status "
                           "http_status finish_reason started_at latency_ms latency_seconds model response_model "
                           "request_bytes response_bytes request_max_tokens request_sha256 response_sha256 "
                           "protocol thinking tool_choice reconstructed pre_request_persistence_verified")
    if isinstance(value.get("reconstruction_source"), dict):
        result["reconstruction_source"] = scalar_fields(value["reconstruction_source"], "field file index line source_sha256")
    if isinstance(value.get("usage"), dict):
        result["usage"] = usage(value["usage"])
    return result


def event_counts(events: list[dict]) -> dict:
    requests, responses = {}, {}
    for event in events:
        target = requests if event.get("event") == "request" else responses if event.get("event") == "response" else None
        if target is None or type(event.get("http_attempt")) is not int:
            raise ValueError("HTTP ledger requires request/response events with integer http_attempt")
        attempt = event["http_attempt"]
        if attempt in target:
            raise ValueError("duplicate HTTP event for one attempt")
        target[attempt] = event
    for attempt in requests.keys() & responses.keys():
        for field in ("question_id", "arm", "turn", "request_sha256"):
            if requests[attempt].get(field) != responses[attempt].get(field):
                raise ValueError("mismatched request/response identity")
    return {"http_attempts": len(requests.keys() | responses.keys()),
            "request_events": len(requests), "response_events": len(responses),
            "failed_response_events": sum(row.get("status") != "ok" for row in responses.values()),
            "unmatched_request_attempts": sorted(requests.keys() - responses.keys()),
            "response_attempts_without_request": sorted(responses.keys() - requests.keys()),
            "finish_reason_counts": dict(collections.Counter(str(row.get("finish_reason", "missing")) for row in responses.values()))}


def block_projection(block: dict) -> dict:
    if not isinstance(block.get("id"), str) or not isinstance(block.get("text"), str):
        raise ValueError("visible evidence must contain string id and text")
    result = scalar_fields(block, "id title text kind provenance path start_line end_line start_char end_char "
                           "text_truncated sources_truncated relations_truncated")
    result["sources"] = [scalar_fields(item, "path start_line end_line start_char end_char")
                         for item in objects(block.get("sources"))]
    if "relations" in block:
        result["relations"] = []
        for item in objects(block["relations"]):
            relation = scalar_fields(item, "source target source_id target_id source_name target_name "
                                     "kind description confidence provenance")
            if "source_refs" in item:
                relation["source_refs"] = strings(item["source_refs"])
            result["relations"].append(relation)
    return result


def run_config_projection(config: dict) -> dict:
    result = scalar_fields(config, "format harness workers max_http_attempts seed dataset_sha256 kg_sha256 "
                           "raw_index_sha256 adapter_sha256 system_prompt_sha256 gold_forwarded_to_solver adapter_source_sha256 "
                           "tools_sha256 tool_policy")
    # The endpoint is intentionally excluded: URLs can embed credentials or query secrets.
    result["config"] = scalar_fields(config.get("config"), "model thinking max_tokens timeout max_retries "
                                      "max_searches max_model_turns per_search_chars total_context_chars top_k")
    result["solver_field_whitelist"] = strings(config.get("solver_field_whitelist"))
    result["job_order"] = [scalar_fields(item, "question_id arm") for item in objects(config.get("job_order"))]
    result["harness_source_sha256"] = scalar_fields(config.get("harness_source_sha256"), "agent_eval.py retrieval.py")
    return result


def summary_projection(summary: dict) -> dict:
    result = scalar_fields(summary, "format derived questions completed_jobs successful_jobs http_attempts failed_http_attempts "
                           "retries input_tokens output_tokens total_tokens cache_hit_tokens cache_miss_tokens attempts_without_usage "
                           "usage_complete kg_sha256_before kg_sha256_after kg_unchanged receipt ledger_integrity")
    if isinstance(summary.get("reconstruction"), dict):
        result["reconstruction"] = scalar_fields(summary["reconstruction"], "http_attempts original_request_events "
                           "original_response_events reconstructed_request_events response_records_recovered_from_results "
                           "pre_request_persistence_verified_attempts")
    kg_claims = summary.get("kg_claims_copied_from_original_summary_not_revalidated")
    if isinstance(kg_claims, dict):
        result["kg_claims_copied_from_original_summary_not_revalidated"] = scalar_fields(kg_claims, "kg_sha256_before kg_sha256_after kg_unchanged")
    metering = summary.get("metering")
    if isinstance(metering, dict):
        result["metering"] = scalar_fields(metering, "attempts_without_usage usage_complete cache_usage_complete policy")
        token_fields = "prompt_tokens completion_tokens total_tokens prompt_cache_hit_tokens prompt_cache_miss_tokens"
        for key in ("reported_token_totals", "attempts_reporting_field", "attempts_missing_field"):
            if isinstance(metering.get(key), dict):
                result["metering"][key] = scalar_fields(metering[key], token_fields)
    result["arms"] = strings(summary.get("arms"))
    result["unmatched_request_attempts"] = [item for item in summary.get("unmatched_request_attempts", [])
                                               if type(item) is int] if isinstance(summary.get("unmatched_request_attempts"), list) else []
    return result


def grade_projection(row: dict, status_only: bool) -> dict:
    result = scalar_fields(row, "blind_id request_id status judge_model review_type judge_config_sha256 "
                           "blind_input_sha256 request_sha256 response_sha256 finish_reason")
    if isinstance(row.get("usage"), dict):
        result["usage"] = usage(row["usage"])
    if status_only:
        return result
    verdict = row.get("verdict")
    if isinstance(verdict, dict):
        safe = {}
        for name, fields in (("fact_verdicts", "fact_index status answer_quote reason"),
                             ("claim_verdicts", "claim answer_quote support reason retrieval_support retrieval_reason"),
                             ("forbidden_claim_verdicts", "claim_index present answer_quote reason")):
            safe[name] = []
            for item in objects(verdict.get(name)):
                record = scalar_fields(item, fields)
                if "evidence_refs" in item:
                    record["evidence_refs"] = strings(item["evidence_refs"])
                if name == "claim_verdicts" and "retrieved_refs" in item:
                    record["retrieved_refs"] = strings(item["retrieved_refs"])
                if name == "claim_verdicts" and "retrieved_quotes" in item:
                    record["retrieved_quotes"] = [scalar_fields(quote, "ref quote") for quote in objects(item["retrieved_quotes"])]
                safe[name].append(record)
        safe["abstention"] = scalar_fields(verdict.get("abstention"), "appropriate unnecessary reason")
        safe["overall"] = scalar_fields(verdict.get("overall"), "rating reason gold_ambiguity")
        result["verdict"] = safe
    if isinstance(row.get("scores"), dict):
        result["scores"] = scalar_fields(row["scores"], "required_fact_score supported_claims "
                                           "unsupported_or_contradicted_claims claim_support_precision forbidden_claims_present "
                                           "appropriate_abstention unnecessary_abstention strict_pass source_support_precision "
                                           "retrieval_supported_claims retrieval_unsupported_claims retrieval_support_precision grounded_strict_pass")
    return result


def export_receipts(run_dir: Path, output_dir: Path, grades: Path | None = None,
                    arm_map: Path | None = None, grade_status_only: bool = False,
                    split: str = "unspecified") -> dict:
    if output_dir.exists():
        raise FileExistsError("refusing to overwrite an existing receipt directory")
    if grades is not None and arm_map is None:
        raise ValueError("--grades requires --arm-map to bind blind judgments")
    source_manifest = {}

    def read(path: Path, logical_name: str, jsonl: bool = False):
        data = path.read_bytes()  # Hash exactly the bytes parsed in this export.
        source_manifest[logical_name] = {"sha256": sha(data), "bytes": len(data)}
        try:
            value = [json.loads(line) for line in data.decode("utf-8").splitlines() if line.strip()] if jsonl else json.loads(data)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ValueError(f"invalid/incomplete input: {logical_name}") from exc
        if jsonl and not all(isinstance(item, dict) for item in value):
            raise ValueError(f"non-object input row: {logical_name}")
        if not jsonl and not isinstance(value, dict):
            raise ValueError(f"expected JSON object: {logical_name}")
        return value

    config = read(run_dir / "run-config.json", "run-config.json")
    results = read(run_dir / "results.jsonl", "results.jsonl", True)
    events = read(run_dir / "requests.jsonl", "requests.jsonl", True)
    summary = read(run_dir / "summary.json", "summary.json") if (run_dir / "summary.json").is_file() else None
    mapping = read(arm_map, "arm-map.json").get("mapping", {}) if arm_map else {}
    if not isinstance(mapping, dict):
        raise ValueError("arm map must contain a mapping object")
    grade_rows = read(grades, "grades.jsonl", True) if grades else []
    files: dict[str, bytes] = {}
    questions: dict[str, dict] = {}
    blocks = {}
    result_keys = set()

    def case(qid: str, arm: str) -> dict:
        qid, arm = identity(qid, "question id"), identity(arm, "arm")
        question = questions.setdefault(qid, {"schema_version": 1, "question_id": qid, "arms": {}})
        return question["arms"].setdefault(arm, {"status": "missing_result", "answer": None,
                                                  "citations": [], "invalid_citations": [], "searches": [],
                                                  "http_events": [], "grades": []})

    def register(block: dict) -> str:
        safe = block_projection(block)
        payload = encoded(safe)
        # Full metadata is part of identity: equal id/text with different sources
        # must not be silently conflated; equal blocks across arms are shared.
        ref = sha(payload)
        blocks.setdefault(ref, {"text_sha256": sha(safe["text"].encode("utf-8")), **safe})
        return f"evidence/{ref}.json"

    for job in objects(config.get("job_order")):
        case(job.get("question_id"), job.get("arm"))
    for row in results:
        qid, arm = row.get("question_id"), row.get("arm")
        item = case(qid, arm)
        if (qid, arm) in result_keys:
            raise ValueError("duplicate result for question/arm")
        result_keys.add((qid, arm))
        text = row.get("question")
        if not isinstance(text, str):
            raise ValueError("result question must be a string")
        previous = questions[qid].setdefault("question", text)
        if previous != text:
            raise ValueError("different question text across arms")
        item.update(scalar_fields(row, "status answer failure_kind elapsed_seconds had_http_retries"))
        item["citations"] = strings(row.get("citations"))
        item["invalid_citations"] = strings(row.get("invalid_citations"))
        item["budgets"] = scalar_fields(row.get("budgets"), "max_model_turns max_searches per_search_chars "
                                         "top_k total_context_chars unit visible_context_chars_used")
        flattened = []
        for search in objects(row.get("searches")):
            projected = scalar_fields(search, "search_index model_turn tool_call_id query status executed latency_ms truncated "
                                       "budget_exhausted visible_chars")
            shown = objects(search.get("evidence"))
            flattened.extend(shown)
            projected["evidence_refs"] = [register(block) for block in shown]
            item["searches"].append(projected)
        # Verify the summary view did not introduce unseen evidence. Its text is
        # not duplicated in this receipt; the ordered searches are authoritative.
        retrieved = objects(row.get("retrieved_evidence"))
        if [block_projection(block) for block in retrieved] != [block_projection(block) for block in flattened]:
            raise ValueError("retrieved_evidence differs from the ordered visible search evidence")
        visible_ids = {block["id"] for block in flattened}
        item["invalid_citations_recomputed"] = [citation for citation in item["citations"] if citation not in visible_ids]
        item["evidence_reference_count"] = len(flattened)
        item["unique_evidence_count"] = len({ref for search in item["searches"] for ref in search["evidence_refs"]})

    for event in events:
        case(event.get("question_id"), event.get("arm"))["http_events"].append(event_projection(event))
    overall_events = event_counts([event_projection(event) for event in events])
    for row in grade_rows:
        blind_id = row.get("blind_id")
        meta = mapping.get(blind_id)
        if not isinstance(meta, dict):
            raise ValueError("grade has no matching blind arm-map record")
        qid, arm = meta.get("question_id"), meta.get("arm")
        if (qid, arm) not in result_keys:
            raise ValueError("grade refers to a solver case outside this run")
        item = case(qid, arm)
        expected = meta.get("answer_sha256")
        # grade.py deliberately represents a failed solver's null answer as "".
        answer = item.get("answer") or ""
        if expected is not None and (not isinstance(answer, str) or sha(answer.encode()) != expected):
            raise ValueError("grade arm map does not match the solver answer hash")
        item["grades"].append(grade_projection(row, grade_status_only))
        item["grade_case"] = scalar_fields(meta, "answerable split category solver_status answer_sha256")
    for qid, question in sorted(questions.items()):
        for item in question["arms"].values():
            item["http_event_counts"] = event_counts(item["http_events"])
        files[f"questions/{qid}.json"] = encoded(question)
    for ref, block in sorted(blocks.items()):
        files[f"evidence/{ref}.json"] = encoded(block)
    files["run-config.json"] = encoded(run_config_projection(config))
    if summary is not None:
        files["summary.json"] = encoded(summary_projection(summary))
    manifest = {
        "format": "cairn-quality-review-receipts/v1", "split": split,
        "projection": "Strict field-whitelist copies; original input hashes refer to source bytes, not projected copies.",
        "retention": "Full solver-visible evidence text and allowlisted source metadata; answers, ordered queries, and HTTP event metadata. NOT raw complete provider responses; no model content duplicates, headers, credentials, or reasoning traces.",
        "heldout_policy": "Held-out raw artifacts are archived for audit only, not for tuning. This exporter does not inspect gold or alter questions, retrieval, prompts, answers, or scores.",
        "evidence_resolution": "Search evidence_refs are paths relative to this directory. Files are deduplicated by visible id/text/metadata; text_sha256 distinguishes clipped versions. Original retrieval truncation flags are preserved.",
        "grade_projection": "status and audit metadata only" if grade_status_only else "structured verdicts, scores, and audit metadata; no judge_raw_content",
        "original_inputs": source_manifest, "http_event_counts": overall_events,
        "exporter_sha256": sha(Path(__file__).read_bytes()),
        "questions": len(questions), "results": len(result_keys), "unique_evidence_blocks": len(blocks),
        "evidence_references": sum(item.get("evidence_reference_count", 0) for q in questions.values() for item in q["arms"].values()),
        "files": {name: {"sha256": sha(data), "bytes": len(data)} for name, data in sorted(files.items())},
    }
    files["manifest.json"] = encoded(manifest)
    output_dir.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix=".receipts-", dir=output_dir.parent))
    try:
        for name, data in files.items():
            destination = stage / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(data)
        if output_dir.exists():
            raise FileExistsError("receipt destination appeared during export")
        stage.rename(output_dir)
    finally:
        if stage.exists():
            shutil.rmtree(stage)
    return manifest


def export_judge_pilot(run_dir: Path, output_dir: Path) -> dict:
    """Archive pilot accounting and unvalidated candidates, never held-out analysis."""
    if output_dir.exists():
        raise FileExistsError("refusing to overwrite an existing judge receipt directory")
    inputs = {}

    def read(name, jsonl=False):
        data = (run_dir / name).read_bytes()
        inputs[name] = {"sha256": sha(data), "bytes": len(data)}
        return [json.loads(line) for line in data.decode().splitlines() if line.strip()] if jsonl else json.loads(data)

    raw_grades = read("grades.jsonl", True)
    raw_events = read("judge-usage.jsonl", True)
    selection = read("selection.json") if (run_dir / "selection.json").is_file() else None
    mapping = read("arm-map.json").get("mapping", {})
    if any(meta.get("split") == "holdout" for meta in mapping.values()):
        raise ValueError("judge-pilot exporter refuses held-out cases")
    # Hash the blinded input for binding; do not copy gold, original documents,
    # or the repeated solver-visible passages from the judge's prompt.
    if (run_dir / "blind.json").is_file():
        read("blind.json")
    if selection is None:
        selection = {"purpose": "Historical pilot; candidate identities taken from the persisted blind arm map, not selected anew.",
                     "selected": [scalar_fields(meta, "question_id arm") for meta in mapping.values()]}
    projected_grades = []
    for row in raw_grades:
        safe = grade_projection(row, False)
        safe.update(scalar_fields(row, "attempt failure_kind final_content_chars final_content_sha256 final_content_truncated"))
        meta = mapping.get(row.get("blind_id"))
        if not isinstance(meta, dict):
            raise ValueError("judge pilot grade lacks blind-map binding")
        safe["case"] = scalar_fields(meta, "question_id arm split category answerable solver_status answer_sha256")
        candidate, origin = row.get("candidate_verdict"), "candidate_verdict"
        if candidate is None and isinstance(row.get("judge_raw_content"), str):
            # v1/v2 saved the final content rather than a separate candidate.
            # Only parse a JSON object; arbitrary final text is never copied.
            try:
                candidate = json.loads(row["judge_raw_content"])
                origin = "JSON object parsed from recorded final content"
            except json.JSONDecodeError:
                candidate = None
        if isinstance(candidate, dict):
            projected = grade_projection({"verdict": candidate}, False).get("verdict", {})
            safe["candidate_origin"] = origin
            safe["candidate_matches_accepted_verdict"] = safe.get("verdict") == projected
            if not safe["candidate_matches_accepted_verdict"]:
                safe["candidate_verdict"] = projected
                safe["candidate_status"] = "unvalidated; not an accepted grade"
        projected_grades.append(safe)
    events = []
    for row in raw_events:
        safe = scalar_fields(row, "request_id blind_id attempt judge_model judge_config_sha256 blind_input_sha256 "
                             "review_type request_sha256 response_sha256 event reserved_tokens accounted_tokens started_at_unix "
                             "concurrency status failure_kind latency_seconds usage_is_estimate")
        if isinstance(row.get("usage"), dict):
            safe["usage"] = usage(row["usage"])
        events.append(safe)
    responses = [row for row in events if row.get("event") == "response"]
    totals, present, missing = {}, {}, {}
    for field in ("prompt_tokens", "completion_tokens", "total_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens"):
        values = [row.get("usage", {}).get(field) for row in responses]
        reported = [value for value in values if type(value) is int]
        totals[field], present[field], missing[field] = sum(reported), len(reported), len(values) - len(reported)
    summary = {"scope": "pilot harness validation only; not formal quality metrics or held-out failure analysis",
               "event_counts": dict(collections.Counter(row.get("event", "missing") for row in events)),
               "grade_status_counts": dict(collections.Counter(row.get("status", "missing") for row in projected_grades)),
               "reported_token_totals": totals, "attempts_reporting_field": present, "attempts_missing_field": missing,
               "estimated_response_events": sum(row.get("usage_is_estimate") is True for row in responses),
               "policy": "Only explicitly reported integer usage fields are summed; missing values are not filled or inferred."}
    selected = scalar_fields(selection, "purpose")
    selected["selected"] = [scalar_fields(item, "source_run question_id arm source_results_sha256")
                            for item in objects(selection.get("selected"))]
    files = {"selection.json": encoded(selected), "accounting.json": encoded(summary),
             "judge-events.json": encoded(events), "grade-attempts.json": encoded(projected_grades)}
    manifest = {"format": "cairn-judge-pilot-receipt/v1", "exporter_sha256": sha(Path(__file__).read_bytes()),
                "original_inputs": inputs, "retention": "Whitelist metadata, explicit usage, accepted structured verdicts and labeled unvalidated candidates only; no raw final content, headers, key or reasoning traces.",
                "files": {name: {"sha256": sha(data), "bytes": len(data)} for name, data in files.items()}}
    files["manifest.json"] = encoded(manifest)
    output_dir.mkdir(parents=True, exist_ok=False)
    for name, data in files.items():
        (output_dir / name).write_bytes(data)
    return manifest


def self_test() -> None:
    """Only fabricated data; no real evaluation run or provider calls."""
    with tempfile.TemporaryDirectory(prefix="receipt-fixture-") as temporary:
        root = Path(temporary)
        run = root / "run"
        run.mkdir()
        secret = "FIXTURE-SECRET-MUST-NOT-LEAK"
        block = {"id": "e:fixture", "text": "完整可见证据", "title": "sample", "headers": {"Authorization": secret},
                 "sources": [{"path": "docs/example.md", "start_line": 1, "end_line": 1, "api_key": secret}],
                 "relations": [{"source": "a", "target": "b", "kind": "references", "source_refs": ["docs/example.md"], "api_key": secret}]}
        clipped = {**block, "text": "完整", "text_truncated": True}
        result = {"question_id": "q01", "question": "虚构问题", "arm": "raw", "status": "ok", "answer": "虚构答案",
                  "citations": ["e:fixture", "invalid"], "invalid_citations": ["invalid"], "headers": secret,
                  "model_calls": [{"content": secret, "reasoning_content": secret}],
                  "searches": [{"search_index": index, "query": f"query{index}", "status": "ok", "evidence": [item]}
                               for index, item in enumerate([block, block, clipped], 1)],
                  "retrieved_evidence": [block, block, clipped]}
        config = {"job_order": [{"question_id": "q01", "arm": "raw"}], "tools_sha256": "c" * 64, "tool_policy": "fixture policy",
                  "config": {"model": "fixture", "endpoint": "https://user:" + secret + "@example.invalid", "api_key": secret}}
        base = {"question_id": "q01", "arm": "raw", "turn": 1, "http_attempt": 1, "request_sha256": "a" * 64}
        events = [{**base, "event": "request", "status": "in_flight", "headers": {"Authorization": secret},
                   "reconstructed": True, "pre_request_persistence_verified": False,
                   "reconstruction_source": {"file": "results.jsonl", "field": "model_calls", "line": 1, "index": 0, "api_key": secret}},
                  {**base, "event": "response", "status": "ok", "finish_reason": "stop", "response_sha256": "b" * 64,
                   "latency_ms": 2.5, "content": secret, "reasoning_content": secret,
                   "usage": {"prompt_tokens": 7, "completion_tokens": 3, "api_key": secret}},
                  {**base, "http_attempt": 2, "event": "request", "status": "in_flight"}]
        mapping = {"mapping": {"case-1": {"question_id": "q01", "arm": "raw", "split": "holdout",
                                           "answer_sha256": sha(result["answer"].encode()), "api_key": secret}}}
        grade = {"blind_id": "case-1", "status": "ok", "judge_raw_content": secret,
                 "verdict": {"overall": {"rating": "correct", "reason": "fixture explanation", "api_key": secret}},
                 "scores": {"strict_pass": True, "headers": secret}}
        for filename, data in (("run-config.json", config), ("summary.json", {"questions": 1, "api_key": secret}), ("arm-map.json", mapping)):
            (run / filename).write_bytes(encoded(data))
        for filename, rows in (("results.jsonl", [result]), ("requests.jsonl", events), ("grades.jsonl", [grade])):
            (run / filename).write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows))
        out = root / "receipt"
        manifest = export_receipts(run, out, run / "grades.jsonl", run / "arm-map.json", split="holdout")
        assert manifest["unique_evidence_blocks"] == 2 and manifest["evidence_references"] == 3
        question = json.loads((out / "questions/q01.json").read_text())["arms"]["raw"]
        assert question["searches"][0]["evidence_refs"] == question["searches"][1]["evidence_refs"]
        assert question["searches"][2]["evidence_refs"] != question["searches"][0]["evidence_refs"]
        assert question["http_event_counts"]["unmatched_request_attempts"] == [2]
        assert question["invalid_citations_recomputed"] == ["invalid"]
        assert question["http_events"][0]["reconstructed"] is True
        assert question["http_events"][0]["pre_request_persistence_verified"] is False
        assert question["http_events"][0]["reconstruction_source"]["field"] == "model_calls"
        assert manifest["exporter_sha256"] == sha(Path(__file__).read_bytes())
        projected_summary = summary_projection({"derived": True, "reconstruction": {"original_request_events": 1, "api_key": secret},
            "metering": {"reported_token_totals": {"prompt_tokens": 7}, "attempts_missing_field": {"prompt_tokens": 0}, "policy": "no inference"},
            "kg_claims_copied_from_original_summary_not_revalidated": {"kg_unchanged": True, "api_key": secret}})
        assert projected_summary["metering"]["reported_token_totals"]["prompt_tokens"] == 7
        assert projected_summary["derived"] is True and secret not in encoded(projected_summary).decode()
        example = json.loads((out / question["searches"][0]["evidence_refs"][0]).read_text())
        assert example["text"] == block["text"]
        assert example["relations"][0]["source_refs"] == ["docs/example.md"]
        for path in out.rglob("*.json"):
            assert secret not in path.read_text(), path
        second = root / "status-only"
        export_receipts(run, second, run / "grades.jsonl", run / "arm-map.json", grade_status_only=True)
        status_grade = json.loads((second / "questions/q01.json").read_text())["arms"]["raw"]["grades"][0]
        assert status_grade["status"] == "ok" and "verdict" not in status_grade and "scores" not in status_grade
        v2 = {**grade, "verdict": {"claim_verdicts": [{"claim": "fixture claim", "retrieval_support": "supported",
              "retrieved_refs": ["retrieved:1"], "retrieved_quotes": [{"ref": "retrieved:1", "quote": "可见", "api_key": secret}],
              "retrieval_reason": "short evidence explanation", "headers": secret}]},
              "scores": {"source_support_precision": 1.0, "retrieval_support_precision": 1.0, "grounded_strict_pass": True}}
        projected = grade_projection(v2, False)
        assert projected["verdict"]["claim_verdicts"][0]["retrieved_quotes"] == [{"ref": "retrieved:1", "quote": "可见"}]
        assert projected["scores"]["grounded_strict_pass"] is True
        assert secret not in encoded(projected).decode()
        try:
            export_receipts(run, out)
        except FileExistsError:
            pass
        else:
            raise AssertionError("existing output overwritten")
        (run / "selection.json").write_bytes(encoded({"purpose": "synthetic pilot", "selected": []}))
        (run / "judge-usage.jsonl").write_text(json.dumps({"event": "response", "status": "error",
            "usage": {"prompt_tokens": 11}, "headers": secret}) + "\n")
        judge_out = root / "judge"
        try:
            export_judge_pilot(run, judge_out)
        except ValueError:
            assert not judge_out.exists()
        else:
            raise AssertionError("judge pilot exporter accepted held-out case")
        mapping["mapping"]["case-1"]["split"] = "pilot"
        (run / "arm-map.json").write_bytes(encoded(mapping))
        (run / "grades.jsonl").write_text(json.dumps({**grade, "status": "error", "candidate_verdict": v2["verdict"]}) + "\n")
        export_judge_pilot(run, judge_out)
        accounting = json.loads((judge_out / "accounting.json").read_text())
        assert accounting["reported_token_totals"]["prompt_tokens"] == 11
        assert accounting["attempts_missing_field"]["completion_tokens"] == 1
        candidates = json.loads((judge_out / "grade-attempts.json").read_text())
        assert candidates[0]["candidate_status"] == "unvalidated; not an accepted grade"
        for path in judge_out.glob("*.json"):
            assert secret not in path.read_text(), path


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", type=Path)
    parser.add_argument("--judge-pilot-dir", type=Path, help="export completed pilot judging metadata; refuses holdout cases")
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--grades", type=Path)
    parser.add_argument("--arm-map", type=Path)
    parser.add_argument("--grade-status-only", action="store_true")
    parser.add_argument("--split", choices=("pilot", "dev", "holdout", "unspecified"), default="unspecified")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        print("synthetic receipt fixture checks passed")
        return
    if args.judge_pilot_dir is not None:
        if args.output_dir is None or args.run_dir is not None:
            parser.error("--judge-pilot-dir requires --output-dir and cannot be combined with --run-dir")
        manifest = export_judge_pilot(args.judge_pilot_dir, args.output_dir)
        print(json.dumps({"judge_pilot_files": len(manifest["files"])}))
        return
    if args.run_dir is None or args.output_dir is None:
        parser.error("--run-dir and --output-dir are required")
    manifest = export_receipts(args.run_dir, args.output_dir, args.grades, args.arm_map,
                               args.grade_status_only, args.split)
    print(json.dumps({"questions": manifest["questions"], "results": manifest["results"],
                      "unique_evidence_blocks": manifest["unique_evidence_blocks"]}))


if __name__ == "__main__":
    main()
