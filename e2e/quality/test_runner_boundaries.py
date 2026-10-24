"""Offline runner fault injection; synthetic keys, local mocks, no provider calls.

These tests isolate scheduling/persistence. Verdict validation and scoring are
stubbed because their schema behavior is covered separately by semantic tests.
"""
from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack, redirect_stdout
import io
import json
import os
from pathlib import Path
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest import mock
from urllib.error import HTTPError

import agent_eval_native as native
from durable_ledger import JSONLedger, LedgerIntegrityError, read_jsonl
import grade
from test_native_ledger_abort import EmptyEngine, tool_response


class Response:
    status = 200

    def __init__(self, payload):
        self.data = json.dumps(payload).encode()

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False

    def read(self, limit=None):
        return self.data if limit is None else self.data[:limit]


def judge_response():
    return Response({"usage": {"total_tokens": 19}, "choices": [{"finish_reason": "stop",
                     "message": {"content": "{}", "reasoning_content": "NEVER_RETAIN"}}]})


class RunnerBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.stack.enter_context(mock.patch("socket.create_connection", side_effect=AssertionError("real network forbidden")))
        self.stack.enter_context(mock.patch.dict("os.environ", {"CAIRN_OFFLINE_JUDGE_KEY": "offline-only-secret",
                                                                "CAIRN_LLM_API_KEY": "offline-only-secret"}))
        self.stack.enter_context(mock.patch("grade.validate_verdict", return_value={}))
        self.stack.enter_context(mock.patch("grade.scores", return_value={"strict_pass": True}))
        self.stack.enter_context(redirect_stdout(io.StringIO()))

    def judge_args(self, count=8, concurrency=3):
        blind = self.root / "blind.json"
        blind.write_text(json.dumps({"cases": [{"blind_id": f"b{i}", "answer": "fixture"} for i in range(count)]}))
        return SimpleNamespace(blind=str(blind), out=str(self.root / "grades.jsonl"),
            usage=str(self.root / "usage.jsonl"), extra_body=None, model="offline-model",
            api_base="https://offline.invalid/v1", temperature=0, max_tokens=100, max_calls=30,
            max_total_tokens=1000000, max_input_chars=10000, timeout=5,
            validation_retries=1, api_key_env="CAIRN_OFFLINE_JUDGE_KEY", limit=0, concurrency=concurrency)

    def test_native_authorization_stops_workers_retries_and_drains_all_admitted(self):
        # Previously each 401/403 was retried and later queued jobs kept sending.
        for status in (401, 403):
            with self.subTest(status=status), tempfile.TemporaryDirectory(dir=self.root) as directory:
                root = Path(directory)
                requests, results = JSONLedger(root / "requests.jsonl"), JSONLedger(root / "results.jsonl")
                client = native.NativeDeepSeekClient(native.EvalConfig(), "offline-only-secret",
                    native.CallBudget(100), requests, results)
                barrier, observed, lock = threading.Barrier(3, timeout=5), threading.Event(), threading.Lock()
                dispatched = []
                original_stop = client.durability.stop_authorization

                def stop(code):
                    original_stop(code)
                    observed.set()

                def transport(request, timeout):
                    with lock:
                        position = len(dispatched)
                        dispatched.append(request.data)
                    barrier.wait()
                    if position == 0:
                        raise HTTPError(request.full_url, status, "authorization", {}, io.BytesIO(b"forbidden"))
                    if not observed.wait(5):
                        raise AssertionError("auth latch was not observed")
                    return Response(tool_response())

                with mock.patch.object(client.durability, "stop_authorization", side_effect=stop), \
                     mock.patch("agent_eval_native.urlopen", side_effect=transport), \
                     ThreadPoolExecutor(max_workers=3) as pool:
                    jobs = [pool.submit(native.evaluate_question, f"q{i}", "Question", "fts", EmptyEngine(), client, native.EvalConfig()) for i in range(12)]
                    rows = [future.result() for future in jobs]
                    for row in rows:
                        client.durability.append_result(row)
                self.assertEqual(len(dispatched), 3)
                self.assertEqual(client.budget.used, 3)
                events = read_jsonl(requests.path)
                responses = [row for row in events if row["event"] == "response"]
                self.assertEqual(len(responses), 3)
                self.assertEqual(sum(row.get("usage_unknown", False) for row in responses), 1)
                self.assertEqual(sum(bool(row["model_calls"]) for row in rows), 3)
                self.assertEqual(sum(row["execution_state"] == "not_started" for row in rows), 9)
                self.assertTrue(all(row["failure_kind"] == "authorization_abort" for row in rows))
                self.assertEqual(len(read_jsonl(results.path)), 12)
                requests.close()
                results.close()

    def test_native_transient_http_error_still_retries(self):
        requests = JSONLedger(self.root / "requests.jsonl")
        self.addCleanup(requests.close)
        client = native.NativeDeepSeekClient(native.EvalConfig(), "offline-only-secret", native.CallBudget(10), requests)
        with mock.patch("agent_eval_native.urlopen", side_effect=[
                HTTPError("https://offline.invalid", 500, "temporary", {}, io.BytesIO(b"temporary")),
                Response(tool_response())]) as transport:
            _, attempts = client.complete([], {"question_id": "q", "arm": "fts", "turn": 1}, "required")
        self.assertEqual(transport.call_count, 2)
        self.assertEqual([row["http_status"] for row in attempts], [500, 200])
        self.assertIsNone(client.durability.authorization_failure)

    def test_native_main_auth_stop_keeps_denominator_and_exits_failed(self):
        output = self.root / "run"
        dataset = self.root / "questions.json"
        dataset.write_text(json.dumps([{"id": "q1", "question": "one"}, {"id": "q2", "question": "two"}]))
        for name in ("kg.db", "raw.db", "adapter"):
            (self.root / name).write_bytes(b"offline placeholder")
        argv = ["agent_eval_native.py", "--dataset", str(dataset), "--output-dir", str(output),
                "--kg-db", str(self.root / "kg.db"), "--raw-db", str(self.root / "raw.db"),
                "--adapter", str(self.root / "adapter"), "--arms", "fts", "--workers", "1", "--allow-paid"]
        with mock.patch("sys.argv", argv), mock.patch("agent_eval_native.RetrievalEngine", EmptyEngine), \
             mock.patch("agent_eval_native.urlopen", side_effect=HTTPError("https://offline.invalid", 403, "auth", {}, io.BytesIO())) as transport, \
             self.assertRaises(native.AuthorizationAbort):
            native.main()
        self.assertEqual(transport.call_count, 1)
        rows = read_jsonl(output / "results.jsonl")
        self.assertEqual(len(rows), 2)
        self.assertEqual(sum(row["network_attempted"] for row in rows), 1)
        summary = json.loads((output / "summary.json").read_text())
        self.assertEqual(summary["attempts_without_usage"], 1)
        self.assertEqual(summary["completed_jobs"], 2)
        self.assertEqual(json.loads((output / "ledger-integrity.json").read_text())["status"], "verified")

    def test_judge_authorization_stops_new_admission_and_drains(self):
        # Previously the scheduler kept admitting cases after its first auth failure.
        for status in (401, 403):
            with self.subTest(status=status), tempfile.TemporaryDirectory(dir=self.root) as directory:
                previous = self.root
                self.root = Path(directory)
                args = self.judge_args()
                barrier, lock = threading.Barrier(3, timeout=5), threading.Lock()
                observed = threading.Event()
                original_append = JSONLedger.append
                dispatched = []

                def append(ledger, row):
                    original_append(ledger, row)
                    if row.get("event") == "response" and row.get("http_status") == status:
                        observed.set()

                def transport(request, timeout):
                    events = read_jsonl(Path(args.usage))
                    self.assertTrue(any(row["event"] == "request" and row["request_sha256"] == grade.sha(request.data) for row in events))
                    with lock:
                        position = len(dispatched)
                        dispatched.append(request.data)
                    barrier.wait()
                    if position == 0:
                        raise HTTPError(request.full_url, status, "authorization", {}, io.BytesIO())
                    if not observed.wait(5):
                        raise AssertionError("authorization outcome was not collected")
                    return judge_response()

                with mock.patch("grade.urlopen", side_effect=transport), mock.patch.object(JSONLedger, "append", append), \
                     self.assertRaisesRegex(RuntimeError, "authorization"):
                    grade.judge(args)
                self.assertEqual(len(dispatched), 3)
                events, rows = read_jsonl(Path(args.usage)), read_jsonl(Path(args.out))
                self.assertEqual(sum(row["event"] == "response" for row in events), 3)
                self.assertEqual(len(rows), 3)
                auth = next(row for row in rows if row.get("http_status") == status)
                self.assertEqual(auth["failure_kind"], "authorization_abort")
                self.assertTrue(auth["usage_unknown"])
                self.assertNotIn("NEVER_RETAIN", Path(args.out).read_text())
                with mock.patch("grade.urlopen", side_effect=AssertionError("resume must not send")):
                    with self.assertRaisesRegex(RuntimeError, "authorization"):
                        grade.judge(args)
                self.root = previous

    def test_judge_http_500_does_not_globally_stop_other_cases(self):
        args = self.judge_args(count=3, concurrency=1)
        with mock.patch("grade.urlopen", side_effect=[HTTPError("https://offline.invalid", 500, "temporary", {}, io.BytesIO()),
                                                      judge_response(), judge_response()]) as transport:
            grade.judge(args)
        self.assertEqual(transport.call_count, 3)
        self.assertEqual([row["status"] for row in read_jsonl(Path(args.out))], ["error", "ok", "ok"])

    def test_judge_resume_cannot_hide_real_403_by_editing_only_usage_status(self):
        # The initial repair trusted only usage.http_status: changing that 403 to
        # 500 hid grade.http_status=403 and incorrectly admitted the next case.
        args = self.judge_args(count=2, concurrency=1)
        with mock.patch("grade.urlopen", side_effect=HTTPError("https://offline.invalid", 403, "auth", {}, io.BytesIO())), \
             self.assertRaisesRegex(RuntimeError, "authorization"):
            grade.judge(args)
        events = read_jsonl(Path(args.usage))
        self.assertEqual(events[-1]["http_status"], 403)
        self.assertEqual(read_jsonl(Path(args.out))[0]["http_status"], 403)
        events[-1]["http_status"] = 500
        Path(args.usage).write_text("".join(json.dumps(row) + "\n" for row in events))
        before = Path(args.usage).read_bytes()
        with mock.patch("grade.urlopen", side_effect=AssertionError("contradictory auth must never send")) as transport, \
             self.assertRaises(LedgerIntegrityError):
            grade.judge(args)
        self.assertEqual(transport.call_count, 0)
        self.assertEqual(Path(args.usage).read_bytes(), before)

    def test_judge_replaced_result_ledger_before_admission_sends_nothing(self):
        args = self.judge_args()
        original_verify = JSONLedger.verify
        replaced = False

        def verify(ledger):
            nonlocal replaced
            if ledger.path == Path(args.out) and not replaced:
                replaced = True
                ledger.path.rename(self.root / "original-grades.jsonl")
                ledger.path.write_bytes(b"replacement\n")
            return original_verify(ledger)

        with mock.patch.object(JSONLedger, "verify", verify), \
             mock.patch("grade.urlopen", side_effect=AssertionError("HTTP forbidden")) as transport, \
             self.assertRaises(LedgerIntegrityError):
            grade.judge(args)
        self.assertEqual(transport.call_count, 0)
        self.assertEqual(Path(args.out).read_bytes(), b"replacement\n")
        self.assertEqual(read_jsonl(Path(args.usage)), [])

    def test_judge_response_write_failure_collects_all_inflight_and_retains_every_record(self):
        # Previously the first failed append escaped the loop; other paid responses vanished.
        for failing_name in ("usage.jsonl", "grades.jsonl"):
            with self.subTest(ledger=failing_name), tempfile.TemporaryDirectory(dir=self.root) as directory:
                previous = self.root
                self.root = Path(directory)
                args = self.judge_args()
                barrier = threading.Barrier(3, timeout=5)
                original_append = JSONLedger.append
                changed = False

                def transport(request, timeout):
                    barrier.wait()
                    return judge_response()

                def append(ledger, row):
                    nonlocal changed
                    is_response = row.get("event") == "response" if ledger.path.name == "usage.jsonl" else True
                    if ledger.path.name == failing_name and is_response and not changed:
                        changed = True
                        ledger.path.rename(self.root / (failing_name + ".original"))
                        ledger.path.write_bytes(b"replacement must survive\n")
                    return original_append(ledger, row)

                with mock.patch("grade.urlopen", side_effect=transport) as transport_mock, \
                     mock.patch.object(JSONLedger, "append", append), self.assertRaises(LedgerIntegrityError):
                    grade.judge(args)
                self.assertEqual(transport_mock.call_count, 3)
                failed_path = self.root / failing_name
                self.assertEqual(failed_path.read_bytes(), b"replacement must survive\n")
                receipts = [json.loads(path.read_text()) for path in self.root.glob(Path(failing_name).stem + "-ledger-failure-*.json")]
                records = [row["record"] for row in receipts if row["record"] is not None]
                self.assertEqual(len(records), 3)
                self.assertEqual(len({row["request_id"] for row in records}), 3)
                other = Path(args.out) if failing_name == "usage.jsonl" else Path(args.usage)
                good_rows = read_jsonl(other)
                self.assertEqual(sum(row.get("event", "response") == "response" for row in good_rows), 3)
                self.assertTrue(all(row.get("usage", {}).get("total_tokens") == 19 for row in records))
                self.root = previous

    def test_judge_partial_request_append_fails_before_http_and_preserves_receipt(self):
        args = self.judge_args()
        original_write = os.write
        partial = False

        def write(descriptor, data):
            nonlocal partial
            if os.readlink(f"/proc/self/fd/{descriptor}") == args.usage:
                if partial:
                    raise OSError("injected partial request append")
                partial = True
                return original_write(descriptor, data[:6])
            return original_write(descriptor, data)

        with mock.patch("os.write", side_effect=write), \
             mock.patch("grade.urlopen", side_effect=AssertionError("HTTP forbidden")) as transport, \
             self.assertRaises(LedgerIntegrityError):
            grade.judge(args)
        self.assertEqual(transport.call_count, 0)
        self.assertEqual(len(Path(args.usage).read_bytes()), 6)
        receipts = [json.loads(path.read_text()) for path in self.root.glob("usage-ledger-failure-*.json")]
        request = next(row for row in receipts if row["record"] is not None)
        self.assertEqual(request["record"]["event"], "request")
        self.assertFalse(request["pre_request_persistence_verified"])
        with mock.patch("grade.urlopen", side_effect=AssertionError("HTTP forbidden")), self.assertRaises(LedgerIntegrityError):
            grade.judge(args)

    def test_judge_interrupted_request_is_reserved_unknown_and_never_resent(self):
        args = self.judge_args(count=3, concurrency=1)
        args.limit = 1
        with mock.patch("grade.urlopen", return_value=judge_response()):
            grade.judge(args)
        request = read_jsonl(Path(args.usage))[0]
        # Model a crash after durable request admission, before any response receipt.
        Path(args.usage).write_text(json.dumps(request) + "\n")
        Path(args.out).write_text("")
        args.limit = 0
        sent = []
        with mock.patch("grade.urlopen", side_effect=lambda req, timeout: sent.append(req.data) or judge_response()):
            grade.judge(args)
        self.assertEqual(len(sent), 2)
        self.assertTrue(all('"blind_id": "b0"' not in data.decode() for data in sent))
        status = json.loads(Path(args.out + ".run-status.json").read_text())
        self.assertEqual(status["interrupted_request_ids"], [request["request_id"]])
        self.assertEqual(status["accounted_tokens"], request["reserved_tokens"] + 38)
        self.assertEqual(read_jsonl(Path(args.usage))[0], request)

    def test_judge_resume_rejects_partial_or_misattributed_or_missing_grade(self):
        for damage in ("partial", "wrong identity", "wrong config", "missing grade", "duplicate response", "orphan response",
                       "usage mismatch", "unknown mismatch", "accounting mismatch", "estimate mismatch", "hash mismatch",
                       "invalid usage type", "unknown reservation mismatch"):
            with self.subTest(damage=damage), tempfile.TemporaryDirectory(dir=self.root) as directory:
                previous = self.root
                self.root = Path(directory)
                args = self.judge_args(count=2, concurrency=1)
                args.limit = 1
                with mock.patch("grade.urlopen", return_value=judge_response()):
                    grade.judge(args)
                events = read_jsonl(Path(args.usage))
                if damage == "partial":
                    with Path(args.usage).open("ab") as stream:
                        stream.write(b'{"unfinished":')
                elif damage == "wrong identity":
                    events[0]["blind_input_sha256"] = "wrong"
                    Path(args.usage).write_text("".join(json.dumps(row) + "\n" for row in events))
                elif damage == "wrong config":
                    args.model = "changed-model"
                elif damage == "missing grade":
                    Path(args.out).write_text("")
                elif damage == "orphan response":
                    Path(args.usage).write_text(json.dumps(events[-1]) + "\n")
                elif damage in ("usage mismatch", "unknown mismatch", "hash mismatch"):
                    rows = read_jsonl(Path(args.out))
                    if damage == "usage mismatch":
                        rows[0]["usage"]["total_tokens"] += 1
                    elif damage == "unknown mismatch":
                        rows[0]["usage_unknown"] = True
                    else:
                        rows[0]["response_sha256"] = "changed"
                    Path(args.out).write_text(json.dumps(rows[0]) + "\n")
                elif damage in ("accounting mismatch", "estimate mismatch", "invalid usage type", "unknown reservation mismatch"):
                    if damage == "accounting mismatch":
                        events[-1]["accounted_tokens"] += 1
                    elif damage == "estimate mismatch":
                        events[-1]["usage_is_estimate"] = True
                    elif damage == "invalid usage type":
                        events[-1]["usage"] = []
                    else:
                        events[-1].update(usage={}, usage_unknown=True, usage_is_estimate=True, accounted_tokens=0)
                    Path(args.usage).write_text("".join(json.dumps(row) + "\n" for row in events))
                else:
                    with Path(args.usage).open("a") as stream:
                        stream.write(json.dumps(events[-1]) + "\n")
                before = Path(args.usage).read_bytes()
                with mock.patch("grade.urlopen", side_effect=AssertionError("HTTP forbidden")) as transport, \
                     self.assertRaises(LedgerIntegrityError):
                    grade.judge(args)
                self.assertEqual(transport.call_count, 0)
                self.assertEqual(Path(args.usage).read_bytes(), before)
                self.root = previous

    def test_resume_failure_receipts_and_symlink_are_not_silently_accepted(self):
        target = self.root / "ledger.jsonl"
        target.write_text("{}\n")
        (self.root / "ledger-ledger-failure-fixture.json").write_text("{}\n")
        with self.assertRaises(LedgerIntegrityError):
            JSONLedger(target, resume=True)
        link = self.root / "linked.jsonl"
        link.symlink_to(target)
        with self.assertRaises(OSError):
            JSONLedger(link, resume=True)


if __name__ == "__main__":
    unittest.main()
