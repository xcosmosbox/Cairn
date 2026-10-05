import unittest

from agent_eval import EvalConfig
from retrieval import evidence_budget, json_text
from retrieval_round2 import query_records, replay_trace, source_metrics, summarize_group


class FixtureRetrieval:
    """Offline unit fixture only; no claim of a real solver/paid test."""

    def __init__(self, text_length=20, fail_first=False):
        self.queries = []
        self.text_length = text_length
        self.fail_first = fail_first

    def search(self, query, max_chars=10000):
        self.queries.append(query)
        if self.fail_first and len(self.queries) == 1:
            shown = json_text({"error": "deliberate retrieval fixture error", "evidence": []})
            return {"status": "error", "query": query, "error": "deliberate retrieval fixture error",
                    "serialized": shown, "visible_chars": len(shown), "evidence": [], "truncated": False}
        limited = evidence_budget([{"id": query, "text": "证" * self.text_length,
                                    "sources": [{"path": "gold.md", "start_line": 1, "end_line": 10}]}], max_chars, 10)
        return {"status": "ok", "query": query, "diagnostics": {"returned_blocks": 1, "fts_seeds": []}, **limited}


def trace(turns=(1, 1, 1, 1)):
    return {"trace_id": "m02/fts", "question_id": "m02", "question": "frozen question", "query_origin_arm": "fts",
            "events": [{"event_id": f"m02/fts/{index}", "query": f"q{index}", "search_index": index,
                        "model_turn": turn, "tool_call_id": f"original_call_{index}",
                        "historical_status": "ok" if index < 4 else "search_budget_exhausted",
                        "include_in_query_metrics": index < 4} for index, turn in enumerate(turns, 1)]}


class Round2ReplayTests(unittest.TestCase):
    def test_native_batch_preserves_denied_reply_but_never_executes_or_counts_it(self):
        engine = FixtureRetrieval(fail_first=True)
        result = replay_trace(trace(), "literal", "fts", engine, EvalConfig())
        self.assertEqual(engine.queries, ["q1", "q2", "q3"])
        self.assertEqual(len(result["searches"]), 4)
        self.assertEqual(result["searches"][0]["status"], "error")
        self.assertEqual(result["searches"][3]["status"], "search_budget_exhausted")
        self.assertFalse(result["searches"][3]["executed"])
        self.assertEqual(result["tool_context"][3]["content"], '{"error":"budget_exhausted"}')
        rows = query_records(result, {"evidence": [{"path": "gold.md", "start_line": 7, "end_line": 8}]})
        self.assertEqual(len(rows), 3)
        self.assertEqual(sum(row["status"] == "error" for row in rows), 1)
        self.assertEqual(sum(len(item["content"]) for item in result["tool_context"]),
                         result["budgets"]["visible_context_chars_used"])
        self.assertEqual(result["actual_token_usage"], 0)

    def test_native_context_exhaustion_preserves_all_query_denominators(self):
        engine = FixtureRetrieval(text_length=30000)
        result = replay_trace(trace(), "han-v1", "graph", engine, EvalConfig())
        self.assertLessEqual(result["budgets"]["visible_context_chars_used"], 20000)
        self.assertEqual(engine.queries, ["q1", "q2"])
        self.assertEqual(result["searches"][2]["status"], "context_budget_exhausted")
        self.assertTrue(result["searches"][0]["truncated"])
        self.assertTrue(all(len(item["content"]) <= 10000 for item in result["tool_context"]))
        rows = query_records(result, {"evidence": []})
        summary = summarize_group(rows)
        self.assertEqual(summary["fixed_query_events"], 3)
        self.assertEqual(summary["executed"], 2)
        self.assertEqual(summary["budget_or_trace_denied"], 1)
        self.assertEqual(summary["empty_successes"], 0)

    def test_later_batch_cannot_fit_is_preserved_as_trace_failure(self):
        engine = FixtureRetrieval(text_length=30000)
        result = replay_trace(trace((1, 1, 2, 2)), "trigram", "graph", engine, EvalConfig())
        self.assertEqual(engine.queries, ["q1", "q2"])
        self.assertEqual(result["native_trace_status"], "error")
        self.assertIn("mandatory error replies", result["native_trace_error"])
        self.assertEqual(len(result["unshown_events"]), 2)
        self.assertEqual(len(query_records(result, {"evidence": []})), 3)

    def test_source_overlap_is_line_sensitive_and_counts_rank_not_claim_quality(self):
        blocks = [{"sources": [{"path": "gold.md", "start_line": 10, "end_line": 15}]},
                  {"sources": [{"path": "gold.md", "start_line": 2, "end_line": 7}]}]
        metrics = source_metrics(blocks, [{"path": "gold.md", "start_line": 5, "end_line": 8}])
        self.assertEqual(metrics["source_path_mrr_at10"], 1)
        self.assertEqual(metrics["source_path_line_mrr_at10"], .5)
        self.assertEqual(source_metrics([{"path": "gold.md"}], [{"path": "gold.md", "start_line": 5, "end_line": 8}])["source_path_line_overlap_at10"], 0)

    def test_backend_nonempty_is_distinct_from_zero_visible_evidence(self):
        result = {"factor": "literal", "arm": "fts", "trace_id": "m02/fts", "question_id": "m02", "query_origin_arm": "fts",
                  "searches": [{"include_in_query_metrics": True, "event_id": "m02/fts/1", "search_index": 1,
                                "query": "q", "status": "ok", "executed": True, "visible_chars": 20,
                                "evidence": [], "diagnostics": {"returned_blocks": 2}}], "unshown_events": []}
        rows = query_records(result, {"evidence": []})
        summary = summarize_group(rows)
        self.assertEqual(summary["empty_successes"], 0)
        self.assertEqual(summary["visible_empty_successes"], 1)
        self.assertEqual(summary["nonempty_backend_but_no_visible_evidence"], 1)


if __name__ == "__main__":
    unittest.main()
