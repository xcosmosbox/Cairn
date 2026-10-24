# Issue #10 fresh workspace verification — blocked checkpoint

As of 2026-10-24 08:55 UTC. Session: `cairn-10-20261024T085233Z-SYe4dbpU`.
Task: [#10](https://github.com/xcosmosbox/Cairn/issues/10).
[Claim](https://github.com/xcosmosbox/Cairn/issues/10#issuecomment-6382218282) and
[coordinator ACK](https://github.com/xcosmosbox/Cairn/issues/10#issuecomment-6382227425).

**Result: partially restored; full free acceptance remains blocked.** The source checkout,
Python regressions and Viewer work in this new environment. Go is unavailable, so no
Cairn binary, fake Bundle, CLI knowledge result or real MCP session was verified here.
No production source, lockfile, shared experiment input or paid model run was changed.
This report does not satisfy every acceptance condition or close #10.

## Source and environment

- Fresh `git clone --branch main --single-branch https://github.com/xcosmosbox/Cairn.git repo`
  completed with exit 0 in a new scratch directory; no prior local Cairn work was reused.
- Main baseline and tested source: `faf8ee705112f6c7272706d33cdd787b247353f0`.
  Remote main was read again before reporting and still matched that SHA.
- Isolated branch: `verify/issue10-restore-20261024-SYe4dbpU`.
- OS: Ubuntu 24.04.3 LTS, Linux x86_64; Git 2.51.1; GCC 13.3.0; Make 4.3.
- Node v24.19.0 (satisfies 22+; CI's exact Node 22 was not run locally), npm 11.9.0,
  Python 3.12.14. Go is missing (`go version` exits 127).
- The provided shell was already active. No activation/setup script was found among
  tracked repository paths; access to any separately saved cloud environment is unknown.
  No activation command was invented or existing toolchain copied from another task.
- Initial worktree was clean. After runtime checks, tracked files still matched baseline;
  all three dependency files are byte-identical. See [integrity receipt](logs/13-worktree-integrity.json).
  `docs/` is ignored by the existing `.gitignore`; this bounded public evidence directory
  must be explicitly added with `git add -f`, without changing ignore rules.

## Commands and results

All command paths below are relative to the repository root unless a Viewer working
directory is stated. Each log starts with command arguments, UTC timestamps and exit code.
The inventory shell's aggregate exit 0 is not a Go success: its output records Go missing.

| Result | Command | Exit | Evidence |
| --- | --- | ---: | --- |
| PASS: inventory captured; Go unavailable | OS/toolchain/source inventory | 0 aggregate | [log](logs/01-environment.log) |
| BLOCKED: Go absent before vet/build | `make verify` | 2 | [log](logs/02-make-verify.log) |
| BLOCKED: Go absent | `go test -race -count=1 ./...` | 127 | [log](logs/03-go-race.log) |
| PASS: 85 tests | `python3 -m unittest discover -s e2e/quality -p 'test_*.py'` | 0 | [log](logs/04-python-quality.log) |
| PASS: 3 synthetic protocol regressions | `python3 scripts/dev/zz_smoke_mcp_test.py` | 0 | [log](logs/05-smoke-script-regressions.log) |
| PASS: clean locked installation | `npm ci --fetch-retries=0 --fetch-timeout=15000 --no-audit --no-fund` in `graph-viewer` | 0 | [log](logs/06-npm-ci.log) |
| PASS: production build, subpath JS/CSS, WASM and fixture loading | `npm run test:subpath` in `graph-viewer` | 0 | [log](logs/07-viewer-subpath.log) |
| BLOCKED: `bin/cairnd` cannot exist without build | `./bin/cairnd run --once --fake --config configs/cairnd-fake.example.yaml` | 127 | [log](logs/08-fake-demo.log) |
| PASS: static architecture checks | `make check-invariants` | 0 | [log](logs/09-invariants.log) |
| PASS | `bash -n scripts/dev/smoke-mcp.sh` | 0 | [log](logs/10-shell-syntax.log) |
| PASS: synthetic receipt fixtures | `python3 e2e/quality/export_receipts.py --self-test` | 0 | [log](logs/11-export-self-test.log) |
| BLOCKED: official Go binary host proxy CONNECT timeout; no download/install | bounded `curl --head` against official Go 1.22.12 archive | 28 | [log](logs/12-go-bootstrap-probe.log) |
| NOT RUN: no fake DB/Bundle | CLI status, known-content query, configured runtime MCP smoke | — | dependent on build + fake run |

[Machine-readable initial command receipts](logs/results.json) cover steps 01–11.
[Artifact manifest](artifact-manifest.json) records byte sizes and SHA256 of each log.
The commit containing this report is the immutable artifact location; the task checkpoint
and PR pin that exact commit. Logs are small, public synthetic/check output, not private data.
Viewer emitted a non-fatal bundle-size warning; npm emitted an environment-config warning.
Neither changed the lockfile or caused a failing exit. Script regression success is not a
claim that the actual Cairn server started or that a knowledge result was semantically valid.

## Blocker and next owner

The environment needs an accessible Go 1.22+ installation and Go module dependencies.
`command -v go` and bounded checks under `/usr/local`, `/usr/lib` and `/opt` found none.
The official `go.dev/dl` and `dl.google.com` HTTPS probes timed out at proxy CONNECT;
the archived dl.google.com probe has exit 28. This is an environment limitation, not a
reproduced production-code defect, so no code-defect Issue was opened. Git clone and npm
installation did work; do not generalize this into an assertion that all networking failed.
A maintainer/environment owner can provide an authorized Go-enabled environment or exact
activation path. Do not weaken checks, copy opaque binaries, change lockfiles or rerun paid
experiments to remove the blocker.

## Recovery

These are **next steps**, not commands claimed to have passed:

```bash
git clone --branch verify/issue10-restore-20261024-SYe4dbpU https://github.com/xcosmosbox/Cairn.git cairn-issue10
cd cairn-issue10
git rev-parse HEAD
git status --short
go version
make verify
go test -race -count=1 ./...
./bin/cairnd run --once --fake --config configs/cairnd-fake.example.yaml
```

Read the latest #10 checkpoint/lease before resuming writes. Record the provided Go
installation/activation path and version, source/result SHA and all new exit codes.
After the fake run, inspect its printed session directory, manifest and actual knowledge
DB path. Copy `configs/mcp-local.example.yaml` to an ignored config, point it at the
verified fake DB and use the real configured KG name. Run CLI status, a query for known
fake content and `scripts/dev/smoke-mcp.sh <local-config> <kg> <known-keyword>` with
concrete arguments in the next report. Require a nonempty, semantically correct result.
No fake session directory, Bundle hash or DB path exists from this attempt to reuse.
Recheck source/lockfile integrity, archive the new logs and ask the maintainer to accept
only after all outstanding runtime conditions have evidence. No background job is left.
