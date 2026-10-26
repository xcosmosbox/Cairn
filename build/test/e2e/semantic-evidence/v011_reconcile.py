#!/usr/bin/env python3
"""只读复核固定 #105 packet，仅输出脱敏计数。 Read-only saved-packet audit.

This is finite evidence preparation, not a builder, model replay or semantic oracle.
Do not send its output/expected inventory to construction prompts.
"""

import argparse
import base64
from collections import Counter, defaultdict
import hashlib
import json
from pathlib import Path
import sqlite3
import tarfile


ARCHIVE_SHA256 = "cdf5f96ae41bee23ff16eba3e690bf392606691e5f0acf22de3d05c501c86921"
STAGES = [
    "05_extract_result.json", "05b_extract_repaired.json",
    "05d_extract_described.json", "05e_cross_validated.json",
    "05e_extract_related.json", "05f_uuid_assigned.json",
    "05g_file_slugs.json", "05h_materialization_candidate.json",
]
MARKER = "## 标注单元（数组下标从0开始） / annotated items\n"


def load(path):
    return json.loads(path.read_text(encoding="utf-8"))


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def item_key(item):
    # IDs are added after assessment. Preserve every other field and multiplicity.
    return json.dumps({k: v for k, v in item.items() if k != "id"},
                      sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def removed_occurrences(initial, accepted):
    """返回原始数组中被移除的 occurrence；同内容重复行不能互相掩盖。"""
    remaining = Counter(item_key(it) for it in accepted)
    removed = []
    for index, item in enumerate(initial):
        key = item_key(item)
        if remaining[key]:
            remaining[key] -= 1
        else:
            removed.append(index)
    if any(remaining.values()):
        raise ValueError("accepted annotation includes changed or additional rows")
    return removed


def nodes(result):
    return [node for domain in result["Domains"] for sub in domain["subdomains"]
            for kind in ("entities", "concepts") for node in sub[kind]]


def reconcile_stage(result, member_sources, accepted_incidences):
    leaves = nodes(result)
    members = {member for node in leaves for member in node["members"]}
    unknown = members - member_sources.keys()
    rows = [(node["id"], member, source) for node in leaves
            for member in node["members"] for source in member_sources.get(member, [])]
    incidences = {(source, member) for _, member, source in rows}
    return {
        "leaf_nodes": len(leaves), "distinct_members": len(members),
        "raw_source_rows": len(rows), "distinct_selected_associations": len(set(rows)),
        "distinct_document_member_incidences": len(incidences),
        "missing_accepted_incidences": len(accepted_incidences - incidences),
        "unexpected_incidences": len(incidences - accepted_incidences),
        "unknown_members": len(unknown),
    }, set(rows), accepted_incidences - incidences


def assessments(trace, initial):
    """按完整数组匹配，不依赖并发 request 次序或某个名字。"""
    requests, responses = {}, {}
    for event in trace:
        if event["event"] == "request":
            wire = json.loads(base64.b64decode(event["request_body"]))
            user = wire["messages"][-1]["content"]
            if MARKER not in user:
                continue
            request_items = json.loads(user.split(MARKER, 1)[1])
            for source_id, doc in initial.items():
                if [item_key(x) for x in request_items] == [item_key(x) for x in doc["items"]]:
                    requests[event["attempt_id"]] = (source_id, len(request_items))
        elif event["event"] == "response":
            responses[event["attempt_id"]] = event
    out = defaultdict(list)
    for attempt, (source_id, count) in requests.items():
        event = responses[attempt]
        wire = json.loads(base64.b64decode(event["response_body"]))
        choice = wire["choices"][0]
        verdict = json.loads(choice["message"]["content"])
        indices = verdict.get("unsupported_items", [])
        out[source_id].append({
            "attempt_id": attempt, "item_count": count,
            "http_status": event["http_status"], "finish_reason": choice["finish_reason"],
            "supported": verdict.get("supported"), "unsupported_indices": indices,
            "invalid_indices": [i for i in indices if not isinstance(i, int) or i < 0 or i >= count],
            "request_sha256": event["request_sha256"],
            "response_sha256": event["response_sha256"],
        })
    return {key: sorted(value, key=lambda x: x["attempt_id"]) for key, value in out.items()}


def audit(packet):
    archive = packet / "private-evidence.tar.gz"
    if archive.stat().st_size != 2073985 or digest(archive) != ARCHIVE_SHA256:
        raise ValueError("fixed private packet identity mismatch")
    dump = packet / "build-dump"
    evidence_files = [*sorted((dump / "01_annotated").rglob("*.json")),
                      *sorted((dump / "03_assessed").rglob("*.json")),
                      *[dump / name for name in ["04_extract_input.json", *STAGES, "06_ingest_result.json"]],
                      packet / "build-trace/requests.jsonl", packet / "graph.db"]
    # 固定 archive 与本地读入字节逐个对账；仅 archive 哈希不足以绑定已解包副本。
    # Bind every consumed local copy to its fixed archived bytes without extraction.
    with tarfile.open(archive, "r:gz") as saved:
        for path in evidence_files:
            member = saved.extractfile(path.relative_to(packet).as_posix())
            if member is None or hashlib.sha256(member.read()).hexdigest() != digest(path):
                raise ValueError("local evidence differs from fixed archive")
    initial_docs = [load(f) for f in sorted((dump / "01_annotated").rglob("*.json"))]
    initial_docs.sort(key=lambda doc: doc["file_path"])
    initial = {f"S{i:02d}": doc for i, doc in enumerate(initial_docs, 1)}
    paths = {doc["file_path"]: sid for sid, doc in initial.items()}
    accepted = {paths[doc["file_path"]]: doc for doc in
                (load(f) for f in sorted((dump / "03_assessed").rglob("*.json")))}
    rows = []
    for sid, doc in initial.items():
        kept = accepted.get(sid)
        rows.append({"source_id": sid, "initial_rows": len(doc["items"]),
                     "accepted_rows": len(kept["items"]) if kept else None,
                     "removed_initial_indices": removed_occurrences(doc["items"], kept["items"]) if kept else [],
                     "disposition": "accepted" if kept else "rejected_document"})
    input_docs = load(dump / "04_extract_input.json")
    accepted_counter = Counter((doc["file_path"], item_key(it))
                               for doc in accepted.values() for it in doc["items"])
    input_counter = Counter((doc["file_path"], item_key(it)) for doc in input_docs for it in doc["items"])
    if accepted_counter != input_counter:
        raise ValueError("03 accepted and 04 input occurrence sets differ")
    member_sources = defaultdict(list)
    for doc in input_docs:
        for item in doc["items"]:
            member_sources[item["id"]].append(doc["file_path"])
    incidences = {(source, member) for member, sources in member_sources.items() for source in sources}
    stage_results = {}
    earliest_missing = None
    for stage in STAGES:
        counts, selected, missing = reconcile_stage(load(dump / stage), member_sources, incidences)
        if earliest_missing is None:
            earliest_missing = missing
        elif missing != earliest_missing:
            raise ValueError("missing incidence identity changes between saved stages")
        stage_results[stage] = counts
    # mode=ro plus immutable avoids creating journal/SHM files in historical inputs.
    with sqlite3.connect((packet / "graph.db").resolve().as_uri() + "?mode=ro&immutable=1", uri=True) as db:
        persisted = set(db.execute("SELECT node_uuid, member_id, file_path FROM node_sources"))
    trace = [json.loads(line) for line in (packet / "build-trace/requests.jsonl").read_text().splitlines()]
    missing_by_source = Counter(paths[source] for source, _ in missing)
    result = {
        "schema_version": "v011-loss-audit-1", "archive_sha256": ARCHIVE_SHA256,
        "archive_entries_matched": len(evidence_files),
        "source_rows": rows, "initial_rows_all_six": sum(row["initial_rows"] for row in rows),
        "initial_rows_accepted_documents": sum(row["initial_rows"] for row in rows if row["accepted_rows"] is not None),
        "accepted_rows": len(list(input_counter.elements())),
        "distinct_accepted_members": len(member_sources), "distinct_accepted_incidences": len(incidences),
        "assessments": assessments(trace, initial), "stages": stage_results,
        "missing_incidence_sources": dict(missing_by_source),
        "persisted_distinct_associations": len(persisted),
        "selected_minus_persisted": len(selected - persisted),
        "persisted_minus_selected": len(persisted - selected),
        "reported_NodeSourcesInserted": load(dump / "06_ingest_result.json")["NodeSourcesInserted"],
        "input_sha256": {name: digest(dump / name) for name in ["04_extract_input.json", *STAGES, "06_ingest_result.json"]},
        "trace_sha256": digest(packet / "build-trace/requests.jsonl"),
        "database_sha256": digest(packet / "graph.db"),
    }
    check_fixed_observations(result)
    return result


def check_fixed_observations(result):
    expected = {"initial_rows_all_six": 205, "initial_rows_accepted_documents": 176,
                "accepted_rows": 173, "distinct_accepted_members": 165,
                "distinct_accepted_incidences": 172, "persisted_distinct_associations": 171,
                "selected_minus_persisted": 0, "persisted_minus_selected": 0,
                "reported_NodeSourcesInserted": 172}
    for key, value in expected.items():
        if result[key] != value:
            raise ValueError("fixed observation mismatch: " + key)
    if [row["initial_rows"] for row in result["source_rows"]] != [32, 29, 38, 34, 33, 39]:
        raise ValueError("source order mismatch")
    if sum(len(row["removed_initial_indices"]) for row in result["source_rows"]) != 3:
        raise ValueError("removed occurrence mismatch")
    rejected = result["assessments"].get("S02", [])
    if len(rejected) != 1 or any(rejected[0][key] != value for key, value in {
        "attempt_id": 9, "item_count": 29, "http_status": 200, "finish_reason": "stop",
        "supported": False, "unsupported_indices": [29], "invalid_indices": [29],
    }.items()):
        raise ValueError("rejected-document boundary mismatch")
    for stage in result["stages"].values():
        if any(stage[key] != value for key, value in {
            "leaf_nodes": 157, "distinct_members": 164, "raw_source_rows": 172,
            "distinct_selected_associations": 171, "distinct_document_member_incidences": 171,
            "missing_accepted_incidences": 1, "unexpected_incidences": 0, "unknown_members": 0,
        }.items()):
            raise ValueError("saved extraction boundary mismatch")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("packet", type=Path, help="authorized retained diagnostic105 directory")
    args = parser.parse_args()
    try:
        result = audit(args.packet)
    except Exception as exc:
        # Paths, item names and provider messages may be private; never echo them.
        raise SystemExit("audit failed (" + type(exc).__name__ + "); inspect authorized inputs privately") from None
    print(json.dumps(result, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
