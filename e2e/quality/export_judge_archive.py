#!/usr/bin/env python3
"""Offline whitelist archive of a completed automatic judge run.

The public receipt is separate from the solver manifest. Original blinded inputs
are archived outside the receipt directory, as an exact-byte deterministic gzip;
they are not included in the public file list. Held-out records are retained for
audit, not analyzed for tuning. No provider calls or primary-run writes occur.
"""
from __future__ import annotations

import argparse
import collections
import contextlib
import gzip
import io
import json
import shutil
import tempfile
from pathlib import Path

import export_receipts as receipts
import grade


CASE_FIELDS = ("question_id arm split category answerable solver_status answer_sha256 "
               "answer_empty declared_citation_count retrieved_evidence_count unresolved_citation_count")
EVENT_FIELDS = ("request_id blind_id attempt judge_model judge_config_sha256 blind_input_sha256 "
                "review_type request_sha256 response_sha256 event reserved_tokens accounted_tokens "
                "started_at_unix concurrency status failure_kind error retry_feedback latency_seconds usage_is_estimate")
FORBIDDEN_FIELDS = {"api_key", "apikey", "authorization", "headers", "reasoning", "reasoning_content",
                    "judge_raw_content", "final_content", "assistant_message", "api_base"}


def reject_forbidden_fields(value):
    if isinstance(value, dict):
        if FORBIDDEN_FIELDS.intersection(key.lower() for key in value):
            raise ValueError("forbidden field in public projection")
        for child in value.values():
            reject_forbidden_fields(child)
    elif isinstance(value, list):
        for child in value:
            reject_forbidden_fields(child)


def config_projection(config):
    result = receipts.scalar_fields(config, "model temperature judge_schema_version max_tokens system_sha256 "
        "max_calls max_total_tokens max_input_chars timeout_seconds validation_retries_per_case final_content_limit_chars")
    extra = config.get("extra_body")
    if isinstance(extra, dict):
        result["extra_body"] = receipts.scalar_fields(extra, "reasoning_effort")
        if isinstance(extra.get("thinking"), dict):
            result["extra_body"]["thinking"] = receipts.scalar_fields(extra["thinking"], "type")
    return result


def attempt_projection(row):
    result = receipts.grade_projection(row, False)
    result.update(receipts.scalar_fields(row, "attempt failure_kind error retry_feedback final_content_chars "
                                        "final_content_sha256 final_content_truncated"))
    candidate = row.get("candidate_verdict")
    if isinstance(candidate, dict):
        safe = receipts.grade_projection({"verdict": candidate}, False)["verdict"]
        result["candidate_matches_accepted_verdict"] = safe == result.get("verdict")
        if not result["candidate_matches_accepted_verdict"]:
            result["candidate_verdict"] = safe
            result["candidate_status"] = "unvalidated; not an accepted grade"
    return result


def accounting(rows, events, mapping):
    requests, responses, attempts = {}, {}, {}
    for event in events:
        target = requests if event.get("event") == "request" else responses if event.get("event") == "response" else None
        rid = event.get("request_id")
        if target is None or not isinstance(rid, str) or rid in target:
            raise ValueError("invalid or duplicate judge HTTP event")
        target[rid] = event
    for row in rows:
        rid = row.get("request_id")
        if not isinstance(rid, str) or rid in attempts:
            raise ValueError("invalid or duplicate judge attempt")
        attempts[rid] = row
    if requests.keys() != responses.keys() or requests.keys() != attempts.keys():
        raise ValueError("completed archive requires paired HTTP events and one grade per request")
    for rid, row in attempts.items():
        for field in ("blind_id", "attempt", "judge_config_sha256", "blind_input_sha256", "request_sha256"):
            if not (row.get(field) == requests[rid].get(field) == responses[rid].get(field)):
                raise ValueError("grade/HTTP identity mismatch")
        for field in ("status", "response_sha256", "usage"):
            if row.get(field) != responses[rid].get(field):
                raise ValueError("grade/response mismatch")
    totals, present, missing = {}, {}, {}
    for field in ("prompt_tokens", "completion_tokens", "total_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens"):
        values = [row.get("usage", {}).get(field) for row in responses.values()]
        reported = [value for value in values if type(value) is int and value >= 0]
        totals[field], present[field], missing[field] = sum(reported), len(reported), len(values) - len(reported)
    latest = {row["blind_id"]: row for row in rows}
    return {"review_type": "original automatic AI grading; not human review or later adjudication",
            "expected_cases": len(mapping), "attempts": len(rows),
            "grade_status_counts": dict(collections.Counter(row.get("status", "missing") for row in rows)),
            "latest_case_status_counts": dict(collections.Counter(latest.get(cid, {}).get("status", "missing") for cid in mapping)),
            "event_counts": {"request": len(requests), "response": len(responses)}, "ledger_pairing": "PASS",
            "finish_reason_counts": dict(collections.Counter(str(row.get("finish_reason", "missing")) for row in rows)),
            "failure_kind_counts": dict(collections.Counter(row["failure_kind"] for row in rows if "failure_kind" in row)),
            "candidate_records": sum(isinstance(row.get("candidate_verdict"), dict) for row in rows),
            "candidate_records_on_error": sum(row.get("status") != "ok" and isinstance(row.get("candidate_verdict"), dict) for row in rows),
            "reported_token_totals": totals, "attempts_reporting_field": present, "attempts_missing_field": missing,
            "accounted_tokens": sum(row["accounted_tokens"] for row in responses.values()),
            "estimated_response_events": sum(row.get("usage_is_estimate") is True for row in responses.values()),
            "policy": "Only explicit nonnegative integer usage fields are summed. Missing usage is not inferred as zero; accounting estimates remain separately labeled."}


