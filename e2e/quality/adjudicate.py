#!/usr/bin/env python3
"""Prepare and validate independent AI adjudication without reading arm maps.

This offline tool never calls a model. Run prepare only after the automatic
judge has stopped. Its receipts keep automatic coverage distinct from subsequent
AI adjudication; the original grades and request/usage ledger are never changed.
"""
import argparse
from collections import Counter
from datetime import datetime, timezone
import json
from pathlib import Path

import grade

REVIEW_TYPE = "independent AI adjudication; not DeepSeek automatic success; not human"
REVIEWER_LIMITATION = "Independent means separate from solver execution and automatic DeepSeek grading. Some AI reviewers previously participated in benchmark authorship, source review, implementation or dev diagnosis; they are not all fresh reviewers without prior knowledge. Assigned review inputs omit arm/split maps, but prior context and evidence style can weaken blinding. Failed candidates are visible and can anchor judgments. No human review is claimed."
CASE_KEYS = {"blind_id", "question", "answerable", "expected_answer", "required_facts",
             "forbidden_claims", "source_evidence", "verified_retrieved_originals",
             "answer", "retrieved_evidence"}
INSTRUCTIONS = """Independently assess every required fact, every substantive claim,
every forbidden claim, and abstention under the supplied frozen rubric. Use only
the anonymous case, its original-source evidence, actual retrieved evidence, and
failed candidate verdicts. Candidates are fallible suggestions, not ground truth.
Do not open arm maps, solver result files, split assignments, or other cases.
Do not alter gold. Do not mechanically convert a failed candidate into success.
Source support and actual retrieval support are separate. Retrieval contradiction
uses retrieval_support=unsupported and empty retrieved_refs/retrieved_quotes;
explain the contradiction in retrieval_reason. Preserve exact quote validation.
Return JSONL with blind_id, reviewer_id, reviewer_model (unknown if unavailable),
review_notes describing substantive independent checks, and verdict using exactly
the frozen schema. Do not provide hidden chain-of-thought. Brief evidence-based
verdict rationales are required. This is AI review, never human review or a
successful automatic DeepSeek judgment. Keep findings in the output file; send
the coordinator only aggregate progress, since split identities are hidden.
"""


def digest(path):
    return grade.sha(Path(path).read_bytes())


def read_rows(path):
    return grade.jsonl(path) if Path(path).exists() else []


def write_new(path, value):
    path = Path(path)
    if path.exists():
        raise ValueError("refusing to overwrite: " + str(path))
    grade.dump(path, value)


def write_rows_new(path, rows):
    path = Path(path)
    if path.exists():
        raise ValueError("refusing to overwrite: " + str(path))
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("x", encoding="utf-8") as stream:
        for row in rows:
            stream.write(json.dumps(row, ensure_ascii=False) + "\n")


def verify_sources(manifest):
    for item in manifest["inputs"].values():
        if digest(item["path"]) != item["sha256"]:
            raise ValueError("finalized adjudication input changed: " + item["path"])
    if digest(Path(grade.__file__)) != manifest["validator_file_sha256"]:
        raise ValueError("frozen validator changed during adjudication")
    if grade.sha(grade.SYSTEM.encode()) != manifest["rubric_system_sha256"]:
        raise ValueError("frozen rubric changed during adjudication")


