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
    def test_v2_rpc_evidence_is_separate_from_original_receipts(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            old = out / "mcp-rpc.json"
            old.write_text("historical-receipt")
            evidence = out / "audit-v2-evidence"
            evidence.mkdir()
            # Fail before any subprocess/network starts; even failure must only
            # touch v2 evidence, leaving the prior MCP receipt untouched.
            with mock.patch.object(demo.subprocess, "Popen", side_effect=OSError("offline-failure")):
                with self.assertRaises(OSError):
                    demo.rpc_checks(out, [], evidence)
            self.assertEqual(old.read_text(), "historical-receipt")
            self.assertTrue((evidence / "mcp.yaml").is_file())
            self.assertFalse((out / "mcp.yaml").exists())

    def test_optional_spans_do_not_relax_complete_member_uuid_ownership(self):
        items = {("queue.md", "shared-member"): {}, ("timeout.md", "shared-member"): {}}
        candidate = {"Domains": [{"subdomains": [{"entities": [], "concepts": [
            {"id": "budget-uuid", "members": ["shared-member"]}]}]}]}
        sources = [{"node_uuid": "budget-uuid", "member_id": "shared-member", "file_path": path}
                   for path in ("queue.md", "timeout.md")]
        demo.check_source_ownership(sources, items, candidate)
        for wrong in (sources[:1], sources + sources[:1],
                      [dict(sources[0], node_uuid="other-uuid"), sources[1]]):
            with self.assertRaisesRegex(ValueError, "member ownership"):
                demo.check_source_ownership(wrong, items, candidate)

    def test_original_annotation_hint_may_be_unknown_and_is_not_rewritten_location(self):
        source = {"start_line": None, "end_line": None}
        self.assertEqual(demo.original_span_hint(source, {})["status"], "unknown_not_supplied")
        hint = {"source_span": {"start_line": 100, "end_line": 110}}
        # An original hint can exceed the rewritten document's length. Never
        # reinterpret it as a generated range or silently fabricate a citation.
        result = demo.original_span_hint({"start_line": 100, "end_line": 110}, hint)
        self.assertFalse(result["original_quote_verified"])
        with self.assertRaisesRegex(ValueError, "differs from member"):
            demo.original_span_hint(source, hint)

    def test_generated_ranges_require_unique_matching_uuid_markers(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "generated.md"
            path.write_text("header\n<!-- kg:uuid=node-a shared=true mirror=true source=_shared/a.md -->\n"
                            "> generated summary\n<!-- /kg:uuid=node-a -->\n")
            result = demo.generated_block_range(path, "node-a")
            self.assertEqual((result["start_line"], result["end_line"]), (2, 4))
            self.assertIn("not_original", result["semantics"])
            with self.assertRaisesRegex(ValueError, "generated UUID block"):
                demo.generated_block_range(path, "node-b")
            path.write_text(path.read_text() * 2)
            with self.assertRaisesRegex(ValueError, "generated UUID block"):
                demo.generated_block_range(path, "node-a")
            path.write_text("<!-- kg:uuid=node-a tag=concept -->\nunterminated\n")
            with self.assertRaisesRegex(ValueError, "generated UUID block"):
                demo.generated_block_range(path, "node-a")

    def test_textual_fact_retention_does_not_replace_required_direct_dependency(self):
        nodes = [{"id": "queue", "name": "阻塞队列", "summary": "容量为 8", "description": ""},
                 {"id": "budget", "name": "重试预算", "summary": "", "description": "最多等待 50 毫秒，最多重试 2 次，依赖阻塞队列"}]
        result = demo.required_semantics(nodes, [])
        self.assertEqual(result["status"], "fail_required_checks")
        self.assertFalse(result["checks"]["direct_budget_depends_on_queue_edge"])
        reversed_edge = {"source_id": "queue", "target_id": "budget", "kind": "depends_on"}
        self.assertEqual(demo.required_semantics(nodes, [reversed_edge])["status"], "fail_required_checks")
        edge = {"source_id": "budget", "target_id": "queue", "kind": "depends_on"}
        self.assertEqual(demo.required_semantics(nodes, [edge])["status"], "pass_required_checks")

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
