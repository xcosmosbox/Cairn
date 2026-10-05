#!/usr/bin/env python3
"""原文回填付费 pilot 的只读预检；a pinned plan, with paid execution disabled.

Every data path and trust anchor is supplied explicitly. The existing frozen
native solver and grader cannot yet meet the experiment's run-wide authorization
and judge-ledger failure rules. `run` therefore verifies the plan and refuses
execution before reading any credential or instantiating a provider client.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
from pathlib import Path
import re

from source_backfill_round3 import (binding, check_bindings, checked_hash, checked_path,
                                    json_text, read_json, verify_freeze, write_new_json)


HERE = Path(__file__).resolve().parent
RUNNER_FILES = ("run_source_backfill_pilot.py", "test_source_backfill_pilot.py")
ARMS = ("fts", "graph")
SOLVER = {"model": "deepseek-flash", "thinking": "disabled", "max_tokens": 4096,
          "max_searches": 3, "max_model_turns": 4, "top_k": 10, "per_search_chars": 10000,
          "total_context_chars": 20000, "max_retries": 1, "timeout_seconds": 120,
          "workers": 6, "max_http_attempts_total": 48}
JUDGE = {"model": "deepseek-flash", "thinking": "enabled", "reasoning_effort": "low",
         "max_tokens": 32768, "max_attempts_per_case": 2, "max_http_attempts_total": 24,
         "max_accounted_tokens_total": 1500000, "workers": 4,
         "rubric": "unchanged frozen grade.py and gold"}
BLOCKERS = [
    {"code": "native_authorization_stop_missing", "component": "agent_eval_native.NativeDeepSeekClient.complete",
     "detail": "HTTP 401/403 are recorded as ordinary provider failures; the frozen client can retry and admit other jobs instead of latching a run-wide authorization stop."},
    {"code": "judge_authorization_stop_missing", "component": "grade.judge",
     "detail": "The frozen judge records HTTP authorization failures but its scheduler can still admit other cases; it has no run-wide authorization stop."},
    {"code": "judge_durable_admission_and_drain_missing", "component": "grade.judge / grade.append",
     "detail": "The frozen judge uses append/fsync without native ledger identity guards or a shared fail-stop admission latch; a write failure can unwind collection before every in-flight response has a retained receipt."},
]


def pin(path: Path, expected: str) -> dict:
    if not isinstance(expected, str) or not re.fullmatch(r"[0-9a-f]{64}", expected):
        raise ValueError("pilot: an independently supplied lowercase SHA-256 is required")
    return {"path": str(checked_path(path)), "sha256": checked_hash(path, expected)}


def verify_gate(gate: dict, report: dict, pins: dict, question_ids: list[str]) -> dict:
    """验证独立 gate 的绑定和分母，不重新裁决语义 / verify trusted gate arithmetic."""
    freeze_sha = pins["implementation_freeze"]["sha256"]
    report_pin = pins["offline_report"]
    if (gate.get("format") != "cairn-round3-semantic-gate/v1"
            or report.get("format") != "cairn-round3-offline-results/v1"
            or gate.get("implementation_freeze_sha256") != freeze_sha
            or report.get("implementation_freeze_sha256") != freeze_sha
            or gate.get("trusted_offline_report_sha256") != report_pin["sha256"]
            or gate.get("offline_report_path") != report_pin["path"]):
        raise ValueError("pilot: gate/offline/implementation trust anchors disagree")
    if (gate.get("all_review_inputs_unchanged") is not True
            or gate.get("offline_artifact_bindings_verified") is not True
            or report.get("input_identities_verified_before_and_after") is not True
            or report.get("paid_deepseek_requests") != 0
            or (report.get("comparison_cells"), report.get("budgeted_traces")) != (960, 320)
            or report.get("packet_counts") != {qid: 8 for qid in question_ids}
            or gate.get("question_ids") != question_ids or gate.get("packet_count") != 48
            or gate.get("review_file_count") != 6
            or gate.get("excluded_packets") != 0 or gate.get("excluded_required_fact_units") != 0):
        raise ValueError("pilot: incomplete or changed offline/gate denominator")
    offline = Path(report_pin["path"]).parent
    required_artifacts = {str(offline / name) for name in ("comparison-cells.jsonl", "native-traces.jsonl", "private-review-mapping.json")}
    packet_paths = {str(offline / "review-packets" / (qid + ".json")) for qid in question_ids}
    required_artifacts.update(packet_paths)
    artifacts = report.get("artifacts", [])
    if len(artifacts) != 9 or {item["path"] for item in artifacts} != required_artifacts:
        raise ValueError("pilot: incomplete offline artifact bindings")
    check_bindings(artifacts)
    artifact_hashes = {item["path"]: item["sha256"] for item in artifacts}
    gate_inputs = gate.get("input_sha256", {})
    expected_inputs = {report_pin["path"]: report_pin["sha256"],
                       str(offline / "private-review-mapping.json"): artifact_hashes[str(offline / "private-review-mapping.json")],
                       **{path: artifact_hashes[path] for path in packet_paths}}
    if any(gate_inputs.get(path) != digest for path, digest in expected_inputs.items()):
        raise ValueError("pilot: gate inputs differ from trusted offline artifacts")
    review_paths = set(gate_inputs) - set(expected_inputs)
    if len(review_paths) != 6 or {Path(path).name for path in review_paths} != {qid + ".json" for qid in question_ids}:
        raise ValueError("pilot: gate requires six independently bound review files")
    check_bindings([{"path": path, "sha256": digest} for path, digest in gate_inputs.items()])
    selection, changes = gate.get("selection", {}), gate.get("paired_changes_from_node_only", {})
    gained, lost, net = (changes.get(key) for key in ("gained_fully_supported_facts", "lost_fully_supported_facts", "net_fully_supported_change"))
    if (selection.get("gate_passed") is not True or selection.get("selected_factor") != "source-backfill"
            or any(type(value) is not int for value in (gained, lost, net))
            or min(gained, lost) < 0 or net <= 0 or net != gained - lost
            or len(changes.get("gains", [])) != gained or len(changes.get("losses", [])) != lost):
        raise ValueError("pilot: independently pinned gate must have a positive, complete paired net gain")
    aggregates = gate.get("aggregates", {})
    units = gate.get("required_fact_units_per_factor")
    if type(units) is not int or units <= 0 or set(aggregates) != {"node-only", "source-backfill"}:
        raise ValueError("pilot: invalid fact denominator")
    for summary in aggregates.values():
        counts = [summary.get(verdict) for verdict in ("fully_supported", "partly_supported", "contradicted", "absent")]
        if (any(type(value) is not int or value < 0 for value in counts) or sum(counts) != units
                or summary.get("required_fact_units") != units or summary.get("packets") != 24):
            raise ValueError("pilot: gate fact count does not preserve its denominator")
    if aggregates["source-backfill"]["fully_supported"] - aggregates["node-only"]["fully_supported"] != net:
        raise ValueError("pilot: gate aggregate and paired changes disagree")
    return {"fully_supported_before": aggregates["node-only"]["fully_supported"],
            "fully_supported_after": aggregates["source-backfill"]["fully_supported"],
            "required_fact_units_per_factor": units, "gained": gained, "lost": lost, "net": net,
            "reported_losses": changes["losses"]}


def baseline_identities(path: Path, questions: dict) -> list[dict]:
    rows = {}
    with path.open(encoding="utf-8", newline="") as stream:
        for line in stream:
            row = json.loads(line)
            # 非候选行只用于全文件 hash，不分析其内容 / select the permitted IDs first.
            if row.get("question_id") not in questions or row.get("arm") not in ARMS:
                continue
            key = (row["question_id"], row["arm"])
            if (key in rows or row.get("question") != questions[key[0]]
                    or row.get("harness_protocol") != "native_tool_calls/v3-batches"
                    or row.get("status") not in ("ok", "error")):
                raise ValueError("pilot: invalid or duplicate reused baseline task")
            rows[key] = {"question_id": key[0], "arm": key[1], "status": row["status"],
                         "record_sha256": hashlib.sha256(line.encode()).hexdigest()}
    if set(rows) != {(qid, arm) for qid in questions for arm in ARMS}:
        raise ValueError("pilot: reused paid baseline must contain all twelve candidate identities")
    return [rows[key] for key in sorted(rows)]


def make_plan(pins: dict) -> dict:
    required = {"implementation_freeze", "semantic_gate", "offline_report", "baseline_results"}
    if set(pins) != required:
        raise ValueError("pilot: four explicit independently pinned inputs are required")
    pins = {name: pin(Path(item["path"]), item["sha256"]) for name, item in pins.items()}
    frozen, protocol, roster = verify_freeze(Path(pins["implementation_freeze"]["path"]))
    pilot = protocol["future_paid_pilot"]
    if pilot.get("solver") != SOLVER or pilot.get("judge") != JUDGE:
        raise ValueError("pilot: frozen solver/judge budget or protocol parameters changed")
    controls = protocol["fixed_controls"]
    if any(controls.get(key) != SOLVER[key] for key in ("max_searches", "max_model_turns", "top_k", "per_search_chars", "total_context_chars")):
        raise ValueError("pilot: native context/search budgets changed")
    ids = pilot.get("dev_ids", [])
    if (len(ids) != 6 or len(set(ids)) != 6 or protocol["semantic_gate"]["dev_ids"] != ids
            or pilot.get("tasks") != 12 or pilot.get("arms") != list(ARMS)):
        raise ValueError("pilot: exactly six frozen DEV questions and twelve candidate tasks are required")
    questions = {trace["question_id"]: trace["question"] for trace in roster["traces"] if trace["question_id"] in ids}
    if set(questions) != set(ids):
        raise ValueError("pilot: candidate questions are missing from the verified DEV roster")
    gate_summary = verify_gate(read_json(Path(pins["semantic_gate"]["path"])),
                               read_json(Path(pins["offline_report"]["path"])), pins, ids)
    baselines = baseline_identities(Path(pins["baseline_results"]["path"]), questions)
    # 排除并发改写造成的旧 hash/新内容混用 / verify pinned bytes after all reads too.
    check_bindings(list(pins.values()))
    return {"format": "cairn-source-backfill-pilot-plan/v1", "status": "ready_plan_execution_blocked",
            "execution_enabled": False, "paid_requests": 0, "credential_inspected": False,
            "pins": pins, "candidate_factor": "source-backfill",
            "tasks": [{"question_id": qid, "arm": arm} for qid in ids for arm in ARMS],
            "solver_dataset": [{"question_id": qid, "question": questions[qid]} for qid in ids],
            "solver": SOLVER, "judge": JUDGE, "wrapper": frozen["wrappers"]["source-backfill"],
            "solver_entrypoint": "agent_eval_native.evaluate_question with NativeDeepSeekClient and native durable ledgers",
            "judge_entrypoint": "unchanged grade.prepare and grade.judge rubric; original sources and frozen gold",
            "baseline": {"policy": "reuse existing round2 paid results only; no paid control rerun", "tasks": baselines},
            "semantic_gate": gate_summary, "execution_blockers": BLOCKERS,
            "next_requirement": "A separately reviewed authorization-stop and judge-durability implementation requires an explicit new pilot freeze; this tool never substitutes credentials or executes the existing CLIs unsafely."}


def freeze_runner(pins: dict, output: Path) -> dict:
    plan = make_plan(pins)
    document = {"format": "cairn-source-backfill-pilot-runner-freeze/v1",
                "created_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
                "implementation": [binding(HERE / name) for name in RUNNER_FILES], "plan": plan}
    checked_path(output.parent, directory=True)
    write_new_json(output, document)  # exclusive create: explicit freezes never replace old attempts.
    return {"runner_freeze": str(output.absolute()), "sha256": binding(output)["sha256"],
            "execution_enabled": False, "paid_requests": 0}


def preflight(freeze_path: Path, expected_sha256: str) -> dict:
    anchor = pin(freeze_path, expected_sha256)
    document = read_json(Path(anchor["path"]))
    if (document.get("format") != "cairn-source-backfill-pilot-runner-freeze/v1"
            or {item["path"] for item in document.get("implementation", [])} != {str(HERE / name) for name in RUNNER_FILES}):
        raise ValueError("pilot: incomplete pilot runner/test bindings")
    check_bindings(document["implementation"])
    plan = make_plan(document["plan"]["pins"])
    if plan != document["plan"]:
        raise ValueError("pilot: plan, task, or budget changed; automatic re-freeze is forbidden")
    checked_hash(freeze_path, expected_sha256)
    return {**plan, "pilot_runner_freeze": anchor, "runner_bindings_verified": True}


def blocked_run(freeze_path: Path, expected_sha256: str, *, key_env: str | None = None, key_file: Path | None = None) -> dict:
    if bool(key_env) == (key_file is not None) or (key_env and not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key_env)):
        raise ValueError("pilot: explicitly select one dedicated runtime key env name or key file")
    plan = preflight(freeze_path, expected_sha256)
    # 密钥只允许在完整可执行入口通过后读取；this blocked path never accesses a secret.
    return {**plan, "status": "execution_blocked_before_credential_access"}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("plan", "freeze-runner"):
        command = commands.add_parser(name)
        for item in ("implementation-freeze", "semantic-gate", "offline-report", "baseline-results"):
            command.add_argument("--" + item, type=Path, required=True)
            command.add_argument("--" + item + "-sha256", required=True)
        if name == "freeze-runner":
            command.add_argument("--output", type=Path, required=True)
    for name in ("preflight", "run"):
        command = commands.add_parser(name)
        command.add_argument("--runner-freeze", type=Path, required=True)
        command.add_argument("--runner-freeze-sha256", required=True)
        if name == "run":
            command.add_argument("--allow-paid", action="store_true", required=True)
            keys = command.add_mutually_exclusive_group(required=True)
            keys.add_argument("--key-env")
            keys.add_argument("--key-file", type=Path)
    args = parser.parse_args(argv)
    if args.command in ("plan", "freeze-runner"):
        pins = {name: {"path": str(getattr(args, name)), "sha256": getattr(args, name + "_sha256")}
                for name in ("implementation_freeze", "semantic_gate", "offline_report", "baseline_results")}
        result = make_plan(pins) if args.command == "plan" else freeze_runner(pins, args.output)
    elif args.command == "preflight":
        result = preflight(args.runner_freeze, args.runner_freeze_sha256)
    else:
        result = blocked_run(args.runner_freeze, args.runner_freeze_sha256, key_env=args.key_env, key_file=args.key_file)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 2 if args.command == "run" else 0


if __name__ == "__main__":
    raise SystemExit(main())
