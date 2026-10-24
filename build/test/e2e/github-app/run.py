#!/usr/bin/env python3
"""Run explicitly opted-in native live tests, saving only secret-safe stdout receipts."""
import argparse
import datetime
import json
import os
from pathlib import Path
import subprocess
import sys

parser = argparse.ArgumentParser()
parser.add_argument("--output", required=True, type=Path)
args = parser.parse_args()
if os.environ.get("CAIRN_LIVE_GITHUB_APP") != "1" or not os.environ.get("CAIRN_APP_PRIVATE_KEY_FILE"):
    raise SystemExit("Requires CAIRN_LIVE_GITHUB_APP=1 and a user-supplied runtime key file.")
root = Path(__file__).resolve().parents[4]
args.output.mkdir(parents=True, exist_ok=False)
source_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
summary = {"source_sha": source_sha, "app_id":5454525, "installation_id":174795212,
           "repository":"xcosmosbox/cairn-app-sandbox", "key_reference":"user supplied runtime file", "commands":[]}
for name, command in [
    ("01-native-auth-live", ["go", "test", "-tags=livegithubapp", "-count=1", "-v", "-run", "^TestLiveAppAcceptance$", "./core/githubapp"]),
    ("02-native-forge-live", ["go", "run", "-buildvcs=false", "./build/test/e2e/github-app"]),
]:
    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    with (args.output / (name + ".log")).open("w") as log:
        log.write(json.dumps({"command":command,"source_sha":source_sha,"started_utc":started})+"\n")
        log.flush()
        result = subprocess.run(command, cwd=root, stdout=log, stderr=subprocess.STDOUT, timeout=650)
        log.write(json.dumps({"exit_code":result.returncode,"ended_utc":datetime.datetime.now(datetime.timezone.utc).isoformat()})+"\n")
    summary["commands"].append({"command":command,"exit_code":result.returncode,"log":name+".log"})
    (args.output / "commands.json").write_text(json.dumps(summary, indent=2)+"\n")
    print(json.dumps(summary["commands"][-1]), flush=True)
    if result.returncode:
        sys.exit(result.returncode)
