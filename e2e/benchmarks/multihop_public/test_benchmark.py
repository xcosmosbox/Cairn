"""合成离线故障回归；no downloaded news or model/API access."""
import copy
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

import benchmark as b


def document(url, body):
    return dict(title="Synthetic title", author=None, source="fixture", published_at="2026-01-01", category="test", url=url, body=body)


def question(query, urls=(), fact="red bridge", kind="inference_query"):
    return dict(query=query, answer="evaluator only secret", question_type=kind,
                evidence_list=[dict(url=url, fact=fact) for url in urls])


class BenchmarkTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.protocol = b.load(b.HERE / "protocol.json")

    def index(self):
        corpus = [document("https://fixture/a", "red bridge connects alpha city"), document("https://fixture/b", "green bridge connects beta city")]
        path = self.root / "fixture.db"
        receipt = b.build_index(corpus, path, self.protocol, b.digest("synthetic corpus"))
        return corpus, path, receipt

    def test_corpus_contract_rejects_labels_and_duplicates(self):
        corpus = [document("a", "red bridge")]
        corpus[0]["answer"] = "secret"
        with self.assertRaisesRegex(ValueError, "schema"):
            b.build_index(corpus, self.root / "wrong.db", self.protocol, "hash")
        with self.assertRaisesRegex(ValueError, "unique"):
            b.validate_corpus([document("a", "x"), document("a", "y")])

    def test_original_offsets_and_deterministic_index(self):
        corpus, path, receipt = self.index()
        second = self.root / "second.db"
        second_receipt = b.build_index(list(reversed(corpus)), second, self.protocol, b.digest("synthetic corpus"))
        self.assertEqual(receipt, second_receipt)
        for row in corpus:
            for chunk in b.chunks(row, self.protocol):
                self.assertEqual(chunk["text"], row["body"][chunk["start"]:chunk["end"]])
        with self.assertRaises(FileExistsError):
            b.build_index(corpus, path, self.protocol, "hash")

    def test_retriever_label_firewall_readonly_hash_and_stable_ties(self):
        _, path, receipt = self.index()
        retriever = b.Retriever(path, receipt)
        self.addCleanup(retriever.close)
        request = dict(qid=b.query_id("bridge"), query="bridge")
        for arm in ("raw_lexical_sanity", "raw_source_fts_sqlite"):
            hits = retriever.search(request, arm, 10)
            self.assertEqual(hits, retriever.search(request, arm, 10))
            self.assertEqual(len(hits), 2)
        with self.assertRaisesRegex(ValueError, "labels forbidden"):
            retriever.search({**request, "evidence_list": []}, "raw_source_fts_sqlite", 10)
        with self.assertRaisesRegex(ValueError, "no real graph"):
            retriever.search(request, "graph", 10)
        with self.assertRaises(sqlite3.OperationalError):
            retriever.db.execute("DELETE FROM chunks")
        self.assertEqual(b.file_hash(path), receipt["index_sha256"])
        with self.assertRaisesRegex(ValueError, "hash"):
            b.Retriever(path, {**receipt, "index_sha256": "0" * 64})

    def test_gold_validation_and_support_group_isolation(self):
        docs = b.validate_corpus([document("a", "red bridge")])
        rows = b.prepare_questions([question("first question", ["a"]), question("second question", ["a"])], docs)
        self.assertEqual(rows[0]["group_hash"], rows[1]["group_hash"])
        self.assertEqual(b.assigned_split(rows[0]), b.assigned_split(rows[1]))
        nulls = b.prepare_questions([question("missing one", kind="null_query"), question("missing two", kind="null_query")], docs)
        self.assertNotEqual(nulls[0]["group_hash"], nulls[1]["group_hash"])
        with self.assertRaisesRegex(ValueError, "exact original"):
            b.prepare_questions([question("wrong", ["a"], "not in corpus")], docs)
        with self.assertRaisesRegex(ValueError, "duplicate normalized"):
            b.prepare_questions([question("Ａ  B", ["a"]), question("a b", ["a"])], docs)

    def test_freeze_reproducibility_and_no_reseed_on_insufficient(self):
        docs = b.validate_corpus([document("a", "red bridge")])
        rows = b.prepare_questions([question(f"different null synthetic number {n}", kind="null_query") for n in range(40)], docs)
        protocol = {**self.protocol, "types": ["null_query"], "dev_per_type": 2, "holdout_per_type": 2}
        frozen = b.freeze_rows(rows, protocol)
        self.assertEqual(frozen, b.freeze_rows(list(reversed(rows)), protocol))
        self.assertFalse({r["group_hash"] for r in frozen["selected"]["dev"]} & {r["group_hash"] for r in frozen["selected"]["holdout"]})
        with self.assertRaisesRegex(ValueError, "do not reseed"):
            b.freeze_rows(rows, {**protocol, "dev_per_type": 100})

    def test_near_duplicate_quarantine(self):
        rows = [dict(query="one two three four five six seven", qid="a", group_hash="00000000"),
                dict(query="one two three four five six seven", qid="b", group_hash="00000001")]
        self.assertEqual(b.near_duplicate_pairs(rows), [("a", "b")])
        with self.assertRaisesRegex(ValueError, "quarantine"):
            b.freeze_rows(rows, self.protocol)

    def test_budget_clipping_and_fact_union_require_visible_original_body(self):
        corpus, path, receipt = self.index()
        retriever = b.Retriever(path, receipt)
        self.addCleanup(retriever.close)
        hits = retriever.search(dict(qid=b.query_id("red"), query="red"), "raw_source_fts_sqlite", 10)
        visible, truncated, chars = b.pack_context(hits, 240)
        self.assertLessEqual(chars, 240)
        if visible:
            self.assertEqual(visible[0]["text"], corpus[0]["body"][visible[0]["start"]:visible[0]["end"]])
        q = b.prepare_questions([question("red bridge", ["https://fixture/a"])], b.validate_corpus(corpus))[0]
        doc_id = b.document_id("https://fixture/a")
        intervals = [dict(doc_id=doc_id, start=0, end=4), dict(doc_id=doc_id, start=4, end=10)]
        score = b.score_question(q, hits, intervals, b.validate_corpus(corpus))
        self.assertEqual(score["fact_recall"], 1)
        intervals[1]["start"] = 5
        self.assertEqual(b.score_question(q, hits, intervals, b.validate_corpus(corpus))["fact_recall"], 0)
        self.assertEqual(b.score_question(q, hits, [], b.validate_corpus(corpus))["doc_recall_at_10"], 1)

    def test_nulls_excluded_and_failures_remain_positive_denominator(self):
        positive = dict(unique_support_doc_count=2, evidence_fact_count=3, retrieved_count=0, error=True, truncated=False, latency_ms=1, doc_hit_at_5=0, doc_hit_at_10=0, visible_doc_recall=0, visible_all_gold_docs=0,
                        doc_recall_at_5=0, doc_recall_at_10=0, all_gold_docs_at_5=0, all_gold_docs_at_10=0, fact_recall=0, all_gold_facts=0)
        null = dict(unique_support_doc_count=0, evidence_fact_count=0, retrieved_count=10, error=False, truncated=True, latency_ms=2)
        summary = b.summarize([positive, null])
        self.assertEqual(summary["positive_queries"], 1)
        self.assertEqual(summary["macro"]["fact_recall"], 0)
        self.assertEqual(summary["null"]["retrieved_count_mean"], 10)
        self.assertEqual(summary["null"]["answer_abstention_accuracy"], "not_measured")

    def test_source_hash_and_size_refusal(self):
        path = self.root / "source.json"
        path.write_text("[]")
        b.checked(path, dict(bytes=2, sha256=b.file_hash(path)))
        with self.assertRaisesRegex(ValueError, "identity"):
            b.checked(path, dict(bytes=2, sha256="0" * 64))

    def test_metadata_is_budgeted_and_never_counts_as_body_fact(self):
        corpus = [document("a", "red bridge " + "blue " * 100)]
        corpus[0]["title"] = "red bridge " + "long metadata " * 100
        path = self.root / "metadata.db"
        receipt = b.build_index(corpus, path, self.protocol, "synthetic")
        retriever = b.Retriever(path, receipt)
        self.addCleanup(retriever.close)
        hits = retriever.search(dict(qid=b.query_id("red"), query="red"), "raw_source_fts_sqlite", 10)
        no_meta_hits = [{**hit, "metadata": {}} for hit in hits]
        visible, truncated, chars = b.pack_context(hits, 500)
        plain_visible, _, _ = b.pack_context(no_meta_hits, 500)
        self.assertTrue(truncated)
        self.assertEqual(visible, [])
        self.assertTrue(plain_visible)
        self.assertLessEqual(chars, 500)
        q = b.prepare_questions([question("red", ["a"])], b.validate_corpus(corpus))[0]
        # Title contains the entire fact, but only body offsets after it are visible.
        body_after_fact = [{"doc_id": b.document_id("a"), "start": 11, "end": 20, "metadata": {"title": "red bridge"}}]
        score = b.score_question(q, hits, body_after_fact, b.validate_corpus(corpus))
        self.assertEqual(score["visible_all_gold_docs"], 1)
        self.assertEqual(score["fact_recall"], 0)
        self.assertEqual(score["unique_support_doc_count"], 1)
        self.assertEqual(score["evidence_fact_count"], 1)


if __name__ == "__main__":
    unittest.main()
