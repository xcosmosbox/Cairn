# Restore a Cairn workspace

This is a portable repository workflow, not evidence that a saved cloud environment has
already restored. Task #10 owns actual verification and environment-specific instructions.

## Inspect before running

Clone `https://github.com/xcosmosbox/Cairn.git` or open a clean task worktree. Read
AGENTS.md and CONTRIBUTING.md. Record `git rev-parse HEAD`, `git status --short`, OS,
`go version`, `node --version`, `npm --version` and `python3 --version`.
Use Go 1.22+, Node 22+ for Viewer, Python 3 for evaluation checks, and a C toolchain
for Go race/SQLite builds. Dependency download needs the corresponding network access.
An existing cloud environment may have a separate activation script; locate and record
its exact path rather than assuming it exists or inventing commands.

## Free acceptance path

Run from the repository root, saving logs and exit codes:

```bash
make verify
go test -race -count=1 ./...
python3 -m unittest discover -s e2e/quality -p 'test_*.py'
python3 scripts/dev/zz_smoke_mcp_test.py
```

In `graph-viewer` run `npm ci` and `npm run test:subpath` (includes the deployment build).
Use `npm ci`, not lockfile-regenerating updates. These commands do not need a model key.

Create the isolated fake demo from the repository root:

```bash
./bin/cairnd run --once --fake --config configs/cairnd-fake.example.yaml
```

Record its printed session directory, final `Stable` state and generated Bundle. Inspect
the actual output manifest and locate its knowledge DB; do not guess the filesystem path.
Copy `configs/mcp-local.example.yaml` to an ignored local config and point its DB path
and KG name to this demo. Run `./bin/cairn --db-path <db> status`, one known-content
query, then `scripts/dev/smoke-mcp.sh <local-config> <kg-name>`.
Read `scripts/dev/smoke-mcp.sh` options and require nonempty, semantically valid demo
results rather than accepting HTTP/stdio transport success alone. Save the commands
with concrete local arguments in the report, not the angle-bracket placeholders above.

Fake mode verifies controller flow using fixed synthetic knowledge. It does not exercise
real model extraction, production GitHub App permissions or production token refresh.

## Finish or diagnose

Compare worktree/lockfile state before and after; do not edit tracked source/locks as part
of #10 verification. Record pass/fail/blocked/not-run separately. Attribute missing Go,
network access, credentials or activation paths to environment blockers. Attribute a
reproduced code defect to a new Issue with exact steps. Do not rewrite baselines to pass.
Post the task checkpoint and link a committed report; never claim a command ran if it
only appears in this guide. No authentication repair or model spending is implied here.

## Recorded fresh-environment attempt

The [2026-10-24 Issue #10 report](reports/issue10-restore-20261024-SYe4dbpU/README.md)
records a clean checkout at `faf8ee705112f6c7272706d33cdd787b247353f0` with passing
Python/Viewer checks and a missing Go toolchain. Full free runtime acceptance remains
blocked; neither a fake Bundle nor an actual MCP knowledge query was produced there.
The report includes command exits, public logs, source/lockfile integrity and recovery steps.
Use the live #10 checkpoint for the latest state; this dated attempt is not proof that a
future environment has restored. If Go is missing, record the exact activation/install
path supplied by the environment owner and its version before rerunning dependent gates.
