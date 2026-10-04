"""Offline acceptance tests for native tool calls; never contacts a provider."""
from __future__ import annotations

import copy
import dataclasses
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from agent_eval import load_solver_questions
from agent_eval_native import (
    CallBudget,
    EvalConfig,
    JSONLedger,
    NativeDeepSeekClient,
    evaluate_question,
    parse_tool_call,
    parse_tool_calls,
    tool_choice_for_turn,
)
from retrieval import evidence_budget, json_text, normalized_block


BLOCK = {
    "id": "fixture-evidence",
    "title": "source section",
    "text": "Source-grounded detail. " * 200,
    "kind": "original_text",
    "provenance": "original_source",
    "sources": [{"path": "guide.md", "start_line": 3, "end_line": 7}],
}


def tool_message(name="search", args=None, call_id="call-1"):
    if args is None:
        args = {"query": "keyword"}
    return {
        "role": "assistant",
        "content": None,
        "tool_calls": [{
            "id": call_id,
            "type": "function",
            "function": {"name": name, "arguments": json_text(args)},
        }],
    }


def tool_batch(*messages):
    return {"role": "assistant", "content": None,
            "tool_calls": [call for message in messages for call in message["tool_calls"]]}


class FakeEngine:
    def __init__(self):
        self.calls = []

    def search(self, query, max_chars):
        self.calls.append((query, max_chars))
        return {"status": "ok", "query": query, **evidence_budget([BLOCK], max_chars),
                "diagnostics": {}, "latency_ms": 0}


class FakeClient:
    def __init__(self, responses):
        self.responses = iter(copy.deepcopy(responses))
        self.calls = []

    def complete(self, messages, context, tool_choice):
        self.calls.append({"messages": copy.deepcopy(messages), "context": copy.deepcopy(context),
                           "tool_choice": copy.deepcopy(tool_choice)})
        return next(self.responses), [{"http_attempt": len(self.calls), "attempt_for_turn": 1,
                                      "status": "ok", "usage": {"prompt_tokens": 7, "completion_tokens": 3}}]


class MockResponse:
    status = 200

    def __init__(self, payload):
        self.payload = payload

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False

    def read(self):
        return json_text(self.payload).encode()


def provider_response(finish="tool_calls", message=None):
    message = copy.deepcopy(message if message is not None else tool_message())
    message["reasoning_content"] = "DO_NOT_STORE_REASONING"
    return {
        "id": "response-fixture", "model": "deepseek-flash",
        "usage": {"prompt_tokens": 11, "completion_tokens": 13, "total_tokens": 24},
        "choices": [{"finish_reason": finish, "message": message}],
    }


