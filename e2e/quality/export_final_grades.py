#!/usr/bin/env python3
"""Offline whitelist export of frozen final grades, after blinded adjudication.

The arm map is used only for reporting after verdicts have been frozen. This
does not include raw blind inputs, provider reasoning, or request ledgers.
"""
import argparse
from collections import Counter, defaultdict
from pathlib import Path
import json

from export_receipts import encoded, grade_projection, identity, scalar_fields, sha, strings


def export(run_dir, output_dir):
    if output_dir.exists():
        raise ValueError("refusing to overwrite a final-grade projection")
    names = ("final-derived-grades.jsonl", "arm-map.json", "final-summary.json",
             "adjudication-receipt.json", "adjudication-reviewer-provenance.json")
    raw = {name: (run_dir / name).read_bytes() for name in names}
    sources = {name: {"sha256": sha(data), "bytes": len(data)} for name, data in raw.items()}
    rows = [json.loads(line) for line in raw[names[0]].decode().splitlines() if line.strip()]
    private = json.loads(raw["arm-map.json"])
    mapping = private["mapping"]
    summary = json.loads(raw["final-summary.json"])
    receipt = json.loads(raw["adjudication-receipt.json"])
    provenance = json.loads(raw["adjudication-reviewer-provenance.json"])
    if receipt["final_derived_grades_sha256"] != sources[names[0]]["sha256"]:
        raise ValueError("completion receipt does not bind final grades")
    if set(mapping) != {row["blind_id"] for row in rows} or len(rows) != len(mapping):
        raise ValueError("mapping and final grades must match one-to-one")
    if summary["gold_sha256"] != private["gold_sha256"]:
        raise ValueError("summary/gold identity mismatch")
    files, index, groups = {}, [], defaultdict(list)
    for row in rows:
        cid = identity(row["blind_id"], "blind ID")
        if row.get("status") != "ok" or row["blind_input_sha256"] != private["blind_input_sha256"]:
            raise ValueError("invalid or mismatched final grade")
        safe = grade_projection(row, False)
        safe.pop("usage", None)  # Total fees, including failures, belong to the complete automatic receipts.
        safe.update(scalar_fields(row, "grade_origin reviewer_id reviewer_model review_notes submitted_at_utc "
                                  "judge_schema_version rubric_system_sha256 validator_file_sha256 "
                                  "adjudication_manifest_sha256 review_file_sha256 "
                                  "candidate_verdicts_visible_before_review"))
        safe["mapping"] = scalar_fields(mapping[cid], "question_id arm answerable split category solver_status "
                                         "answer_sha256 answer_empty retrieved_evidence_count declared_citation_count unresolved_citation_count")
        safe["source_final_record_sha256"] = sha(encoded(row))
        safe["source_final_record_hash_encoding"] = "canonical sorted JSON, UTF-8, two-space indentation and final newline"
        path = "cases/" + cid + ".json"
        files[path] = encoded(safe)
        if len(files[path]) > 160 * 1024:
            raise ValueError("case projection exceeds 160 KiB: " + cid)
        index.append({"blind_id": cid, **scalar_fields(safe["mapping"], "question_id arm split category"),
                      "grade_origin": safe["grade_origin"], "file": path, "sha256": sha(files[path])})
        for split in (safe["mapping"]["split"], "full"):
            groups[(safe["mapping"]["arm"], split)].append(safe)
    aggregates = []
    for (arm, split), values in sorted(groups.items()):
        n = len(values)
        strict = sum(v["scores"]["strict_pass"] for v in values)
        grounded = sum(v["scores"]["grounded_strict_pass"] for v in values)
        aggregates.append({"arm": arm, "split": split, "expected": n,
                           "strict_pass_count": strict, "strict_pass_rate": strict / n,
                           "grounded_strict_pass_count": grounded, "grounded_strict_pass_rate": grounded / n,
                           "mean_required_fact_score": sum(v["scores"]["required_fact_score"] for v in values) / n,
                           "grade_origin_counts": dict(Counter(v["grade_origin"] for v in values))})
    safe_summary = scalar_fields(summary, "format review_type limitations gold_sha256")
    safe_summary["metric_notes"] = strings(summary.get("metric_notes"))
    group_fields = "arm split expected judged solver_error_or_missing_count empty_answer_count citation_metadata_available_count " \
                   "answers_without_declared_citations answers_without_retrieved_evidence answers_with_unresolved_citations " \
                   "judge_errors_or_missing mean_required_fact_score mean_claim_support_precision mean_source_support_precision " \
                   "mean_retrieval_support_precision retrieval_support_judged_count grounded_strict_pass_count " \
                   "grounded_strict_pass_rate_judged strict_pass_count strict_pass_rate_judged answers_with_unsupported_claims " \
                   "unanswerable_judged gold_ambiguity_note_count_unadjudicated appropriate_abstentions unnecessary_abstentions"
    safe_summary["groups"] = [scalar_fields(group, group_fields) for group in summary["groups"]]
    safe_summary["full_and_split_pass_aggregates"] = aggregates
    files["final-summary.json"] = encoded(safe_summary)
    coverage = scalar_fields(receipt, "format review_type reviewer_provenance_limitations adjudication_manifest_sha256 "
                             "automatic_grades_sha256 automatic_usage_sha256 adjudicated_grades_sha256 "
                             "final_derived_grades_sha256 expected_cases automatic_success_count automatic_coverage "
                             "independently_adjudicated_count unresolved_count final_scored_count "
                             "automatic_failures_and_fees_preserved disclosure")
    coverage["automatic_failure_archive"] = "../judge-auto/"
    files["coverage.json"] = encoded(coverage)
    safe_provenance = scalar_fields(provenance, "format review_type limitations model_identifiers original_frozen_inputs_and_verdicts_unchanged")
    safe_provenance["reviewers"] = {k: v for k, v in provenance.get("reviewers", {}).items() if isinstance(k, str) and isinstance(v, str)}
    files["reviewer-provenance.json"] = encoded(safe_provenance)
    files["index.json"] = encoded({"format": "cairn-final-grade-index/v1", "cases": index})
    manifest = {"format": "cairn-final-grade-projection/v1", "cases": len(rows), "source_files": sources,
                "gold_sha256": private["gold_sha256"], "blind_input_sha256": private["blind_input_sha256"],
                "projection": "Explicit scalar/nested field whitelists; no raw blind input, raw final provider content, CoT, or complete request ledger. Verdict rationales and exact evidence quotes are retained.",
                "automatic_failure_archive": "../judge-auto/",
                "coverage_disclosure": "Final grades combine automatic and separately labeled AI adjudication. Automatic coverage remains as stated in coverage.json, not 100%.",
                "heldout_policy": "Holdout per-case files are frozen audit artifacts, not input to optimization; detailed analysis is dev-only.",
                "files": {name: {"sha256": sha(data), "bytes": len(data)} for name, data in sorted(files.items())}}
    files["manifest.json"] = encoded(manifest)
    for name, data in files.items():
        path = output_dir / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
    print(json.dumps({"cases": len(rows), "files": len(files), "max_case_bytes": max(len(v) for k, v in files.items() if k.startswith("cases/")), "output": str(output_dir)}))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--run-dir", type=Path, required=True)
    p.add_argument("--output-dir", type=Path, required=True)
    args = p.parse_args()
    export(args.run_dir, args.output_dir)


if __name__ == "__main__":
    main()
