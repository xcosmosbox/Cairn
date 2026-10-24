"""Observe only a uniquely identified direct child across nested PID namespaces."""
import os
from pathlib import Path


def _read(path):
    status = dict(line.split(":", 1) for line in (path / "status").read_text().splitlines() if ":" in line)
    # comm may contain spaces and parentheses; fields after the final ')' start at state(3).
    stat = (path / "stat").read_text().rpartition(")")[2].split()
    return status, int(stat[19]), (path / "cmdline").read_bytes()


class ProcessRSS:
    """Missing/ambiguous/changed identities stay unknown; never substitute zero."""
    def __init__(self, pid, argv, proc_root=Path("/proc")):
        self.pid, self.root = pid, Path(proc_root)
        self.argv = b"\0".join(os.fsencode(arg) for arg in argv) + b"\0"
        self.identity, self.reason = None, "process identity unavailable"
        try:
            self.parent = int(dict(line.split(":", 1) for line in (self.root / "self/status").read_text().splitlines() if ":" in line)["Pid"])
            matches = []
            for path in self.root.iterdir():
                if not path.name.isdecimal():
                    continue
                try:
                    status, start, cmdline = _read(path)
                    if self._matches(path, status, cmdline):
                        matches.append((int(path.name), start))
                except (OSError, ValueError, KeyError, IndexError):
                    continue
            if len(matches) != 1:
                self.reason = "process identity missing or ambiguous"
                return
            self.identity = matches[0]
            if self.sample() is None:
                self.identity = None
        except (OSError, ValueError, KeyError, IndexError):
            pass

    def _matches(self, path, status, cmdline):
        return (cmdline == self.argv and int(status["Pid"]) == int(path.name)
                and int(status["PPid"]) == self.parent
                and int(status["NSpid"].split()[-1]) == self.pid)

    def sample(self):
        if self.identity is None:
            return None
        outer, expected_start = self.identity
        path = self.root / str(outer)
        try:
            status, start, cmdline = _read(path)
            if start != expected_start or not self._matches(path, status, cmdline):
                self.reason = "process identity changed"
                return None
            values = {}
            for source, target in (("VmRSS", "rss_kib"), ("VmHWM", "hwm_kib")):
                raw = status[source].split()
                if len(raw) != 2 or raw[1] != "kB" or int(raw[0]) < 0:
                    raise ValueError("invalid memory field")
                values[target] = int(raw[0])
            # A second identity read rejects exit/reuse while memory fields were read.
            status2, start2, cmdline2 = _read(path)
            if start2 != expected_start or not self._matches(path, status2, cmdline2):
                self.reason = "process identity changed during sample"
                return None
            self.reason = None
            return {"inner_pid": self.pid, "outer_pid": outer, "parent_outer_pid": self.parent,
                    "start_ticks": expected_start, **values}
        except (OSError, ValueError, KeyError, IndexError):
            self.reason = "process identity or memory fields unavailable"
            return None
