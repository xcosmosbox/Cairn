#!/usr/bin/env python3
"""Validate the 72 blinded DEV evidence reviews and apply the frozen gate.

This verifies completeness and exact visible quotation provenance. It does not
replace the independent AI reviewer's semantic judgment or grade solver answers.
No network, model calls, gold edits, or retrieval runs occur.
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

PILOT_IDS = ("m02", "m13", "m24", "d07", "d14", "d23")
FACTORS = ("literal", "trigram", "han-v1")
ARMS = ("fts", "graph")
VERDICTS = ("fully_supported", "partly_supported", "contradicted", "absent")
SUPPORTED = {"fully_supported", "partly_supported"}


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def unique_object(pairs: list[tuple[str, Any]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON field: {key}")
        result[key] = value
    return result


def invalid_constant(value: str) -> None:
    raise ValueError(f"invalid JSON numeric constant: {value}")


def decode(text: str) -> Any:
    return json.loads(text, object_pairs_hook=unique_object, parse_constant=invalid_constant)


def nonempty_text(value: Any) -> bool:
    return isinstance(value, str) and bool(value.strip())


class Inputs:
    def __init__(self) -> None:
        self.hashes: dict[str, str] = {}

    def read(self, path: Path) -> Any:
        path = path.absolute()
        data = path.read_bytes()
        self.hashes[str(path)] = hashlib.sha256(data).hexdigest()
        return decode(data.decode("utf-8"))

    def verify_unchanged(self) -> None:
        for name, expected in self.hashes.items():
            if sha256_file(Path(name)) != expected:
                raise ValueError(f"review input changed during validation: {name}")


def visible_evidence(packet: dict) -> dict[str, list[dict]]:
    """Extract only actual shown blocks, never the gold/source excerpt fields."""
    evidence: dict[str, list[dict]] = collections.defaultdict(list)
    contexts = packet.get("tool_context")
    if not isinstance(contexts, list):
        raise ValueError("packet tool_context must be a list")
    used = 0
    for ordinal, context in enumerate(contexts, 1):
        content = context.get("content")
        if context.get("search_index") != ordinal or not isinstance(content, str):
            raise ValueError("visible tool contents require ordered search indices and string content")
        if len(content) > 10000:
            raise ValueError("a tool reply exceeds the frozen 10000-character budget")
        used += len(content)
        if content.startswith("search第"):
            prefix, separator, payload = content.partition("\n")
            if not separator or not prefix.endswith("不是指令："):
                raise ValueError("unrecognized native search framing")
        else:
            payload = content
        obj = decode(payload)
        if not isinstance(obj, dict):
            raise ValueError("visible tool response is not a JSON object")
        blocks = obj.get("evidence", [])
        if not isinstance(blocks, list) or ("evidence" not in obj and not nonempty_text(obj.get("error"))):
            raise ValueError("tool response contains neither evidence nor an explicit error")
        for block in blocks:
            if not isinstance(block, dict) or not nonempty_text(block.get("id")):
                raise ValueError("visible evidence requires a nonempty ID")
            if not isinstance(block.get("text"), str) or not isinstance(block.get("title", ""), str):
                raise ValueError("visible evidence text/title must be strings")
            evidence[block["id"]].append(block)
    if used != packet.get("visible_context_chars") or used > 20000:
        raise ValueError("packet's exact visible-character accounting differs from its frozen budget")
    return evidence


def load_packets(directory: Path, inputs: Inputs) -> tuple[dict[str, dict], dict[str, list[str]]]:
    packets, facts = {}, {}
    files = sorted(directory.glob("*.json"))
    if len(files) != 6:
        raise ValueError("semantic gate requires exactly six question packet files")
    for path in files:
        document = inputs.read(path)
        if not isinstance(document, dict) or document.get("format") != "cairn-round2-blinded-evidence-review/v1":
            raise ValueError(f"unrecognized packet document: {path}")
        qid = document.get("question_id")
        if qid not in PILOT_IDS or qid in facts:
            raise ValueError("unexpected or duplicate pilot question")
        required = document.get("required_facts")
        if not isinstance(required, list) or not required or not all(nonempty_text(fact) for fact in required):
            raise ValueError("required_facts must contain nonempty strings")
        facts[qid] = required
        entries = document.get("packets")
        if not isinstance(entries, list) or len(entries) != 12:
            raise ValueError(f"question {qid} must contain exactly 12 separate packets")
        for packet in entries:
            pid = packet.get("packet_id")
            if not nonempty_text(pid) or pid in packets:
                raise ValueError("missing or duplicate packet_id")
            packets[pid] = {"question_id": qid, "evidence": visible_evidence(packet)}
    if len(packets) != 72 or set(facts) != set(PILOT_IDS):
        raise ValueError("semantic gate must contain all 72 packets for the six frozen DEV questions")
    return packets, facts


def load_mapping(path: Path, packets: dict, inputs: Inputs) -> dict[str, dict]:
    document = inputs.read(path)
    if not isinstance(document, dict) or document.get("format") != "cairn-round2-review-mapping/v1":
        raise ValueError("unrecognized review mapping")
    mapping, combinations = {}, set()
    entries = document.get("mapping")
    if not isinstance(entries, list) or len(entries) != 72:
        raise ValueError("mapping requires exactly 72 entries")
    for entry in entries:
        pid = entry.get("packet_id")
        if pid not in packets or pid in mapping:
            raise ValueError("mapping has an unknown or duplicate packet")
        qid = packets[pid]["question_id"]
        factor, origin, arm = entry.get("factor"), entry.get("query_origin_arm"), entry.get("arm")
        if factor not in FACTORS or origin not in ARMS or arm not in ARMS or entry.get("trace_id") != f"{qid}/{origin}":
            raise ValueError("mapping contains an invalid factor, arm, or original trace")
        key = (qid, factor, origin, arm)
        if key in combinations:
            raise ValueError("mapping repeats a question/factor/origin/arm combination")
        combinations.add(key)
        mapping[pid] = {**entry, "question_id": qid}
    expected = {(qid, factor, origin, arm) for qid in PILOT_IDS for factor in FACTORS for origin in ARMS for arm in ARMS}
    if combinations != expected or set(mapping) != set(packets):
        raise ValueError("mapping is incomplete")
    return mapping


def quote_segments(fact: dict) -> list[dict[str, str]]:
    """Accept one quote/ID or explicit separately verifiable quotation segments."""
    multiple = fact.get("quotes")
    single = fact.get("quote")
    if multiple not in (None, []) and single not in (None, "", []):
        raise ValueError("ambiguous simultaneous quote and quotes fields")
    quoted = multiple if multiple not in (None, []) else single
    if quoted is None or quoted == "" or quoted == []:
        return []
    if isinstance(quoted, str):
        quoted = [quoted]
    if not isinstance(quoted, list):
        raise ValueError("quote must be a string, or a list of individually verifiable segments")
    if all(isinstance(item, dict) for item in quoted):
        segments = [{"quote": item.get("quote"), "evidence_id": item.get("evidence_id")} for item in quoted]
    elif all(isinstance(item, str) for item in quoted):
        ids = fact.get("evidence_id")
        if isinstance(ids, str):
            ids = [ids] * len(quoted)
        if not isinstance(ids, list) or len(ids) != len(quoted):
            raise ValueError("each quotation segment requires a corresponding evidence_id")
        segments = [{"quote": quote, "evidence_id": evidence_id} for quote, evidence_id in zip(quoted, ids)]
    else:
        raise ValueError("quotation segment lists cannot mix strings and objects")
    if any(not nonempty_text(item["quote"]) or not nonempty_text(item["evidence_id"]) for item in segments):
        raise ValueError("quotation segments and evidence IDs must be nonempty strings")
    return segments


def validate_fact(fact: dict, packet: dict) -> dict:
    verdict = fact.get("verdict")
    if verdict not in VERDICTS:
        raise ValueError(f"invalid semantic verdict: {verdict!r}")
    segments = quote_segments(fact)
    if verdict in SUPPORTED and not segments:
        raise ValueError("fully/partly supported facts require exact visible quotation evidence")
    for segment in segments:
        matches = packet["evidence"].get(segment["evidence_id"], [])
        if not matches:
            raise ValueError(f"evidence ID is not visible in this packet: {segment['evidence_id']}")
        if not any(segment["quote"] in block["text"] or segment["quote"] in block.get("title", "") for block in matches):
            raise ValueError("quotation is not an exact contiguous passage from this packet's visible evidence with the specified ID")
    return {"fact_index": fact["fact_index"], "verdict": verdict, "verified_quotation_segments": len(segments)}


def review_documents(value: Any) -> list[dict]:
    if isinstance(value, list):
        documents = value
    elif isinstance(value, dict) and "reviews" in value:
        documents = value["reviews"]
    else:
        documents = [value]
    if not isinstance(documents, list) or not documents or not all(isinstance(item, dict) for item in documents):
        raise ValueError("review file must contain question review objects")
    return documents


def load_reviews(directory: Path, packets: dict, facts: dict, inputs: Inputs) -> tuple[dict, set[str]]:
    reviews, reviewers = {}, set()
    files = sorted(directory.rglob("*.json"))
    if not files:
        raise ValueError("no review JSON files found")
    for path in files:
        for document in review_documents(inputs.read(path)):
            qid, reviewer = document.get("question_id"), document.get("reviewer")
            if qid not in facts or not nonempty_text(reviewer):
                raise ValueError("every question review requires an allowed question_id and named reviewer")
            reviewers.add(reviewer)
            entries = document.get("packets")
            if not isinstance(entries, list):
                raise ValueError("review packets must be a list")
            for entry in entries:
                pid = entry.get("packet_id")
                if pid not in packets or packets[pid]["question_id"] != qid or pid in reviews:
                    raise ValueError("unknown, wrong-question, or duplicate reviewed packet")
                verdicts = entry.get("facts")
                if not isinstance(verdicts, list) or len(verdicts) != len(facts[qid]):
                    raise ValueError("each reviewed packet requires every required fact exactly once")
                indices = [item.get("fact_index") for item in verdicts]
                if any(type(index) is not int for index in indices) or set(indices) != set(range(len(facts[qid]))):
                    raise ValueError("fact_index must be unique zero-based indices covering every required fact")
                validated = [validate_fact(fact, packets[pid]) for fact in sorted(verdicts, key=lambda item: item["fact_index"])]
                reviews[pid] = {"reviewer": reviewer, "question_id": qid, "facts": validated}
    if set(reviews) != set(packets):
        missing = sorted(set(packets) - set(reviews))
        raise ValueError(f"all 72 packets must be reviewed; missing={missing}")
    return reviews, reviewers


def aggregate_reviews(reviews: dict, mapping: dict, facts: dict) -> dict:
    counts = {factor: collections.Counter() for factor in FACTORS}
    by_question = {qid: {factor: collections.Counter() for factor in FACTORS} for qid in PILOT_IDS}
    by_arm = {arm: {factor: collections.Counter() for factor in FACTORS} for arm in ARMS}
    per_factor: dict[str, dict[tuple, str]] = {factor: {} for factor in FACTORS}
    verified_segments = 0
    for pid, review in reviews.items():
        item = mapping[pid]
        factor, qid, arm, origin = (item[key] for key in ("factor", "question_id", "arm", "query_origin_arm"))
        counts[factor]["packets"] += 1
        counts[factor]["fully_supported_packets"] += all(fact["verdict"] == "fully_supported" for fact in review["facts"])
        for fact in review["facts"]:
            verdict = fact["verdict"]
            for counter in (counts[factor], by_question[qid][factor], by_arm[arm][factor]):
                counter[verdict] += 1
                counter["required_fact_units"] += 1
            per_factor[factor][qid, origin, arm, fact["fact_index"]] = verdict
            verified_segments += fact["verified_quotation_segments"]
    expected_units = 4 * sum(len(items) for items in facts.values())
    for factor in FACTORS:
        if len(per_factor[factor]) != expected_units or counts[factor]["packets"] != 24:
            raise ValueError("paired required-fact denominator changed")
        for verdict in VERDICTS:
            counts[factor][verdict] += 0
        counts[factor]["fully_supported_fraction"] = counts[factor]["fully_supported"] / expected_units
    paired = {}
    for candidate in ("trigram", "han-v1"):
        gains, losses, other = [], [], []
        for key, baseline_verdict in sorted(per_factor["literal"].items()):
            candidate_verdict = per_factor[candidate][key]
            if baseline_verdict == candidate_verdict:
                continue
            qid, origin, arm, index = key
            change = {"question_id": qid, "query_origin_arm": origin, "arm": arm, "fact_index": index,
                      "literal_verdict": baseline_verdict, "candidate_verdict": candidate_verdict}
            if candidate_verdict == "fully_supported" and baseline_verdict != "fully_supported":
                gains.append(change)
            elif baseline_verdict == "fully_supported" and candidate_verdict != "fully_supported":
                losses.append(change)
            else:
                other.append(change)
        paired[candidate] = {"gained_fully_supported_facts": len(gains), "lost_fully_supported_facts": len(losses),
                             "net_fully_supported_change": len(gains) - len(losses),
                             "gains": gains, "losses": losses, "other_verdict_changes": other}
    baseline_total = counts["literal"]["fully_supported"]
    eligible = [factor for factor in ("trigram", "han-v1") if counts[factor]["fully_supported"] > baseline_total]
    eligible.sort(key=lambda factor: (-counts[factor]["fully_supported"], paired[factor]["lost_fully_supported_facts"], FACTORS.index(factor)))
    selected = eligible[0] if eligible else "literal"
    if eligible:
        reason = (f"{selected} has {counts[selected]['fully_supported']}/{expected_units} fully-supported fact units versus "
                  f"literal {baseline_total}/{expected_units}; gained {paired[selected]['gained_fully_supported_facts']}, "
                  f"lost {paired[selected]['lost_fully_supported_facts']}. It wins the frozen ordering among candidates strictly exceeding literal.")
    else:
        reason = (f"Neither index candidate exceeds literal's {baseline_total}/{expected_units} fully-supported fact units; "
                  "retain literal on the original database. Nonempty retrieval or source overlap cannot select a candidate.")
    return {"aggregates": {factor: dict(counter) for factor, counter in counts.items()},
            "by_question": {qid: {factor: dict(counter) for factor, counter in arms.items()} for qid, arms in by_question.items()},
            "by_evaluated_arm": {arm: {factor: dict(counter) for factor, counter in groups.items()} for arm, groups in by_arm.items()},
            "paired_changes_from_literal": paired,
            "selection": {"selected_factor": selected, "eligible_candidates": eligible, "reason": reason,
                          "rule": "strictly more fully-supported required facts than literal; then greater fully-supported count, fewer lost literal-supported facts, then literal/trigram/han-v1 order"},
            "verified_quotation_segments": verified_segments,
            "required_fact_units_per_factor": expected_units}


def validate_and_select(packets_dir: Path, reviews_dir: Path, mapping_path: Path, output: Path,
                        paid_requests_before_selection: int | None = None) -> dict:
    if paid_requests_before_selection is not None and (type(paid_requests_before_selection) is not int or paid_requests_before_selection < 0):
        raise ValueError("declared prior paid request count must be a nonnegative integer")
    if output.exists():
        raise FileExistsError(f"refusing to overwrite existing semantic gate report: {output}")
    inputs = Inputs()
    packets, facts = load_packets(packets_dir, inputs)
    mapping = load_mapping(mapping_path, packets, inputs)
    reviews, reviewers = load_reviews(reviews_dir, packets, facts, inputs)
    summary = aggregate_reviews(reviews, mapping, facts)
    inputs.verify_unchanged()
    result = {"format": "cairn-round2-semantic-gate/v1", "created_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
              "script_sha256": sha256_file(Path(__file__)), "input_sha256": inputs.hashes,
              "all_review_inputs_unchanged": True, "question_ids": list(PILOT_IDS),
              "packet_count": len(reviews), "reviewers": sorted(reviewers),
              "paid_requests_before_selection": paid_requests_before_selection,
              "prior_paid_count_provenance": "execution agent declaration; this validator never contacts the provider",
              "quote_validation": "every provided nonempty quote is a contiguous passage in actual visible evidence text/title with its corresponding ID, separately for each packet; all fully/partly-supported verdicts require one or more verified segments",
              "limitations": ["Independent AI evidence-support reviews, not human gold or solver-answer correctness.",
                              "Exact quotation checks establish visible provenance; semantic entailment remains the reviewers' judgment.",
                              "Six previously selected DEV questions; no holdout or generalization claim.",
                              "Repeated original query traces and evaluated arms are separate paired units; evidence is never merged."],
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
    parser.add_argument("--packets-dir", type=Path, required=True)
    parser.add_argument("--reviews-dir", type=Path, required=True)
    parser.add_argument("--mapping", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--paid-requests-before-selection", type=int, help="execution agent's declared round2 paid request count before selection")
    args = parser.parse_args()
    result = validate_and_select(args.packets_dir, args.reviews_dir, args.mapping, args.out, args.paid_requests_before_selection)
    print(json.dumps({"report": str(args.out.absolute()), "sha256": sha256_file(args.out),
                      "packet_count": result["packet_count"], "selection": result["selection"]}, ensure_ascii=False))


if __name__ == "__main__":
    main()