def export_archive(run_dir: Path, output_dir: Path, blind_archive: Path):
    if output_dir.exists() or blind_archive.exists():
        raise FileExistsError("refusing to overwrite archive output")
    if output_dir.resolve() in blind_archive.resolve().parents:
        raise ValueError("raw blinded input must be outside the public receipt directory")
    source_bytes = {name: (run_dir / name).read_bytes() for name in
                    ("grades.jsonl", "judge-usage.jsonl", "arm-map.json", "blind.json")}
    rows = [json.loads(line) for line in source_bytes["grades.jsonl"].splitlines() if line.strip()]
    raw_events = [json.loads(line) for line in source_bytes["judge-usage.jsonl"].splitlines() if line.strip()]
    private = json.loads(source_bytes["arm-map.json"])
    blind = json.loads(source_bytes["blind.json"])
    blind_hash = receipts.sha(source_bytes["blind.json"])
    mapping = private["mapping"]
    if private["blind_input_sha256"] != blind_hash or private["gold_sha256"] != blind["gold_sha256"]:
        raise ValueError("blinded input/private map mismatch")
    case_ids = [row["blind_id"] for row in blind["cases"]]
    if len(case_ids) != len(set(case_ids)) or set(case_ids) != mapping.keys():
        raise ValueError("blinded case/private map mismatch")
    if any(row.get("blind_input_sha256") != blind_hash or row.get("blind_id") not in mapping for row in rows + raw_events):
        raise ValueError("judge rows belong to a different input")
    projected_map = receipts.scalar_fields(private, "format blind_input_sha256 gold_sha256")
    projected_map["mapping"] = {cid: receipts.scalar_fields(meta, CASE_FIELDS) for cid, meta in mapping.items()}
    configs = {}
    for row in rows:
        config_sha = row["judge_config_sha256"]
        safe = config_projection(row["judge_config"])
        if config_sha in configs and configs[config_sha] != safe:
            raise ValueError("inconsistent judge configuration under one hash")
        configs[config_sha] = safe
    projected_events = []
    for row in raw_events:
        safe = receipts.scalar_fields(row, EVENT_FIELDS)
        if isinstance(row.get("usage"), dict):
            safe["usage"] = receipts.usage(row["usage"])
        projected_events.append(safe)
    public = {"arm-map.json": projected_map, "judge-configs.json": configs,
              "grade-attempts.json": [attempt_projection(row) for row in rows],
              "judge-events.json": projected_events,
              "accounting.json": accounting(rows, raw_events, mapping)}
    # Run the existing offline summary implementation on frozen bytes, never on a
    # potentially changing primary file. Raw dev bad cases are temporary only.
    with tempfile.TemporaryDirectory(prefix="judge-summary-") as temporary:
        temp = Path(temporary)
        for name in ("grades.jsonl", "arm-map.json"):
            (temp / name).write_bytes(source_bytes[name])
        with contextlib.redirect_stdout(io.StringIO()):
            grade.summarize(argparse.Namespace(private_map=str(temp / "arm-map.json"),
                grades=str(temp / "grades.jsonl"), out=str(temp / "summary.json"),
                dev_badcases=str(temp / "dev-badcases.json")))
        public["summary.json"] = json.loads((temp / "summary.json").read_bytes())
        dev = json.loads((temp / "dev-badcases.json").read_bytes())
        if dev.get("split") != "dev" or any(row.get("split") != "dev" for row in dev["cases"]):
            raise ValueError("summary emitted non-dev detailed bad cases")
        public["dev-badcases.json"] = {"split": "dev", "projection": "grade.py summarize output with whitelist only",
            "cases": [{**receipts.scalar_fields(row, CASE_FIELDS), **attempt_projection(row)} for row in dev["cases"]],
            "gold_review_notes": [receipts.scalar_fields(row, "question_id arm note interpretation") for row in dev["gold_review_notes"]]}
    for value in public.values():
        reject_forbidden_fields(value)
    files = {name: receipts.encoded(value) for name, value in public.items()}
    compressed = gzip.compress(source_bytes["blind.json"], mtime=0)
    manifest = {"format": "cairn-automatic-judge-archive/v1", "review_type": "AI judge, not human",
        "exporter_sha256": receipts.sha(Path(__file__).read_bytes()),
        "projection_module_sha256": receipts.sha(Path(receipts.__file__).read_bytes()),
        "summary_module_sha256": receipts.sha(Path(grade.__file__).read_bytes()),
        "original_inputs": {name: {"sha256": receipts.sha(data), "bytes": len(data)} for name, data in source_bytes.items()},
        "local_blind_input_archive": {"sha256": receipts.sha(compressed), "bytes": len(compressed),
            "uncompressed_sha256": blind_hash, "included_in_public_receipt": False,
            "retention": "Exact original blinded input bytes; local audit only, excluded from public file list."},
        "retention": "Whitelist metadata, reported usage, accepted verdicts and labeled unvalidated candidates. No raw final content, provider reasoning traces, headers, credentials, endpoint or raw HTTP payloads.",
        "heldout_policy": "Held-out input and automatic grades are archived for later audit only; no held-out failure analysis or tuning is performed here.",
        "adjudication": "Original automatic attempts only. Subsequent independent adjudication is a separate artifact and does not replace these records.",
        "solver_manifest": "Parent solver receipt manifest and solver source hashes are unchanged; this manifest covers only judge-auto files.",
        "public_files": sorted([*files, "manifest.json"]),
        "files": {name: {"sha256": receipts.sha(data), "bytes": len(data)} for name, data in files.items()}}
    files["manifest.json"] = receipts.encoded(manifest)
    if any((run_dir / name).read_bytes() != data for name, data in source_bytes.items()):
        raise ValueError("source judge run changed during export")
    output_dir.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix=".judge-archive-", dir=output_dir.parent))
    try:
        for name, data in files.items():
            (stage / name).write_bytes(data)
        blind_archive.parent.mkdir(parents=True, exist_ok=True)
        with blind_archive.open("xb") as handle:
            handle.write(compressed)
        stage.rename(output_dir)
    finally:
        if stage.exists():
            shutil.rmtree(stage)
    return manifest


