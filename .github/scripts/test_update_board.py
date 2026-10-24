"""Regression coverage for misleading state, untrusted text and accidental hub loss."""
import copy
import unittest
from unittest.mock import patch

from update_board import BEGIN, END, GitHub, render, replace_region, status, update


def issue(number=10, labels=("coord:task", "status:ready"), **kw):
    return {"number": number, "title": "example", "labels": [{"name": n} for n in labels],
            "state": "open", "updated_at": "2026-10-24T08:00:00Z", **kw}


class MemoryAPI:
    def __init__(self):
        self.hub = {"body": "human preface\n" + BEGIN + "\nold\n" + END + "\nhuman footer", "updated_at": "first"}
        self.tasks = [issue()]
        self.writes = []

    def request(self, path, method="GET", data=None):
        if method == "PATCH":
            self.writes.append(data)
            self.hub["body"] = data["body"]
        return copy.deepcopy(self.hub)

    def issues(self, label):
        return self.tasks


CONFIG = {"hub_issue": 7, "task_label": "coord:task"}


class BoardTests(unittest.TestCase):
    def test_closed_is_not_automatically_completed(self):
        self.assertEqual(status(issue(state="closed")), "closed-unaccepted")
        self.assertEqual(status(issue(labels=("status:done",), state="closed", state_reason="completed")), "done")
        self.assertEqual(status(issue(labels=("status:done",), state="closed", state_reason="not_planned")), "closed-unaccepted")

    def test_missing_conflicting_unknown_and_open_done_states(self):
        for labels in [(), ("status:ready", "status:active"), ("status:typo",), ("status:done",)]:
            with self.subTest(labels=labels):
                self.assertEqual(status(issue(labels=labels)), "needs-triage")

    def test_untrusted_title_cannot_create_rows_links_or_html(self):
        result = render([issue(title="x|evil\n[click](https://bad) <img> `code` \\*")], "coord:task")
        row = next(line for line in result.splitlines() if line.startswith("| #10"))
        self.assertEqual(row.count("|"), 5)
        self.assertNotIn("<img>", row)
        self.assertNotIn("[click]", row)
        self.assertIn("&#124;evil", row)

    def test_pull_requests_and_unrelated_issues_are_excluded(self):
        result = render([issue(11, pull_request={}), issue(12, labels=("status:ready",)), issue()], "coord:task")
        self.assertNotIn("#11", result)
        self.assertNotIn("#12", result)
        self.assertIn("#10", result)

    def test_deterministic_order_and_render(self):
        self.assertEqual(render([issue(12), issue(10)], "coord:task"), render([issue(10), issue(12)], "coord:task"))

    def test_only_marked_region_is_replaced(self):
        body = "manual\n" + BEGIN + "\nold\n" + END + "\ntail"
        expected = "manual\n" + BEGIN + "\nnew\n" + END + "\ntail"
        self.assertEqual(replace_region(body, "new"), expected)
        self.assertEqual(replace_region(expected, "new"), expected)

    def test_missing_duplicate_and_reversed_markers_refuse_write(self):
        for body in ["", BEGIN + BEGIN + END, END + BEGIN, BEGIN]:
            with self.subTest(body=body), self.assertRaises(ValueError):
                replace_region(body, "new")

    def test_update_is_idempotent_and_preserves_human_text(self):
        api = MemoryAPI()
        self.assertTrue(update(api, CONFIG))
        self.assertFalse(update(api, CONFIG))
        self.assertEqual(len(api.writes), 1)
        self.assertTrue(api.hub["body"].startswith("human preface"))
        self.assertTrue(api.hub["body"].endswith("human footer"))

    def test_intervening_human_edit_is_not_overwritten(self):
        api = MemoryAPI()
        def mutate(label):
            api.hub["body"] += "concurrent human edit"
            return [issue()]
        api.issues = mutate
        with self.assertRaises(RuntimeError):
            update(api, CONFIG)
        self.assertEqual(api.writes, [])

    def test_failed_source_fetch_never_patches(self):
        api = MemoryAPI()
        def fail(label):
            raise OSError("API unavailable")
        api.issues = fail
        with self.assertRaises(OSError):
            update(api, CONFIG)
        self.assertEqual(api.writes, [])

    def test_pagination_collects_all_tasks(self):
        api = GitHub("example/repo", "unused-test-token")
        with patch.object(api, "request", side_effect=[[issue(n) for n in range(100)], [issue(101)]]) as request:
            result = api.issues("coord:task")
        self.assertEqual(len(result), 101)
        self.assertIn("page=2", request.call_args.args[0])

    def test_bad_repository_is_rejected_before_network(self):
        for repo in ["evil/../../other", "example/repo?x=1", "https://evil.test/x"]:
            with self.subTest(repo=repo), self.assertRaises(ValueError):
                GitHub(repo, "unused")


if __name__ == "__main__":
    unittest.main()
