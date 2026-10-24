#!/usr/bin/env python3
"""Bounded actual MCP RSS follow-up; reuses product queries and real watcher harness."""
from concurrent.futures import ThreadPoolExecutor
import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import time
from types import SimpleNamespace

import run as perf
from process_rss import ProcessRSS


def allocation_probe():
    marker = "cairn-rss-allocation-" + str(time.time_ns())
    code = ('import os,sys;print(os.getpid(),flush=True);sys.stdin.readline();'
            'x=bytearray(33554432);print("allocated",flush=True);sys.stdin.readline()')
    child = subprocess.Popen([sys.executable, "-c", code, marker], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    try:
        self_pid = int(child.stdout.readline().strip())
        observer = ProcessRSS(child.pid, child.args)
        before = observer.sample()
        child.stdin.write("allocate\n"); child.stdin.flush()
        acknowledged = child.stdout.readline().strip() == "allocated"
        after = observer.sample()
        result = {"child_self_pid": self_pid, "popen_pid": child.pid, "before": before, "after": after,
                  "acknowledged": acknowledged, "rss_delta_kib": after["rss_kib"]-before["rss_kib"] if before and after else None}
        if not acknowledged or self_pid != child.pid or not before or not after or result["rss_delta_kib"] < 31*1024:
            raise RuntimeError("controlled allocation did not validate process RSS resolver")
        return result
    finally:
        if child.poll() is None:
            child.terminate()
        child.wait(timeout=5)
        child.stdin.close(); child.stdout.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--synthetic-root", required=True, type=Path)
    parser.add_argument("--fake-bundle", required=True, type=Path)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--updates", type=int, default=4)
    args = parser.parse_args()
    if not 1 <= args.updates <= 10:
        parser.error("1..10 updates required")
    args.output.mkdir(parents=True, exist_ok=False)
    inputs = {"fake": args.fake_bundle / "knowledge.db", **{str(n): args.synthetic_root / str(n) / "a/bundle/knowledge.db" for n in (1000,10000)}}
    hashes = {str(p): perf.digest(p) for p in inputs.values()}
    for n in (1000,10000):
        path = args.synthetic_root / str(n) / "b/bundle/knowledge.db"
        hashes[str(path)] = perf.digest(path)
    environment = {"source_commit": args.source_commit, "binary_sha256": perf.digest(args.binary),
                   "platform": platform.platform(), "python": platform.python_version(),
                   "loadavg_before": list(os.getloadavg()), "input_sha256_before": hashes,
                   "parameters": {k: str(v) if isinstance(v,Path) else v for k,v in vars(args).items()}}
    budget = perf.Budget(180,2048)
    runner = perf.Runner(SimpleNamespace(output=args.output,bin_dir=args.binary.parent,updates=args.updates),budget)
    cells, status, error = [], "passed", None
    try:
        environment["allocation_probe"] = allocation_probe()
        for name, db in inputs.items():
            config = args.output / (name+".json")
            perf.write_config(config,db)
            for kind, concurrency in (("stdio",1),("sse",4)):
                with perf.client_server(kind,args.binary,config,budget) as primary:
                    pid = primary.proc.pid
                    if budget.current_rss(pid) is None:
                        raise RuntimeError("actual service process RSS identity unavailable")
                    clients = [primary]
                    try:
                        for _ in range(concurrency-1):
                            attached = perf.AttachedSSE(primary.base);perf.initialize(attached);clients.append(attached)
                        def query(client):
                            return perf.search(client,"队列" if name=="fake" else "marker",True)
                        if name=="fake":
                            # Existing fake fixture's known literal term is checked by real MCP.
                            def query(client):
                                return perf.search(client,"阻塞队列",True)
                        for client in clients:
                            for _ in range(20):
                                budget.check();query(client)
                        idle = budget.current_rss(pid)
                        identity = {"size":name,"transport":kind,"concurrency":concurrency,"phase":"warm_memory"}
                        def worker(worker):
                            for _ in range(100//concurrency):
                                runner.measure(identity,lambda:query(clients[worker]))
                        with ThreadPoolExecutor(max_workers=concurrency) as pool:
                            list(pool.map(worker,range(concurrency)))
                        budget.check()
                        cells.append({**identity,"requests":100,"warm_idle_rss_kib":idle,"peak_rss_or_hwm_kib":budget.peaks.get(pid),"pid":pid})
                    finally:
                        for attached in clients[1:]:attached.close()
        runner.hot(args.synthetic_root / "10000")
        if runner.errors:
            raise RuntimeError("business/protocol errors retained in raw samples")
    except Exception as caught:
        status,error = "failed",str(caught)
    finally:
        runner.close();budget.close()
        environment["input_sha256_after"] = {p:perf.digest(p) for p in hashes}
        environment["loadavg_after"] = list(os.getloadavg())
        (args.output/"environment.json").write_text(json.dumps(environment,indent=2)+"\n")
        (args.output/"rss-samples.jsonl").write_text("".join(json.dumps(row)+"\n" for row in budget.rss_samples))
        summary = {"status":status,"error":error,"elapsed_s":time.monotonic()-budget.start,"cells":cells,
                   "query_samples":len(runner.rows),"business_errors":len(runner.errors),"identities":budget.identities,
                   "rss_samples":len(budget.rss_samples),"unavailable_rss_samples":sum(row["sample"] is None for row in budget.rss_samples),
                   "peaks_kib":budget.peaks,"sampling_interval_seconds":.01,"sampling_budget_reason":budget.reason,
                   "input_hashes_unchanged":hashes==environment["input_sha256_after"],"os_memory_reservation_verified":False,
                   "subinterval_peak_exclusion":False}
        (args.output/"summary.json").write_text(json.dumps(summary,indent=2)+"\n")
        print(json.dumps(summary,indent=2))
    return 0 if status=="passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
