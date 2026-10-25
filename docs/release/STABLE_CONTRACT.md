# First stable release: support and acceptance contract

As of **2026-10-25 01:10 UTC**; inspected integrated source
`a1a016f92cd2883675ed54f9f6afcbefa4a823d4`. This updates the accepted #18 design
under [#67](https://github.com/xcosmosbox/Cairn/issues/67). Cairn remains **Alpha**.
`1.0.0` / `1.0.0-rc.1` are planned packaging targets; actual final candidate, tag
and release are **NONE**. This document does not publish a support guarantee.
Live task evidence is in [#9](https://github.com/xcosmosbox/Cairn/issues/9),
with direction and decisions in [#8](https://github.com/xcosmosbox/Cairn/issues/8).

## Bounded support and evidence boundary

The proposed release keeps Cairn's build/update → immutable Bundle/Catalog →
read-only CLI/MCP/Viewer purpose. It does not reposition the product as the
public benchmark's original-text FTS adapter. Graph accuracy/default promotion
needs a controlled answer-quality decision; graph may remain experimental only
under an explicit maintainer support decision. Query-side zero LLM/embedding,
immutable identity, human-curated edits and source provenance remain required.

The intended first support boundary is Linux amd64 native CLI and stdio MCP.
REST/SSE are bounded loopback surfaces, with no public hosting/authentication or
internet-exposure guarantee. Viewer root/subdirectory checks use Node 22; neither
those checks nor the current Node 24 recovery environment establish a full browser,
operating-system or deployment matrix. The final maintainer decision must accept
these boundaries with evidence on the frozen candidate.

| Surface | Accepted evidence within its recorded source/scope | Still needed for the stable decision |
| --- | --- | --- |
| Free example | #10/PR23; #16/PR31 clean fixed-fake CLI/MCP/immutable Bundle path | Final-candidate replay; fixed fake does not consume the supplied multi-document references |
| Real extraction and source lineage | #16/PR58 actual two-reference dependency stored and traversed through MCP; PR61 targeted positive/negative verifier replay | General construction and answer quality remain unaccepted; unknown MCP original-span hints and unexercised live UUID masking remain explicit |
| Local compatibility/recovery | #40/PR43 actual read-only schema 4/5 A→B→A and invalid-generation rejection, immutable inventories | Replay on the final candidate; no arbitrary schema/state downgrade guarantee |
| Private native update/recovery | #44/PR55 actual Linux stdio one-installation native App/Catalog A→B→bad-checksum retention→originalA on source `5d99a24` | Final-candidate bounded recovery evidence; original 404 retained, native HTTP/poll counts unknown, no full daemon lifecycle or natural expiry guarantee |
| GitHub App/HTTPForge | #15/PR34 JWT/install/cache/permission/revocation and accelerated refresh; #47/PR51 authenticated assets/redirect fence | Natural one-hour expiry, transparent 401 and broader platform/lifecycle coverage remain unverified; all-repository installation scope was not changed |
| Retrieval and answers | #21/PR28 lexical DEV baseline; #27/PR30 negative cap2; #54/PR66 synthetic-only paired evaluator and ADR | Controlled actual answer/refusal/visible-support results under frozen resources and a fresh HOLDOUT; evaluator implementation is not measured utility |
| Construction/capacity | #41/PR49 and #56/PR60 exact-wire tracing/token guard/audit; #63 sole calibration actually failed all 12 coverage, only 11 materialized | Immutable #63 report review and separately frozen corrected validation; #64 annotation revision and #68 local contradiction correction are active, unaccepted implementations; no full609 or QA admission |
| Resource/reliability | #17/PR32 timing on `bf5d7ec`; PR39 identity-bound RSS on `18e7e7b` (observed maximum43,716 KiB) | Candidate-specific observations and support disposition; no SLO, hard 2 GiB enforcement or production capacity established |
| Viewer | Node 22 CI root/subdirectory/WASM tests and #10 free restoration | Exact final-candidate root/subdirectory check; no complete browser/platform matrix |

#33's original SQLITE_BUSY cause remains **UNKNOWN/unfixed**. Root accepted only
the narrow local risk that a nonfatal evolution-sidecar Record failure may omit a
history entry. Preserve this warning in candidate evidence; it is not a complete
concurrent-history or general failure-safety guarantee. See the
[dated evidence register](../coordination/EVIDENCE.md) and latest task records for
exact commits, reviews and original negative results.

The old SourceBackfill pilot stays disabled under #13. #11 full archive audit
and #13 are requirements when relying on those historical controls, not automatic
prerequisites for every new public experiment. Their limits cannot be silently
transferred to a different dataset or accepted as passed.

Public demo commands, CI and committed sanitized reports are the public reproduction
path. Historical private inputs/full response archives remain restricted; their
existence or inaccessible hashes do not establish public reproducibility. Mark
unavailable evidence unknown, and publish no private content or credential locations.

## Versions, old libraries and recovery

| Boundary | Current behavior / compatibility evidence | Proposed release and rollback requirement |
| --- | --- | --- |
| Software version | Shared `devel`/stamp/buildinfo identity is accepted in #59/PR62; software version is separate from Bundle/schema/MCP/Viewer versions | Planned1.0.0/1.0.0-rc.1 only; freeze candidate commit, exact official Go 1.27.3 and binary hashes before final acceptance; retain previous binaries/configuration |
| Bundle format | [`dk-kb-bundle/v1`](../../core/kbbundle/bundle.go) is a permanent historical identifier; promotion changes Catalog channel, never repacks the Bundle | Verify checksums, aggregate digest, source/builder/config/schema identity; preserve exact old Bundle bytes and Catalog commit |
| Current DB | Builder migration list ends at schema 5; query contract checks actual tables/columns/FTS as well as version | Run current reader/build tests on candidate; do not claim arbitrary older/newer DB compatibility |
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

Follow the [final-candidate checklist](RELEASE_CHECKLIST.md). From a clean pinned
candidate checkout with official Go 1.27.3 and the existing locked dependencies,
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

Viewer checks use Node 22 as in [Correctness CI](../../.github/workflows/correctness.yml).
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

The historical credential-egress approval block was superseded by the maintainer's
[authorization record](https://github.com/xcosmosbox/Cairn/issues/8#issuecomment-6384918656)
and subsequent actual execution receipts. The permanent policy authorizes ordinary
Cairn DeepSeek experiments at official `https://api.deepseek.com` with
`deepseek-flash`; do not request that user authorization repeatedly. Task/profile
freezes, independent review and acceptance remain technical controls. No model run
is performed by this document, and the old #13 pilot remains disabled within its
separate historical-control boundary. See [execution policy](../coordination/EXECUTION_POLICY.md).

The maintainer's final decision must accept the support scope, compatibility and
rollback results, resource/reliability disposition and preregistered quality gates
with their evidence. No numerical improvement threshold is adopted by this file.
Open gaps stay unaccepted; a successful checklist or CI alone does not publish stable.
