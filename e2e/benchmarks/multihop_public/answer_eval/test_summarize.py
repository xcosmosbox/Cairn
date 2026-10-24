import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import summarize as s
from adapter import ARMS

try:
    import numpy as np
except ImportError:
    np = None


def fixture():
    rows = []
    for kind in s.POSITIVE_TYPES:
        for i in range(2):
            for arm in ARMS:
                rows.append({'question_id': kind + str(i), 'arm': arm, 'question_type': kind, 'group_hash': 'shared' if i == 0 else kind,
                             'answerable': True, 'grounded_correct': 1, 'visible_supported_fact_recall': .5,
                             'attempt': 1, 'null_success': 0, 'judge_resolved': True, 'invalid_citations': 0, 'invalid_quotes': 0})
    for i in range(2):
        for arm in ARMS:
            rows.append({'question_id': 'null' + str(i), 'arm': arm, 'question_type': 'null_query', 'group_hash': 'null-group' + str(i),
                         'answerable': False, 'grounded_correct': 0, 'visible_supported_fact_recall': 0,
                         'attempt': 0, 'null_success': 1, 'judge_resolved': True, 'invalid_citations': 0, 'invalid_quotes': 0})
    return rows


class SummaryTests(unittest.TestCase):
    def test_missing_paired_arm_is_not_dropped(self):
        with self.assertRaises(ValueError): s.paired_rows(fixture()[:-1])

    @unittest.skipIf(np is None, 'NumPy PCG64 analysis runtime unavailable; no fallback')
    def test_global_shared_cluster_draws_rng_freeze_and_type_balancing(self):
        rows = fixture(); paired = s.paired_rows(rows)
        positives = [row for row in paired if row['answerable']]; nulls = [row for row in paired if not row['answerable']]
        weights = s.bootstrap_weights(positives, nulls, replicates=64)
        self.assertEqual(weights['positive'].shape, (64, 4))
        self.assertEqual(weights['null'].shape, (64, 2))
        self.assertTrue(np.all(weights['positive'].sum(axis=1) == 4))
        self.assertTrue(np.all(weights['null'].sum(axis=1) == 2))
        again = s.bootstrap_weights(positives, nulls, replicates=64)
        self.assertTrue(np.array_equal(weights['positive'], again['positive']))
        shared_indices = [i for i, row in enumerate(positives) if row['group_hash'] == 'shared']
        expanded = weights['positive'][:, weights['indices']]
        self.assertTrue(all(np.array_equal(expanded[:, shared_indices[0]], expanded[:, j]) for j in shared_indices))
        self.assertGreater(weights['rejected'], 0)
        summary, _ = s.analyze(rows, weights=weights)
        self.assertEqual(summary['difference']['lower_one_sided_95'], 0)
        self.assertTrue(summary['difference']['degenerate'])
        self.assertFalse(summary['gates']['formal_statistics_ready'])

    @unittest.skipIf(np is None, 'NumPy PCG64 analysis runtime unavailable; no fallback')
    def test_zero_attempts_are_undefined_and_both_bad_cannot_answer_pass(self):
        rows = fixture()
        for row in rows:
            row['attempt'] = 0
            row['grounded_correct'] = 0
            row['visible_supported_fact_recall'] = 0
        summary, _ = s.analyze(rows, replicates=128)
        self.assertIsNone(summary['arms']['graph']['attempt_precision']['point'])
        self.assertEqual(summary['arms']['graph']['attempt_precision']['undefined_replicates_assigned_zero'], 128)
        self.assertTrue(summary['gates']['noninferiority'])
        self.assertFalse(summary['gates']['intrinsic_evidence_floor'])
        self.assertFalse(summary['gates']['answer_demo'])

    @unittest.skipIf(np is None, 'NumPy PCG64 analysis runtime unavailable; no fallback')
    def test_errors_remain_attempts_and_unresolved_judge_prevents_formal_pass(self):
        rows = fixture()
        for row in rows:
            if row['arm'] == ARMS[1] and row['answerable']:
                row.update(grounded_correct=0, attempt=1, judge_resolved=False)
        summary, _ = s.analyze(rows, replicates=128)
        self.assertEqual(summary['difference']['point'], -1)
        self.assertFalse(summary['gates']['all_judges_resolved'])
        self.assertFalse(summary['gates']['noninferiority'])
