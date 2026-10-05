"""Offline fault injection only: mocked HTTP never claims a paid provider run."""
from __future__ import annotations

import io
import json
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

import agent_eval_native as native
from durable_ledger import JSONLedger, LedgerIntegrityError, read_jsonl
from retrieval import json_text


def tool_response(name="search", turn=1):
    arguments = {"query": "fixture"} if name == "search" else {"answer": "Cannot confirm", "citations": []}
    return {"id": "offline-fixture", "model": "deepseek-flash",
            "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
            "choices": [{"finish_reason": "tool_calls", "message": {
                "role": "assistant", "content": None,
                "tool_calls": [{"id": f"offline-call-{turn}", "type": "function",
                                "function": {"name": name, "arguments": json_text(arguments)}}]}}]}


class Response:
    status = 200

    def __init__(self, payload):
        self.payload = payload

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False

    def read(self):
        return json_text(self.payload).encode()


class EmptyEngine:
    def __init__(self, *args, **kwargs):
        pass

    def search(self, query, max_chars):
        text = json_text({"evidence": [], "truncated": False})
        return {"status": "ok", "query": query, "evidence": [], "serialized": text,
                "visible_chars": len(text), "diagnostics": {}, "latency_ms": 0}


class NativeLedgerAbortTests(unittest.TestCase):
    def setUp(self):
        self.no_network = mock.patch("socket.create_connection", side_effect=AssertionError("real network forbidden"))
        self.no_network.start()
        self.addCleanup(self.no_network.stop)

    def test_replaced_result_ledger_blocks_before_budget_and_http(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            requests, results = JSONLedger(root / "requests.jsonl"), JSONLedger(root / "results.jsonl")
            self.addCleanup(requests.close)
            self.addCleanup(results.close)
            budget = native.CallBudget(10)
            client = native.NativeDeepSeekClient(native.EvalConfig(), "offline-only-key", budget, requests, results)
            results.path.rename(root / "original-results.jsonl")
            replacement = b"REPLACEMENT MUST NOT BE WRITTEN\n"
            results.path.write_bytes(replacement)
            with mock.patch("urllib.request.urlopen", side_effect=AssertionError("HTTP must not be admitted")) as http:
                row = native.evaluate_question("blocked", "Question", "fts", EmptyEngine(), client, native.EvalConfig())
                again = native.evaluate_question("queued", "Question", "fts", EmptyEngine(), client, native.EvalConfig())
            for result in (row, again):
                self.assertEqual(result["status"], "error")
                self.assertEqual(result["failure_kind"], "ledger_durability_abort")
                self.assertEqual(result["execution_state"], "not_started")
                self.assertFalse(result["network_attempted"])
                self.assertEqual(result["model_calls"], [])
            self.assertEqual(budget.used, 0)
            self.assertEqual(http.call_count, 0)
            self.assertEqual(read_jsonl(requests.path), [])
            self.assertEqual(results.path.read_bytes(), replacement)
            self.assertTrue(list(root.glob("results-ledger-failure-*.json")))

    def test_result_failure_drains_inflight_collects_queue_without_new_http(self):
        with tempfile.TemporaryDirectory() as directory:
            root, output = Path(directory), Path(directory) / "run"
            dataset = root / "questions.json"
            dataset.write_text(json.dumps([{"id": f"q-{i}", "question": f"Question-{i}"} for i in range(12)]))
            for name in ("kg.db", "raw.db", "adapter"):
                (root / name).write_bytes(b"offline fixture; never opened by mocked retrieval")
            argv = ["agent_eval_native.py", "--dataset", str(dataset), "--output-dir", str(output),
                    "--kg-db", str(root / "kg.db"), "--raw-db", str(root / "raw.db"),
                    "--adapter", str(root / "adapter"), "--arms", "fts", "--workers", "3",
                    "--max-http-attempts", "100", "--max-retries", "1", "--allow-paid"]
            all_first_calls = threading.Barrier(3, timeout=10)
            failure_seen = threading.Event()
            dispatch_lock = threading.Lock()
            first_order, transport_calls = [], []
            original_append = JSONLedger.append
            replacement = b"REPLACEMENT MUST NOT BE WRITTEN\n"

            def replace_before_first_result(ledger, record):
                if ledger.path.name == "results.jsonl":
                    try:
                        return original_append(ledger, record)
                    finally:
                        # append_result still owns the admission lock here.
                        # Other workers cannot admit a new call before the
                        # same critical section latches this append failure.
                        failure_seen.set()
                return original_append(ledger, record)

            def transport(request, timeout):
                payload = json.loads(request.data)
                question = payload["messages"][1]["content"]
                turn = 1 + sum(message["role"] == "assistant" for message in payload["messages"])
                with dispatch_lock:
                    transport_calls.append((question, turn))
                    if turn == 1:
                        first_order.append(question)
                        position = len(first_order) - 1
                if turn == 1:
                    all_first_calls.wait()
                    if position:
                        if not failure_seen.wait(10):
                            raise AssertionError("main did not detect injected result replacement")
                    return Response(tool_response("search", 1))
                if question != first_order[0] or failure_seen.is_set():
                    raise AssertionError("new HTTP dispatched after result failure")
                # Inject the real inode replacement while this final call is
                # already in flight. Queued jobs can race main's result append,
                # but either observer must detect the same replacement before
                # admitting any additional request.
                result_path = output / "results.jsonl"
                result_path.rename(output / "original-results.jsonl")
                result_path.write_bytes(replacement)
                return Response(tool_response("submit_answer", turn))

            with mock.patch("sys.argv", argv), mock.patch.dict("os.environ", {"CAIRN_LLM_API_KEY": "offline-only-key"}), \
                    mock.patch.object(native, "RetrievalEngine", EmptyEngine), \
                    mock.patch.object(JSONLedger, "append", new=replace_before_first_result), \
                    mock.patch("urllib.request.urlopen", side_effect=transport), \
                    mock.patch("sys.stdout", new_callable=io.StringIO):
                with self.assertRaises(LedgerIntegrityError):
                    native.main()

            self.assertEqual(len(transport_calls), 4)  # 3 admitted searches + first worker's final response.
            self.assertEqual(len(first_order), 3)       # 9 queued jobs never sent HTTP.
            self.assertEqual((output / "results.jsonl").read_bytes(), replacement)
            events = read_jsonl(output / "requests.jsonl")
            request_events = [row for row in events if row["event"] == "request"]
            response_events = [row for row in events if row["event"] == "response"]
            self.assertEqual(len(request_events), 4)
            self.assertEqual(len(response_events), 4)
            self.assertTrue(all(row["usage"]["total_tokens"] == 18 for row in response_events))
            receipts = [json.loads(path.read_text()) for path in output.glob("results-ledger-failure-*.json")]
            rows = [receipt["record"] for receipt in receipts if isinstance(receipt.get("record"), dict)]
            self.assertEqual(len(rows), 12)
            self.assertEqual(len({(row["question_id"], row["arm"]) for row in rows}), 12)
            self.assertEqual(sum(row["status"] == "ok" for row in rows), 1)
            partial = [row for row in rows if row.get("execution_state") == "partial"]
            blocked = [row for row in rows if row.get("execution_state") == "not_started"]
            self.assertEqual(len(partial), 2)
            self.assertEqual(len(blocked), 9)
            self.assertTrue(all(not row["network_attempted"] and not row["model_calls"] for row in blocked))
            calls = [call for row in rows for call in row["model_calls"]]
            self.assertEqual(sorted(calls, key=lambda row: row["http_attempt"]),
                             sorted(response_events, key=lambda row: row["http_attempt"]))
            integrity = json.loads((output / "ledger-integrity.json").read_text())
            self.assertEqual(integrity["status"], "failed")
            self.assertEqual(integrity["collected_jobs"], 12)
            self.assertEqual(integrity["http_attempts_reserved"], 4)
            self.assertFalse(integrity["summary_written"])
            self.assertFalse((output / "summary.json").exists())
            self.assertNotIn("offline-only-key", json_text(receipts))

    def test_provider_retry_checks_result_ledger_before_reserving_again(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            requests, results = JSONLedger(root / "requests.jsonl"), JSONLedger(root / "results.jsonl")
            self.addCleanup(requests.close)
            self.addCleanup(results.close)
            budget = native.CallBudget(10)
            client = native.NativeDeepSeekClient(native.EvalConfig(), "offline-only-key", budget, requests, results)

            def invalid_response(request, timeout):
                results.path.rename(root / "original-results.jsonl")
                results.path.write_bytes(b"replacement\n")
                payload = tool_response()
                payload["choices"][0]["finish_reason"] = "length"
                return Response(payload)

            with mock.patch("urllib.request.urlopen", side_effect=invalid_response) as http:
                row = native.evaluate_question("retry", "Question", "fts", EmptyEngine(), client, native.EvalConfig())
            self.assertEqual(http.call_count, 1)
            self.assertEqual(budget.used, 1)
            self.assertEqual(row["failure_kind"], "ledger_durability_abort")
            self.assertEqual(row["execution_state"], "partial")
            self.assertEqual(len(row["model_calls"]), 1)
            self.assertEqual(row["model_calls"][0]["usage"]["total_tokens"], 18)
            self.assertEqual([row["event"] for row in read_jsonl(requests.path)], ["request", "response"])


if __name__ == "__main__":
    unittest.main()
