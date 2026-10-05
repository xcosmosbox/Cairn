import copy
import json
import tempfile
import unittest
from pathlib import Path

from reconcile_ledger import json_bytes, jsonl_bytes, reconcile, request_record, sha256


def response(attempt, turn):
    return {"question_id": "synthetic", "arm": "raw", "turn": turn, "http_attempt": attempt,
            "attempt_for_turn": 1, "started_at": "2026-10-04T00:00:00+00:00",
            "request_sha256": sha256(f"request-{attempt}".encode()), "request_bytes": 500,
            "request_max_tokens": 4096, "model": "synthetic-model", "thinking": "disabled",
            "tool_choice": "required", "protocol": "synthetic-protocol", "event": "response",
            "status": "ok", "http_status": 200, "response_sha256": sha256(f"response-{attempt}".encode()),
            "response_bytes": 800, "usage": {"prompt_tokens": 10, "completion_tokens": 2,
            "total_tokens": 12, "prompt_cache_hit_tokens": 3, "prompt_cache_miss_tokens": 7}}


class ReconciliationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "original"
        self.output = self.root / "derived"
        self.source.mkdir()
        self.calls = [response(1, 1), response(2, 2)]
        self.result = {"question_id": "synthetic", "arm": "raw", "status": "ok",
                       "answer": "OPAQUE ANSWER: do not grade or inspect", "model_calls": self.calls}
        self.events = [request_record(self.calls[0]), self.calls[0]]
        self.config = {"config": {"model": "synthetic-model", "thinking": "disabled", "max_tokens": 4096,
                                  "max_model_turns": 4, "max_retries": 1}, "max_http_attempts": 700,
                       "job_order": [{"question_id": "synthetic", "arm": "raw"}]}
        self.original_summary = {"questions": 1, "arms": ["raw"], "completed_jobs": 1,
                                 "successful_jobs": 1, "http_attempts": 2, "input_tokens": 10}

    def write_source(self):
        data = {"results.jsonl": jsonl_bytes([self.result]), "requests.jsonl": jsonl_bytes(self.events),
                "run-config.json": json_bytes(self.config), "summary.json": json_bytes(self.original_summary)}
        for name, content in data.items():
            (self.source / name).write_bytes(content)
        return data

    def test_partial_ledger_is_reconciled_with_explicit_provenance(self):
        original = self.write_source()
        summary = reconcile(self.source, self.output)
        self.assertEqual(summary["input_tokens"], 20)
        self.assertEqual(summary["total_tokens"], 24)
        self.assertTrue(summary["derived"])
        self.assertTrue(summary["usage_complete"])
        events = [json.loads(line) for line in (self.output / "requests.jsonl").read_text().splitlines()]
        self.assertEqual(events[0], self.events[0])
        self.assertEqual(events[1], self.calls[0])
        self.assertEqual(events[3], self.calls[1])
        self.assertTrue(events[2]["reconstructed"])
        self.assertFalse(events[2]["pre_request_persistence_verified"])
        self.assertEqual(events[2]["reconstruction_source"]["index"], 1)
        self.assertEqual(summary["reconstruction"]["reconstructed_request_events"], 1)
        self.assertEqual(summary["reconstruction"]["response_records_recovered_from_results"], 1)
        self.assertIsNone(summary["reconstruction"]["pre_request_persistence_verified_attempts"])
        for name, content in original.items():
            self.assertEqual((self.source / name).read_bytes(), content)
        for name in ("results.jsonl", "run-config.json"):
            self.assertEqual((self.output / name).read_bytes(), original[name])
        self.assertEqual((self.output / "original-summary.json").read_bytes(), original["summary.json"])
        self.assertEqual((self.output / "original-requests.jsonl").read_bytes(), original["requests.jsonl"])
        manifest = json.loads((self.output / "manifest.json").read_text())
        for name, metadata in manifest["artifact_files"].items():
            self.assertEqual(sha256((self.output / name).read_bytes()), metadata["sha256"])
        receipt = json.loads((self.output / "reconciliation-receipt.json").read_text())
        self.assertEqual(receipt["source_loss_root_cause"], "unknown")
        self.assertNotIn("OPAQUE ANSWER", json.dumps(receipt))

    def test_surviving_request_with_missing_response_is_preserved(self):
        self.events.append(request_record(self.calls[1]))
        self.write_source()
        summary = reconcile(self.source, self.output)
        self.assertEqual(summary["reconstruction"]["reconstructed_request_events"], 0)
        receipt = json.loads((self.output / "reconciliation-receipt.json").read_text())
        self.assertEqual(receipt["original_request_http_attempts_without_original_response"], [2])

    def test_duplicate_or_conflicting_records_fail_before_output(self):
        original_result = copy.deepcopy(self.result)
        original_events = copy.deepcopy(self.events)
        mutations = [
            lambda: self.events.append(copy.deepcopy(self.events[0])),
            lambda: self.events.append(copy.deepcopy(self.events[1])),
            lambda: self.result["model_calls"].append(copy.deepcopy(self.result["model_calls"][0])),
            lambda: self.events[0].update(request_bytes=999),
            lambda: self.events[1]["usage"].update(prompt_tokens=999),
            lambda: self.events[0].update(turn=True),
            lambda: self.events[1]["usage"].update(prompt_tokens=10.0),
            lambda: self.result["model_calls"][1].update(question_id="wrong-owner"),
            lambda: self.result["model_calls"][1].update(request_sha256="invalid"),
            lambda: self.result["model_calls"][1].update(http_attempt=3),
            lambda: self.result["model_calls"][1]["usage"].update(total_tokens=999),
        ]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                self.result = copy.deepcopy(original_result)
                self.events = copy.deepcopy(original_events)
                mutate()
                original = self.write_source()
                with self.assertRaises(ValueError):
                    reconcile(self.source, self.output)
                self.assertFalse(self.output.exists())
                for name, content in original.items():
                    self.assertEqual((self.source / name).read_bytes(), content)

    def test_missing_usage_is_not_reconstructed_or_filled(self):
        del self.calls[1]["usage"]
        self.write_source()
        summary = reconcile(self.source, self.output)
        self.assertFalse(summary["usage_complete"])
        self.assertEqual(summary["attempts_without_usage"], 1)
        self.assertEqual(summary["input_tokens"], 10)
        self.assertEqual(summary["metering"]["attempts_missing_field"]["prompt_tokens"], 1)
        events = [json.loads(line) for line in (self.output / "requests.jsonl").read_text().splitlines()]
        self.assertNotIn("usage", events[3])

    def test_absent_usage_field_stays_unknown_even_when_derivable(self):
        for call in self.calls:
            del call["usage"]["total_tokens"]
            del call["usage"]["prompt_cache_miss_tokens"]
        self.write_source()
        summary = reconcile(self.source, self.output)
        self.assertIsNone(summary["total_tokens"])
        self.assertIsNone(summary["cache_miss_tokens"])
        self.assertFalse(summary["usage_complete"])
        self.assertFalse(summary["metering"]["cache_usage_complete"])

    def test_missing_configured_case_and_existing_output_fail(self):
        self.config["job_order"].append({"question_id": "missing", "arm": "raw"})
        self.write_source()
        with self.assertRaises(ValueError):
            reconcile(self.source, self.output)
        self.assertFalse(self.output.exists())
        self.output.mkdir()
        with self.assertRaises(ValueError):
            reconcile(self.source, self.output)
        self.assertEqual(list(self.output.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
