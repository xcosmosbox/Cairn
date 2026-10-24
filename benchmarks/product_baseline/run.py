#!/usr/bin/env python3
"""Bounded real product measurements. Standard library plus existing MCP helpers."""
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
import argparse
import hashlib
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import platform
import signal
import sqlite3
import statistics
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("consumer_smoke", ROOT / "e2e/real-consumer-smoke.py")
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def current_rss(pid):
    try:
        return next(int(line.split()[1]) for line in Path(f"/proc/{pid}/status").read_text().splitlines() if line.startswith("VmRSS:"))
    except (OSError, StopIteration, ValueError):
        return None


def quantiles(values):
    ordered = sorted(values)
    def at(p):
        pos = (len(ordered) - 1) * p
        low = int(pos)
        return ordered[low] + (ordered[min(low + 1, len(ordered) - 1)] - ordered[low]) * (pos - low)
    return {"n": len(values), **({"p50_ms": at(.5), "p95_ms": at(.95), "p99_ms": at(.99)} if values else {})}


class LimitReached(RuntimeError):
    pass


class Budget:
    """Wall deadline and actual child RSS ceiling; watchdog kills registered children."""
    def __init__(self, seconds, rss_mib):
        self.start = time.monotonic()
        self.deadline = self.start + seconds
        self.ceiling_kib = rss_mib * 1024
        self.pids, self.peaks = set(), {}
        self.reason = None
        self.lock = threading.Lock()
        self.done = threading.Event()
        self.thread = threading.Thread(target=self.watch, daemon=True)
        self.thread.start()

    def register(self, pid):
        with self.lock:
            self.pids.add(pid)
            self.peaks[pid] = 0

    def remove(self, pid):
        with self.lock:
            self.pids.discard(pid)
        return self.peaks.get(pid, 0)

    def check(self):
        if time.monotonic() >= self.deadline:
            self.reason = self.reason or "10-minute measured wall budget exhausted"
        if self.reason:
            raise LimitReached(self.reason)

    def watch(self):
        while not self.done.wait(.01):
            with self.lock:
                pids = list(self.pids)
            for pid in pids:
                try:
                    fields = dict(line.split(":", 1) for line in Path(f"/proc/{pid}/status").read_text().splitlines() if ":" in line)
                    rss = max(int(fields.get(key, "0 kB").split()[0]) for key in ("VmRSS", "VmHWM"))
                    self.peaks[pid] = max(rss, self.peaks.get(pid, 0))
                    if rss > self.ceiling_kib:
                        self.reason = "child RSS ceiling exceeded"
                except (OSError, ValueError):
                    pass
            if time.monotonic() >= self.deadline:
                self.reason = self.reason or "measured wall deadline exhausted"
            if self.reason:
                for pid in pids:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass

    def close(self):
        self.done.set()
        self.thread.join(timeout=1)


class AttachedSSE(smoke.HTTPMCP):
    """Reuse the existing HTTP/SSE protocol implementation, attach to one real server."""
    def __init__(self, base):
        self.base, self.next_id, self.calls, self.closed = base, 0, [], False
        parsed = urlsplit(base)
        self.connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=5)
        self.connection.request("GET", "/sse", headers={"Accept": "text/event-stream"})
        self.stream = self.connection.getresponse()
        if self.stream.status != 200:
            raise ValueError("SSE attach failed")
        event, self.endpoint = self.event()
        if event != "endpoint" or not self.endpoint.startswith("/rpc?session="):
            raise ValueError("invalid local SSE endpoint")

    def close(self, success=True):
        self.stream.close()
        self.connection.close()
        self.closed = True


