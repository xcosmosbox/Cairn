#!/usr/bin/env python3
"""Run exactly three original targeted test iterations, bounded to 180 seconds."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import time

HERE = Path(__file__).resolve().parent
cmd = ["/tmp/cairn-runtime33-diagnostic.test", "-test.run=^TestConcurrentRecordAndManifestStayConsistent$", "-test.count=3", "-test.v", "-test.timeout=170s"]
started = time.monotonic()
try:
    result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=180)
    data, code = result.stdout, result.returncode
except subprocess.TimeoutExpired as error:
    data, code = error.stdout or b"", 124
(HERE / "targeted-race.log").write_bytes(data)
rows = [json.loads(line.split(" ", 1)[1]) for line in data.decode().splitlines() if line.startswith("LOCK_DIAGNOSTIC ")]
concurrent = [row for row in rows if row["tool"] == "concurrent"]
summary = {"command": cmd, "exit_code": code, "elapsed_seconds": time.monotonic() - started, "platform": platform.platform(), "loadavg_after": list(os.getloadavg()), "records": len(rows), "concurrent_records": len(concurrent), "error_records": sum(row["error"] != "<nil>" for row in rows), "max_begin_wait_ms": max(row["begin_wait_ns"] for row in concurrent)/1e6 if concurrent else None, "max_hold_upper_ms": max(row.get("hold_upper_ns",0) for row in concurrent)/1e6 if concurrent else None, "record_durations_ms": [row["elapsed_ns"]/1e6 for row in concurrent], "active_manifest_at_begin_count": sum(row["active_manifests_at_begin"] > 0 for row in concurrent), "manifest_calls_during_concurrent": [row["manifest_calls_at_end"]-row["manifest_calls_at_start"] for row in concurrent], "log_sha256": hashlib.sha256(data).hexdigest()}
(HERE / "summary.json").write_text(json.dumps(summary,indent=2)+"\n")
print(json.dumps(summary,indent=2))
raise SystemExit(code)
