#!/usr/bin/env python3
"""Free synthetic operator replay; no daemon, Catalog, credentials or model."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import sqlite3
import subprocess
import tempfile


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inventory(directory):
    return {str(p.relative_to(directory)): {"sha256": sha(p), "bytes": p.stat().st_size,
            "inode": p.stat().st_ino, "mtime_ns": p.stat().st_mtime_ns}
            for p in sorted(directory.rglob("*")) if p.is_file()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, type=Path, help="new receipt directory (must not exist)")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    work = Path(tempfile.mkdtemp(prefix="cairn-local-recovery-"))
    receipt = {"result": "incomplete", "scope": "synthetic_linux_local_cli_stdio_mcp",
               "commands": [], "rpc": [], "started_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
               "platform": platform.platform(), "work_directory": str(work)}

    def run(argv, expected=0, timeout=240, error_contains=None):
        try:
            proc = subprocess.run([str(a) for a in argv], cwd=root, text=True,
                                  capture_output=True, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            receipt["commands"].append({"argv": [str(a) for a in argv], "timeout_seconds": timeout,
                                        "stdout": str(error.stdout), "stderr": str(error.stderr)})
            raise
        receipt["commands"].append({"argv": [str(a) for a in argv], "exit_code": proc.returncode,
                                    "stdout": proc.stdout, "stderr": proc.stderr})
        require(proc.returncode == expected, "unexpected exit: %r\n%s" % (argv, proc.stderr))
        if error_contains:
            require(error_contains in proc.stderr, "wrong rejection reason: " + proc.stderr)
        return proc.stdout

    def query(word, absent, schema):
        current = work / "installed/recovery/current/knowledge.db"
        found = run([binary["cairn"], "--db-path", current, "find", word])
        require(word in found and "recovery-" in found, "CLI lost current content")
        require("No matching entities found" in run([binary["cairn"], "--db-path", current, "find", absent]), "CLI returned retired content")
        run([binary["cairn"], "--db-path", current, "status"])
        cfg = work / "mcp.yaml"
        cfg.write_text("mcp:\n  knowledge_bases:\n    - name: recovery\n      path: " + json.dumps(str(current)) + "\n")
        requests = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "local-recovery", "version": "1"}}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
        ]
        for rid, name, arguments in [
            (2, "domain_status", {"kg": "recovery"}),
            (3, "domain_search", {"kg": "recovery", "keyword": word}),
            (4, "domain_search", {"kg": "recovery", "keyword": absent}),
            (5, "resolve_node", {"kg": "recovery", "uuid": "recovery-" + ("a" if schema == 4 else "b"), "include_sources": True}),
        ]:
            requests.append({"jsonrpc": "2.0", "id": rid, "method": "tools/call", "params": {"name": name, "arguments": arguments}})
        proc = subprocess.run([str(binary["cairn-mcp"]), "--config", str(cfg)], cwd=root,
                              input="".join(json.dumps(r) + "\n" for r in requests), text=True,
                              capture_output=True, timeout=30)
        responses = [json.loads(line) for line in proc.stdout.splitlines() if line.strip()]
        receipt["rpc"].append({"schema": schema, "word": word, "requests": requests,
                               "responses": responses, "exit_code": proc.returncode, "stderr": proc.stderr})
        require(proc.returncode == 0 and len(responses) == 5, "MCP process/protocol failed")
        by_id = {r.get("id"): r for r in responses}
        require(set(by_id) == {1, 2, 3, 4, 5}, "MCP response IDs wrong")
        for response in responses:
            require("error" not in response and not response["result"].get("isError"), "MCP business error")
        require(by_id[1]["result"]["protocolVersion"] == "2024-11-05", "MCP negotiation changed")
        require("Schema Version: %d" % schema in by_id[2]["result"]["content"][0]["text"], "MCP schema mismatch")
        require(word in by_id[3]["result"]["content"][0]["text"], "MCP lost content")
        require("No matching results found." in by_id[4]["result"]["content"][0]["text"], "MCP returned retired content")
        metadata = json.loads(by_id[5]["result"]["content"][1]["text"])["source_metadata"]
        require(metadata["status"] == "available" and "synthetic/reference.md" in metadata["source_refs"], "stored source reference lost")
        require(any(x["file_path"] == "synthetic/reference.md" and x["start_line"] == 3 and x["end_line"] == 5 for x in metadata["locations"]), "stored source span lost")

    try:
        receipt["source_sha"] = run(["git", "rev-parse", "HEAD"]).strip()
        receipt["source_status"] = run(["git", "status", "--porcelain"])
        receipt["scripts"] = {str(p.relative_to(root)): sha(p) for p in [Path(__file__).resolve(), root / "scripts/release/fixture/main.go"]}
        receipt["go_version"] = run(["go", "version"]).strip()
        receipt["build_environment"] = {k: os.environ.get(k, "") for k in ["GOFLAGS", "GOTOOLCHAIN", "GOPATH", "GOCACHE"]}
        binary = {}
        for name, package in [("cairn", "./service/cmd/cairn"), ("cairn-mcp", "./service/cmd/cairn-mcp"), ("cairnctl", "./build/cmd/cairnctl"), ("fixture", "./scripts/release/fixture")]:
            binary[name] = work / name
            run(["go", "build", "-o", binary[name], package])
        receipt["binaries"] = {name: sha(path) for name, path in binary.items()}
        fixtures = work / "fixtures"
        run([binary["fixture"], "create", fixtures])
        manifests = {name: json.loads((fixtures / name / "kb-manifest.json").read_text()) for name in ["a", "b"]}
        receipt["manifests"] = manifests
        initial = {name: inventory(fixtures / name) for name in ["a", "b"]}
        installed_initial = {}
        for name, word, absent, schema in [("a", "LegacyAnchor", "ModernAnchor", 4), ("b", "ModernAnchor", "LegacyAnchor", 5), ("a", "LegacyAnchor", "ModernAnchor", 4)]:
            run([binary["fixture"], "verify", fixtures / name])
            run([binary["cairnctl"], "bundle", "pull", "--kg", "recovery", "--install-dir", work / "installed", "--src-dir", fixtures / name])
            current = work / "installed/recovery/current"
            resolved = current.resolve(strict=True)
            manifest = json.loads((resolved / "kb-manifest.json").read_text())
            require(manifest == manifests[name], "install changed immutable manifest")
            require(resolved.name == manifest["bundle_digest"].removeprefix("sha256:")[:16], "installed generation path does not bind digest")
            before = inventory(resolved)
            require({k: v["sha256"] for k, v in before.items()} == {k: v["sha256"] for k, v in initial[name].items()}, "installed Bundle differs from source bytes")
            if name in installed_initial:
                require(before == installed_initial[name], "reinstall rewrote preserved generation")
            else:
                installed_initial[name] = before
            with sqlite3.connect((resolved / "knowledge.db").as_uri() + "?mode=ro", uri=True) as db:
                require(db.execute("PRAGMA user_version").fetchone()[0] == schema, "schema mismatch")
                columns = [r[1] for r in db.execute("PRAGMA table_info(nodes)")]
                require(("file_slug" in columns) == (schema == 5), "fixture is not true schema4/5 shape")
            query(word, absent, schema)
            require(inventory(resolved) == before, "CLI/MCP changed installed generation")
            receipt.setdefault("transitions", []).append({"generation": name, "current": str(resolved), "inventory": before})
        run([binary["cairnctl"], "bundle", "list", "--kg", "recovery", "--install-dir", work / "installed"])
        healthy = (work / "installed/recovery/current").resolve()
        healthy_inventory = inventory(healthy)
        for defect in ["checksum", "contract"]:
            bad = fixtures / ("bad-" + defect)
            shutil.copytree(fixtures / "b", bad)
            if defect == "checksum":
                with (bad / "knowledge.db").open("ab") as f:
                    f.write(b"checksum mismatch")
            else:
                run([binary["fixture"], "break-contract", bad])
                for line in (bad / "checksums.sha256").read_text().splitlines():
                    digest, filename = line.split(None, 1)
                    require(sha(bad / filename.strip()) == digest, "bad-contract fixture checksum is invalid")
                # Verify reaches actual-schema validation only after aggregate and all checksums pass.
                run([binary["fixture"], "verify", bad], expected=1, error_contains="KG query contract")
            before = inventory(bad)
            run([binary["cairnctl"], "bundle", "pull", "--kg", "recovery", "--install-dir", work / "installed", "--src-dir", bad], expected=1, error_contains="摘要不匹配" if defect == "checksum" else "KG query contract")
            require((work / "installed/recovery/current").resolve() == healthy, "rejection changed current")
            require(inventory(healthy) == healthy_inventory and inventory(bad) == before, "rejection changed bytes")
        query("LegacyAnchor", "ModernAnchor", 4)
        require(inventory(healthy) == healthy_inventory, "restored generation changed after rejection")
        for name in ["a", "b"]:
            require(inventory(fixtures / name) == initial[name], "source Bundle changed")
            require(inventory(work / "installed/recovery" / manifests[name]["bundle_digest"].removeprefix("sha256:")[:16]) == installed_initial[name], "retained generation changed")
        receipt["source_bundle_inventories"] = initial
        receipt["result"] = "pass"
        print("PASS: schema4 A -> schema5 B -> preserved A; CLI/MCP/source/negative checks; immutable bytes; both bad-generation rejections")
    finally:
        receipt["finished_at_utc"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        (out / "receipt.json").write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n")
        print("Receipt: " + str(out / "receipt.json"))


if __name__ == "__main__":
    main()