def prepare(args):
    if not args.judge_complete:
        raise ValueError("prepare requires explicit confirmation that automatic judging has stopped")
    if args.batch_size < 1:
        raise ValueError("batch-size must be positive")
    out = Path(args.out_dir)
    if out.exists():
        raise ValueError("use a fresh adjudication output directory")
    blind = grade.read(args.blind)
    blind_sha = digest(args.blind)
    cases = blind["cases"]
    ids = [c["blind_id"] for c in cases]
    if len(set(ids)) != len(ids):
        raise ValueError("duplicate blind case ID")
    rows, ledger = read_rows(args.grades), read_rows(args.usage)
    for row in rows + ledger:
        if row.get("blind_input_sha256") != blind_sha or row.get("blind_id") not in ids:
            raise ValueError("automatic record does not match blind input")
    configs = {row["judge_config_sha256"] for row in rows + ledger}
    if len(configs) > 1:
        raise ValueError("automatic inputs mix judge configurations")
    success = {r["blind_id"] for r in rows if r.get("status") == "ok"}
    case_map = {c["blind_id"]: c for c in cases}
    for row in rows:
        if row.get("status") == "ok":
            grade.validate_verdict(case_map[row["blind_id"]], row["verdict"])
            if row["scores"] != grade.scores(case_map[row["blind_id"]], row["verdict"]):
                raise ValueError("automatic success has mismatched scores")
    requests = Counter(r["blind_id"] for r in ledger if r.get("event") == "request")
    response_ids = {r["request_id"] for r in ledger if r.get("event") == "response"}
    interrupted = [r["request_id"] for r in ledger if r.get("event") == "request" and r["request_id"] not in response_ids]
    if interrupted and not args.allow_interrupted:
        raise ValueError("automatic ledger has unresolved requests; confirm termination and use allow-interrupted to disclose these explicitly")
    eligible = [c for c in cases if c["blind_id"] not in success]
    limits = rows[0].get("judge_config", {}) if rows else {}
    request_cap_reached = bool(limits.get("max_calls") and sum(requests.values()) >= limits["max_calls"])
    latest = {r["blind_id"]: r for r in rows}
    selection_reasons = {}
    for case in eligible:
        cid = case["blind_id"]
        if requests[cid] >= 2:
            reason = "automatic_attempt_limit_exhausted"
        elif request_cap_reached and grade.retry_feedback(latest.get(cid, {})):
            reason = "automatic_error_request_budget_exhausted_before_retry"
        elif not requests[cid] and request_cap_reached:
            reason = "not_attempted_request_budget_exhausted"
        else:
            reason = "no_valid_automatic_judgment_at_closed_run"
        selection_reasons[cid] = reason
    batches = []
    out.mkdir(parents=True)
    for offset in range(0, len(eligible), args.batch_size):
        selected = []
        for case in eligible[offset:offset + args.batch_size]:
            failures = [{"attempt": r.get("attempt"), "failure_kind": r.get("failure_kind"),
                         "error": r.get("error", "")[:500],
                         "candidate_verdict": grade.candidate_verdict(r.get("candidate_verdict"))}
                        for r in rows if r["blind_id"] == case["blind_id"] and r.get("status") != "ok"]
            selected.append({"case": {k: v for k, v in case.items() if k in CASE_KEYS},
                             "automatic_attempt_count": requests[case["blind_id"]],
                             "automatic_failures": failures})
        filename = "batch-%03d.json" % (len(batches) + 1)
        write_new(out / filename, {"format": "cairn-independent-ai-adjudication-batch/v1",
                                  "review_type": REVIEW_TYPE, "instructions": INSTRUCTIONS,
                                  "frozen_rubric": grade.SYSTEM, "cases": selected})
        batches.append({"path": filename, "sha256": digest(out / filename),
                        "blind_ids": [x["case"]["blind_id"] for x in selected]})
    manifest = {"format": "cairn-independent-ai-adjudication-manifest/v1",
                "review_type": REVIEW_TYPE, "automatic_judge_complete_acknowledged": True,
                "prepared_at_utc": datetime.now(timezone.utc).isoformat(),
                "candidate_verdicts_visible_before_review": True,
                "selection_rule": "Every case without any successful automatic judgment, including exhausted retries, budget-stop and missing judgments; no arm or split filtering.",
                "inputs": {name: {"path": str(Path(path).resolve()), "sha256": digest(path)}
                           for name, path in (("blind", args.blind), ("automatic_grades", args.grades), ("automatic_usage", args.usage))},
                "validator_file_sha256": digest(Path(grade.__file__)),
                "rubric_system_sha256": grade.sha(grade.SYSTEM.encode()),
                "judge_schema_version": grade.JUDGE_SCHEMA_VERSION,
                "automatic_config_sha256": next(iter(configs), None),
                "expected_cases": len(cases), "automatic_success_count": len(success),
                "automatic_coverage": len(success) / len(cases) if cases else None,
                "automatic_request_count": sum(requests.values()),
                "automatic_limits": {k: limits.get(k) for k in ("max_calls", "max_total_tokens")},
                "automatic_request_cap_reached": request_cap_reached,
                "interrupted_request_ids": interrupted,
                "adjudication_required_count": len(eligible),
                "eligible_blind_ids": [c["blind_id"] for c in eligible],
                "eligible_attempt_counts": {c["blind_id"]: requests[c["blind_id"]] for c in eligible},
                "eligible_selection_reasons": selection_reasons,
                "batches": batches,
                "limitations": "A separate AI adjudicator receives failed AI candidates and can be anchored by them; this is not independent of previous judgments. Original and retrieved evidence must be checked afresh. Format/length failures may overrepresent complex cases, so this subset does not measure overall automatic judge reliability. Style may weaken method blinding. No human review is claimed."}
    write_new(out / "manifest.json", manifest)
    print(json.dumps({"expected": len(cases), "automatic_success": len(success), "to_adjudicate": len(eligible), "batches": len(batches)}))


