#!/usr/bin/env python3
"""Explicit opt-in live recovery rehearsal in the fixed private GitHub sandbox."""
import argparse
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import select
import shutil
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

REPO = "xcosmosbox/cairn-app-sandbox"
REPO_ID = 1432125826


def require(value, message):
    if not value:
        raise RuntimeError(message)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inventory(directory):
    return {str(p.relative_to(directory)): {"sha256": sha(p), "bytes": p.stat().st_size,
            "inode": p.stat().st_ino, "mtime_ns": p.stat().st_mtime_ns}
            for p in sorted(directory.rglob("*")) if p.is_file()}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--live-sandbox", action="store_true", required=True)
    parser.add_argument("--key-file", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    require(args.live_sandbox, "explicit sandbox opt-in required")
    root = Path(__file__).resolve().parents[2]
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    work = Path(tempfile.mkdtemp(prefix="cairn-remote-recovery-"))
    token_file = work / "runtime-token"
    receipt = {"result": "incomplete", "scope": "private_sandbox_remote_catalog_native_app_mcp",
               "source_sha": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
               "commands": [], "http": [], "rpc": [], "remote_objects": [], "pointer_commits": [],
               "started_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
               "script_sha256": sha(Path(__file__)), "helper_sha256": sha(root / "scripts/release/remote_fixture/main.go")}
    deadline = None
    proc = None
    errors = None
    token = ""
    opener = urllib.request.build_opener(NoRedirect)

    def run(argv, redact=False):
        result = subprocess.run([str(a) for a in argv], cwd=root, capture_output=True, text=True, timeout=240)
        recorded = [str(a) for a in argv]
        if redact:
            recorded[-2:] = ["<runtime-key>", "<private-token-output>"]
        receipt["commands"].append({"argv": recorded, "exit_code": result.returncode,
                                    "stdout": result.stdout, "stderr": result.stderr.replace(str(args.key_file), "<runtime-key>")})
        require(result.returncode == 0, "local preparation/helper command failed")
        return result.stdout

    def remaining():
        require(deadline is None or time.monotonic() < deadline, "live ten-minute limit exceeded")
        return 30 if deadline is None else max(0.1, min(30, deadline-time.monotonic()))

    def api(method, path, payload=None, raw=None):
        remaining()
        url = path if path.startswith("https:") else "https://api.github.com" + path
        parsed = urllib.parse.urlparse(url)
        require(parsed.hostname in ["api.github.com", "uploads.github.com"], "unapproved API/upload host")
        data = raw if raw is not None else (json.dumps(payload).encode() if payload is not None else None)
        request = urllib.request.Request(url, data=data, method=method, headers={
            "Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
            "User-Agent": "Cairn-remote-recovery", "Content-Type": "application/gzip" if raw is not None else "application/json"})
        try:
            with opener.open(request, timeout=remaining()) as response:
                body = response.read()
                status = response.status
                request_id = response.headers.get("X-GitHub-Request-Id")
        except urllib.error.HTTPError as error:
            receipt["http"].append({"method": method, "host": parsed.hostname, "path": parsed.path,
                                    "status": error.code, "request_id": error.headers.get("X-GitHub-Request-Id")})
            raise RuntimeError("sandbox HTTP %d on %s %s" % (error.code, method, parsed.path)) from None
        receipt["http"].append({"method": method, "host": parsed.hostname, "path": parsed.path,
                                "status": status, "request_id": request_id})
        return json.loads(body) if body else {}

    rid = 0

    def rpc(name, arguments):
        nonlocal rid
        rid += 1
        request = {"jsonrpc": "2.0", "id": rid, "method": "tools/call", "params": {"name": name, "arguments": arguments}}
        proc.stdin.write(json.dumps(request) + "\n"); proc.stdin.flush()
        require(select.select([proc.stdout], [], [], remaining())[0], "MCP response timeout")
        response = json.loads(proc.stdout.readline())
        receipt["rpc"].append({"request": request, "response": response})
        require(response.get("id") == rid and "error" not in response and not response.get("result", {}).get("isError"), "MCP business/protocol error")
        return response["result"]

    def check(word, absent, letter):
        require(word in rpc("domain_search", {"kg": "recovery", "keyword": word})["content"][0]["text"], "MCP lost selected generation")
        require("No matching results found." in rpc("domain_search", {"kg": "recovery", "keyword": absent})["content"][0]["text"], "MCP returned retired generation")
        sources = json.loads(rpc("resolve_node", {"kg": "recovery", "uuid": "recovery-"+letter, "include_sources": True})["content"][1]["text"])["source_metadata"]
        require(sources["node_uuid"] == "recovery-"+letter and sources["status"] == "available" and sources["locations"][0]["file_path"] == "synthetic/reference.md", "MCP source metadata mismatch")

    def wait_install(digest):
        until = min(deadline, time.monotonic()+60)
        while time.monotonic() < until:
            text = (work / "mcp-stderr.log").read_text()
            require("拉取失败" not in text and "获取 installation token 失败" not in text,
                    "actual watcher pull/auth failure; stopped without retry")
            current = work / "installed/current"
            if current.exists() and json.loads((current / "kb-manifest.json").read_text())["bundle_digest"] == digest and "热切换完成（digest=" + digest + "）" in text:
                return current.resolve()
            require(proc.poll() is None, "MCP exited while waiting for remote generation")
            time.sleep(0.1)
        raise RuntimeError("watcher generation not visible within bounded60s")

    try:
        run(["go", "version"])
        binaries = {}
        for name, package in [("mcp", "./service/cmd/cairn-mcp"), ("fixture", "./scripts/release/fixture"), ("remote-fixture", "./scripts/release/remote_fixture")]:
            binaries[name] = work / name
            run(["go", "build", "-o", binaries[name], package])
        receipt["binaries"] = {k: sha(v) for k, v in binaries.items()}
        run([binaries["fixture"], "create", work / "fixtures"])
        shutil.copytree(work / "fixtures/a", work / "fixtures/bad")
        with (work / "fixtures/bad/knowledge.db").open("ab") as file:
            file.write(b"intentional checksum failure")
        for name in ["a", "b", "bad"]:
            run([binaries["remote-fixture"], "pack", work / "fixtures" / name, work / (name+".tar.gz")])
        receipt["source_inventories"] = {n: inventory(work / "fixtures" / n) for n in ["a", "b", "bad"]}
        receipt["archives"] = {n: {"sha256": sha(work/(n+".tar.gz")), "bytes": (work/(n+".tar.gz")).stat().st_size} for n in ["a", "b", "bad"]}
        deadline = time.monotonic()+600
        receipt["live_started_at_utc"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        run([binaries["remote-fixture"], "token", args.key_file, token_file], redact=True)
        token = token_file.read_text()
        repo = api("GET", "/repos/"+REPO)
        require(repo["id"] == REPO_ID and repo["private"] is True and repo["full_name"] == REPO, "private sandbox identity guard failed")
        base = api("GET", "/repos/"+REPO+"/git/ref/heads/main")["object"]["sha"]
        label = "recovery-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S") + "-" + str(os.getpid())
        branch = "acceptance/"+label
        api("POST", "/repos/"+REPO+"/git/refs", {"ref": "refs/heads/"+branch, "sha": base})
        receipt["remote_branch"] = {"repository": REPO, "branch": branch, "base_sha": base}
        published = {}
        for name in ["a", "b", "bad"]:
            tag = label+"-"+name
            release = api("POST", "/repos/"+REPO+"/releases", {"tag_name": tag, "target_commitish": branch,
                          "name": "Synthetic Catalog recovery "+name, "body": "Private test fixture; no product stable release.", "draft": False, "prerelease": True})
            # Save a created release before attempting upload so partial objects remain recoverable.
            item = {"name": name, "tag": tag, "release_id": release["id"], "html_url": release["html_url"], "asset": None}
            receipt["remote_objects"].append(item)
            asset = api("POST", release["upload_url"].split("{")[0]+"?name=bundle.tar.gz", raw=(work/(name+".tar.gz")).read_bytes())
            item["asset"] = {k: asset.get(k) for k in ["id", "name", "size", "digest", "created_at", "updated_at", "state"]}
            require(asset["size"] == receipt["archives"][name]["bytes"], "uploaded asset size mismatch")
            published[name] = item
        file_sha = None

        def pointer(name):
            nonlocal file_sha
            original = "a" if name == "bad" else name
            manifest = json.loads((work/"fixtures"/original/"kb-manifest.json").read_text())
            manifest.update({"channel": "stable", "published_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"), "release_tag": published[name]["tag"], "asset_name": "bundle.tar.gz"})
            body = {"message": "test(recovery): select "+name, "branch": branch,
                    "content": base64.b64encode(json.dumps(manifest).encode()).decode()}
            if file_sha:
                body["sha"] = file_sha
            changed = api("PUT", "/repos/"+REPO+"/contents/knowledge-bases/recovery/stable.json", body)
            file_sha = changed["content"]["sha"]
            receipt["pointer_commits"].append({"selected": name, "commit": changed["commit"]["sha"], "blob": file_sha, "catalog": manifest})
            return manifest["bundle_digest"]

        digest_a = pointer("a")
        config = "github:\n  app_id: 5454525\n  private_key_file: " + json.dumps(str(args.key_file)) + "\n  api_base_url: https://api.github.com\nmcp:\n  knowledge_bases:\n    - name: recovery\n      path: " + json.dumps(str(work/"installed/current/knowledge.db")) + "\n      catalog_repo: " + REPO + "\n      catalog_branch: " + branch + "\n      manifest_dir: knowledge-bases\n      install_dir: " + json.dumps(str(work/"installed")) + "\n      poll_interval: 2s\n"
        (work/"mcp.yaml").write_text(config)
        errors = (work/"mcp-stderr.log").open("w")
        proc = subprocess.Popen([str(binaries["mcp"]), "--config", str(work/"mcp.yaml")], cwd=root,
                                text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors)
        first = wait_install(digest_a)
        receipt["a_initial_inventory"] = inventory(first)
        # Startup handshake follows actual remote install; the watcher, not the helper, downloaded the Bundle.
        proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "remote-recovery", "version": "1"}}})+"\n"); proc.stdin.flush()
        require(select.select([proc.stdout], [], [], remaining())[0], "MCP initialize timeout")
        receipt["initialize"] = json.loads(proc.stdout.readline())
        proc.stdin.write('{"jsonrpc":"2.0","method":"notifications/initialized"}\n'); proc.stdin.flush()
        check("LegacyAnchor", "ModernAnchor", "a")
        digest_b = pointer("b")
        second = wait_install(digest_b)
        receipt["b_initial_inventory"] = inventory(second)
        check("ModernAnchor", "LegacyAnchor", "b")
        error_offset = len((work/"mcp-stderr.log").read_text())
        pointer("bad")
        until = min(deadline, time.monotonic()+60)
        while time.monotonic() < until:
            text = (work/"mcp-stderr.log").read_text()[error_offset:]
            if "拉取失败" in text:
                require("摘要不匹配" in text, "bad candidate failed for unexpected reason")
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("no bounded bad-candidate rejection observed")
        require((work/"installed/current").resolve() == second and inventory(second) == receipt["b_initial_inventory"], "bad candidate changed healthy B")
        check("ModernAnchor", "LegacyAnchor", "b")
        # Ignore only the already recorded intentional checksum rejection, retaining the full log.
        old_log = (work/"mcp-stderr.log").read_text()
        (work/"prior-rejection.log").write_text(old_log)
        errors.flush()
        # wait_install uses the whole retained log; a separate bounded rollback loop checks only new bytes.
        rollback_offset = len(old_log)
        pointer("a")
        until = min(deadline, time.monotonic()+60)
        while time.monotonic() < until:
            text = (work/"mcp-stderr.log").read_text()[rollback_offset:]
            require("拉取失败" not in text, "rollback actual pull failed")
            if "热切换完成" in text and (work/"installed/current").resolve() == first:
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("original A rollback not observed")
        check("LegacyAnchor", "ModernAnchor", "a")
        require(inventory(first) == receipt["a_initial_inventory"] and inventory(second) == receipt["b_initial_inventory"], "rollback rewrote retained generation")
        for name, item in published.items():
            actual = api("GET", "/repos/"+REPO+"/releases/"+str(item["release_id"]))
            asset = next(a for a in actual["assets"] if a["id"] == item["asset"]["id"])
            require({k: asset.get(k) for k in item["asset"]} == item["asset"], "published asset metadata changed")
        receipt["result"] = "pass"
        print("PASS: actual private Catalog A -> B -> rejected badA -> original A")
    except Exception as error:
        receipt["failure"] = str(error).replace(str(args.key_file), "<runtime-key>").replace(token, "<token>") if token else str(error).replace(str(args.key_file), "<runtime-key>")
        raise
    finally:
        if proc is not None:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill(); proc.wait()
            receipt["mcp_exit_code"] = proc.returncode
        if errors is not None:
            errors.close()
        if (work/"mcp-stderr.log").exists():
            (out/"mcp-stderr.log").write_text((work/"mcp-stderr.log").read_text().replace(str(args.key_file), "<runtime-key>"))
        if token_file.exists():
            token_file.unlink()
        receipt["finished_at_utc"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        (out/"receipt.json").write_text(json.dumps(receipt, ensure_ascii=False, indent=2)+"\n")
        print("Receipt: "+str(out/"receipt.json"))


if __name__ == "__main__":
    main()
