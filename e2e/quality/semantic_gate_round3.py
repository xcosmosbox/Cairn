#!/usr/bin/env python3
"""校验 48 份 DEV 证据审查并执行冻结门槛 / validate the round3 support gate.

Only a caller-pinned offline report, its blinded packets and private mapping, and
six completed review files are read as evaluation inputs. This does not retrieve
sources, inspect hidden answers, grade solver answers, or call a model/provider.
"""
from __future__ import annotations

import argparse
import collections
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
from typing import Any

from semantic_gate_round2 import (Inputs, VERDICTS, decode, nonempty_text, quote_segments,
                                  sha256_file, visible_evidence)
from source_backfill_round3 import validate_text_fact


PILOT_IDS = ("m02", "m13", "m24", "d07", "d14", "d23")
FACTORS = ("node-only", "source-backfill")
ARMS = ("fts", "graph")
PACKETS_PER_QUESTION = 8
PACKET_COUNT = 48


def object_value(value: Any, context: str) -> dict:
    if not isinstance(value, dict):
        raise ValueError(f"round3 semantic gate: {context} must be an object")
    return value


def require_sha256(value: Any, context: str) -> str:
    if not isinstance(value, str) or len(value) != 64 or any(char not in "0123456789abcdef" for char in value):
        raise ValueError(f"round3 semantic gate: {context} requires a lowercase SHA-256 digest")
    return value


def load_offline_report(path: Path, expected_sha256: str, inputs: Inputs) -> dict:
    # 先验证外部冻结的报告摘要，不能从可能被替换的报告里自取信任根。
    # The caller pins report bytes independently, before any artifact is trusted.
    expected_sha256 = require_sha256(expected_sha256, "trusted offline report")
    path = path.absolute()
    data = path.read_bytes()
    actual = hashlib.sha256(data).hexdigest()
    if actual != expected_sha256:
        raise ValueError(f"round3 semantic gate: offline report SHA-256 mismatch: {path}")
    inputs.hashes[str(path)] = actual
    document = object_value(decode(data.decode("utf-8")), "offline report")
    if document.get("format") != "cairn-round3-offline-results/v1":
        raise ValueError("round3 semantic gate: unrecognized offline report")
    require_sha256(document.get("implementation_freeze_sha256"), "implementation freeze")
    return document


def verify_offline_artifacts(report: dict, paths: tuple[Path, ...], inputs: Inputs) -> None:
    entries = report.get("artifacts")
    if not isinstance(entries, list):
        raise ValueError("round3 semantic gate: offline report requires artifact bindings")
    bindings = {}
    for value in entries:
        entry = object_value(value, "offline artifact binding")
        name = entry.get("path")
        if not nonempty_text(name) or not Path(name).is_absolute() or name in bindings:
            raise ValueError("round3 semantic gate: offline artifact paths must be unique and absolute")
        bindings[name] = require_sha256(entry.get("sha256"), f"offline artifact {name}")
    # 只比对已读取的七份审查输入；报告中的原始 trace/cell 路径不被打开。
    # Bind the exact bytes already loaded, without opening trace/cell artifacts.
    for path in paths:
        name = str(path.absolute())
        if name not in bindings:
            raise ValueError(f"round3 semantic gate: input is not bound by the trusted offline report: {name}")
        if inputs.hashes.get(name) != bindings[name]:
            raise ValueError(f"round3 semantic gate: offline artifact SHA-256 mismatch: {name}")


class ReviewInputs(Inputs):
    """同时固定文件集合，防止审查途中新增文件被忽略 / freeze input membership too."""

    def __init__(self) -> None:
        super().__init__()
        self.directories: dict[Path, tuple[Path, ...]] = {}

    def question_files(self, directory: Path, kind: str) -> tuple[Path, ...]:
        directory = directory.absolute()
        if not directory.is_dir():
            raise ValueError(f"round3 semantic gate: {kind} directory does not exist: {directory}")
        files = tuple(sorted(directory.rglob("*.json")))
        if len(files) != len(PILOT_IDS) or not all(path.is_file() for path in files):
            raise ValueError(f"round3 semantic gate: exactly six {kind} JSON files are required, one per DEV question")
        self.directories[directory] = files
        return files

    def verify_unchanged(self) -> None:
        super().verify_unchanged()
        for directory, expected in self.directories.items():
            if tuple(sorted(directory.rglob("*.json"))) != expected:
                raise ValueError(f"round3 semantic gate: review input file set changed during validation: {directory}")