def collect(args):
    manifest = grade.read(args.manifest)
    verify_sources(manifest)
    cases = {c["blind_id"]: c for c in grade.read(manifest["inputs"]["blind"]["path"])["cases"]}
    eligible = set(manifest["eligible_blind_ids"])
    existing = read_rows(args.out)
    existing_ids = {r["blind_id"] for r in existing}
    if len(existing_ids) != len(existing):
        raise ValueError("duplicate existing adjudication")
    manifest_sha = digest(args.manifest)
    for row in existing:
        if row.get("adjudication_manifest_sha256") != manifest_sha:
            raise ValueError("existing output uses another adjudication manifest")
    new, seen = [], set()
    for path in args.reviews:
        for row in grade.jsonl(path):
            cid = row["blind_id"]
            if cid not in eligible or cid in seen or cid in existing_ids:
                raise ValueError("ineligible or duplicate adjudication: " + cid)
            seen.add(cid)
            for field in ("reviewer_id", "reviewer_model", "review_notes"):
                if not isinstance(row.get(field), str) or not row[field].strip():
                    raise ValueError("review lacks " + field + ": " + cid)
            verdict = grade.validate_verdict(cases[cid], row["verdict"])
            new.append({"blind_id": cid, "status": "ok", "review_type": REVIEW_TYPE,
                        "grade_origin": "independent_ai_adjudication", "candidate_verdicts_visible_before_review": True,
                        "submitted_at_utc": datetime.now(timezone.utc).isoformat(),
                        "reviewer_id": row["reviewer_id"], "reviewer_model": row["reviewer_model"],
                        "review_notes": row["review_notes"], "verdict": verdict,
                        "scores": grade.scores(cases[cid], verdict),
                        "judge_schema_version": grade.JUDGE_SCHEMA_VERSION,
                        "rubric_system_sha256": manifest["rubric_system_sha256"],
                        "validator_file_sha256": manifest["validator_file_sha256"],
                        "blind_input_sha256": manifest["inputs"]["blind"]["sha256"],
                        "adjudication_manifest_sha256": manifest_sha,
                        "review_file_sha256": digest(path),
                        "case_sha256": grade.sha(json.dumps(cases[cid], sort_keys=True, ensure_ascii=False).encode()),
                        "automatic_attempt_count": manifest["eligible_attempt_counts"][cid]})
    # Validate the full submission before persisting any of its records.
    if not args.check_only:
        for row in new:
            grade.append(args.out, row)
    print(json.dumps({"validated": len(new), "previously_collected": len(existing),
                      "remaining_after_collection": len(eligible) - len(existing) - len(new),
                      "check_only": args.check_only}))