def initialize(client):
    result = client.request("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                          "clientInfo": {"name": "product-baseline", "version": "1"}})
    if result.get("protocolVersion") != "2024-11-05":
        raise ValueError("MCP handshake mismatch")
    client.request("notifications/initialized", notification=True)


def search(client, query, hit):
    text = client.call("domain_search", {"kg": "bench", "keyword": query, "limit": 10})
    has_hit = "- ID:" in text
    if has_hit != hit:
        raise ValueError("query hit/non-hit fixture assertion failed")
    if hit and query not in text:
        raise ValueError("expected fixture term absent from business payload")
    if query == "marker" and ("generationa" in text) == ("generationb" in text):
        raise ValueError("marker query must expose exactly one immutable generation")
    return text


def write_config(path, db, watch=None):
    kb = {"name": "bench", "path": str(Path(db).resolve())}
    cfg = {"mcp": {"knowledge_bases": [kb]}}
    if watch:
        base, install = watch
        cfg["github"] = {"api_base_url": base}
        kb.update(catalog_repo="synthetic/catalog", catalog_branch="main", manifest_dir="knowledge-bases",
                  install_dir=str(install), token_env="CAIRN_BENCH_LOOPBACK_TOKEN", poll_interval="100ms")
    Path(path).write_text(json.dumps(cfg))


@contextmanager
def client_server(kind, binary, config, budget):
    client = None
    try:
        client = smoke.MCP(binary, config) if kind == "stdio" else smoke.HTTPMCP(binary, config)
        budget.register(client.proc.pid)
        initialize(client)
        yield client
    finally:
        if client is not None:
            try:
                client.close(False)
            finally:
                budget.remove(client.proc.pid)


class Catalog:
    """Loopback-only GitHub Contents/Release/asset fixture from existing pull tests."""
    def __init__(self, root):
        self.root, self.current, self.events = Path(root), "a", []
        self.lock = threading.Lock()
        owner = self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def do_GET(self):
                with owner.lock:
                    generation = owner.current
                    event = {"t_ns": time.perf_counter_ns(), "path": self.path, "generation": generation}
                    owner.events.append(event)
                if "/contents/" in self.path:
                    body = (owner.root / generation / "catalog.json").read_bytes()
                elif "/releases/tags/" in self.path:
                    generation = self.path.rsplit("generation-", 1)[-1]
                    body = json.dumps({"tag_name": "generation-" + generation, "assets": [{"name": "bundle.tar.gz",
                        "browser_download_url": owner.base + "/asset/" + generation}]}).encode()
                elif self.path.startswith("/asset/"):
                    generation = self.path.rsplit("/", 1)[-1]
                    if generation not in ("a", "b"):
                        self.send_error(404)
                        return
                    body = (owner.root / generation / "bundle.tar.gz").read_bytes()
                else:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                event["response_write_end_ns"] = time.perf_counter_ns()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.base = "http://127.0.0.1:%d" % self.server.server_port
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def publish(self, generation):
        with self.lock:
            self.current = generation
            return time.perf_counter_ns()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=1)