def load_packets(directory: Path, inputs: ReviewInputs) -> tuple[dict[str, dict], dict[str, list[str]]]:
    packets, facts = {}, {}
    for path in inputs.question_files(directory, "packet"):
        document = object_value(inputs.read(path), f"packet document {path}")
        if document.get("format") != "cairn-round3-blinded-evidence-review/v1":
            raise ValueError(f"round3 semantic gate: unrecognized packet document: {path}")
        qid = document.get("question_id")
        if qid not in PILOT_IDS or qid in facts:
            raise ValueError("round3 semantic gate: unexpected or duplicate DEV question in packet files")
        required = document.get("required_facts")
        if not isinstance(required, list) or not required or not all(nonempty_text(fact) for fact in required):
            raise ValueError("round3 semantic gate: required_facts must contain nonempty strings")
        facts[qid] = required
        entries = document.get("packets")
        if not isinstance(entries, list) or len(entries) != PACKETS_PER_QUESTION:
            raise ValueError(f"round3 semantic gate: question {qid} requires exactly eight packets")
        for value in entries:
            packet = object_value(value, f"packet for {qid}")
            pid = packet.get("packet_id")
            if not nonempty_text(pid) or pid in packets:
                raise ValueError("round3 semantic gate: missing or duplicate packet_id")
            if "question_id" in packet and packet["question_id"] != qid:
                raise ValueError("round3 semantic gate: packet question_id does not match its document")
            contexts = packet.get("tool_context")
            # 检索次数上限不限制预算拒绝回复 / suppressed calls still produce visible replies.
            if not isinstance(contexts, list):
                raise ValueError("round3 semantic gate: packet tool_context must be a list")
            if type(packet.get("visible_context_chars")) is not int or packet["visible_context_chars"] < 0:
                raise ValueError("round3 semantic gate: visible_context_chars must be a nonnegative integer")
            for context in contexts:
                object_value(context, f"tool context for {pid}")
                if type(context.get("search_index")) is not int:
                    raise ValueError("round3 semantic gate: search_index must be an integer")
            packets[pid] = {"question_id": qid, "evidence": visible_evidence(packet)}
    if len(packets) != PACKET_COUNT or set(facts) != set(PILOT_IDS):
        raise ValueError("round3 semantic gate: all 48 packets for the six frozen DEV questions are required")
    return packets, facts


def load_mapping(path: Path, packets: dict, inputs: Inputs) -> dict[str, dict]:
    document = object_value(inputs.read(path), "review mapping")
    if document.get("format") != "cairn-round3-review-mapping/v1":
        raise ValueError("round3 semantic gate: unrecognized review mapping")
    entries = document.get("mapping")
    if not isinstance(entries, list) or len(entries) != PACKET_COUNT:
        raise ValueError("round3 semantic gate: mapping requires exactly 48 entries")
    mapping, combinations = {}, set()
    for value in entries:
        entry = object_value(value, "mapping entry")
        pid = entry.get("packet_id")
        if not nonempty_text(pid) or pid not in packets or pid in mapping:
            raise ValueError("round3 semantic gate: mapping has an unknown or duplicate packet")
        qid = packets[pid]["question_id"]
        if entry.get("question_id") != qid:
            raise ValueError("round3 semantic gate: mapping question_id does not match its packet")
        factor, origin, arm = entry.get("factor"), entry.get("query_origin_arm"), entry.get("arm")
        if factor not in FACTORS or origin not in ARMS or arm not in ARMS or entry.get("trace_id") != f"{qid}/{origin}":
            raise ValueError("round3 semantic gate: mapping contains an invalid factor, arm, or original trace")
        key = (qid, factor, origin, arm)
        if key in combinations:
            raise ValueError("round3 semantic gate: mapping repeats a question/factor/origin/arm combination")
        combinations.add(key)
        mapping[pid] = {"packet_id": pid, "question_id": qid, "factor": factor,
                        "query_origin_arm": origin, "arm": arm, "trace_id": entry["trace_id"]}
    expected = {(qid, factor, origin, arm) for qid in PILOT_IDS for factor in FACTORS for origin in ARMS for arm in ARMS}
    if combinations != expected or set(mapping) != set(packets):
        raise ValueError("round3 semantic gate: mapping is incomplete")
    return mapping


