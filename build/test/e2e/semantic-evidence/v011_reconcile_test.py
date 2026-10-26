"""Small offline set/multiplicity regressions; no model or private corpus required."""

import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("reconcile", Path(__file__).with_name("v011_reconcile.py"))
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


class ReconciliationTests(unittest.TestCase):
    def test_duplicate_occurrences_cannot_hide_removal(self):
        row = {"name": "same", "detail": "one fact"}
        self.assertEqual(audit.removed_occurrences([row, row], [dict(row, id="assigned")]), [1])

    def test_changed_or_added_accepted_row_is_not_a_removal(self):
        with self.assertRaises(ValueError):
            audit.removed_occurrences([{"detail": "conditional"}], [{"detail": "unconditional"}])

    def test_raw_rows_distinct_incidences_and_persistence_keys_differ(self):
        result = {"Domains": [{"subdomains": [{"entities": [
            {"id": "node-a", "members": ["m1"]},
            {"id": "node-b", "members": ["m2"]}], "concepts": []}]}]}
        sources = {"m1": ["doc-a", "doc-a", "doc-b"], "m2": ["doc-a"], "lost": ["doc-c"]}
        expected = {("doc-a", "m1"), ("doc-b", "m1"), ("doc-a", "m2"), ("doc-c", "lost")}
        counts, rows, missing = audit.reconcile_stage(result, sources, expected)
        self.assertEqual(counts["raw_source_rows"], 4)
        self.assertEqual(counts["distinct_selected_associations"], 3)
        self.assertEqual(counts["distinct_document_member_incidences"], 3)
        self.assertEqual(missing, {("doc-c", "lost")})
        self.assertEqual(len(rows), 3)

    def test_unknown_member_does_not_gain_invented_source_credit(self):
        result = {"Domains": [{"subdomains": [{"entities": [], "concepts": [
            {"id": "node", "members": ["unknown"]}]}]}]}
        counts, rows, _ = audit.reconcile_stage(result, {}, set())
        self.assertEqual(counts["unknown_members"], 1)
        self.assertEqual(rows, set())


if __name__ == "__main__":
    unittest.main()
