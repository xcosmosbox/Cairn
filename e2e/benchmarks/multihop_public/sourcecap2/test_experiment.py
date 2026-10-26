"""Synthetic single-factor boundary tests; no data downloads or holdout."""
import collections
import copy
import unittest

from sourcecap2 import experiment as e


def hit(doc, ordinal, start=0, end=10):
    return dict(doc_id=doc, chunk_id=f"chunk-{ordinal}", start=start, end=end,
                text="red bridge"[start:end], metadata=dict(title="synthetic", source="fixture", published_at="2026"))


class SourceCapTests(unittest.TestCase):
    def test_both_selections_share_unmodified_ranked_pool(self):
        pool = [hit(f"doc{n // 4}", n) for n in range(50)]
        frozen = copy.deepcopy(pool)
        control = e.select_chunks(pool, e.ARMS[0])
        candidate = e.select_chunks(pool, e.ARMS[1])
        self.assertEqual(control, pool[:10])
        self.assertEqual(candidate, [pool[n] for n in (0, 1, 4, 5, 8, 9, 12, 13, 16, 17)])
        self.assertLessEqual(max(collections.Counter(h["doc_id"] for h in candidate).values()), 2)
        self.assertEqual(pool, frozen)

    def test_insufficient_distinct_sources_preserves_short_selection(self):
        pool = [hit("same-source", n) for n in range(50)]
        self.assertEqual(e.select_chunks(pool, e.ARMS[1]), pool[:2])
        self.assertEqual(e.select_chunks([], e.ARMS[1]), [])
        self.assertEqual(e.select_chunks(pool[:1], e.ARMS[1]), pool[:1])
        with self.assertRaises(ValueError):
            e.select_chunks(pool, "graph")

    def test_shared_baseline_packer_keeps_original_budget_and_offsets(self):
        pool = [hit(f"d{n // 3}", n) for n in range(50)]
        for arm in e.ARMS:
            selected = e.select_chunks(pool, arm)
            visible, truncated, chars = e.baseline.pack_context(selected, 350)
            self.assertTrue(truncated)
            self.assertLessEqual(chars, 350)
            self.assertEqual(chars, len(e.baseline.canonical({"evidence": visible})))
            for block in visible:
                original = next(h for h in pool if h["chunk_id"] == block["chunk_id"])
                self.assertEqual(block["text"], original["text"][:block["end"] - block["start"]])
                self.assertEqual(block["metadata"], original["metadata"])

    def test_fact_losses_partition_facts_and_keep_same_source_counterexample(self):
        documents = {url: dict(body="red bridge") for url in ("a", "b", "c", "d")}
        docs = {url: e.baseline.document_id(url) for url in documents}
        question = dict(evidence_list=[dict(url=url, fact="red bridge") for url in documents])
        pool = [hit(docs["a"], 0, 0, 4), hit(docs["a"], 1, 0, 4), hit(docs["a"], 2), hit(docs["b"], 3), hit(docs["c"], 4)]
        selected = e.select_chunks(pool, e.ARMS[1])
        visible = [h for h in selected if h["doc_id"] != docs["b"]]
        losses, visible_facts, selected_facts = e.loss_breakdown(question, pool, selected, visible, documents)
        self.assertEqual(losses["source_absent_pool50"], 1)
        self.assertEqual(losses["source_selected_wrong_chunks"], 1)
        self.assertEqual(losses["budget_only_loss"], 1)
        self.assertEqual(losses["visible_fact"], 1)
        self.assertEqual(sum(losses.values()), 4)
        control_visible = e.fact_coverage(question, e.select_chunks(pool, e.ARMS[0]), documents)
        # The third same-source chunk has the fact; cap2 loses it, and this loss must survive.
        self.assertIn(("a", "red bridge"), control_visible - selected_facts)
        self.assertNotIn(("a", "red bridge"), visible_facts)

    def test_paired_counts_preserve_gains_losses_ties_and_exclude_null_recall(self):
        keys = ("doc_recall_at_10", "all_gold_docs_at_10", "visible_doc_recall", "visible_all_gold_docs", "fact_recall", "all_gold_facts", "unique_visible_docs")
        rows = [dict(unique_support_doc_count=2, delta={k: v for k in keys}) for v in (1, -1, 0)]
        rows.append(dict(unique_support_doc_count=0, delta=dict(unique_visible_docs=10)))
        self.assertEqual(e.paired_summary(rows)["fact_recall"], dict(gain=1, loss=1, tie=1))

    def test_historical_manifest_describes_original_method(self):
        manifest = e.baseline.load(e.HERE / "manifest.json")
        self.assertEqual(manifest["baseline_merge_sha"], "bf5d7ec0828b85695a0ad5e196f11f32f02462cb")
        self.assertEqual(manifest["shared_candidate_pool"]["query_calls_per_question"], 1)
        self.assertEqual(manifest["shared_candidate_pool"]["limit"], 50)
        self.assertIn("HOLDOUT not evaluated", manifest["split"])


if __name__ == "__main__":
    unittest.main()
