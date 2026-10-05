#!/usr/bin/env python3
"""付费入口的合成信任边界；no provider, real corpus, or DEV query is used."""
import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import run_source_backfill_pilot as pilot


class PilotTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.ids = ["synthetic-" + str(i) for i in range(6)]
        self.protocol = {"future_paid_pilot": {"dev_ids": self.ids, "arms": ["fts", "graph"], "tasks": 12,
                                               "solver": copy.deepcopy(pilot.SOLVER), "judge": copy.deepcopy(pilot.JUDGE)},
                         "semantic_gate": {"dev_ids": self.ids},
                         "fixed_controls": {key: pilot.SOLVER[key] for key in ("max_searches", "max_model_turns", "top_k", "per_search_chars", "total_context_chars")}}
        self.roster = {"traces": [{"question_id": qid, "question": "Synthetic question " + qid} for qid in self.ids]}
        self.frozen = {"wrappers": {"source-backfill": {"path": "synthetic-wrapper", "sha256": "0" * 64}}}
        self.patcher = patch.object(pilot, "verify_freeze", return_value=(self.frozen, self.protocol, self.roster))
        self.verify = self.patcher.start()
        self.addCleanup(self.patcher.stop)
        self.offline = self.root / "offline"
        self.offline.mkdir()
        (self.offline / "review-packets").mkdir()
        (self.root / "reviews").mkdir()
        implementation = self.write(self.root / "implementation.json", {"synthetic": True})
        artifacts = []
        for name in ("comparison-cells.jsonl", "native-traces.jsonl", "private-review-mapping.json"):
            artifacts.append(self.write(self.offline / name, {}))
        for qid in self.ids:
            artifacts.append(self.write(self.offline / "review-packets" / (qid + ".json"), {"synthetic": qid}))
        reviews = [self.write(self.root / "reviews" / (qid + ".json"), {}) for qid in self.ids]
        report = {"format": "cairn-round3-offline-results/v1", "implementation_freeze_sha256": implementation["sha256"],
                  "input_identities_verified_before_and_after": True, "paid_deepseek_requests": 0,
                  "comparison_cells": 960, "budgeted_traces": 320, "packet_counts": {qid: 8 for qid in self.ids}, "artifacts": artifacts}
        offline = self.write(self.offline / "offline-results.json", report)
        self.gate = {"format": "cairn-round3-semantic-gate/v1", "implementation_freeze_sha256": implementation["sha256"],
                     "trusted_offline_report_sha256": offline["sha256"], "offline_report_path": offline["path"],
                     "all_review_inputs_unchanged": True, "offline_artifact_bindings_verified": True,
                     "question_ids": self.ids, "packet_count": 48, "review_file_count": 6,
                     "excluded_packets": 0, "excluded_required_fact_units": 0,
                     "input_sha256": {entry["path"]: entry["sha256"] for entry in [offline, artifacts[2], *artifacts[3:], *reviews]},
                     "selection": {"gate_passed": True, "selected_factor": "source-backfill"},
                     "paired_changes_from_node_only": {"gained_fully_supported_facts": 2, "lost_fully_supported_facts": 1,
                         "net_fully_supported_change": 1, "gains": [{}, {}], "losses": [{"synthetic_loss": True}]},
                     "required_fact_units_per_factor": 24,
                     "aggregates": {factor: {"fully_supported": supported, "partly_supported": 0, "contradicted": 0,
                                                "absent": 24 - supported, "required_fact_units": 24, "packets": 24}
                                    for factor, supported in (("node-only", 10), ("source-backfill", 11))}}
        gate = self.write(self.root / "gate.json", self.gate)
        baseline = self.root / "baseline.jsonl"
        baseline.write_text("".join(json.dumps({"question_id": q["question_id"], "question": q["question"], "arm": arm,
                                                "harness_protocol": "native_tool_calls/v3-batches", "status": "ok"}) + "\n"
                                    for q in self.roster["traces"] for arm in ("fts", "graph")))
        self.pins = {"implementation_freeze": implementation, "semantic_gate": gate, "offline_report": offline,
                     "baseline_results": pilot.binding(baseline)}

    def write(self, path, value):
        path.write_text(json.dumps(value, sort_keys=True))
        return pilot.binding(path)

    def repin_gate(self):
        self.pins["semantic_gate"] = self.write(Path(self.pins["semantic_gate"]["path"]), self.gate)

    def freeze(self):
        path = self.root / "pilot-runner-freeze.json"
        pilot.freeze_runner(self.pins, path)
        return path, pilot.binding(path)["sha256"]

    def test_plan_keeps_twelve_candidates_and_reuses_all_baselines(self):
        result = pilot.make_plan(self.pins)
        self.assertFalse(result["execution_enabled"])
        self.assertEqual(result["candidate_factor"], "source-backfill")
        self.assertEqual(len(result["tasks"]), 12)
        self.assertEqual(len(result["baseline"]["tasks"]), 12)
        self.assertEqual(result["semantic_gate"]["reported_losses"], [{"synthetic_loss": True}])
        self.verify.assert_called_once_with(Path(self.pins["implementation_freeze"]["path"]))
        self.assertEqual({b["code"] for b in result["execution_blockers"]}, {
            "native_authorization_stop_missing", "judge_authorization_stop_missing", "judge_durable_admission_and_drain_missing"})

    def test_gate_bytes_require_independent_pin(self):
        self.gate["selection"]["gate_passed"] = False
        self.write(Path(self.pins["semantic_gate"]["path"]), self.gate)
        with self.assertRaisesRegex(ValueError, "SHA-256"):
            pilot.make_plan(self.pins)

    def test_even_repinned_gate_must_be_positive_and_complete(self):
        for key, value, error in (("net_fully_supported_change", 0, "positive"), ("losses", [], "complete")):
            original = copy.deepcopy(self.gate)
            self.gate["paired_changes_from_node_only"][key] = value
            self.repin_gate()
            with self.assertRaisesRegex(ValueError, error):
                pilot.make_plan(self.pins)
            self.gate = original
        self.gate["question_ids"] = self.ids[:-1]
        self.repin_gate()
        with self.assertRaisesRegex(ValueError, "denominator"):
            pilot.make_plan(self.pins)

    def test_mutated_gate_input_is_detected(self):
        (self.root / "reviews" / (self.ids[0] + ".json")).write_text("changed")
        with self.assertRaisesRegex(ValueError, "SHA-256"):
            pilot.make_plan(self.pins)

    def test_budget_changes_are_rejected_before_runner_freeze(self):
        self.protocol["future_paid_pilot"]["solver"]["max_http_attempts_total"] = 49
        with self.assertRaisesRegex(ValueError, "budget"):
            pilot.make_plan(self.pins)

    def test_extra_freeze_rejects_runner_or_saved_plan_change(self):
        path, digest = self.freeze()
        document = json.loads(path.read_text())
        document["implementation"][0]["sha256"] = "0" * 64
        current = self.write(path, document)
        with self.assertRaisesRegex(ValueError, "SHA-256"):
            pilot.preflight(path, current["sha256"])
        document["implementation"][0] = pilot.binding(Path(document["implementation"][0]["path"]))
        document["plan"]["solver"]["max_http_attempts_total"] = 49
        current = self.write(path, document)
        with self.assertRaisesRegex(ValueError, "automatic re-freeze"):
            pilot.preflight(path, current["sha256"])

    def test_freeze_is_exclusive_and_baseline_denominator_is_not_dropped(self):
        path, _ = self.freeze()
        with self.assertRaises(FileExistsError):
            pilot.freeze_runner(self.pins, path)
        baseline = Path(self.pins["baseline_results"]["path"])
        baseline.write_text("\n".join(baseline.read_text().splitlines()[:-1]) + "\n")
        self.pins["baseline_results"] = pilot.binding(baseline)
        with self.assertRaisesRegex(ValueError, "all twelve"):
            pilot.make_plan(self.pins)

    def test_preflight_and_blocked_run_never_access_keys_or_network(self):
        path, digest = self.freeze()
        secret = self.root / "dedicated-key"
        secret.write_text("SYNTHETIC_SECRET_MUST_NOT_APPEAR")
        read_bytes = Path.read_bytes
        def guarded_read(target):
            if target == secret:
                raise AssertionError("credential was read")
            return read_bytes(target)
        class UnreadableEnvironment(dict):
            def get(self, *args, **kwargs):
                raise AssertionError("environment credential lookup")
            def __getitem__(self, key):
                raise AssertionError("environment credential lookup")
        with patch("os.environ", UnreadableEnvironment()), \
                patch.object(Path, "read_bytes", guarded_read), \
                patch("urllib.request.urlopen", side_effect=AssertionError("provider forbidden")):
            result = pilot.preflight(path, digest)
            blocked = pilot.blocked_run(path, digest, key_file=secret)
            by_env = pilot.blocked_run(path, digest, key_env="EXPLICIT_DEEPSEEK_PILOT_KEY")
        self.assertFalse(result["credential_inspected"])
        self.assertEqual(blocked["status"], "execution_blocked_before_credential_access")
        self.assertFalse(by_env["execution_enabled"])
        self.assertNotIn("SYNTHETIC_SECRET", json.dumps([result, blocked, by_env]))


if __name__ == "__main__":
    unittest.main()
