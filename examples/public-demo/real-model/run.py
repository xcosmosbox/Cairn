#!/usr/bin/env python3
"""Prepare frozen inputs, then explicitly opt in to the existing real builder.

No mode publishes remotely. prepare/audit never read the model key or call a model.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
INPUT = HERE.parent
ENDPOINT = "https://api.deepseek.com/chat/completions"
MODEL = "deepseek-flash"
FILES = ("SKILL.md", "references/queue.md", "references/timeout.md")
REFS = set(FILES[1:])


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def require(ok, message):
    if not ok:
        raise ValueError(message)


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def write(path, value):
    Path(path).write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")


def load(path):
    return json.loads(Path(path).read_text())


def verify_inputs(root):
    freeze = load(HERE / "input-freeze.json")
    for entry in freeze["files"]:
        path = root / entry["path"]
        require(path.is_file() and not path.is_symlink(), "Missing/linked frozen input: " + entry["path"])
        require(path.stat().st_size == entry["bytes"] and digest(path) == entry["sha256"],
                "Frozen input identity changed: " + entry["path"])
    require(sorted(str(p.relative_to(root)) for p in root.rglob("*") if p.is_file()) == sorted(FILES),
            "Prepared input contains unexpected files")
    return freeze


def prepare(out):
    require(not out.exists(), "Output already exists; use a new run directory")
    out.mkdir(parents=True)
    source = out / "source"
    source.mkdir()
    for name in FILES:
        (source / name).parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(INPUT / name, source / name)
    freeze = verify_inputs(source)
    # Keep originals separate: the real builder deliberately rewrites source/.
    shutil.copytree(source, out / "original")
    receipt = {"status": "prepared_no_model_call", "time_utc": now(),
               "builder_source_sha": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
               "helper_sha256": digest(__file__), "freeze": freeze,
               "endpoint": ENDPOINT, "requested_model": MODEL,
               "ingest_options": ["--max-tokens", "16384", "--timeout", "180s", "--max-retries", "1",
                                  "--max-rollbacks", "1", "--no-evolve"],
               "deadline_seconds": 600,
               "model_calls": "not_run", "sandbox_publication": "not_run"}
    write(out / "preparation.json", receipt)
    print("Prepared frozen two-reference inputs; no credential read or model request.")


def key_for(args):
    require(args.authorize_deepseek, "External DeepSeek credential use requires --authorize-deepseek")
    require(args.key_file is not None, "--key-file is required for live modes")
    key = args.key_file.read_text().strip()
    require(bool(key), "Runtime key file is empty")
    return key


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("Official endpoint redirected; credential forwarding refused")


def canary(args):
    key = key_for(args)
    out = args.out
    require((out / "preparation.json").is_file(), "Run prepare first")
    require(not (out / "canary.json").exists(), "Canary receipt already exists; do not overwrite")
    body = {"model": MODEL, "messages": [
        {"role": "system", "content": "Return JSON only."},
        {"role": "user", "content": 'Connectivity canary. Return {"ok":true} as JSON.'}],
        "thinking": {"type": "disabled"}, "response_format": {"type": "json_object"},
        "max_tokens": 64, "stream": False}
    receipt = {"started_at_utc": now(), "endpoint": ENDPOINT, "requested_model": MODEL,
               "thinking": "disabled", "json_mode": True, "max_tokens": 64,
               "status": "failed", "attempts": 1}
    started = time.monotonic()
    try:
        request = urllib.request.Request(ENDPOINT, json.dumps(body).encode(),
                                         {"Authorization": "Bearer " + key, "Content-Type": "application/json"})
        with urllib.request.build_opener(NoRedirect()).open(request, timeout=45) as response:
            data = json.load(response)
            receipt.update(http_status=response.status, request_id=response.headers.get("x-request-id"),
                           response_id=data.get("id"), returned_model=data.get("model"), usage=data.get("usage"))
        choice = data["choices"][0]
        require(choice.get("finish_reason") == "stop", "Canary response incomplete")
        require(json.loads(choice["message"]["content"]) == {"ok": True}, "Canary content mismatch")
        # Preserve provider-reported model ID; never infer an undocumented fallback.
        require(receipt["returned_model"] in (MODEL, "deepseek-v4.1-flash-1017"),
                "Unexpected returned model; stop and reconcile official model identity")
        receipt["finish_reason"] = choice["finish_reason"]
        receipt["status"] = "pass_connectivity_only"
    except Exception as error:
        receipt["error_type"] = type(error).__name__
        # HTTP/provider error bodies may echo secrets. Record status, never body/header/text.
        if isinstance(error, urllib.error.HTTPError):
            receipt["http_status"] = error.code
        raise ValueError("Canary failed; sanitized receipt retains error type/status") from None
    finally:
        receipt.update(finished_at_utc=now(), elapsed_seconds=round(time.monotonic() - started, 3))
        write(out / "canary.json", receipt)
    print("Connectivity canary passed; no graph or quality result.")


def smoke(args):
    key = key_for(args)
    out = args.out
    require(load(out / "canary.json")["status"] == "pass_connectivity_only", "Successful exact-model canary required")
    require(not (out / "ingest.json").exists(), "Ingest already attempted; preserve attempt and use a new directory")
    verify_inputs(out / "source")
    verify_inputs(out / "original")
    binary = ROOT / "bin/cairn-ingest"
    command = [str(binary), "--repo", str(out / "source"), "--db", str(out / "knowledge.db"),
               "--repository-identity", "cairn-public-real-two-doc-v1", "--model", MODEL, "--endpoint", ENDPOINT,
               "--dump-dir", str(out / "dump")] + load(out / "preparation.json")["ingest_options"]
    receipt = {"started_at_utc": now(), "command": command, "binary_sha256": digest(binary),
               "requested_model": MODEL, "returned_model": "not_retained_by_product_client",
               "thinking": "enabled_product_default_high", "json_mode": True,
               "external_deadline_seconds": 600, "exit_code": None, "status": "failed"}
    env = dict(os.environ)
    env["CAIRN_LLM_API_KEY"] = key
    started = time.monotonic()
    log = ""
    try:
        result = subprocess.run(command, cwd=ROOT, env=env, capture_output=True, text=True, timeout=600)
        log = result.stdout + result.stderr
        receipt["exit_code"] = result.returncode
        require(result.returncode == 0, "Existing builder failed; inspect sanitized ingest.log")
        receipt["status"] = "completed_needs_audit"
    except subprocess.TimeoutExpired as error:
        log = (error.stdout or b"") + (error.stderr or b"")
        if isinstance(log, bytes):
            log = log.decode(errors="replace")
        receipt["timed_out"] = True
        raise ValueError("Builder exceeded 600s; attempt retained, no automatic retry") from None
    finally:
        log = log.replace(key, "[REDACTED]")
        (out / "ingest.log").write_text(log)
        usage = re.findall(r"\[llm-extract\].*?tokens\(prompt=(\d+) completion=(\d+)\)", log)
        receipt.update(successful_call_logs=len(usage),
                       prompt_tokens_successful_calls=sum(int(x[0]) for x in usage),
                       completion_tokens_successful_calls=sum(int(x[1]) for x in usage),
                       usage_scope="successful logged responses only; errors/retries may incur additional usage",
                       finished_at_utc=now(), elapsed_seconds=round(time.monotonic() - started, 3))
        write(out / "ingest.json", receipt)
    print("Existing builder completed; run audit for consumption/provenance/MCP checks.")


def rpc_checks(out, targets):
    config = out / "mcp.yaml"
    config.write_text("mcp:\n  knowledge_bases:\n    - name: real-demo\n      path: " + json.dumps(str(out / "knowledge.db")) + "\n")
    receipts = []
    with (out / "mcp-stderr.log").open("w") as errors:
        proc = subprocess.Popen([str(ROOT / "bin/cairn-mcp"), "--config", str(config)], cwd=ROOT,
                                text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors)
        timer = threading.Timer(45, proc.kill)
        timer.start()
        def request(method, params=None, rid=None):
            value = {"jsonrpc": "2.0", "method": method}
            if params is not None:
                value["params"] = params
            if rid is not None:
                value["id"] = rid
            proc.stdin.write(json.dumps(value, ensure_ascii=False) + "\n")
            proc.stdin.flush()
            if rid is None:
                return None
            response = json.loads(proc.stdout.readline())
            receipts.append({"request": value, "response": response})
            require(response.get("id") == rid and "error" not in response, "MCP RPC failed")
            require(not response["result"].get("isError"), "MCP tool error")
            return response["result"]
        try:
            request("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                   "clientInfo": {"name": "real-two-doc-smoke", "version": "1"}}, 1)
            request("notifications/initialized")
            rid = 2
            for target in targets:
                result = request("tools/call", {"name": "domain_search", "arguments":
                                 {"kg": "real-demo", "keyword": target["name"]}}, rid)
                require(target["id"] in json.dumps(result, ensure_ascii=False), "MCP name search missed target UUID")
                result = request("tools/call", {"name": "resolve_node", "arguments":
                                 {"kg": "real-demo", "uuid": target["id"], "include_sources": True}}, rid + 1)
                metadata = json.loads(result["content"][1]["text"])["source_metadata"]
                require(metadata["node_uuid"] == target["id"] and metadata["status"] == "available",
                        "MCP source ownership mismatch")
                require(any(x["file_path"] == target["file_path"] for x in metadata["locations"]),
                        "MCP omitted selected reference path")
                rid += 2
            result = request("tools/call", {"name": "resolve_node", "arguments":
                             {"kg": "real-demo", "uuid": "public-demo-unknown-v1", "include_sources": True}}, rid)
            unknown = json.loads(result["content"][1]["text"])["source_metadata"]
            require(unknown["status"] == "unknown" and not unknown.get("locations"), "Unknown UUID invented citations")
            proc.stdin.close()
            require(proc.wait(timeout=5) == 0, "MCP server did not close cleanly")
        finally:
            timer.cancel()
            if proc.poll() is None:
                proc.kill()
                proc.wait()
            write(out / "mcp-rpc.json", receipts)


def audit(out):
    require(load(out / "ingest.json")["status"] == "completed_needs_audit", "Builder completion required")
    require(not (out / "audit.json").exists(), "Audit receipt exists; preserve previous attempt")
    verify_inputs(out / "original")
    report = {"status": "failed", "started_at_utc": now(), "original_span_semantics":
              "04 source_span is an unverified model annotation on frozen originals; node_sources/MCP spans refer to rewritten documents",
              "cross_document_semantics": "manual_review_required", "sandbox_publication": "not_run"}
    db_path = out / "knowledge.db"
    before = digest(db_path)
    try:
        docs = load(out / "dump/04_extract_input.json")
        require(len(docs) == 2 and {d["file_path"] for d in docs} == REFS and all(d["items"] for d in docs),
                "Both nonempty reference documents must reach actual extraction")
        db = sqlite3.connect(db_path.as_uri() + "?mode=ro", uri=True)
        db.row_factory = sqlite3.Row
        try:
            require(db.execute("PRAGMA quick_check").fetchone()[0] == "ok", "SQLite quick_check failed")
            sources = [dict(x) for x in db.execute("SELECT * FROM node_sources ORDER BY file_path,member_id,node_uuid")]
            require({s["file_path"] for s in sources} == REFS, "Source ledger omitted/added input paths")
            nodes = [dict(x) for x in db.execute("SELECT id,label,name,summary,description,provenance FROM nodes ORDER BY id")]
            edges = [dict(x) for x in db.execute("SELECT source_id,target_id,kind,description,provenance FROM edges ORDER BY id")]
        finally:
            db.close()
        items = {(d["file_path"], i["id"]): i for d in docs for i in d["items"]}
        for source in sources:
            require((source["file_path"], source["member_id"]) in items, "Ledger lacks original annotation member")
            require(source["start_line"] and source["end_line"] >= source["start_line"], "Missing rewritten source span")
            lines = (out / "source" / source["file_path"]).read_text().splitlines()
            require(source["end_line"] <= len(lines), "Rewritten span exceeds document")
            require((out / "source" / (source["file_path"] + ".kg.yaml")).is_file(), "Missing source sidecar")
        targets = []
        for path in sorted(REFS):
            source = next(s for s in sources if s["file_path"] == path)
            node = next(n for n in nodes if n["id"] == source["node_uuid"])
            targets.append(dict(node, file_path=path))
        command = [str(ROOT / "bin/cairn"), "--db-path", str(db_path), "status"]
        status = subprocess.run(command, cwd=ROOT, text=True, capture_output=True, timeout=30)
        write(out / "cli-status.json", {"command": command, "exit_code": status.returncode,
                                        "stdout": status.stdout, "stderr": status.stderr})
        require(status.returncode == 0, "CLI status failed")
        rpc_checks(out, targets)
        after = digest(db_path)
        require(before == after, "Queries modified database bytes")
        report.update(status="pass_structural_consumption_and_mcp", nodes=nodes, edges=edges,
                      source_ledger=sources, original_annotations=docs, db_sha256_before_and_after=after,
                      expected_names={name: any(name in n["name"] for n in nodes) for name in ("阻塞队列", "重试预算")},
                      shared_nodes=[n["id"] for n in nodes if len({s["file_path"] for s in sources if s["node_uuid"] == n["id"]}) > 1])
    finally:
        report["finished_at_utc"] = now()
        write(out / "audit.json", report)
    print("Structural consumption/provenance/MCP checks passed; semantic relation/fact review remains separate.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("prepare", "canary", "smoke", "audit"))
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--key-file", type=Path)
    parser.add_argument("--authorize-deepseek", action="store_true",
                        help="explicit live credential-use opt-in, only after applicable authorization/review")
    args = parser.parse_args()
    args.out = args.out.resolve()
    # All output stays in the ignored runtime area, never tracked example inputs.
    require(args.out.is_relative_to(ROOT / ".local/real-model"), "--out must be beneath repository .local/real-model/")
    if args.mode == "prepare":
        prepare(args.out)
    elif args.mode == "canary":
        canary(args)
    elif args.mode == "smoke":
        smoke(args)
    else:
        audit(args.out)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, json.JSONDecodeError) as error:
        # Do not print credential paths, provider bodies or inherited environments.
        print("Real demo failed: " + (str(error) if isinstance(error, ValueError) else type(error).__name__), file=sys.stderr)
        sys.exit(1)
