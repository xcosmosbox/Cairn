# First stable release: proposed acceptance contract

As of 2026-10-24; inspected source `18e7e7bfc989fc0216625b0851e105690edd3950`.
This is the bounded #18 proposal for maintainer review. Cairn remains Alpha;
no stable version, release date, tag or new support guarantee is assigned here.
Live task evidence is in [#9](https://github.com/xcosmosbox/Cairn/issues/9),
with direction and decisions in [#8](https://github.com/xcosmosbox/Cairn/issues/8).

## Proposed support and evidence boundary

The proposed release keeps Cairn's build/update → immutable Bundle/Catalog →
read-only CLI/MCP/Viewer purpose. It does not reposition the product as the
public benchmark's original-text FTS adapter. Graph accuracy/default promotion
needs a controlled answer-quality decision; graph may remain experimental only
under an explicit maintainer support decision. Query-side zero LLM/embedding,
immutable identity, human-curated edits and source provenance remain required.

| Surface | Accepted baseline evidence | Still needed for stable decision |
| --- | --- | --- |
| Development and free example | #10/#23 restored Go, build/race, fake Bundle, semantic CLI/MCP; #16/#31 clean-clone fixed-fake demo | Final candidate replay; #16 added multi-document inputs have not been consumed by that fake |
| Real extraction/update | Historical #1–#6 implementation and regressions | Fresh bounded real multi-document smoke, retained source lineage and update/recovery evidence; current #16 work is free preparation only |
| Bundle/Catalog and query lifecycle | Existing strict pull, immutable generation and in-flight-reader regressions; #17/#32 actual loopback watcher timing | Exact final-candidate compatibility/rollback replay and published supported-platform scope |
| GitHub App and HTTPForge | #15/#34 real JWT, token/cache, reduced-token denial, revocation propagation, accelerated refresh, branch/PR/prerelease replay | State the tested scope explicitly: natural one-hour expiry, transparent401 recovery, assets/merges and full daemon/MCP lifecycle were not established |
| Retrieval/answers | #21/#28 lexical DEV baseline; #27/#30 negative cap2 result retained and adoption rejected | New controlled graph/answer/refusal protocol, same resources, intact provenance, closed fresh HOLDOUT until preregistration; numerical quality/efficiency gates remain proposals awaiting decision |
| Resource/reliability | #17/#32 timing on source `bf5d7ec`, 21,271 samples and10 watcher changes | #17 trustworthy server RSS/peak/update increment; #33 original SQLITE_BUSY cause/risk disposition. Current timing is neither a current-main capacity claim nor an SLO |
| Viewer | Existing CI subdirectory/WASM regression; #10 free restoration | Final candidate's supported deployment-path check |

The old SourceBackfill pilot stays disabled under #13. #11 full archive audit
and #13 are requirements when relying on those historical controls, not automatic
prerequisites for every new public experiment. Their limits cannot be silently
transferred to a different dataset or accepted as passed.

## Versions, old libraries and recovery

| Boundary | Current behavior / compatibility evidence | Proposed release and rollback requirement |
| --- | --- | --- |
| Software version | No first-stable version selected; evidence binds exact commits and binary hashes | Record candidate commit, built binary hashes/toolchains and selected version before acceptance; retain previous binaries/configuration |
| Bundle format | [`dk-kb-bundle/v1`](../../core/kbbundle/bundle.go) is a permanent historical identifier; promotion changes Catalog channel, never repacks the Bundle | Verify checksums, aggregate digest, source/builder/config/schema identity; preserve exact old Bundle bytes and Catalog commit |
| Current DB | Builder migration list ends at schema5; query contract checks actual tables/columns/FTS as well as version | Run current reader/build tests on candidate; do not claim arbitrary older/newer DB compatibility |
| Legacy schema4 library | [`ValidateQueryContract`](../../core/storage/query_contract.go) supports4..5; v4 uses a NULL `file_slug` projection. Read-only open skips migrations | Exercise legacy startup and hot swap; compare before/after DB hashes. Querying must not upgrade the library |
| Missing/broken/unsupported DB | Contract-invalid generations are rejected; lifecycle tests retain the healthy generation | Retain rejection and byte-preservation evidence. Rebuild/migrate only an explicitly copied writable build input; no in-place query migration or assumed downgrade |
| Reader declaration | Manifest stores `minimum_reader_version`; this baseline has no semantic-version enforcement of that field | Publish the tested reader/schema matrix; do not promise an automatic minimum-version gate or cross-version rollback without a test |
| Installed generation | [`Install`](../../core/kbbundle/bundle.go) verifies immutable digest directories, atomically switches `current`, returns previous path; acquired readers pin their generation | Preserve previous directory and matching binaries. Validate local rollback before resuming watcher; no rewriting an installed generation |
| Controller state | `cairnctl` inspects persistent controller state; retry/unblock mutate its run state | Stop daemon before a consistent state/config snapshot. Restore the matching snapshot and binary for downgrade; arbitrary state-schema downgrade is unverified |

The existing local Bundle reinstall route can select a preserved generation;
these are operator commands with explicit placeholders, not a completed rehearsal:

```sh
./bin/cairnctl bundle list --kg KG --install-dir /ABSOLUTE/INSTALL_ROOT
./bin/cairnctl bundle pull --kg KG --install-dir /ABSOLUTE/INSTALL_ROOT \
  --src-dir /ABSOLUTE/PRESERVED_VERIFIED_BUNDLE
./bin/cairn --db-path /ABSOLUTE/INSTALL_ROOT/KG/current/knowledge.db status
./bin/cairnctl status --state-db /ABSOLUTE/CONTROLLER_SNAPSHOT_COPY
```

Pause the Catalog watcher/daemon before local rollback so a newer remote stable
pointer cannot immediately select the rejected generation again. Restart consumers
against the validated restored configuration, verify known-content and negative
queries plus source identity, and record the restored digest. Any remote Catalog
rollback requires a separate authorized, reviewed pointer update to the preserved
Bundle; this document performs no such write. `retry`/`unblock` are not DB repair.

## Candidate acceptance and minimal operations

From a clean pinned candidate checkout with Go1.22+ and the existing dependencies,
the following existing free commands cover the basic engineering/reproduction
gates. Run them for the final candidate; their presence here is not a new run:

```sh
git rev-parse HEAD
go version
go test -race -count=1 ./...
make verify
python3 examples/public-demo/demo.py
python3 scripts/dev/zz_smoke_mcp_test.py
python3 -m unittest discover -s e2e/quality -p 'test_*.py'
python3 -m unittest discover -s e2e/benchmarks/multihop_public -p 'test_*.py'
python3 e2e/quality/export_receipts.py --self-test
(cd graph-viewer && npm ci && npm run test:subpath)
```

Viewer checks use Node22 as in [Correctness CI](../../.github/workflows/correctness.yml).
If an environment requires `GOFLAGS=-buildvcs=false`, retain the original failure
and explicit override with exact source/binary identity, as in #10/#15; never erase
the failure. The full race run includes legacy-schema, invalid-swap, in-flight
reader and immutable-generation rollback tests. It does not replace a deployed
real update/recovery rehearsal or controlled answer evaluation.

Before accepting a candidate, record commands/exit codes, CI and separate review,
binary/input/Bundle identities, retained original failures, supported deployment
and transport matrix, actual resource observations and remaining limits. Observe
controller run state/reason, stable source/digest, watcher transition/error,
query/MCP business errors and process-identity-verified memory. Investigate auth
failure with sanitized status/revocation/refresh evidence, bad Bundle with checksum
and query-contract evidence, and SQLITE_BUSY with #33's bounded diagnostics; never
publish secrets or treat zero/absent resource samples as measurements.

Current model execution is blocked by automatic approval review of external
credential egress. This is an authorization block, not evidence of a bad key,
provider outage, budget exhaustion or model quality. No agent retries requests
until applicable authorization clears review. Free preparation may continue.

The maintainer's final decision must accept the support scope, compatibility and
rollback results, resource/reliability disposition and preregistered quality gates
with their evidence. No numerical improvement threshold is adopted by this file.
Open gaps stay unaccepted; a successful checklist or CI alone does not publish stable.
