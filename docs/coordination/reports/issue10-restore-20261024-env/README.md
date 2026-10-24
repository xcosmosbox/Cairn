# Issue #10 — restored local free acceptance

As of 2026-10-24 09:30 UTC. Session `cairn-10-20261024T0923Z-env`.
Task: [#10](https://github.com/xcosmosbox/Cairn/issues/10).
[Claim](https://github.com/xcosmosbox/Cairn/issues/10#issuecomment-6382508235) /
[takeover ACK](https://github.com/xcosmosbox/Cairn/issues/10#issuecomment-6382517404).

**All required local free acceptance checks passed at source
`1492bfdab9702f7a214083032c9ec514c07f7906`.** Go was restored from the official archive,
the eight binaries built, race checks passed, the isolated fake controller reached
`Stable`, and CLI plus a real MCP server returned the expected nonempty knowledge result.
No production source, dependency lockfile, shared RESTORE document or paid experiment
changed. Acceptance by the maintainer remains a separate event.

## Environment and reproducible activation

Fresh `git clone https://github.com/xcosmosbox/Cairn.git cairn-env` completed successfully.
Clone HEAD and connector main agreed on the source SHA above; the initial worktree was
clean. The previous session and merged PR #20 are preserved as historical blocked evidence.
This report records a new successful attempt, rather than reclassifying that attempt.

Ubuntu 24.04.3 LTS; Linux 6.18.55 x86_64; Git 2.51.1; GCC 13.3.0; Make 4.3;
Python 3.12.14; Node v24.19.0; npm 11.9.0. Node meets the documented 22+ requirement;
this local run does not establish exact Node 22 execution. See [inventory](logs/02-environment.log).
Go was initially absent from PATH and checked runtime/standard/cache locations.
No separate saved-environment activation script was established.

Official download HEAD probes now returned HTTP 200 for both the Go archive and official
toolchain module endpoint. The archive GET completed; the first metadata request needed
a bounded retry ([metadata log](logs/01-go-download-metadata.log)). An Ubuntu mirror probe
and a separate checksum-sidecar probe timed out. Thus networking was intermittent, not
uniformly unavailable. No proxy variables, cookies or signed redirect URLs are published.
The archive was extracted only after matching the official metadata:

| Item | Verified value |
| --- | --- |
| Archive | `go1.22.12.linux-amd64.tar.gz` |
| Official source | `https://go.dev/dl/go1.22.12.linux-amd64.tar.gz` |
| Official metadata | `https://go.dev/dl/?mode=json&include=all` |
| Bytes | 68,995,422 |
| SHA256 | `4fa4f869b0f7fc6bb1eb2660e74657fbf04cdd290b5aef905585c86051b34d43` |
| Actual executable | `/workspace/scratch/110c4593aeb1/cairn-env/.local/toolchain/go/bin/go` |
| Actual version | `go version go1.22.12 linux/amd64` |

[Install verification](logs/06-go-install.log) records the official JSON entry, measured
size/hash and version. This is a pinned reproduction toolchain, not a recommendation that
this historical Go patch is the current release. From a clean repository root, recreate it:

```bash
mkdir -p .local/toolchain
curl --fail --location --silent --show-error --connect-timeout 8 --max-time 90 \
  -o .local/toolchain/go1.22.12.linux-amd64.tar.gz \
  https://go.dev/dl/go1.22.12.linux-amd64.tar.gz
printf '%s  %s\n' \
  4fa4f869b0f7fc6bb1eb2660e74657fbf04cdd290b5aef905585c86051b34d43 \
  .local/toolchain/go1.22.12.linux-amd64.tar.gz | sha256sum --check -
tar -xzf .local/toolchain/go1.22.12.linux-amd64.tar.gz -C .local/toolchain
export PATH="$PWD/.local/toolchain/go/bin:$PATH"
export GOPATH="$PWD/.local/gopath"
export GOCACHE="$PWD/.cache/go-build"
export GOTOOLCHAIN=local
go version
```

The command runner applied exactly these PATH/GOPATH/GOCACHE/GOTOOLCHAIN settings to
the checks below; see its [saved source](scripts/verify.py). Module downloads succeeded.
`npm ci --offline` used the existing npm cache. Reproduction needs accessible official
downloads/modules or a preexisting verified toolchain and dependency cache.

## Actual commands and outcomes

All commands ran in the isolated `cairn-env` checkout at the stated source SHA. Each log
starts with exact arguments, working directory, UTC start/end timestamps and exit code.
The daemon's own log lines use the process wall-clock zone; the receipt header is UTC.

| Result | Command / purpose | Exit | Evidence |
| --- | --- | ---: | --- |
| PASS | `make verify` — vet, eight binaries, invariants | 0 | [log](logs/09-make-verify.log) |
| PASS | `go test -race -count=1 ./...` | 0 | [log](logs/10-go-race.log) |
| PASS | `npm ci --offline --no-audit --no-fund` in graph-viewer | 0 | [log](logs/03-npm-ci.log) |
| PASS | `npm run test:subpath` — build, JS/CSS/WASM, fixture | 0 | [log](logs/07-viewer-subpath.log) |
| PASS | Python quality regressions, 85 tests | 0 | [log](logs/04-python-quality.log) |
| PASS | Synthetic MCP script regressions, 3 tests | 0 | [log](logs/05-mcp-script-regressions.log) |
| PASS | Architecture invariants | 0 | [log](logs/08-invariants.log) |
| PASS | Export receipt self-test / shell syntax | 0 / 0 | [receipt](logs/11-export-receipts.log), [syntax](logs/12-shell-syntax.log) |
| PASS | `./bin/cairnd run --once --fake --config configs/cairnd-fake.example.yaml` | 0 | [log](logs/13-fake-demo.log) |
| PASS | Actual controller, manifest, checksums, DB and concrete MCP config inspection | 0 | [log](logs/14-fake-artifact-inspection.log) |
| PASS | CLI `status` on the concrete DB | 0 | [log](logs/15-cli-status.log) |
| PASS | CLI `find 阻塞队列` on the concrete DB | 0 | [log](logs/16-cli-query.log) |
| PASS | `bash scripts/dev/smoke-mcp.sh configs/mcp-issue10-env.yaml demo 阻塞队列` | 0 | [log](logs/17-mcp-smoke.log) |
| PASS | Real MCP raw response, known ID/name/summary, nonempty result and unchanged Bundle bytes | 0 | [log](logs/18-mcp-semantic.log) |
| PASS after verifier corrections | Source ledger and manifest-pinned source lines | 0 | [final log](logs/19c-provenance.log) |

The supplementary provenance verifier initially used an incorrect local Git-directory
glob (exit 1, [original log](logs/19-provenance.log)), then incorrectly expected the node
name in the summary's source span (exit 1, [second log](logs/19b-provenance.log)). Inspection
showed the actual directory and summary lines; only the report verifier was corrected.
The final assertion matches the recorded summary at lines 6–8. These were verifier errors,
not Cairn gate failures; the negative logs are retained. No product code was changed.
Viewer's existing chunk-size warning and npm environment-config warning were nonfatal.

## Concrete fake Bundle and semantic query

Session directory:
`.local/cairnd-fake/fake-session-2342728636`.
Run ID: `run-33c7dae4-8e49-4c70-a81d-49c053b16c67-demo`.
The actual controller `runs.state` was `Stable`, and the candidate status was `stable`;
both local source-writeback and catalog-publication PR lifecycles were simulated.

| Identity | Value |
| --- | --- |
| Stable Bundle directory | `.local/cairnd-fake/fake-session-2342728636/bundles/demo/stable` |
| Manifest | [exact generated JSON](bundle-metadata/kb-manifest.json) |
| Checksums | [exact generated checksums](bundle-metadata/checksums.sha256) |
| Bundle digest | `sha256:ccd70bd8d5e7e309887ba0f857428186ebe819bae27c9481999c8a2a10bb5f88` |
| Knowledge DB bytes / SHA256 | 135,168 / `8cc8022fb64ed4f0305ae754734b4fb92ec66efab7305ba73ca092e91a79061a` |
| Schema / graph | 5 / 4 nodes, 3 edges |
| Source repository / commit | `synthetic/source` / `148fc3df0d3c1b7cf16907c15a8e48105d7e3a98` |
| Queried concept | `ccd9e2cf-1848-51c3-b013-229fbcef88b2` / `阻塞队列` |
| Expected summary | `Bounded queue blocks when full or empty` |
| Source ledger | member `fake-concept-blocking-queue`, skill `fake`, `SKILL.md:6–8` |

The concrete `configs/mcp-issue10-env.yaml` configuration points KG `demo` at the actual
stable DB's absolute path; the full credential-free YAML is printed in log 14. Both the
CLI and real stdio MCP server returned one hit for the complete Chinese name. Log 18
archives the actual JSON-RPC request and response and asserts the exact concept ID,
name and expected summary, explicitly rejecting zero-result success. The standard smoke
also checked initialize, tool discovery, KB catalogue and status counts.

MCP `domain_search` exposes ID/type/domain/summary/confidence at this baseline; its
response does not provide source-file citation fields. This report does not claim such
fields were returned. Instead, log 19c independently checks the DB's `source_refs=fake`
and `node_sources` UUID/member/skill/file/span, then reads `SKILL.md` from the exact
manifest source commit and asserts that the span contains the expected queue summary.
The schema's `llm_inferred` provenance and manifest's configured model name are fixture
metadata: this fake runner made no LLM calls.

All three stable Bundle files retained identical SHA256 before CLI/MCP queries and after
the semantic session. The report archives generated metadata and inspection receipts;
the binary DB and fake local Git repositories remain ignored local runtime artifacts,
not remotely downloadable artifacts. Reproduction creates a new session/source commit/
Bundle identity and must inspect that actual output rather than reuse these paths.

## Scope, integrity, and handoff

[Integrity receipt](logs/20-integrity.json) compares source/dependency bytes against the
source SHA and validates the declared report-only write scope. [Artifact manifest](artifact-manifest.json)
records bytes/SHA256 for all saved logs, verifier sources, report and Bundle metadata.
The Issue checkpoint and PR pin the immutable report commit. The saved verifier files
are the exact final sources used from `.local`; their hardcoded session paths identify
this run and need replacement for a newly generated session.

No required local free check remains blocked. No CI result is substituted for local
execution. This is free synthetic controller/publication/read-only query acceptance;
it does not establish real LLM extraction quality, GraphRAG benefit, production GitHub
App JWT/installation permissions/token refresh, paid pilot acceptance or release readiness.
No background job or model run is left running.

For recovery, clone this report's branch/commit, use the pinned bootstrap above, run
the commands in the table, inspect the new fake session's manifest and DB, and configure
MCP with that actual path. Review and maintainer acceptance should follow the fixed
commit evidence; the author makes no independent self-review claim.
