import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import summarize_costs as audit


class MeteringTest(unittest.TestCase):
    def test_failed_retry_pending_and_cache_unknown(self):
        request = {"http_attempt": 1, "event": "request", "request_sha256": "a", "attempt_for_turn": 1}
        first = {**request, "event": "response", "status": "error", "http_status": 200,
                 "finish_reason": "length", "usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}}
        retry = {**request, "http_attempt": 2, "attempt_for_turn": 2}
        second = {**retry, "event": "response", "status": "ok", "finish_reason": "stop", "http_status": 200,
                  "usage": {"prompt_tokens": 11, "completion_tokens": 2, "total_tokens": 13,
                            "prompt_cache_hit_tokens": 4, "prompt_cache_miss_tokens": 7},
                  "assistant_message": {"reasoning_content": "MUST_NOT_BE_EMITTED"}}
        pending = {**request, "http_attempt": 3}
        result = audit.summarize_rows([request, first, retry, second, pending], "solver")
        self.assertEqual((result["http_requests"], result["http_responses_confirmed"], result["unresolved_requests"]), (3, 2, 1))
        self.assertEqual(result["known_token_sums"]["input_tokens"], 21)
        self.assertEqual(result["known_token_sums"]["output_tokens"], 22)
        self.assertEqual((result["failed_attempts"], result["retry_attempts"], result["finish_length"]), (1, 1, 1))
        self.assertEqual(result["missing_token_fields"]["cache_hit_tokens"], 1)
        self.assertFalse(result["cache_usage_complete"])
        self.assertNotIn("MUST_NOT_BE_EMITTED", json.dumps(result))

    def test_judge_joins_only_matching_metadata(self):
        request = {"request_id": "j1", "event": "request", "request_sha256": "x"}
        response = {**request, "event": "response", "status": "error", "response_sha256": "y",
                    "usage": {"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8},
                    "accounted_tokens": 99999, "usage_is_estimate": False}
        grade = {**response, "finish_reason": "length", "final_content": "MUST_NOT_BE_EMITTED"}
        result = audit.summarize_rows([request, response], "judge", [grade])
        self.assertEqual(result["finish_length"], 1)
        self.assertEqual(result["known_token_sums"]["total_tokens"], 8)
        self.assertNotIn("MUST_NOT_BE_EMITTED", json.dumps(result))
        with self.assertRaises(ValueError):
            audit.summarize_rows([request, response], "judge", [{**grade, "response_sha256": "different"}])

    def test_duplicates_partial_append_and_unmetered_transport(self):
        row = {"http_attempt": 1, "transport_error": True}
        result = audit.summarize_rows([row, dict(row)], "flat")
        self.assertEqual((result["http_requests"], result["http_responses_confirmed"]), (1, 0))
        self.assertEqual(result["attempts_missing_input_or_output"], 1)
        self.assertEqual(result["duplicate_identical_events"], 1)
        with self.assertRaises(ValueError):
            audit.summarize_rows([row, {**row, "input_tokens": 4}], "flat")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "ledger.jsonl"
            path.write_bytes(b'{"http_attempt":1}\n{"http_attempt":')
            rows, source = audit.read_source(Path(directory), "ledger.jsonl")
            self.assertEqual(len(rows), 1)
            self.assertGreater(source["ignored_trailing_bytes"], 0)
            with self.assertRaises(ValueError):
                audit.read_source(Path(directory), "ledger.jsonl", final=True)

    def test_final_rejects_missing_unclassified_and_pending_sources(self):
        stage = (("solver-baseline-reconciled", "current-quality", "quality-baseline-reconciled/requests.jsonl", "solver"),)
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(audit, "STAGES", stage):
            root = Path(directory)
            with self.assertRaisesRegex(ValueError, "current source is missing"):
                audit.build_report(root, final=True)
            ledger = root / stage[0][2]
            ledger.parent.mkdir()
            ledger.write_text("", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "reconciliation receipt is missing"):
                audit.build_report(root, final=True)
            (ledger.parent / "reconciliation-receipt.json").write_text("{}", encoding="utf-8")
            unknown = root / "quality-unaccounted" / "requests.jsonl"
            unknown.parent.mkdir()
            unknown.write_text("", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "unclassified quality ledgers"):
                audit.build_report(root, final=True)
            unknown.unlink()
            ledger.write_text('{"event":"request","http_attempt":1,"request_sha256":"x"}\n', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "unresolved requests"):
                audit.build_report(root, final=True)
            self.assertEqual(audit.build_report(root)["status"], "snapshot")


if __name__ == "__main__":
    unittest.main()