def finalize(args):
    manifest = grade.read(args.manifest)
    verify_sources(manifest)
    blind = grade.read(manifest["inputs"]["blind"]["path"])
    rows = read_rows(manifest["inputs"]["automatic_grades"]["path"])
    automatic = {}
    for row in rows:
        if row["blind_id"] not in automatic or row["status"] == "ok" or automatic[row["blind_id"]]["status"] != "ok":
            automatic[row["blind_id"]] = row
    adjudicated = {}
    for row in read_rows(args.adjudicated):
        cid = row["blind_id"]
        if cid in adjudicated or cid not in manifest["eligible_blind_ids"]:
            raise ValueError("duplicate/ineligible adjudicated row")
        if row.get("adjudication_manifest_sha256") != digest(args.manifest) or row.get("review_type") != REVIEW_TYPE:
            raise ValueError("adjudication provenance mismatch")
        case = next(c for c in blind["cases"] if c["blind_id"] == cid)
        grade.validate_verdict(case, row["verdict"])
        if row["scores"] != grade.scores(case, row["verdict"]):
            raise ValueError("adjudicated scores mismatch frozen rubric")
        adjudicated[cid] = row
    final = []
    for case in blind["cases"]:
        cid = case["blind_id"]
        row = adjudicated.get(cid, automatic.get(cid, {"blind_id": cid, "status": "missing", "blind_input_sha256": manifest["inputs"]["blind"]["sha256"]}))
        final.append({**row, "grade_origin": "independent_ai_adjudication" if cid in adjudicated else "automatic_deepseek" if row["status"] == "ok" else "unresolved"})
    unresolved = sum(r["status"] != "ok" for r in final)
    if unresolved and not args.allow_incomplete:
        raise ValueError("unresolved cases remain; use allow-incomplete only with explicit disclosure")
    if manifest["expected_cases"] != manifest["automatic_success_count"] + len(adjudicated) + unresolved:
        raise ValueError("coverage categories do not partition expected cases")
    write_rows_new(args.out, final)
    receipt = {"format": "cairn-independent-ai-adjudication-completion/v1", "review_type": REVIEW_TYPE,
               "reviewer_provenance_limitations": REVIEWER_LIMITATION,
               "adjudication_manifest_sha256": digest(args.manifest),
               "automatic_grades_sha256": manifest["inputs"]["automatic_grades"]["sha256"],
               "automatic_usage_sha256": manifest["inputs"]["automatic_usage"]["sha256"],
               "adjudicated_grades_sha256": digest(args.adjudicated), "final_derived_grades_sha256": digest(args.out),
               "expected_cases": manifest["expected_cases"], "automatic_success_count": manifest["automatic_success_count"],
               "automatic_coverage": manifest["automatic_coverage"], "independently_adjudicated_count": len(adjudicated),
               "unresolved_count": unresolved, "final_scored_count": len(final) - unresolved,
               "automatic_failures_and_fees_preserved": True,
               "disclosure": "Final coverage includes separate independent AI adjudication and must not be described as DeepSeek automatic success. Original failed grades and all usage remain unchanged. The coordinator alone may join arm/split maps for aggregate and dev-only analysis."}
    write_new(args.receipt, receipt)
    print(json.dumps({k: receipt[k] for k in ("expected_cases", "automatic_success_count", "independently_adjudicated_count", "unresolved_count")}))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="command", required=True)
    a = sub.add_parser("prepare")
    for flag in ("blind", "grades", "usage", "out-dir"):
        a.add_argument("--" + flag, required=True)
    a.add_argument("--judge-complete", action="store_true")
    a.add_argument("--allow-interrupted", action="store_true", help="explicitly disclose unresolved request receipts after confirmed termination")
    a.add_argument("--batch-size", type=int, default=4)
    a.set_defaults(func=prepare)
    a = sub.add_parser("collect")
    a.add_argument("--manifest", required=True); a.add_argument("--reviews", action="append", required=True)
    a.add_argument("--out", required=True); a.add_argument("--check-only", action="store_true")
    a.set_defaults(func=collect)
    a = sub.add_parser("finalize")
    for flag in ("manifest", "adjudicated", "out", "receipt"):
        a.add_argument("--" + flag, required=True)
    a.add_argument("--allow-incomplete", action="store_true")
    a.set_defaults(func=finalize)
    args = p.parse_args(); args.func(args)


if __name__ == "__main__":
    main()
