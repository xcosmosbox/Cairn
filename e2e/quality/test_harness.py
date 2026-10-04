import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from agent_eval import (CallBudget, DeepSeekClient, EvalConfig, JSONLedger,
                        evaluate_question, load_solver_questions, parse_action)
from retrieval import RetrievalEngine, build_raw_index, evidence_budget, json_text


class HarnessTests(unittest.TestCase):
    def test_raw_literal_chinese_substring_and_fixed_or_ranking(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            sources = []
            for name, text in [("a.md", "# 运行说明\n任务出现反压与积压问题时检查算子。反压再次出现。"),
                               ("b.md", "# 指标\n检查积压。")]:
                (root / name).write_text(text)
                sources.append({"path": name, "source_sha256": hashlib.sha256(text.encode()).hexdigest()})
            proof = root / "proof.json"
            proof.write_text(json.dumps({"accepted_sources": sources}))
            database = root / "raw.db"
            build_raw_index(root, proof, database)
            found = RetrievalEngine("raw", raw_db=database).search("反压 积压", 10000)
            self.assertEqual(found["status"], "ok")
            self.assertEqual(found["evidence"][0]["path"], "a.md")
            self.assertEqual(len(found["evidence"]), 2)
            self.assertTrue(all(block["id"].startswith("e:") for block in found["evidence"]))

    def test_budget_counts_metadata_and_json_not_only_text(self):
        blocks = [{"id": str(i), "name": "名称" * 15, "body": "证据" * 1000,
                   "sources": [{"path": "nested/path.md", "start_line": 1, "end_line": 80}]} for i in range(12)]
        limited = evidence_budget(blocks, 500)
        self.assertLessEqual(len(limited["serialized"]), 500)
        self.assertEqual(limited["visible_chars"], len(limited["serialized"]))
        self.assertTrue(limited["truncated"])
        self.assertEqual(len(limited["evidence"]), 1)
        self.assertTrue(limited["evidence"][0]["text_truncated"])

    def test_gold_is_not_projected_or_forwarded(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "dataset.json"
            path.write_text(json.dumps([{"id": "q1", "question": "如何查询？", "expected_answer": "GOLD_SENTINEL",
                                         "required_facts": ["GOLD_SENTINEL"], "evidence": ["GOLD_SENTINEL"]}]))
            question = load_solver_questions(path)[0]
            self.assertEqual(set(question), {"question_id", "question"})
            class FakeEngine:
                def search(self, query, max_chars):
                    limited = evidence_budget([{"id": "node1", "body": "先查看状态。", "path": "a.md"}], max_chars)
                    return {"status": "ok", "query": query, "latency_ms": 0, **limited}
            class FakeClient:
                def __init__(self): self.calls = 0
                def complete(inner, messages, context):
                    self.assertNotIn("GOLD_SENTINEL", json_text(messages))
                    inner.calls += 1
                    action = {"action": "search", "query": "状态"} if inner.calls == 1 else {"action": "final", "answer": "先查看状态。", "citations": []}
                    return json_text(action), []
            result = evaluate_question(question["question_id"], question["question"], "raw", FakeEngine(), FakeClient(), EvalConfig())
            self.assertEqual(result["status"], "ok")
            self.assertEqual(len(result["searches"]), 1)
            self.assertLessEqual(result["budgets"]["visible_context_chars_used"], 20000)

    def test_request_is_durable_before_http_and_length_retry_is_counted(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "requests.jsonl"
            ledger = JSONLedger(path)
            client = DeepSeekClient(EvalConfig(), "test-credential", CallBudget(5), ledger)
            count = 0
            class Response:
                status = 200
                def __init__(self, data): self.data = data
                def __enter__(self): return self
                def __exit__(self, *args): pass
                def read(self): return json.dumps(self.data).encode()
            def mocked_http(*args, **kwargs):
                nonlocal count
                count += 1
                lines = [json.loads(line) for line in path.read_text().splitlines()]
                self.assertEqual(lines[-1]["event"], "request")
                self.assertEqual(lines[-1]["http_attempt"], count)
                return Response({"choices": [{"finish_reason": "length" if count == 1 else "stop",
                    "message": {"content": '{"action":"search","query":"test"}', "reasoning_content": "DO_NOT_STORE_REASONING"}}],
                    "usage": {"prompt_tokens": 10, "completion_tokens": 12}})
            with mock.patch("urllib.request.urlopen", side_effect=mocked_http):
                _, attempts = client.complete([{"role": "user", "content": "question"}], {"question_id": "q", "arm": "raw", "turn": 1})
            ledger.close()
            text = path.read_text()
            records = [json.loads(line) for line in text.splitlines()]
            self.assertEqual([r["event"] for r in records], ["request", "response", "request", "response"])
            self.assertEqual([a["status"] for a in attempts], ["error", "ok"])
            self.assertEqual(sum(a["usage"]["completion_tokens"] for a in attempts), 24)
            self.assertNotIn("test-credential", text)
            self.assertNotIn("DO_NOT_STORE_REASONING", text)

    def test_protocol_rejects_duplicate_keys_and_extra_fields(self):
        with self.assertRaises(ValueError):
            parse_action('{"action":"search","action":"final","query":"x"}')
        with self.assertRaises(ValueError):
            parse_action('{"action":"search","query":"x","gold":"secret"}')


if __name__ == "__main__":
    unittest.main()
