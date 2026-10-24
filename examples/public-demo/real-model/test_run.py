"""Offline contract/failure checks; no model credential or network required."""
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("real_demo", Path(__file__).with_name("run.py"))
demo = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(demo)


class RealDemoTests(unittest.TestCase):
    def test_prepare_freezes_exact_inputs_and_refuses_mutation_or_reuse(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "attempt"
            with mock.patch.object(demo.urllib.request, "build_opener", side_effect=AssertionError("network")):
                demo.prepare(out)
            self.assertEqual(demo.load(out / "preparation.json")["model_calls"], "not_run")
            self.assertEqual(set(x["path"] for x in demo.verify_inputs(out / "original")["files"]), set(demo.FILES))
            with self.assertRaisesRegex(ValueError, "already exists"):
                demo.prepare(out)
            (out / "source/references/queue.md").write_text("modified")
            with self.assertRaisesRegex(ValueError, "identity changed"):
                demo.verify_inputs(out / "source")

    def test_live_mode_refuses_before_reading_key_without_opt_in(self):
        key = mock.Mock()
        args = types.SimpleNamespace(authorize_deepseek=False, key_file=key)
        with self.assertRaisesRegex(ValueError, "requires --authorize-deepseek"):
            demo.canary(args)
        key.read_text.assert_not_called()

    def test_prepare_does_not_include_extra_readme_or_verification_files(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "attempt"
            demo.prepare(out)
            (out / "source/README.md").write_text("unfrozen material")
            with self.assertRaisesRegex(ValueError, "unexpected files"):
                demo.verify_inputs(out / "source")

    def test_canary_receipt_rejects_model_mismatch_without_logging_secret(self):
        class Response:
            status = 200
            headers = {}
            def __enter__(self):
                return self
            def __exit__(self, *_):
                return None
            def read(self):
                return json.dumps({"id": "offline-fixture", "model": "unexpected-model",
                                   "usage": {}, "choices": [{"finish_reason": "stop",
                                   "message": {"content": '{"ok":true}'}}]}).encode()
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "attempt"
            demo.prepare(out)
            args = types.SimpleNamespace(out=out, authorize_deepseek=True, key_file=mock.Mock())
            args.key_file.read_text.return_value = "offline-test-secret"
            opener = mock.Mock()
            opener.open.return_value = Response()
            with mock.patch.object(demo.urllib.request, "build_opener", return_value=opener):
                with self.assertRaisesRegex(ValueError, "sanitized receipt"):
                    demo.canary(args)
            receipt = (out / "canary.json").read_text()
            self.assertNotIn("offline-test-secret", receipt)
            self.assertEqual(json.loads(receipt)["status"], "failed")
            self.assertEqual(json.loads(receipt)["returned_model"], "unexpected-model")
            with self.assertRaisesRegex(ValueError, "receipt already exists"):
                demo.canary(args)


if __name__ == "__main__":
    unittest.main()