class NativeHarnessTests(unittest.TestCase):
    def setUp(self):
        # A missed HTTP mock must fail locally, never consume a real request.
        self.no_network = mock.patch("socket.create_connection", side_effect=AssertionError("network disabled in native harness tests"))
        self.no_network.start()
        self.addCleanup(self.no_network.stop)

    def test_forced_choices_and_three_search_tool_role_roundtrip(self):
        config = EvalConfig()
        expected_search = {"type": "function", "function": {"name": "search"}}
        expected_submit = {"type": "function", "function": {"name": "submit_answer"}}
        expected_choices = [expected_search, "required", "required", expected_submit]
        self.assertEqual([tool_choice_for_turn(i, config) for i in range(1, 5)], expected_choices)
        citation = normalized_block(BLOCK)["id"]
        responses = [tool_message(args={"query": f"term-{i}"}, call_id=f"call-{i}") for i in range(1, 4)]
        responses.append(tool_message("submit_answer", {"answer": "Supported answer", "citations": [citation]}, "call-4"))
        client, engine = FakeClient(responses), FakeEngine()
        result = evaluate_question("q1", "What is supported?", "graph", engine, client, config)
        self.assertEqual(result["status"], "ok", result)
        self.assertEqual(result["answer"], "Supported answer")
        self.assertEqual(result["citations"], [citation])
        self.assertEqual(len(engine.calls), 3)
        self.assertEqual([c["tool_choice"] for c in client.calls], expected_choices)
        final_messages = client.calls[-1]["messages"]
        tool_replies = [message for message in final_messages if message["role"] == "tool"]
        self.assertEqual([reply["tool_call_id"] for reply in tool_replies], ["call-1", "call-2", "call-3"])
        for i, message in enumerate(final_messages):
            if message["role"] == "assistant" and message.get("tool_calls"):
                self.assertEqual(final_messages[i + 1]["role"], "tool")
                self.assertEqual(final_messages[i + 1]["tool_call_id"], message["tool_calls"][0]["id"])
                self.assertNotIn("reasoning_content", message)
        self.assertLessEqual(sum(s["visible_chars"] for s in result["searches"]), config.total_context_chars)
        self.assertTrue(all(s["visible_chars"] <= config.per_search_chars for s in result["searches"]))

    def test_exact_tool_evidence_budgets_and_invalid_citations(self):
        config = dataclasses.replace(EvalConfig(), per_search_chars=700, total_context_chars=1600)
        replies = [tool_message(args={"query": f"term-{i}"}, call_id=f"call-{i}") for i in range(3)]
        replies.append(tool_message("submit_answer", {"answer": "Cannot confirm everything", "citations": ["unseen-evidence"]}, "final"))
        client = FakeClient(replies)
        result = evaluate_question("q2", "A question", "fts", FakeEngine(), client, config)
        self.assertEqual(result["status"], "ok", result)
        self.assertEqual(result["invalid_citations"], ["unseen-evidence"])
        searches = result["searches"]
        self.assertEqual(len(searches), 3)
        self.assertLessEqual(sum(s["visible_chars"] for s in searches), config.total_context_chars)
        self.assertEqual(result["budgets"]["visible_context_chars_used"], sum(s["visible_chars"] for s in searches))
        evidence_messages = [m for m in client.calls[-1]["messages"] if m["role"] == "tool"]
        # Check the exact serialized content, not only a self-reported counter.
        for search, message in zip(searches, evidence_messages):
            if search.get("executed"):
                self.assertEqual(search["visible_chars"], len(message["content"]))
                self.assertLessEqual(len(message["content"]), config.per_search_chars)

    def test_parser_requires_one_known_function_with_strict_arguments(self):
        parsed = parse_tool_call(tool_message())
        self.assertEqual(parsed["id"], "call-1")
        self.assertEqual(parsed["type"], "function")
        self.assertEqual(json.loads(parsed["function"]["arguments"]), {"query": "keyword"})
        invalid = [
            {"role": "assistant", "content": "No tool call"},
            {"role": "assistant", "content": None, "tool_calls": []},
            {"role": "assistant", "tool_calls": tool_message()["tool_calls"] * 2},
            tool_message("shell", {"query": "keyword"}),
            tool_message(args={"query": " "}),
            tool_message(args={"query": 1}),
            tool_message(args={"query": "keyword", "unexpected": True}),
            tool_message("submit_answer", {"answer": "", "citations": []}),
            tool_message("submit_answer", {"answer": "answer", "citations": [1]}),
            tool_message("submit_answer", {"answer": "answer", "citations": [], "gold": "hidden"}),
            tool_message(call_id=""),
        ]
        for arguments in ('{"query":"first","query":"last"}', '{"query":NaN}', '[]', 'null'):
            message = tool_message()
            message["tool_calls"][0]["function"]["arguments"] = arguments
            invalid.append(message)
        non_function = tool_message()
        non_function["tool_calls"][0]["type"] = "custom"
        invalid.append(non_function)
        for message in invalid:
            with self.subTest(message=message):
                with self.assertRaises((ValueError, TypeError)):
                    parse_tool_call(message)

    def test_no_call_multiple_calls_and_early_submit_fail_without_retry(self):
        malformed = [
            {"role": "assistant", "content": "answer", "tool_calls": []},
            {"role": "assistant", "tool_calls": tool_message()["tool_calls"] * 2},
            tool_message("submit_answer", {"answer": "Too early", "citations": []}),
        ]
        for message in malformed:
            with self.subTest(message=message):
                client, engine = FakeClient([message]), FakeEngine()
                result = evaluate_question("q3", "Question", "raw", engine, client, EvalConfig())
                self.assertEqual(result["status"], "error", result)
                self.assertEqual(len(client.calls), 1)
                self.assertEqual(len(engine.calls), 0)

    def test_last_forced_submit_cannot_execute_fourth_search(self):
        client = FakeClient([tool_message(call_id=f"call-{i}") for i in range(4)])
        engine = FakeEngine()
        result = evaluate_question("q4", "Question", "graph", engine, client, EvalConfig())
        self.assertEqual(result["status"], "error", result)
        self.assertEqual(len(engine.calls), 3)
        self.assertEqual(len(client.calls), 4)

    def test_batched_searches_match_every_id_and_count_overflow_error_chars(self):
        config = dataclasses.replace(EvalConfig(), per_search_chars=700, total_context_chars=1600)
        batch = tool_batch(*[tool_message(args={"query": f"term-{i}"}, call_id=f"batch-{i}") for i in range(5)])
        client = FakeClient([batch, tool_message("submit_answer", {"answer": "Grounded answer", "citations": []}, "final")])
        engine = FakeEngine()
        result = evaluate_question("batch", "Question", "graph", engine, client, config)
        self.assertEqual(result["status"], "ok", result)
        self.assertEqual([query for query, _ in engine.calls], ["term-0", "term-1", "term-2"])
        self.assertEqual(len(result["searches"]), 5)
        self.assertEqual([s["executed"] for s in result["searches"]], [True, True, True, False, False])
        self.assertEqual(client.calls[1]["tool_choice"], {"type": "function", "function": {"name": "submit_answer"}})
        replies = [m for m in client.calls[1]["messages"] if m["role"] == "tool"]
        self.assertEqual([m["tool_call_id"] for m in replies], [f"batch-{i}" for i in range(5)])
        for search, reply in zip(result["searches"], replies):
            self.assertEqual(search["tool_call_id"], reply["tool_call_id"])
            self.assertEqual(search["visible_chars"], len(reply["content"]))
            self.assertLessEqual(len(reply["content"]), config.per_search_chars)
            if not search["executed"]:
                self.assertEqual(json.loads(reply["content"]), {"error": "budget_exhausted"})
        actual_chars = sum(len(m["content"]) for m in replies)
        self.assertEqual(result["budgets"]["visible_context_chars_used"], actual_chars)
        self.assertLessEqual(actual_chars, config.total_context_chars)
        self.assertEqual(result["budgets"]["executed_searches"], 3)
        self.assertEqual(result["budgets"]["search_tool_calls"], 5)

    def test_batch_context_exhaustion_forces_early_submit_and_meters_all_replies(self):
        config = dataclasses.replace(EvalConfig(), per_search_chars=500, total_context_chars=530)
        batch = tool_batch(*[tool_message(call_id=f"context-{i}") for i in range(3)])
        client = FakeClient([batch, tool_message("submit_answer", {"answer": "Partial evidence", "citations": []}, "final")])
        engine = FakeEngine()
        result = evaluate_question("context", "Question", "fts", engine, client, config)
        self.assertEqual(result["status"], "ok", result)
        self.assertEqual(len(engine.calls), 1)
        self.assertEqual([s["executed"] for s in result["searches"]], [True, False, False])
        self.assertEqual(client.calls[1]["tool_choice"]["function"]["name"], "submit_answer")
        actual_chars = sum(len(m["content"]) for m in client.calls[1]["messages"] if m["role"] == "tool")
        self.assertEqual(result["budgets"]["visible_context_chars_used"], actual_chars)
        self.assertLessEqual(actual_chars, config.total_context_chars)

    def test_mixed_multiple_submit_and_invalid_batch_fail_atomically(self):
        search = tool_message()
        final = tool_message("submit_answer", {"answer": "Answer", "citations": []}, "submit-1")
        other_final = tool_message("submit_answer", {"answer": "Answer", "citations": []}, "submit-2")
        invalid_batches = [tool_batch(search, final), tool_batch(final, search), tool_batch(final, other_final),
                           tool_batch(search, tool_message(args={"query": " "}, call_id="bad-query"))]
        for message in invalid_batches:
            with self.subTest(message=message):
                with self.assertRaises(ValueError):
                    parse_tool_calls(message)
                client, engine = FakeClient([message]), FakeEngine()
                result = evaluate_question("mixed", "Question", "raw", engine, client, EvalConfig())
                self.assertEqual(result["status"], "error", result)
                self.assertEqual(engine.calls, [])
                self.assertEqual(len(client.calls), 1)

    def test_impossible_tool_error_batch_fails_without_discarding_a_prefix(self):
        config = dataclasses.replace(EvalConfig(), total_context_chars=50)
        batch = tool_batch(*[tool_message(call_id=f"impossible-{i}") for i in range(3)])
        client, engine = FakeClient([batch]), FakeEngine()
        result = evaluate_question("impossible", "Question", "raw", engine, client, config)
        self.assertEqual(result["status"], "error", result)
        self.assertIn("mandatory error replies", result["error"])
        self.assertEqual(engine.calls, [])
        self.assertEqual(result["searches"], [])

    def test_forced_submit_ignored_searches_get_matched_errors_without_execution(self):
        initial = tool_batch(*[tool_message(call_id=f"initial-{i}") for i in range(3)])
        ignored = tool_batch(tool_message(call_id="ignored-1"), tool_message(call_id="ignored-2"))
        client = FakeClient([initial, ignored, tool_message("submit_answer", {"answer": "Answer", "citations": []}, "final")])
        engine = FakeEngine()
        result = evaluate_question("ignored", "Question", "graph", engine, client, EvalConfig())
        self.assertEqual(result["status"], "ok", result)
        self.assertEqual(len(engine.calls), 3)
        self.assertEqual([s["status"] for s in result["searches"][-2:]], ["final_answer_required"] * 2)
        for call in client.calls[1:]:
            self.assertEqual(call["tool_choice"]["function"]["name"], "submit_answer")
        replies = [m for m in client.calls[-1]["messages"] if m["role"] == "tool"]
        self.assertEqual([m["tool_call_id"] for m in replies][-2:], ["ignored-1", "ignored-2"])

    def test_gold_projection_and_identical_initial_messages_across_arms(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "questions.json"
            path.write_text(json.dumps([{"id": "q5", "question": "Visible question", "gold": "SECRET_GOLD_MARKER",
                                         "required_facts": ["SECRET_FACT_MARKER"], "source_evidence": "SECRET_EVIDENCE_MARKER"}]))
            questions = load_solver_questions(path)
        self.assertEqual(questions, [{"question_id": "q5", "question": "Visible question"}])
        initial = []
        for arm in ("raw", "fts", "graph"):
            client = FakeClient([tool_message(), tool_message("submit_answer", {"answer": "Insufficient evidence", "citations": []}, "final")])
            q = questions[0]
            result = evaluate_question(q["question_id"], q["question"], arm, FakeEngine(), client, EvalConfig())
            self.assertEqual(result["status"], "ok", result)
            initial.append(client.calls[0]["messages"])
            all_messages = json_text([call["messages"] for call in client.calls])
            for marker in ("SECRET_GOLD_MARKER", "SECRET_FACT_MARKER", "SECRET_EVIDENCE_MARKER"):
                self.assertNotIn(marker, all_messages)
        self.assertEqual(initial[0], initial[1])
        self.assertEqual(initial[1], initial[2])

    def test_native_http_schema_durable_meter_retry_usage_and_no_reasoning(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "requests.jsonl"
            ledger = JSONLedger(path)
            self.addCleanup(ledger.close)
            client = NativeDeepSeekClient(EvalConfig(), "test-native-credential", CallBudget(5), ledger)
            responses = iter([provider_response("length"), provider_response("tool_calls")])
            captured = []

            def open_request(request, timeout):
                records = [json.loads(line) for line in path.read_text().splitlines()]
                self.assertEqual(records[-1]["event"], "request")
                self.assertEqual(records[-1]["status"], "in_flight")
                self.assertEqual(records[-1]["request_sha256"], hashlib.sha256(request.data).hexdigest())
                payload = json.loads(request.data)
                self.assertNotIn("response_format", payload)
                self.assertEqual(payload["tool_choice"], {"type": "function", "function": {"name": "search"}})
                names = {tool["function"]["name"] for tool in payload["tools"]}
                self.assertEqual(names, {"search", "submit_answer"})
                captured.append(payload)
                return MockResponse(next(responses))

            with mock.patch("urllib.request.urlopen", side_effect=open_request):
                message, attempts = client.complete([{"role": "user", "content": "question"}],
                    {"question_id": "q6", "arm": "fts", "turn": 1}, tool_choice_for_turn(1, EvalConfig()))
            self.assertEqual(len(captured), 2)
            self.assertEqual([attempt["status"] for attempt in attempts], ["error", "ok"])
            self.assertEqual(sum(attempt["usage"]["completion_tokens"] for attempt in attempts), 26)
            self.assertNotIn("reasoning_content", message)
            parse_tool_call(message)
            text = path.read_text()
            records = [json.loads(line) for line in text.splitlines()]
            self.assertEqual([record["event"] for record in records], ["request", "response", "request", "response"])
            self.assertNotIn("test-native-credential", text)
            self.assertNotIn("DO_NOT_STORE_REASONING", text)

    def test_stop_without_tools_is_protocol_error_not_http_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger = JSONLedger(Path(directory) / "requests.jsonl")
            self.addCleanup(ledger.close)
            client = NativeDeepSeekClient(EvalConfig(), "test-native-credential", CallBudget(5), ledger)
            payload = provider_response("stop", {"role": "assistant", "content": "plain answer", "tool_calls": []})
            with mock.patch("urllib.request.urlopen", return_value=MockResponse(payload)) as request:
                result = evaluate_question("q7", "Question", "raw", FakeEngine(), client, EvalConfig())
            self.assertEqual(request.call_count, 1)
            self.assertEqual(result["status"], "error", result)
            self.assertEqual(len(result["model_calls"]), 1)
            self.assertEqual(result["model_calls"][0]["status"], "ok")


if __name__ == "__main__":
    unittest.main()
