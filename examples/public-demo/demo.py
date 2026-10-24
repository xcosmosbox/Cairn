#!/usr/bin/env python3
"""Run the existing fixed fake and real read-only CLI/MCP; never ingest this directory."""
import datetime
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import threading


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def main():
    root = Path(__file__).resolve().parents[2]
    expected = json.loads((Path(__file__).with_name("expected-queries.json")).read_text())["fixed_fake"]
    (root / ".local").mkdir(exist_ok=True)
    out = Path(tempfile.mkdtemp(prefix="public-demo-", dir=root / ".local"))
    receipt = {"mode": "fixed_fake_not_example_document_ingest", "result": "incomplete", "commands": [], "rpc": []}

    def run(args, expected_exit=0):
        started = datetime.datetime.now(datetime.timezone.utc).isoformat()
        result = subprocess.run(args, cwd=root, text=True, capture_output=True, timeout=180)
        receipt["commands"].append({"args": args, "started_at_utc": started,
                                    "exit_code": result.returncode, "stdout": result.stdout, "stderr": result.stderr})
        require(result.returncode == expected_exit, "Unexpected exit for %r: %s" % (args, result.stderr))
        return result.stdout

    try:
        receipt["source_sha"] = run(["git", "rev-parse", "HEAD"]).strip()
        receipt["example_script_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
        run(["go", "version"])
        run(["make", "build"])
        sessions = root / ".local/cairnd-fake"
        before = set(sessions.glob("fake-session-*"))
        fake = run(["./bin/cairnd", "run", "--once", "--fake", "--config", "configs/cairnd-fake.example.yaml"])
        require("最终状态: Stable" in fake, "Controller did not reach Stable")
        new_sessions = set(sessions.glob("fake-session-*")) - before
        require(len(new_sessions) == 1, "Expected exactly one new isolated fake session")
        session = new_sessions.pop()
        manifests = [p for p in session.rglob("kb-manifest.json") if p.parent.name == "stable"]
        require(len(manifests) == 1, "Expected one actual stable Bundle manifest")
        manifest_path = manifests[0]
        manifest = json.loads(manifest_path.read_text())
        bundle = manifest_path.parent
        db = bundle / "knowledge.db"
        receipt["session"] = str(session)
        receipt["manifest"] = manifest
        require(manifest["kg"] == expected["kg"], "Unexpected demo KG")
        digests = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in bundle.iterdir() if p.is_file()}
        for line in (bundle / "checksums.sha256").read_text().splitlines():
            digest, name = line.split(None, 1)
            require(digests[name.strip()] == digest, "Bundle checksum mismatch")
        with sqlite3.connect(db.as_uri() + "?mode=ro", uri=True) as connection:
            nodes = connection.execute("SELECT id,name,summary FROM nodes WHERE name=?", (expected["keyword"],)).fetchall()
            counts = [connection.execute("SELECT count(*) FROM " + table).fetchone()[0] for table in ("nodes", "edges")]
        require(counts == [expected["nodes"], expected["edges"]], "Unexpected fixed graph size")
        require(len(nodes) == 1 and nodes[0][2] == expected["summary"], "Expected known fixed concept")
        node_id = nodes[0][0]
        receipt["graph_counts"] = {"nodes": counts[0], "edges": counts[1]}
        receipt["node_uuid"] = node_id
        status = run(["./bin/cairn", "--db-path", str(db), "status"])
        require(str(expected["nodes"]) in status, "CLI status did not describe the fixed graph")
        found = run(["./bin/cairn", "--db-path", str(db), "find", expected["keyword"]])
        require(node_id in found and expected["summary"] in found, "CLI known-content query failed")
        absent = run(["./bin/cairn", "--db-path", str(db), "find", expected["not_loaded_keyword"]])
        require("No matching entities found" in absent, "Example documents must not be reported as loaded")
        missing_db = out / "missing.db"
        run(["./bin/cairn", "--db-path", str(missing_db), "status"], expected["missing_db_exit"])
        require(not missing_db.exists(), "Read-only failure created a database")
        config = out / "mcp.yaml"
        # JSON string quoting is also valid YAML scalar syntax; no YAML library is needed.
        config.write_text("mcp:\n  knowledge_bases:\n    - name: demo\n      path: " + json.dumps(str(db)) + "\n")
        receipt["mcp_config"] = str(config)
        with (out / "mcp-stderr.log").open("w") as errors:
            proc = subprocess.Popen([str(root / "bin/cairn-mcp"), "--config", str(config)],
                                    cwd=root, text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors)
            timer = threading.Timer(30, proc.kill)
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
                result = json.loads(proc.stdout.readline())
                receipt["rpc"].append({"request": value, "response": result})
                require(result.get("id") == rid and "error" not in result, "MCP request failed")
                require(not result["result"].get("isError", False), "MCP tool returned error")
                return result["result"]

            def call(rid, name, arguments):
                return request("tools/call", {"name": name, "arguments": arguments}, rid)

            try:
                hello = request("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                                "clientInfo": {"name": "public-demo", "version": "1"}}, 1)
                require(hello["protocolVersion"] == "2024-11-05", "Unexpected MCP protocol")
                request("notifications/initialized")
                search = call(2, "domain_search", {"kg": "demo", "keyword": expected["keyword"]})
                text = search["content"][0]["text"]
                require(node_id in text and expected["summary"] in text, "MCP search did not return the known concept")
                plain = call(3, "resolve_node", {"kg": "demo", "uuid": node_id})
                require(len(plain["content"]) == 1, "Default resolve output changed")
                resolved = call(4, "resolve_node", {"kg": "demo", "uuid": node_id, "include_sources": True})
                require(resolved["content"][0] == plain["content"][0], "Opt-in changed the legacy text")
                sources = json.loads(resolved["content"][1]["text"])
                require(sources["requested_uuid"] == node_id and sources["scope"] == "resolved_node_only", "Source ownership mismatch")
                metadata = sources["source_metadata"]
                require(metadata["node_uuid"] == node_id and metadata["status"] == "available", "Missing real source metadata")
                require(expected["source_ref"] in metadata["source_refs"], "Wrong stored source reference")
                require(any(row["file_path"] == expected["source_path"] and row.get("start_line") == expected["source_start_line"]
                            and row.get("end_line") == expected["source_end_line"] for row in metadata["locations"]), "Wrong stored source span")
                missing = call(5, "resolve_node", {"kg": "demo", "uuid": "public-demo-missing-uuid", "include_sources": True})
                unknown = json.loads(missing["content"][1]["text"])["source_metadata"]
                require(unknown["status"] == "unknown" and not unknown.get("locations") and not unknown.get("node_uuid"), "Missing node invented a citation")
                no_hit = call(6, "domain_search", {"kg": "demo", "keyword": expected["not_loaded_keyword"]})
                require("No matching results found." in no_hit["content"][0]["text"], "Unloaded document unexpectedly treated as graph evidence")
                proc.stdin.close()
                require(proc.wait(timeout=5) == 0, "MCP server exited unsuccessfully")
            finally:
                timer.cancel()
                if proc.poll() is None:
                    proc.kill()
                    proc.wait()
        after = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in bundle.iterdir() if p.is_file()}
        require(digests == after, "Queries changed Bundle bytes")
        receipt["bundle_sha256_before_and_after"] = after
        receipt["result"] = "pass_fixed_fake_only"
        receipt["multi_document_consumption"] = "not_run_not_supported_by_fixed_fake"
        print("PASS: fixed fake → Stable Bundle → CLI → real MCP → stored source metadata")
        print("PASS: missing DB exit 1, missing UUID unknown, sample-only query zero hits; Bundle unchanged")
        print("NOT RUN: extraction or graph relationships from the synthetic multi-document input")
    finally:
        receipt["finished_at_utc"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        (out / "receipt.json").write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n")
        print("Receipt: " + str(out / "receipt.json"))


if __name__ == "__main__":
    main()