def load_reviews(directory: Path, packets: dict, facts: dict, inputs: ReviewInputs) -> tuple[dict, set[str]]:
    reviews, reviewers, questions = {}, set(), set()
    for path in inputs.question_files(directory, "review"):
        document = object_value(inputs.read(path), f"question review {path}")
        qid, reviewer = document.get("question_id"), document.get("reviewer")
        if not nonempty_text(qid) or qid not in facts or qid in questions:
            raise ValueError("round3 semantic gate: review files require each frozen DEV question exactly once")
        if not nonempty_text(reviewer):
            raise ValueError("round3 semantic gate: every question review requires a named reviewer")
        questions.add(qid)
        reviewers.add(reviewer)
        entries = document.get("packets")
        if not isinstance(entries, list) or len(entries) != PACKETS_PER_QUESTION:
            raise ValueError(f"round3 semantic gate: question {qid} requires exactly eight reviewed packets")
        for value in entries:
            entry = object_value(value, f"reviewed packet for {qid}")
            pid = entry.get("packet_id")
            if not nonempty_text(pid) or pid not in packets or packets[pid]["question_id"] != qid or pid in reviews:
                raise ValueError("round3 semantic gate: unknown, wrong-question, or duplicate reviewed packet")
            if "question_id" in entry and entry["question_id"] != qid:
                raise ValueError("round3 semantic gate: reviewed packet question_id does not match its document")
            verdicts = entry.get("facts")
            if not isinstance(verdicts, list) or len(verdicts) != len(facts[qid]):
                raise ValueError("round3 semantic gate: every required fact must be reviewed exactly once per packet")
            for fact in verdicts:
                object_value(fact, f"reviewed fact for {pid}")
            indices = [fact.get("fact_index") for fact in verdicts]
            if any(type(index) is not int for index in indices) or set(indices) != set(range(len(facts[qid]))):
                raise ValueError("round3 semantic gate: fact_index must be unique zero-based indices covering every required fact")
            validated = []
            for fact in sorted(verdicts, key=lambda item: item["fact_index"]):
                if not nonempty_text(fact.get("reason")):
                    raise ValueError(f"round3 semantic gate: every fact needs a nonempty review reason: {pid}/{fact['fact_index']}")
                # 只验证本 packet 的正文，不能沿用 round2 的标题支持 / body text only.
                checked = validate_text_fact(fact, packets[pid])
                validated.append({**checked, "reason": fact["reason"], "quotes": quote_segments(fact)})
            reviews[pid] = {"reviewer": reviewer, "question_id": qid, "facts": validated}
    if questions != set(PILOT_IDS) or set(reviews) != set(packets):
        raise ValueError(f"round3 semantic gate: all 48 packets must be reviewed; missing={sorted(set(packets) - set(reviews))}")
    return reviews, reviewers


def verdict_counter() -> collections.Counter:
    return collections.Counter({**dict.fromkeys(VERDICTS, 0), "packets": 0,
                                "fully_supported_packets": 0, "required_fact_units": 0})


def change_counts(changes: list[dict], losses: list[dict]) -> dict:
    return {"gained_fully_supported_facts": len(changes), "lost_fully_supported_facts": len(losses),
            "net_fully_supported_change": len(changes) - len(losses)}


def aggregate_reviews(reviews: dict, mapping: dict, facts: dict) -> dict:
    """配对保留两种 query 来源及失败分母 / pair all units without exclusions."""
    counts = {factor: verdict_counter() for factor in FACTORS}
    by_question = {qid: {factor: verdict_counter() for factor in FACTORS} for qid in PILOT_IDS}
    by_arm = {arm: {factor: verdict_counter() for factor in FACTORS} for arm in ARMS}
    per_factor: dict[str, dict[tuple, dict]] = {factor: {} for factor in FACTORS}
    verified_segments = 0
    for pid in sorted(reviews):
        review, item = reviews[pid], mapping[pid]
        factor, qid, arm, origin = (item[key] for key in ("factor", "question_id", "arm", "query_origin_arm"))
        counters = (counts[factor], by_question[qid][factor], by_arm[arm][factor])
        for counter in counters:
            counter["packets"] += 1
            counter["fully_supported_packets"] += all(fact["verdict"] == "fully_supported" for fact in review["facts"])
        for fact in review["facts"]:
            for counter in counters:
                counter[fact["verdict"]] += 1
                counter["required_fact_units"] += 1
            key = (qid, origin, arm, fact["fact_index"])
            if key in per_factor[factor]:
                raise ValueError("round3 semantic gate: repeated paired required-fact unit")
            per_factor[factor][key] = {"packet_id": pid, "reviewer": review["reviewer"], **fact}
            verified_segments += fact["verified_quotation_segments"]
    expected_keys = {(qid, origin, arm, index) for qid in PILOT_IDS for origin in ARMS for arm in ARMS
                     for index in range(len(facts[qid]))}
    expected_units = len(expected_keys)
    for factor in FACTORS:
        if set(per_factor[factor]) != expected_keys or counts[factor]["packets"] != 24:
            raise ValueError("round3 semantic gate: paired required-fact denominator changed")
        counts[factor]["fully_supported_fraction"] = counts[factor]["fully_supported"] / expected_units
    gains, losses, other = [], [], []
    for key in sorted(expected_keys):
        baseline, candidate = (per_factor[factor][key] for factor in FACTORS)
        if baseline["verdict"] == candidate["verdict"]:
            continue
        qid, origin, arm, index = key
        change = {"question_id": qid, "query_origin_arm": origin, "arm": arm, "fact_index": index,
                  "required_fact": facts[qid][index], "node_only": baseline, "source_backfill": candidate}
        if candidate["verdict"] == "fully_supported":
            gains.append(change)
        elif baseline["verdict"] == "fully_supported":
            losses.append(change)
        else:
            other.append(change)
    paired = {**change_counts(gains, losses), "gains": gains, "losses": losses,
              "other_verdict_changes": other,
              "by_evaluated_arm": {arm: change_counts([item for item in gains if item["arm"] == arm],
                                                       [item for item in losses if item["arm"] == arm]) for arm in ARMS}}
    net = paired["net_fully_supported_change"]
    if net != counts["source-backfill"]["fully_supported"] - counts["node-only"]["fully_supported"]:
        raise ValueError("round3 semantic gate: paired change differs from total supported-fact change")
    passed = net > 0
    return {"aggregates": {factor: dict(counter) for factor, counter in counts.items()},
            "by_question": {qid: {factor: dict(counter) for factor, counter in groups.items()} for qid, groups in by_question.items()},
            "by_evaluated_arm": {arm: {factor: dict(counter) for factor, counter in groups.items()} for arm, groups in by_arm.items()},
            "paired_changes_from_node_only": paired,
            "selection": {"gate_passed": passed, "selected_factor": "source-backfill" if passed else "node-only",
                          "rule": "strictly positive net change in fully-supported paired required-fact units",
                          "reason": f"Source backfill adds {len(gains)} fully-supported fact units and loses {len(losses)}; net change is {net:+d}.",
                          "next_step": "deferred paid pilot preflight; remaining frozen preconditions still apply" if passed else "stop without tuning or paying"},
            "verified_quotation_segments": verified_segments,
            "required_fact_units_per_factor": expected_units,
            "excluded_packets": 0, "excluded_required_fact_units": 0}


