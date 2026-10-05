import unittest

from summarize_runs import aggregate, meter_events, paired_comparison


class SummaryTests(unittest.TestCase):
    def test_retries_and_interrupted_request_are_not_lost(self):
        common = {"question_id": "synthetic", "arm": "graph", "turn": 1, "request_sha256": "fixed"}
        events = [{**common, "http_attempt": 1, "event": "request", "attempt_for_turn": 1},
                  {**common, "http_attempt": 1, "event": "response", "status": "error", "finish_reason": "length", "usage": {"prompt_tokens": 10, "completion_tokens": 20, "prompt_cache_hit_tokens": 3}},
                  {**common, "http_attempt": 2, "event": "request", "attempt_for_turn": 2}]
        result = meter_events(events)
        self.assertEqual(result["http_attempts"], 2)
        self.assertEqual(result["retry_attempts"], 1)
        self.assertEqual(result["completion_tokens"], 20)
        self.assertEqual(result["cache_hit_tokens_reported"], 3)
        self.assertEqual(result["unmatched_request_attempts"], 1)
        self.assertEqual(result["attempts_without_usage"], 1)
        self.assertFalse(result["usage_complete"])

    def test_path_overlap_and_invalid_citation_are_separate_from_quality(self):
        result = {"question_id": "synthetic", "arm": "raw", "status": "ok", "answer": "ignored",
                  "citations": ["visible", "invented"], "elapsed_seconds": 2,
                  "retrieved_evidence": [{"id": "visible", "sources": [{"path": "a.md"}]}],
                  "searches": [{"status": "ok", "evidence": [], "executed": True, "truncated": True}]}
        dataset = {"synthetic": {"split": "dev", "evidence": [{"path": "a.md"}, {"path": "b.md"}]}}
        summary = aggregate([result], [], {("synthetic", "raw")}, dataset)
        self.assertEqual(summary["invalid_citations"], 1)
        self.assertEqual(summary["empty_successful_searches"], 1)
        self.assertEqual(summary["truncated_search_responses"], 1)
        self.assertEqual(summary["provenance_path_overlap"]["mean_fraction_of_gold_paths_visible"], .5)
        self.assertNotIn("semantic_recall", str(summary))
        paired = paired_comparison({("synthetic", "raw"): result}, {("synthetic", "raw"): result}, dataset)
        self.assertEqual(paired["groups"]["dev"]["raw"]["mean_provenance_path_overlap_delta"], 0)


if __name__ == "__main__":
    unittest.main()
