"""Simple recording keeps local I/O failures from cancelling later work."""
from pathlib import Path
import tempfile
import unittest
from unittest import mock
from durable_ledger import JSONLedger, read_jsonl

class LedgerTests(unittest.TestCase):
    def test_raw_rows_and_memory_fallback(self):
        with tempfile.TemporaryDirectory() as root:
            ledger = JSONLedger(Path(root) / 'rows.jsonl')
            ledger.append({'id': 'first', 'body': 'exact raw'})
            with mock.patch.object(Path, 'open', side_effect=OSError('disk failed')):
                ledger.append({'id': 'failed-sink'})
            ledger.append({'id': 'later'})
            self.assertEqual([r['id'] for r in ledger.records], ['first', 'failed-sink', 'later'])
            self.assertEqual([r['id'] for r in read_jsonl(ledger.path)], ['first', 'later'])
            self.assertIn('persistence_unavailable', ledger.records[1])