def validate_and_select(packets_dir: Path, reviews_dir: Path, mapping_path: Path, output: Path,
                        offline_report_path: Path, offline_report_sha256: str) -> dict:
    if output.exists() or output.is_symlink():
        raise FileExistsError(f"round3 semantic gate: refusing to overwrite existing report: {output}")
    inputs = ReviewInputs()
    offline_report = load_offline_report(offline_report_path, offline_report_sha256, inputs)
    packets, facts = load_packets(packets_dir, inputs)
    mapping = load_mapping(mapping_path, packets, inputs)
    verify_offline_artifacts(offline_report, inputs.directories[packets_dir.absolute()] + (mapping_path,), inputs)
    reviews, reviewers = load_reviews(reviews_dir, packets, facts, inputs)
    summary = aggregate_reviews(reviews, mapping, facts)
    inputs.verify_unchanged()
    result = {"format": "cairn-round3-semantic-gate/v1", "created_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
              "script_sha256": sha256_file(Path(__file__)), "input_sha256": dict(sorted(inputs.hashes.items())),
              "offline_report_path": str(offline_report_path.absolute()),
              "trusted_offline_report_sha256": offline_report_sha256,
              "implementation_freeze_sha256": offline_report["implementation_freeze_sha256"],
              "offline_artifact_bindings_verified": True,
              "all_review_inputs_unchanged": True, "question_ids": list(PILOT_IDS),
              "packet_count": len(reviews), "review_file_count": len(PILOT_IDS), "reviewers": sorted(reviewers),
              "quote_validation": "Every provided quotation segment occurs contiguously in actual visible text with its corresponding evidence ID in the same packet; all fully/partly-supported verdicts require quotes. Titles, paths, provenance, required facts, and hidden sources are not evidence.",
              "limitations": ["Independent AI evidence-support reviews, not human gold. Offline support is not answer accuracy.",
                              "Exact quotation checks establish visible provenance; semantic entailment remains the reviewers' judgment.",
                              "Six previously selected DEV questions; no holdout or generalization claim.",
                              "Original query arms and evaluated retrieval arms remain separate paired units, including errors and empty replies.",
                              "This offline gate performs no paid requests and does not establish online quality improvement."],
              **summary, "actual_model_requests": 0, "actual_tokens": 0}
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("x", encoding="utf-8") as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--packets", type=Path, required=True)
    parser.add_argument("--mapping", type=Path, required=True)
    parser.add_argument("--reviews", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--offline-report", type=Path, required=True)
    parser.add_argument("--offline-report-sha256", required=True, help="independently frozen SHA-256 of the offline report")
    args = parser.parse_args()
    result = validate_and_select(args.packets, args.reviews, args.mapping, args.output,
                                 args.offline_report, args.offline_report_sha256)
    print(json.dumps({"report": str(args.output.absolute()), "sha256": sha256_file(args.output),
                      "packet_count": result["packet_count"], "selection": result["selection"]}, ensure_ascii=False))


if __name__ == "__main__":
    main()
