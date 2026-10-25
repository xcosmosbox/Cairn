# First stable release: support and acceptance contract

As of **2026-10-25 12:44 UTC**; inspected accepted integrated source
`dcf1dd19ffa99a9baf93fefbba8d0dab68047a56`. This refreshes the accepted #18/#67
design under [#93](https://github.com/xcosmosbox/Cairn/issues/93); it is a dated
evidence snapshot, not a selected candidate. Cairn remains **Alpha**.
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
those source-specific checks nor restored Node 22 availability establish a final
candidate check or a full browser, operating-system or deployment matrix. The final
maintainer decision must accept these boundaries on the frozen candidate.

| Surface | Accepted evidence within its recorded source/scope | Still needed for the stable decision |
| --- | --- | --- |
| Free example | #10/PR23; #16/PR31 clean fixed-fake CLI/MCP/immutable Bundle path | Final-candidate replay; fixed fake does not consume the supplied multi-document references |
| Real extraction and source lineage | #16/PR58 actual two-reference dependency stored and traversed through MCP; PR61 targeted positive/negative verifier replay | General construction and answer quality remain unaccepted; unknown MCP original-span hints and unexercised live UUID masking remain explicit |
| Local compatibility/recovery | #40/PR43 actual read-only schema 4/5 A→B→A and invalid-generation rejection, immutable inventories | Replay on the final candidate; no arbitrary schema/state downgrade guarantee |
| Private native update/recovery | #44/PR55 actual Linux stdio one-installation native App/Catalog A→B→bad-checksum retention→originalA on source `5d99a24` | Final-candidate bounded recovery evidence; original 404 retained, native HTTP/poll counts unknown, no full daemon lifecycle or natural expiry guarantee |
| GitHub App/HTTPForge | #15/PR34 JWT/install/cache/permission/revocation and accelerated refresh; #47/PR51 authenticated assets/redirect fence | Natural one-hour expiry, transparent 401 and broader platform/lifecycle coverage remain unverified; all-repository installation scope was not changed |
| Retrieval and answers | #21/PR28 lexical DEV baseline; #27/PR30 negative cap2; #54/PR66 synthetic-only paired evaluator and ADR | Controlled actual answer/refusal/visible-support results under frozen resources and a fresh HOLDOUT; evaluator implementation is not measured utility |
| Construction and corrective execution | #41/PR49 and #56/PR60 tracing/token guard/audit; #63/PR70 accepted execution/archive with all12 coverage FAIL (11 materialized); #73/PR76/PR78/PR84 accepted driver and UNKNOWN/negative attempt archives; #85/PR87 accepted continuation driver | #85's later two-wire continuation also ended with final GateB rejection/no accepted checkpoint; its reported integrity-pass result awaits final packet acceptance. No all12 corrected coverage, actual bounded full609 construction or QA result |
| Annotation, relations and curated edges | #64/PR72 typed bounded semantic revision; #68/PR71 full-rebuild local contradicts verification/report-v2; #74/PR77 protection of existing human-curated I7/I8 edge payloads | These are accepted implementation scopes, not general semantic accuracy. #68 does not establish new incremental contradiction validation; #74 does not establish true endpoint-deletion or blanket NULL compatibility |
| Bounded construction components | #75/PR79 strict accepted04 importer/planner; #80/PR86 offline staged mapping/recovery; #83/PR90 pure bounded Description/Relation APIs; #88/PR92 pure source-verifier seam | Actual production wire/counter bridge, admitted orchestration/materialization and complete accepted-source inputs remain separate. Pure APIs/fake callbacks do not prove actual construction, wire fit, quality or default promotion |
| Resource/reliability | #17/PR32 timing on `bf5d7ec`; PR39 identity-bound RSS on `18e7e7b` (observed maximum43,716 KiB) | Candidate-specific observations and support disposition; no SLO, hard 2 GiB enforcement or production capacity established |
| Viewer | Node 22 CI root/subdirectory/WASM tests and #10 free restoration | Exact final-candidate root/subdirectory check; no complete browser/platform matrix |

[#33/PR89](https://github.com/xcosmosbox/Cairn/pull/89) is accepted for the
observed **same-process writer admission** failure: a context-aware owner gate for
the canonical existing evolution directory now precedes DB/DDL/IMMEDIATE admission
and covers capture/commit/cleanup. SQLite's 5000ms busy timeout and existing
concurrency assertions are unchanged. A controlled old-style BeginTx admission
failure and two new complete commits support that mechanism; they are not a forced
pristine-old-Record counterfactual. Original failures and later passes remain visible.
Cross-process SQLITE_BUSY remains possible, and precise historical hold durations,
aggregate contention and fairness bounds are not established. Other nonfatal Record
failures may still omit an evolution-history entry; no complete concurrent-history
or general failure-safety guarantee follows. See the
[dated evidence register](../coordination/EVIDENCE.md) and latest task records for
exact source/reviews and retained failures.

Accepted components are **not yet an admitted bounded construction pipeline**.
The legacy accepted cache remains 11 documents; incomplete 609-document input is
NOT ADMISSIBLE,
and no candidate C has been selected. #73's first attempt retains UNKNOWN delivery/
billing after the platform block; its later three-wire attempt and #85's two-wire
continuation both ended in explicit final semantic rejection. Larger item counts
(163 then 193) are not accepted annotations or measured improvement. At this
snapshot, #85 source is accepted; final acceptance of its negative execution packet
is separate and still pending.
Optional empty Detail in the pure API and compatibility with the existing final
materializer still require an explicit integration decision. The opt-in bounded
requests reject canonical UUID-form business/source/display literals as unsupported
input without masking or truncation; the original-source verifier retains its
separate reversible UUID view. No default pipeline promotion or quality-equivalence
claim is adopted here.

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

The maintainer's [original authorization](https://github.com/xcosmosbox/Cairn/issues/8#issuecomment-6384918656)
and [specific data-egress confirmation](https://github.com/xcosmosbox/Cairn/issues/8#issuecomment-6396678380)
cover ordinary paid/network Cairn experiments and authorized public source text,
machine annotations and model-derived feedback sent to official
`https://api.deepseek.com` with `deepseek-flash`, plus the scoped repository
publication destinations in [execution policy](../coordination/EXECUTION_POLICY.md).
Do not request these user permissions repeatedly within that scope. Preserve the
actual earlier platform rejection/UNKNOWN attempt rather than rewriting its outcome.
Exact source/input/profile/budget freezes, independent review, live admission and
platform safeguards remain effective technical controls. No model run is performed
by this document; the old #13 pilot stays disabled within its separate historical
control boundary.

The maintainer's final decision must accept the support scope, compatibility and
rollback results, resource/reliability disposition and preregistered quality gates
with their evidence. No numerical improvement threshold is adopted by this file.
Open gaps stay unaccepted; a successful checklist or CI alone does not publish stable.
