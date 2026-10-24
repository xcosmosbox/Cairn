"""Lightweight accounting and real loopback transport fixture checks."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from urllib.request import urlopen

spec = importlib.util.spec_from_file_location('product_perf', Path(__file__).with_name('run.py'))
perf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(perf)


class MeasurementTests(unittest.TestCase):
    def test_quantile_samples_and_interpolation(self):
        self.assertEqual(perf.quantiles([]), {'n': 0})
        self.assertEqual(perf.quantiles([5])['p99_ms'], 5)
        self.assertEqual(perf.quantiles([10, 0])['p95_ms'], 9.5)

    def test_business_payload_not_only_transport_success(self):
        class Client:
            def __init__(self, text): self.text = text
            def call(self, *args): return self.text
        self.assertEqual(perf.search(Client('- ID: fixture\nmarker generationa'), 'marker', True), '- ID: fixture\nmarker generationa')
        with self.assertRaises(ValueError):
            perf.search(Client('empty success'), 'marker', True)
        with self.assertRaises(ValueError):
            perf.search(Client('- ID: wrong\nunrelated'), 'marker', True)
        with self.assertRaises(ValueError):
            perf.search(Client('- ID: unexpected'), 'absent', False)

    def test_local_catalog_pins_asset_to_requested_generation(self):
        # Changing stable between metadata and download must not serve another generation.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for gen in ('a', 'b'):
                (root / gen).mkdir()
                (root / gen / 'catalog.json').write_text(json.dumps({'generation': gen}))
                (root / gen / 'bundle.tar.gz').write_bytes(gen.encode())
            catalog = perf.Catalog(root)
            try:
                release = json.load(urlopen(catalog.base + '/repos/x/y/releases/tags/generation-a'))
                catalog.publish('b')
                self.assertEqual(urlopen(release['assets'][0]['browser_download_url']).read(), b'a')
                self.assertEqual(json.load(urlopen(catalog.base + '/repos/x/y/contents/stable'))['generation'], 'b')
            finally:
                catalog.close()

    def test_expired_budget_is_explicit_and_has_no_children(self):
        budget = perf.Budget(.001, 2048)
        try:
            budget.deadline = 0
            with self.assertRaises(perf.LimitReached):
                budget.check()
        finally:
            budget.close()


if __name__ == '__main__': unittest.main()
