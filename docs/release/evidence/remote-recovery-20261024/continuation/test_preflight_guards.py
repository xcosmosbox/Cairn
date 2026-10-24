"""Invalid resume records must abort before creating output or reading a key."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[5]
ORIGINAL = json.loads((Path(__file__).resolve().parent.parent / "receipt.json").read_text())
PRODUCT = os.environ.get("CAIRN_RECOVERY_PRODUCT_SOURCE")


@unittest.skipUnless(PRODUCT, "set a clean accepted5d99 product checkout")
class ResumePreflightGuards(unittest.TestCase):
    def reject(self, record, reason):
        with tempfile.TemporaryDirectory() as directory:
            tmp = Path(directory)
            receipt = tmp / "invalid.json"
            receipt.write_text(json.dumps(record))
            out = tmp / "must-not-be-created"
            result = subprocess.run(["python3", str(ROOT / "scripts/release/remote_recovery.py"),
                                     "--live-sandbox", "--key-file", str(tmp / "must-not-be-read"),
                                     "--out", str(out), "--resume-receipt", str(receipt),
                                     "--resume-work", str(tmp), "--product-source", PRODUCT,
                                     "--expected-mcp-sha256", "unused-preflight-hash"],
                                    capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(reason, result.stderr)
            self.assertFalse(out.exists(), "guard ran after side-effect preparation")

    def test_empty_receipt_cannot_fall_back_to_new_fixture_mode(self):
        self.reject({}, "resume receipt must be a nonempty object")

    def test_wrong_branch_cannot_target_other_content(self):
        record = copy.deepcopy(ORIGINAL)
        record["remote_branch"]["branch"] = "main"
        self.reject(record, "unexpected preserved branch")

    def test_wrong_asset_id_is_rejected(self):
        record = copy.deepcopy(ORIGINAL)
        record["remote_objects"][0]["asset"]["id"] = 1
        self.reject(record, "unexpected preserved Release/asset IDs")

    def test_wrong_starting_tip_is_rejected(self):
        record = copy.deepcopy(ORIGINAL)
        record["pointer_commits"][0]["commit"] = "0" * 40
        self.reject(record, "unexpected initial pointer")


if __name__ == "__main__":
    unittest.main()
