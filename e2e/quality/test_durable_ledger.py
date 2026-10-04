"""Offline durability/failure-injection tests; no real provider requests."""
from __future__ import annotations

import concurrent.futures
import copy
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import agent_eval_native as native
from durable_ledger import JSONLedger, LedgerIntegrityError, read_jsonl, verify_run_ledgers
from retrieval import json_text


def failure_receipts(directory):
    return [json.loads(path.read_text()) for path in sorted(directory.glob("*-ledger-failure-*.json"))]


def assert_no_open_ledger_fd(test, path):
    directory = Path("/proc/self/fd")
    if not directory.is_dir():
        test.skipTest("descriptor check requires Linux /proc")
    for descriptor in directory.iterdir():
        try:
            target = os.readlink(descriptor)
        except FileNotFoundError:
            continue
        test.assertNotEqual(target.removesuffix(" (deleted)"), str(path))


def ledger_fixture():
    requests, responses = {}, {}
    for attempt, qid, arm, turn in [(1, "q1", "raw", 1), (2, "q1", "raw", 2), (3, "q2", "graph", 1)]:
        request = {
            "question_id": qid, "arm": arm, "turn": turn, "http_attempt": attempt,
            "attempt_for_turn": 1, "started_at": "2026-10-04T00:00:00+00:00",
            "request_sha256": f"hash-{attempt}", "request_bytes": 100,
            "request_max_tokens": 4096, "model": "deepseek-flash", "thinking": "disabled",
            "tool_choice": "required", "protocol": "native_tool_calls/v3-batches",
            "event": "request", "status": "in_flight",
        }
        requests[attempt] = request
        responses[attempt] = {**request, "event": "response", "status": "ok", "finish_reason": "tool_calls",
                              "usage": {"prompt_tokens": 1, "completion_tokens": 2}, "latency_ms": 10}
    # Global request/response order can interleave across concurrent jobs.
    events = [requests[1], requests[3], responses[1], requests[2], responses[3], responses[2]]
    results = [
        {"question_id": "q1", "arm": "raw", "status": "ok", "model_calls": [copy.deepcopy(responses[1]), copy.deepcopy(responses[2])]},
        {"question_id": "q2", "arm": "graph", "status": "ok", "model_calls": [copy.deepcopy(responses[3])]},
    ]
    jobs = [{"question_id": "q1", "arm": "raw"}, {"question_id": "q2", "arm": "graph"}]
    return events, results, jobs


def write_run(directory, events, results):
    (directory / "requests.jsonl").write_text("".join(json_text(row) + "\n" for row in events))
    (directory / "results.jsonl").write_text("".join(json_text(row) + "\n" for row in results))
    (directory / "run-config.json").write_text('{"format":"offline-fixture"}\n')


