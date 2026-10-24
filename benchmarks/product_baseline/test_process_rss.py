"""Identity errors must never turn unrelated process memory into service RSS."""
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import process_rss

from process_rss import ProcessRSS


class ProcessRSSTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "self").mkdir()
        (self.root / "self/status").write_text("Pid:\t100\n")

    def child(self, outer=200, inner=7, parent=100, start=900, rss=123, argv=("server", "--config", "unique.json")):
        path = self.root / str(outer)
        path.mkdir(exist_ok=True)
        (path / "status").write_text(f"Pid:\t{outer}\nPPid:\t{parent}\nNSpid:\t{outer}\t{inner}\nVmRSS:\t{rss} kB\nVmHWM:\t140 kB\n")
        (path / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv)+b"\0")
        (path / "stat").write_text(f"{outer} (name with ) spaces) S " + " ".join(["0"]*18+[str(start)]+["0"]*4))
        return path

    def observe(self):
        return ProcessRSS(7, ["server", "--config", "unique.json"], self.root)

    def test_nested_pid_ignores_unrelated_direct_pid(self):
        self.child(outer=7, parent=1, argv=("unrelated",))
        self.child()
        result = self.observe().sample()
        self.assertEqual((result["outer_pid"], result["rss_kib"]), (200, 123))

    def test_ambiguity_wrong_parent_or_namespace_is_unknown(self):
        self.child()
        self.child(outer=201)
        self.assertIsNone(self.observe().sample())
        (self.root / "200/status").write_text("Pid: 200\nPPid: 101\nNSpid: 200 7\n")
        (self.root / "201/status").write_text("Pid: 201\nPPid: 100\nNSpid: 201 8\n")
        self.assertIsNone(self.observe().sample())

    def test_pid_reuse_and_argv_change_fail_closed(self):
        self.child()
        observer = self.observe()
        self.child(start=901)
        self.assertIsNone(observer.sample())
        observer = self.observe()
        (self.root / "200/cmdline").write_bytes(b"different\0")
        self.assertIsNone(observer.sample())

    def test_missing_memory_stays_unknown(self):
        path = self.child()
        observer = self.observe()
        text = (path / "status").read_text()
        (path / "status").write_text("\n".join(line for line in text.splitlines() if not line.startswith("VmRSS:")))
        self.assertIsNone(observer.sample())

    def test_identity_change_during_memory_read_is_unknown(self):
        self.child()
        observer = self.observe()
        first = process_rss._read(self.root / "200")
        changed = (first[0], first[1]+1, first[2])
        with mock.patch.object(process_rss, "_read", side_effect=[first,changed]):
            self.assertIsNone(observer.sample())

    def test_valid_namespace_normal_pid_is_supported(self):
        self.child(outer=7)
        self.assertEqual(self.observe().sample()["outer_pid"], 7)


if __name__ == "__main__":
    unittest.main()
