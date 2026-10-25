# Final candidate and release checklist

Prepared **2026-10-25 01:10 UTC** under [#67](https://github.com/xcosmosbox/Cairn/issues/67).
This is an operator checklist for the [support contract](STABLE_CONTRACT.md), not
an execution receipt or an additional acceptance policy. All boxes are unverified
for the final candidate. Cairn remains **Alpha**; planned identities are `1.0.0`
/ `1.0.0-rc.1`, while actual final candidate, tag and release are **NONE**.

## 1. Freeze one candidate and its decision boundary

- [ ] Root accepts the construction/quality/evaluation evidence and final support
  scope. #63's historical calibration remains FAILED (11/12 materialized), with
  its archive/report separately reviewed. #64/#68 implementation or CI cannot
  substitute for an admitted corrected validation or actual answer quality.
- [ ] Freeze candidate commit **C** after the relevant changes are integrated;
  record its full SHA/tree, clean status, branch and independent reviews. The
  present documentation baseline `a1a016f92cd2883675ed54f9f6afcbefa4a823d4` is
  not a selected final candidate. Keep raw receipts/logs outside C so running
  checks does not amend the frozen source or overwrite historical evidence.
- [ ] Record the chosen candidate software stamp, official Go 1.27.3 archive and
  executable hashes, Linux amd64/C toolchain identity, Node 22 exact version,
  Python version and locked dependency identities. Existing Go 1.22 CI coverage
  remains compatibility evidence; Node 24 recovery is not the Node 22 check.
- [ ] Confirm the bounded native CLI/stdio MCP and loopback REST/SSE scope,
  Bundle `dk-kb-bundle/v1`, reader schema 4..5 and builder schema 5. Record Viewer
  root/subdirectory coverage and its browser/platform limits. Keep #33's unknown
  cause and accepted narrow evolution-history warning visible. Natural token
  expiry, transparent 401, full daemon lifecycle, SLO and production capacity
  remain unverified unless separately evidenced and accepted.

The [execution policy](../coordination/EXECUTION_POLICY.md) permanently authorizes
ordinary official DeepSeek/Cairn experiments within its scope. A future run still
needs its exact technical freeze/review; do not request the same user permission
again or run a paid control as routine release regression. QA/gold/HOLDOUT access
and new model runs are not part of this documentation task.

## 2. Run the inherited free gates on C

- [ ] Save actual argv, UTC start/end, exit status, environment, logs and exact C
  for full race/build/vet/invariants, Python regressions and the free demo below.
  Retain initial failures and explicit overrides rather than replacing their logs.
- [ ] Run Node 22 Viewer root/subdirectory/WASM checks with `npm ci`; preserve
  the lockfile. These checks do not constitute a full browser/platform matrix.
- [ ] Run C's fixed-fake example and inspect its actual receipt: Stable state,
  manifest/digest, known-content CLI/MCP, negative cases and unchanged Bundle bytes.
  Fixed fake does not consume the multi-document input references or test a model.
- [ ] Rehearse C's scoped local schema 4/5 immutable A→B→A rollback and invalid
  generation rejection using [LOCAL_RECOVERY.md](LOCAL_RECOVERY.md). Reconcile
  the required private/native update-recovery scope with
  [REMOTE_RECOVERY.md](REMOTE_RECOVERY.md) and the root's candidate plan. Prior
  source-specific success is not a final-candidate replay; this checklist itself
  does not dispatch a remote/App/model run.
- [ ] Confirm exact-C normal CI and separate reviewer evidence. Record each
  gate as pass, fail, blocked or not run; unknown evidence never becomes pass.

Existing commands, to run from the clean candidate checkout with the pinned
compiler/Node 22 on PATH (not executed by preparing this file):

```sh
git rev-parse HEAD
git status --short
go version
node --version
python3 --version
go test -race -count=1 ./...
make verify
python3 scripts/dev/zz_smoke_mcp_test.py
python3 -m unittest discover -s e2e/quality -p 'test_*.py'
python3 -m unittest discover -s e2e/benchmarks/multihop_public -p 'test_*.py'
python3 e2e/quality/export_receipts.py --self-test
python3 examples/public-demo/demo.py
(cd graph-viewer && npm ci && npm run test:subpath)
```

## 3. Build the distributable once and verify the unpacked bytes

- [ ] After the gates, build the final eight binaries once with official
  Go 1.27.3 and the explicitly selected stamp. `make verify` and the free demo
  also create development binaries; replace those only with the final packaging
  build, then freeze its bytes. No package is built by this document.
- [ ] Include `cairnd`, `cairnctl`, `cairn-ingest`, `cairn-incremental`,
  `cairn-rebalance`, `cairn-evolve`, `cairn` and `cairn-mcp`, plus the Node 22-built
  Viewer distribution and required example configuration/operator documents.
  Never package credentials, mutable test state, private originals or raw responses.
- [ ] Record package inventory, per-file byte sizes/SHA256, archive SHA256,
  source C/tree, software stamp, toolchain/runtime/dependency identities and all
  eight binaries' `--version`/`go version -m` output. Software, Bundle, schema,
  MCP protocol and Viewer component versions retain separate identities.
- [ ] Unpack that exact archive into a fresh directory. Verify its manifest and
  all hashes; use only its binaries for version, read-only known-content CLI and
  stdio MCP smoke against a preserved synthetic library. Check source metadata,
  expected negative results and before/after Bundle bytes. Record Viewer root
  and deployed-subdirectory asset/WASM checks against the packaged distribution.
- [ ] Preserve the exact previous package, verified Bundles, Catalog commit,
  installation generation and matching configuration/controller snapshot. Pause
  watcher/daemon before rollback; never repack a Bundle or migrate a read-only
  library. Reinstall the preserved generation and verify known/negative queries
  and digest before restarting. Any remote pointer update remains a separately
  reviewed authorized operation.

The existing packaging build entry is `make build CAIRN_VERSION=SELECTED_STAMP`;
`SELECTED_STAMP` is an operator placeholder, not a selected release. Use the same
frozen source C and record actual concrete argv. If a fix is required after this
build, select a new candidate identity and repeat affected gates; never silently
replace the accepted archive or move a final tag.

## 4. Record the release decision

- [ ] Root reviews the complete exact-C/package evidence, inherited quality
  decisions, compatibility/recovery results, resource/support limits and unresolved
  issues; record explicit acceptance or remaining blockers in #9 and the release task.
- [ ] Only after that decision, create the selected immutable tag pointing to C
  and publish the exact verified archive/manifests under its approved release plan.
  Never move a final tag. Link original failures and restricted-access limitations.
- [ ] Read back tag target, release assets/checksums and supported scope. A checklist,
  version stamp, clean build or successful CI alone does not publish stable.