class DurableLedgerTests(unittest.TestCase):
    def test_concurrent_complete_records_fsync_and_no_retained_descriptor(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "requests.jsonl"
            ledger = JSONLedger(path)
            assert_no_open_ledger_fd(self, path)
            with mock.patch("os.fsync", wraps=os.fsync) as fsync:
                def append_worker(worker):
                    for sequence in range(8):
                        ledger.append({"worker": worker, "sequence": sequence, "text": "并发账本"})
                with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
                    list(pool.map(append_worker, range(4)))
                self.assertEqual(fsync.call_count, 32)
            rows = read_jsonl(path)
            self.assertEqual(len(rows), 32)
            self.assertEqual({(row["worker"], row["sequence"]) for row in rows}, {(w, s) for w in range(4) for s in range(8)})
            for worker in range(4):
                self.assertEqual([row["sequence"] for row in rows if row["worker"] == worker], list(range(8)))
            ledger.verify()
            self.assertEqual(ledger.size, path.stat().st_size)
            assert_no_open_ledger_fd(self, path)
            ledger.close()
            ledger.close()
            with self.assertRaises(LedgerIntegrityError):
                ledger.append({"late": True})

    def test_replacement_fails_without_writing_replacement_and_preserves_record(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "requests.jsonl"
            ledger = JSONLedger(path)
            ledger.append({"event": "first"})
            old = root / "old-original.jsonl"
            path.rename(old)
            replacement = b'{"replacement":"untouched"}\n'
            path.write_bytes(replacement)
            pending = {"event": "response", "http_attempt": 1, "usage": {"completion_tokens": 27}}
            with self.assertRaises(LedgerIntegrityError):
                ledger.append(pending)
            self.assertEqual(path.read_bytes(), replacement)
            self.assertEqual(read_jsonl(old), [{"event": "first"}])
            receipts = failure_receipts(root)
            self.assertEqual(len(receipts), 1)
            self.assertEqual(receipts[0]["record"], pending)
            self.assertFalse(receipts[0]["pre_request_persistence_verified"])
            self.assertEqual(receipts[0]["expected_bytes_before_append"], old.stat().st_size)
            assert_no_open_ledger_fd(self, old)
            assert_no_open_ledger_fd(self, path)
            with self.assertRaises(LedgerIntegrityError):
                ledger.append({"event": "later"})
            self.assertEqual(path.read_bytes(), replacement)
            ledger.close()

    def test_same_inode_truncation_is_terminal_and_preserves_pending_record(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "requests.jsonl"
            ledger = JSONLedger(path)
            ledger.append({"event": "persisted-before-truncate"})
            identity = path.stat().st_ino
            path.write_bytes(b"")
            self.assertEqual(identity, path.stat().st_ino)
            record = {"event": "request", "http_attempt": 2}
            with self.assertRaises(LedgerIntegrityError):
                ledger.append(record)
            self.assertEqual(path.read_bytes(), b"")
            self.assertEqual(failure_receipts(root)[0]["record"], record)
            with self.assertRaises(LedgerIntegrityError):
                ledger.verify()
            ledger.close()

    def test_symlink_replacement_is_not_followed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path, target = root / "requests.jsonl", root / "target.jsonl"
            ledger = JSONLedger(path)
            ledger.append({"original": True})
            path.unlink()
            target.write_bytes(b"TARGET MUST REMAIN UNCHANGED\n")
            path.symlink_to(target)
            with self.assertRaises(LedgerIntegrityError):
                ledger.append({"new": True})
            self.assertEqual(target.read_bytes(), b"TARGET MUST REMAIN UNCHANGED\n")
            self.assertTrue(path.is_symlink())
            self.assertEqual(failure_receipts(root)[0]["record"], {"new": True})
            ledger.close()

    def test_verify_detects_end_of_run_replacement_even_at_identical_size(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "requests.jsonl"
            ledger = JSONLedger(path)
            ledger.append({"event": "complete"})
            data = path.read_bytes()
            replacement = root / "replacement.tmp"
            replacement.write_bytes(data)
            os.replace(replacement, path)
            with self.assertRaises(LedgerIntegrityError):
                ledger.verify()
            self.assertEqual(path.read_bytes(), data)
            self.assertIsNone(failure_receipts(root)[0]["record"])
            ledger.close()

    def test_exclusive_creation_never_clobbers_existing_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "requests.jsonl"
            path.write_bytes(b"EXISTING\n")
            with self.assertRaises(FileExistsError):
                JSONLedger(path)
            self.assertEqual(path.read_bytes(), b"EXISTING\n")

    def test_complete_run_reconciles_exactly_without_reconstruction_or_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            events, results, jobs = ledger_fixture()
            write_run(root, events, results)
            before = {path.name: path.read_bytes() for path in root.iterdir()}
            receipt = verify_run_ledgers(root, 3, jobs)
            self.assertEqual(receipt["status"], "verified")
            self.assertEqual(receipt["reconstructed_events"], 0)
            self.assertEqual(receipt["request_events"], 3)
            self.assertEqual(receipt["response_events"], 3)
            self.assertEqual(receipt["result_model_calls"], 3)
            self.assertEqual(receipt["completed_jobs"], 2)
            self.assertTrue(receipt["all_responses_have_prior_request_event"])
            self.assertTrue(receipt["response_records_match_results_exactly"])
            self.assertEqual(before, {path.name: path.read_bytes() for path in root.iterdir()})

    def test_missing_conflicting_misordered_and_misattributed_records_rejected(self):
        for scenario in ("request missing", "response missing", "response conflict", "response before request",
                         "request metadata mismatch", "result response mismatch", "result call missing",
                         "result call duplicate", "result wrong owner", "job missing", "job duplicate",
                         "boolean attempt", "boolean usage", "boolean request metadata"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                events, results, jobs = ledger_fixture()
                if scenario == "request missing":
                    events = [row for row in events if not (row["event"] == "request" and row["http_attempt"] == 1)]
                elif scenario == "response missing":
                    events = [row for row in events if not (row["event"] == "response" and row["http_attempt"] == 1)]
                elif scenario == "response conflict":
                    events.append({**copy.deepcopy(events[2]), "usage": {"completion_tokens": 999}})
                elif scenario == "response before request":
                    events.insert(0, events.pop(2))
                elif scenario == "request metadata mismatch":
                    events[0]["request_sha256"] = "different-request"
                elif scenario == "result response mismatch":
                    results[0]["model_calls"][0]["usage"]["completion_tokens"] = 999
                elif scenario == "result call missing":
                    results[0]["model_calls"].pop()
                elif scenario == "result call duplicate":
                    results[0]["model_calls"].append(copy.deepcopy(results[0]["model_calls"][0]))
                elif scenario == "result wrong owner":
                    results[0]["model_calls"][0]["arm"] = "graph"
                elif scenario == "job missing":
                    results.pop()
                elif scenario == "job duplicate":
                    results.append(copy.deepcopy(results[0]))
                elif scenario == "boolean attempt":
                    results[0]["model_calls"][0]["http_attempt"] = True
                elif scenario == "boolean usage":
                    results[0]["model_calls"][0]["usage"]["prompt_tokens"] = True
                elif scenario == "boolean request metadata":
                    events[0]["turn"] = True
                write_run(root, events, results)
                before = {path.name: path.read_bytes() for path in root.iterdir()}
                with self.assertRaises(LedgerIntegrityError):
                    verify_run_ledgers(root, 3, jobs)
                self.assertEqual(before, {path.name: path.read_bytes() for path in root.iterdir()})

    def test_native_response_append_failure_keeps_usage_and_blocks_future_http(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "requests.jsonl"
            ledger = JSONLedger(path)
            config = native.EvalConfig()
            client = native.NativeDeepSeekClient(config, "offline-test-credential", native.CallBudget(10), ledger)
            payload = {
                "id": "received-response", "model": "deepseek-flash",
                "usage": {"prompt_tokens": 11, "completion_tokens": 13},
                "choices": [{"finish_reason": "tool_calls", "message": {
                    "role": "assistant", "content": None, "tool_calls": [{"id": "tool-1", "type": "function",
                        "function": {"name": "search", "arguments": '{"query":"test"}'}}]}}],
            }

            class Response:
                status = 200
                def __enter__(self):
                    return self
                def __exit__(self, *args):
                    return False
                def read(self):
                    return json_text(payload).encode()

            def replace_before_response(request, timeout):
                self.assertEqual(read_jsonl(path)[0]["event"], "request")
                path.rename(root / "original-requests.jsonl")
                path.write_bytes(b"REPLACEMENT MUST NOT BE MODIFIED\n")
                return Response()

            with mock.patch("socket.create_connection", side_effect=AssertionError("network forbidden")), \
                    mock.patch("urllib.request.urlopen", side_effect=replace_before_response) as http:
                result = native.evaluate_question("q-ledger", "Question", "raw", None, client, config)
                self.assertEqual(result["status"], "error", result)
                self.assertEqual(http.call_count, 1)
                self.assertEqual(len(result["model_calls"]), 1)
                self.assertEqual(result["model_calls"][0]["usage"], payload["usage"])
                receipt = failure_receipts(root)[0]
                self.assertEqual(receipt["record"], result["model_calls"][0])
                self.assertEqual(receipt["record"]["event"], "response")
                with self.assertRaises((LedgerIntegrityError, native.ModelCallError)):
                    client.complete([{"role": "user", "content": "next"}], {"question_id": "q-next", "arm": "raw", "turn": 1}, "required")
                self.assertEqual(http.call_count, 1)
            self.assertEqual(path.read_bytes(), b"REPLACEMENT MUST NOT BE MODIFIED\n")
            self.assertNotIn("offline-test-credential", json_text(failure_receipts(root)))
            ledger.close()


if __name__ == "__main__":
    unittest.main()
