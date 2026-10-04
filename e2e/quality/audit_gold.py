#!/usr/bin/env python3
"""Audit source-backed QA evidence and optionally freeze an AI-reviewed benchmark.

This verifies provenance, not semantic truth. A separate, explicit AI source review
must approve every question before --freeze can be used. Never build gold from KG
or solver output. Line numbers are one-based, inclusive; quotes are exact text.
"""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
import unicodedata


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def questions(value):
    if isinstance(value, list):
        return value
    return value["questions"]


def safe_source(root, relative):
    path = Path(relative)
    if path.is_absolute() or ".." in path.parts or "\\" in relative:
        raise ValueError("unsafe source path: " + relative)
    resolved = (root / path).resolve(strict=True)
    if not resolved.is_relative_to(root.resolve()):
        raise ValueError("source escapes corpus: " + relative)
    return resolved


def evidence(root, ev):
    path = safe_source(root, ev["path"])
    raw = path.read_bytes()
    text = raw.decode("utf-8")
    lines = text.splitlines(keepends=True)
    start, end = ev["start_line"], ev["end_line"]
    if type(start) is not int or type(end) is not int or not 1 <= start <= end <= len(lines):
        raise ValueError("evidence line interval is outside source")
    quote = ev["quote"]
    if not isinstance(quote, str) or not quote.strip():
        raise ValueError("evidence quote is empty")
    excerpt = "".join(lines[start - 1:end])
    if quote not in excerpt:
        raise ValueError("quote is not verbatim within stated source lines")
    expected = ev.get("source_sha256")
    if expected and expected != sha(raw):
        raise ValueError("source hash changed")
    return {**ev, "source_sha256": sha(raw), "source_excerpt": excerpt}, len(lines)


def audit(paths, root):
    all_questions, errors, warnings = [], [], []
    files, seen_ids, seen_questions = {}, set(), {}
    categories, source_counts, authors = Counter(), Counter(), Counter()
    for path in paths:
        raw = Path(path).read_bytes()
        items = questions(json.loads(raw))
        authors[str(path)] = len(items)
        for q in items:
            qid = q.get("id", "<missing>")
            try:
                if not isinstance(qid, str) or qid == "<missing>" or qid in seen_ids:
                    raise ValueError("missing or duplicate question id")
                seen_ids.add(qid)
                for field in ("question", "category", "expected_answer"):
                    if not isinstance(q.get(field), str) or not q[field].strip():
                        raise ValueError("missing nonempty " + field)
                if type(q.get("answerable")) is not bool:
                    raise ValueError("answerable must be boolean")
                for field in ("required_facts", "forbidden_claims"):
                    if not isinstance(q.get(field), list) or any(not isinstance(x, str) or not x.strip() for x in q[field]):
                        raise ValueError(field + " must contain nonempty strings")
                if not q["required_facts"]:
                    raise ValueError("required_facts must include expected facts or justified abstention")
                normalized = re.sub(r"[\W_]+", "", unicodedata.normalize("NFKC", q["question"]).casefold())
                if normalized in seen_questions:
                    raise ValueError("duplicate question text with " + seen_questions[normalized])
                seen_questions[normalized] = qid
                if not isinstance(q.get("evidence"), list) or not q["evidence"]:
                    raise ValueError("at least one original-source evidence excerpt is required")
                checked = []
                for ev in q["evidence"]:
                    verified, line_count = evidence(root, ev)
                    checked.append(verified)
                    files[ev["path"]] = {"sha256": verified["source_sha256"], "lines": line_count}
                all_questions.append({**q, "evidence": checked})
                categories[q["category"]] += 1
                for source in {e["path"] for e in checked}:
                    source_counts[source] += 1
            except (KeyError, TypeError, ValueError, OSError) as exc:
                errors.append({"question_id": qid, "error": str(exc)})
    # Same evidence can legitimately support distinct tasks; make reuse visible.
    clusters = Counter(tuple(sorted({e["path"] for e in q["evidence"]})) for q in all_questions)
    for sources, count in clusters.items():
        if count >= 5:
            warnings.append({"kind": "source_cluster_reuse", "source_paths": sources, "questions": count})
    report = {"format": "cairn-qa-source-audit/v1", "passed": not errors,
              "review_type": "automated provenance validation; semantic review is AI, not human",
              "question_count": len(all_questions), "answerable": sum(q["answerable"] for q in all_questions),
              "unanswerable": sum(not q["answerable"] for q in all_questions),
              "multi_document_questions": sum(len({e["path"] for e in q["evidence"]}) > 1 for q in all_questions),
              "multiple_excerpt_questions": sum(len(q["evidence"]) > 1 for q in all_questions),
              "distinct_source_documents": len(files), "source_question_counts": dict(source_counts),
              "categories": dict(categories), "author_files": dict(authors),
              "input_sha256": {str(p): sha(Path(p).read_bytes()) for p in paths},
              "source_manifest": files, "errors": errors, "warnings": warnings}
    return report, all_questions


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--questions", action="append", required=True)
    parser.add_argument("--source-root", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--freeze")
    parser.add_argument("--review", help="AI source review JSON: question_id -> approved/reason, made before solving")
    parser.add_argument("--split-plan", help="Frozen pre-solver assignment JSON with seed and assignment {question_id: dev|holdout}")
    args = parser.parse_args()
    report, items = audit(args.questions, Path(args.source_root))
    if args.freeze:
        if not args.review:
            raise SystemExit("--freeze requires explicit --review; provenance checks alone are not semantic review")
        review = read_json(args.review)
        if review.get("review_type") != "AI source review, not human" or review.get("used_solver_outputs") is not False:
            raise SystemExit("review must declare AI source review and no solver-output access")
        approvals = {r["question_id"]: r for r in review["questions"]}
        if not report["passed"] or any(not approvals.get(q["id"], {}).get("approved") for q in items):
            raise SystemExit("cannot freeze questions with audit failures or missing approval")
        if review.get("input_sha256") != report["input_sha256"]:
            raise SystemExit("AI review was not bound to these exact author files")
        split_plan = read_json(args.split_plan) if args.split_plan else None
        if split_plan:
            assignments = split_plan["assignment"]
            if set(assignments) != {q["id"] for q in items} or set(assignments.values()) - {"dev", "holdout"}:
                raise SystemExit("split plan must assign every question exactly once to dev or holdout")
            items = [{**q, "split": assignments[q["id"]]} for q in items]
        frozen = {"format": "cairn-qa-gold/v1", "review_type": review["review_type"],
                  "limitations": "Source-grounded AI-authored and AI-reviewed benchmark; not human gold or proof of absolute correctness.",
                  "author_file_sha256": report["input_sha256"], "source_manifest": report["source_manifest"],
                  "review_sha256": sha(Path(args.review).read_bytes()), "split_plan": split_plan, "questions": items}
        target = Path(args.freeze)
        if target.exists():
            raise SystemExit("refusing to replace frozen benchmark")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(json.dumps(frozen, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        report["frozen_sha256"] = sha(target.read_bytes())
    output = Path(args.out)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({k: report[k] for k in ("passed", "question_count", "distinct_source_documents", "errors")}, ensure_ascii=False))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