def self_test():
    secret = "SYNTHETIC-SECRET-MUST-NOT-LEAK"
    config = config_projection({"model": "fixture", "api_base": secret, "api_key": secret,
        "extra_body": {"thinking": {"type": "enabled", "headers": secret}, "reasoning_effort": "low", "reasoning": secret}})
    row = {"status": "error", "final_content": secret, "headers": secret, "reasoning_content": secret,
        "failure_kind": "verdict_validation", "candidate_verdict": {"claim_verdicts": [{"claim": "fixture",
            "retrieval_support": "supported", "retrieved_refs": ["retrieved:1"],
            "retrieved_quotes": [{"ref": "retrieved:1", "quote": "fixture quote", "api_key": secret}]}]}}
    projected = attempt_projection(row)
    assert projected["candidate_status"].startswith("unvalidated")
    assert projected["candidate_verdict"]["claim_verdicts"][0]["retrieved_quotes"] == [{"ref": "retrieved:1", "quote": "fixture quote"}]
    assert secret not in receipts.encoded([config, projected]).decode()
    reject_forbidden_fields([config, projected])
    base = {"request_id": "request-fixture", "blind_id": "blind-fixture", "attempt": 1,
            "judge_config_sha256": "a", "blind_input_sha256": "b", "request_sha256": "c"}
    record = {**base, "status": "error", "response_sha256": "d", "usage": {"prompt_tokens": 7}}
    events = [{**base, "event": "request"}, {**record, "event": "response", "accounted_tokens": 12, "usage_is_estimate": True}]
    summary = accounting([record], events, {"blind-fixture": {}})
    assert summary["attempts_missing_field"]["completion_tokens"] == 1
    assert summary["reported_token_totals"]["prompt_tokens"] == 7
    assert summary["estimated_response_events"] == 1
    try:
        accounting([record], events[:1], {"blind-fixture": {}})
    except ValueError:
        pass
    else:
        raise AssertionError("incomplete ledger was accepted")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", type=Path)
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--blind-archive", type=Path, help="local-only .gz outside public receipts")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        print("synthetic judge archive checks passed")
    else:
        if not all((args.run_dir, args.output_dir, args.blind_archive)):
            parser.error("--run-dir, --output-dir and --blind-archive are required")
        manifest = export_archive(args.run_dir, args.output_dir, args.blind_archive)
        print(json.dumps({"public_files": len(manifest["public_files"]),
                          "manifest_sha256": receipts.sha(receipts.encoded(manifest))}))


if __name__ == "__main__":
    main()