class Runner:
    def __init__(self, args, budget):
        self.args, self.budget, self.rows, self.cells, self.errors = args, budget, [], [], []
        self.lock = threading.Lock()
        self.output = (args.output / "samples.jsonl").open("x")

    def record(self, row):
        with self.lock:
            self.rows.append(row)
            self.output.write(json.dumps(row) + "\n")

    def measure(self, identity, fn):
        self.budget.check()
        start = time.perf_counter_ns()
        row = {**identity, "start_ns": start, "ok": False}
        try:
            text = fn()
            row.update(ok=True, response_sha256=hashlib.sha256(text.encode()).hexdigest(), response_bytes=len(text.encode()))
            if "generationa" in text:
                row["generation"] = "a"
            elif "generationb" in text:
                row["generation"] = "b"
        except Exception as exc:
            row["error"] = str(exc)[:300]
        row["end_ns"] = time.perf_counter_ns()
        row["latency_ms"] = (row["end_ns"] - start) / 1e6
        if identity.get("transport") == "cli":
            row["child_peak_rss_kib"] = getattr(self, "last_cli_rss", None)
        self.record(row)
        return row

    def cell(self, identity, fn):
        self.budget.check()
        start, before = time.perf_counter_ns(), len(self.rows)
        fn()
        rows = self.rows[before:]
        elapsed = (time.perf_counter_ns() - start) / 1e9
        result = {**identity, **quantiles([r["latency_ms"] for r in rows]), "errors": sum(not r["ok"] for r in rows),
                  "elapsed_s": elapsed, "throughput_requests_s": len(rows) / elapsed if elapsed else None}
        result["query_classes"] = {category: {**quantiles([row["latency_ms"] for row in rows if row.get("query_class") == category]),
            "errors": sum(not row["ok"] for row in rows if row.get("query_class") == category)}
            for category in sorted({row["query_class"] for row in rows if "query_class" in row})}
        if identity.get("transport") == "cli":
            result["child_peak_rss_kib"] = max((row.get("child_peak_rss_kib", 0) or 0 for row in rows), default=None)
        self.cells.append(result)
        self.output.flush()
        print(json.dumps(result), flush=True)

    def cli(self, db, query, hit):
        # wait4 yields this exact child's maxrss, rather than cumulative RUSAGE_CHILDREN.
        with tempfile.TemporaryDirectory(dir=self.args.output) as directory:
            stdout, stderr = Path(directory) / "out", Path(directory) / "err"
            actions = [(os.POSIX_SPAWN_OPEN, 1, str(stdout), os.O_WRONLY | os.O_CREAT, 0o600),
                       (os.POSIX_SPAWN_OPEN, 2, str(stderr), os.O_WRONLY | os.O_CREAT, 0o600)]
            binary = str(self.args.bin_dir / "cairn")
            pid = os.posix_spawn(binary, [binary, "--db-path", str(db), "find", query], os.environ, file_actions=actions)
            self.budget.register(pid)
            try:
                while True:
                    done, status, usage = os.wait4(pid, os.WNOHANG)
                    if done:
                        break
                    self.budget.check()
                    time.sleep(.0005)
                self.last_cli_rss = usage.ru_maxrss
                if os.waitstatus_to_exitcode(status) != 0:
                    raise ValueError("CLI exited unsuccessfully")
                text = stdout.read_text()
                if ("ID:" in text) != hit:
                    raise ValueError("CLI fixture hit assertion failed")
                return text
            finally:
                self.budget.remove(pid)

    def static(self, name, db, queries):
        config = self.args.output / (name + ".json")
        write_config(config, db)
        binary = self.args.bin_dir / "cairn-mcp"
        for repeat in range(self.args.repeats):
            identity = {"size": name, "transport": "cli", "concurrency": 1, "cache": "new_process_uncontrolled_os_cache", "repeat": repeat}
            def cli_loop():
                for i in range(self.args.samples):
                    query, hit, category = queries[i % len(queries)]
                    self.measure({**identity, "query_class": category}, lambda: self.cli(db, query, hit))
            self.cell(identity, cli_loop)
        for kind, concurrency in (("stdio", 1), ("sse", 1), ("sse", 4)):
            for repeat in range(self.args.repeats):
                identity = {"size": name, "transport": kind, "concurrency": concurrency, "cache": "same_process_warmed", "repeat": repeat}
                with client_server(kind, binary, config, self.budget) as primary:
                    pid = primary.proc.pid
                    clients = [primary]
                    try:
                        for _ in range(concurrency - 1):
                            attached = AttachedSSE(primary.base)
                            initialize(attached)
                            clients.append(attached)
                        for client in clients:
                            for i in range(self.args.warmup):
                                self.budget.check()
                                query, hit, _ = queries[i % len(queries)]
                                search(client, query, hit)
                        warm_rss = current_rss(pid)
                        def worker(worker_id):
                            client = clients[worker_id]
                            for i in range(worker_id, self.args.samples, concurrency):
                                query, hit, category = queries[i % len(queries)]
                                self.measure({**identity, "query_class": category}, lambda: search(client, query, hit))
                        def run_workers():
                            with ThreadPoolExecutor(max_workers=concurrency) as pool:
                                list(pool.map(worker, range(concurrency)))
                        self.cell(identity, run_workers)
                        self.cells[-1]["server_peak_rss_kib"] = self.budget.peaks.get(pid)
                        self.cells[-1]["server_warm_idle_rss_kib"] = warm_rss
                    finally:
                        for attached in clients[1:]:
                            attached.close()
        for kind in ("stdio", "sse"):
            identity = {"size": name, "transport": kind, "concurrency": 1, "cache": "startup_handshake_first_query_uncontrolled_os_cache", "repeat": 0}
            def cold_loop():
                for _ in range(self.args.cold_samples):
                    self.budget.check()
                    holder = []
                    def once():
                        client = smoke.MCP(binary, config) if kind == "stdio" else smoke.HTTPMCP(binary, config)
                        holder.append(client)
                        self.budget.register(client.proc.pid)
                        initialize(client)
                        return search(client, queries[0][0], True)
                    try:
                        self.measure(identity, once)
                    finally:
                        for client in holder:
                            client.close(False)
                            self.budget.remove(client.proc.pid)
            self.cell(identity, cold_loop)

    def hot(self, root):
        catalog = Catalog(root)
        config = self.args.output / "watcher.json"
        write_config(config, root / "a/bundle/knowledge.db", (catalog.base, self.args.output / "installed"))
        # A synthetic token is scoped to a loopback mock, never a real provider credential.
        if "CAIRN_BENCH_LOOPBACK_TOKEN" in os.environ:
            raise ValueError("synthetic loopback env name must not already exist")
        os.environ["CAIRN_BENCH_LOOPBACK_TOKEN"] = "synthetic-loopback-only"
        stop = threading.Event()
        updates, clients = [], []
        try:
            with client_server("sse", self.args.bin_dir / "cairn-mcp", config, self.budget) as primary:
                clients = [AttachedSSE(primary.base) for _ in range(4)]
                for client in clients:
                    initialize(client)
                # pread avoids changing the child's shared stderr file offset.
                def log_bytes():
                    return os.pread(primary.stderr.fileno(), 16 * 1024 * 1024, 0)
                wait_until = time.monotonic() + 10
                while b"\xe7\x83\xad\xe5\x88\x87\xe6\x8d\xa2\xe5\xae\x8c\xe6\x88\x90" not in log_bytes():
                    self.budget.check()
                    if time.monotonic() >= wait_until:
                        raise RuntimeError("initial real watcher pull did not finish in 10 seconds")
                    time.sleep(.01)
                baseline_rss = current_rss(primary.proc.pid)
                def load(client):
                    while not stop.is_set():
                        self.budget.check()
                        self.measure({"size": "10000", "transport": "sse", "concurrency": 4, "cache": "during_real_watcher_update", "repeat": 0,
                                      "query_class": "marker"}, lambda: search(client, "marker", True))
                with ThreadPoolExecutor(max_workers=4) as pool:
                    jobs = [pool.submit(load, client) for client in clients]
                    try:
                        for index in range(self.args.updates):
                            target = "b" if index % 2 == 0 else "a"
                            before = log_bytes().count("热切换完成".encode())
                            t0 = catalog.publish(target)
                            deadline = time.monotonic() + 10
                            while True:
                                self.budget.check()
                                result = primary.get("/api/search", {"kg": "bench", "keyword": "marker", "limit": 10})
                                text = json.dumps(result)
                                if "generation" + target in text and log_bytes().count("热切换完成".encode()) > before:
                                    break
                                if time.monotonic() >= deadline:
                                    raise RuntimeError("published bundle not visible within 10 seconds")
                                time.sleep(.005)
                            t1 = time.perf_counter_ns()
                            with catalog.lock:
                                events = [event.copy() for event in catalog.events if event["t_ns"] >= t0]
                            polls = [event["t_ns"] for event in events if "/contents/" in event["path"]]
                            assets = [event for event in events if event["path"] == "/asset/" + target]
                            updates.append({"index": index, "target": target, "published_ns": t0, "visible_ns": t1,
                                            "update_to_visible_ms": (t1 - t0) / 1e6, "completed_watcher_log_observed": True,
                                            "first_catalog_poll_ms": (min(polls) - t0) / 1e6 if polls else None,
                                            "asset_write_end_to_visible_ms": (t1 - assets[-1]["response_write_end_ns"]) / 1e6 if assets and "response_write_end_ns" in assets[-1] else None,
                                            "server_baseline_rss_kib": baseline_rss,
                                            "server_peak_rss_kib": self.budget.peaks.get(primary.proc.pid)})
                    finally:
                        stop.set()
                        for future in jobs:
                            future.result()
                (self.args.output / "watcher-stderr.log").write_bytes(log_bytes())
        finally:
            stop.set()
            for client in clients:
                client.close()
            catalog.close()
            os.environ.pop("CAIRN_BENCH_LOOPBACK_TOKEN", None)
            (self.args.output / "updates.json").write_text(json.dumps({"updates": updates, "catalog_events": catalog.events}, indent=2) + "\n")

    def close(self):
        self.output.flush()
        os.fsync(self.output.fileno())
        self.output.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, required=True)
    parser.add_argument("--synthetic-root", type=Path, required=True)
    parser.add_argument("--fake-bundle", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--seconds", type=float, default=600)
    parser.add_argument("--rss-mib", type=int, default=2048)
    parser.add_argument("--samples", type=int, default=500)
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--warmup", type=int, default=20)
    parser.add_argument("--cold-samples", type=int, default=30)
    parser.add_argument("--updates", type=int, default=10)
    args = parser.parse_args()
    if not 0 < args.seconds <= 600 or not 0 < args.rss_mib <= 2048 or not 1 <= args.updates <= 10:
        parser.error("bounded measured run requires <=600 seconds/2048 MiB/10 updates")
    args.output.mkdir(parents=True, exist_ok=False)
    inputs = {"fake": args.fake_bundle / "knowledge.db", "1000": args.synthetic_root / "1000/a/bundle/knowledge.db",
              "10000": args.synthetic_root / "10000/a/bundle/knowledge.db"}
    initial_hashes = {str(path): digest(path) for path in inputs.values()}
    synthetic_specs = {name: json.loads((args.synthetic_root / name / "fixture.json").read_text()) for name in ("1000", "10000")}
    fixture_checks = {}
    for name, spec in synthetic_specs.items():
        for generation in ("a", "b"):
            path = args.synthetic_root / name / generation / "bundle/knowledge.db"
            with sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True) as conn:
                observed = {"nodes": conn.execute("SELECT count(*) FROM nodes").fetchone()[0],
                            "edges": conn.execute("SELECT count(*) FROM edges").fetchone()[0],
                            "marker": conn.execute("SELECT summary FROM nodes WHERE name='marker'").fetchone()[0]}
                if observed != {"nodes": spec["nodes"], "edges": spec["edges"], "marker": "generation" + generation}:
                    raise ValueError("synthetic graph scale/generation self-check failed")
                fixture_checks[name + generation] = observed
    environment = {"product_source_commit": args.source_commit, "hostname": platform.node(), "platform": platform.platform(),
        "cpu_count": os.cpu_count(), "cpu_max": Path("/sys/fs/cgroup/cpu.max").read_text().strip(),
        "memory_max": Path("/sys/fs/cgroup/memory.max").read_text().strip(), "loadavg_before": list(os.getloadavg()),
        "python": platform.python_version(), "binary_sha256": {name: digest(args.bin_dir / name) for name in ("cairn", "cairn-mcp")},
        "input_db_sha256_before": initial_hashes, "parameters": {key: str(value) if isinstance(value, Path) else value for key, value in vars(args).items()},
        "synthetic_fixture_specs": synthetic_specs,
        "fixture_semantic_checks": fixture_checks,
        "concurrent_work": "Shared Codex host; other agents may run tests/builds. No exclusive CPU/host reservation or OS cache drop.",
        "cache_definition": "New process is not a disk-cold cache claim; warm cells use the same prewarmed process. Input hashing itself warms OS pages."}
    (args.output / "environment.json").write_text(json.dumps(environment, indent=2) + "\n")
    budget = Budget(args.seconds, args.rss_mib)
    runner = Runner(args, budget)
    status = "complete"
    error = None
    try:
        # Prioritize real watcher measurements so the hard budget cannot silently omit them.
        runner.hot(args.synthetic_root / "10000")
        for name, db in inputs.items():
            queries = [("阻塞队列", True, "exact_hit"), ("演示", True, "domain_hit"), ("absentbenchmarkterm", False, "no_hit")] if name == "fake" else [
                ("entity000123", True, "exact_hit"), ("bucket07", True, "many_hits"), ("absentbenchmarkterm", False, "no_hit")]
            runner.static(name, db, queries)
    except Exception as exc:
        status, error = "partial", str(exc)[:500]
    finally:
        runner.close()
        budget.close()
        after = {str(path): digest(path) for path in inputs.values()}
        (args.output / "summary.json").write_text(json.dumps({"status": status, "error": error,
            "measured_elapsed_s": time.monotonic() - budget.start, "budget_stop": budget.reason,
            "cells": runner.cells, "total_samples": len(runner.rows), "errors": sum(not row["ok"] for row in runner.rows),
            "input_db_sha256_after": after, "input_db_unchanged": initial_hashes == after,
            "loadavg_after": list(os.getloadavg()), "all_child_peak_rss_kib": budget.peaks}, indent=2) + "\n")
    return 0 if status == "complete" and initial_hashes == after else 2


if __name__ == "__main__":
    raise SystemExit(main())
