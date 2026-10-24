# Evidence register

Checked **2026-10-24 16:10 UTC / 2026-10-25 00:10 Asia/Shanghai** against integrated
`1f49dc0af50bc4619ae8b2a888f6589351f7a144`. `Verified` is limited to the named observation.
Latest task/PR records supersede this dated index. The [initial register](https://github.com/xcosmosbox/Cairn/blob/c6a2b16f21434147947f4ed30d65a06321571546/docs/coordination/EVIDENCE.md)
and all original reports remain historical evidence, not overwritten results.

## Accepted current evidence

| Task / implementation | Exact integration / evidence | Observation and limit |
| --- | --- | --- |
| #10 free restoration, PR23 | `171644a` / [report](reports/issue10-restore-20261024-env/README.md) | Local free race/build/Viewer/fake/CLI/MCP passed at recorded source; not a current candidate or real extraction result |
| #12 runner, PR25 | `ecf6c914` / [acceptance](https://github.com/xcosmosbox/Cairn/issues/12#issuecomment-6382693732) | Offline durable-stop/recovery evidence, including retained rejection→fix; old pilot still disabled |
| #21 lexical baseline, PR28 | `bf5d7ec0828b85695a0ad5e196f11f32f02462cb` / [adapter](../../e2e/benchmarks/multihop_public/README.md) |609originals, frozen DEV128/HOLDOUT256, only free lexical DEV scored; original-text evaluation adapter, not production FTS |
| #27 negative cap2, PR30 | `ee6b8bb` / [acceptance](https://github.com/xcosmosbox/Cairn/issues/27#issuecomment-6382804686) | Visible exact-span FactRecall39.76%→37.33%,4gains/10losses; adoption rejected, no further same-DEV cap tuning |
| #26 metadata, PR29 | `9a07c54d681e52b14458645dc79ffb07aa35da3f` | Explicit stored source metadata; no original text/backfill or inferred source identity |
| #15 App, PR34 | `18e7e7bfc989fc0216625b0851e105690edd3950` / [acceptance](https://github.com/xcosmosbox/Cairn/issues/15#issuecomment-6384083736) | Real native App/permission/cache/revocation/accelerated refresh scope; natural1h/transparent401/assets/full lifecycle outside this task |
| #17 timing and RSS, PR32/39 | timing `bf5d7ec`; memory `18e7e7b`; merge `c6a2b16f21434147947f4ed30d65a06321571546` |21,271timing samples/10watcher changes; later576valid identity-bound RSS samples, observed max43,716KiB. Distinct sources; no hard2GiB/SLO/production-capacity claim |
| #18 contract, PR35 | `17fee1382d88257e3d49e7183712e77756db07ac` / [contract](../release/STABLE_CONTRACT.md) | Accepted design only; dated unknowns there are not latest task status or runtime passes |
| #33 diagnosis, PR36 | `42246cdb60ef38bf107a9fb740482eaa6c1d0bf6` / [partial acceptance](https://github.com/xcosmosbox/Cairn/issues/33#issuecomment-6384377618) | Three instrumented race iterations/24concurrent records/no reproduced failure. Original CI failure retained, root cause UNKNOWN; no fix/flaky classification |
| #40 local recovery, PR43 | `2d87bbdf075960eade455fac2aa2f5148ee31530` / [packet](../release/evidence/local-recovery-20261024/README.md) |31commands/4MCPsessions, real schema4/5 reinstall/rollback/invalid rejection and immutable inventories; no remote/daemon guarantee |
| #41 offline instrumentation, PR49 | `3bea85ce1f03be8625b1ef8ffce21fbe358e7012` / [acceptance](https://github.com/xcosmosbox/Cairn/issues/41#issuecomment-6385323158) | Exact-wire durable tracing/token guard and free corpus-only12 preparation; no provider capacity run |
| #50 fixture correction, PR52 | `08ab394a1b58f370485e0a9f4362bdc4ed58f185` / [diagnosis](reports/ci46-split-primary-20261024/README.md) | Explicit tied/newer source-fixture cases; original red CI and actual CI timestamps UNKNOWN retained; production resolver unchanged |
| #42 audit/negative packet, PR46 | `56c93c0e2ebed8051effbd3c01df19472d29c7f2` / [packet](reports/issue16-realmodel-20261024-live16/README.md) | Original spans optional/unknown, generated ranges diagnostic; attempts01/02 remain negative, no desired graph dependency |
| #47 private assets, PR51 | `5d99a24b94b1465b59cc19f0de772a499451fe91` / [acceptance](https://github.com/xcosmosbox/Cairn/issues/47#issuecomment-6385362184) | Canonical authenticated API assetID/public compatibility; cross-origin auth removal and HTTPS downgrade refusal. Reviewer-found old downgrade gap preserved |
| #45 cross mechanism, PR53 | `75a77a0d5d1e1d5077c4758e56f2bc7fd3ac62a8` / [ADR](../decisions/0003-source-grounded-cross-relations.md) | Candidate-only source-grounded/UUID-alias-safe validation and two-pass ingest; no crossedge discovery/recall guarantee |
| #44 actual private recovery, PR48/55 | initial report `5f886cf82bdd2b46af4af518ce625257f2a5564c`; continuation `2a6e54a3d7f8e6e76e46b713005ea095abd0b0c1` / [acceptance](https://github.com/xcosmosbox/Cairn/issues/44#issuecomment-6385635676) | Actual source5d99,119.582s/12MCPresponses/A→B→bad rejected/Bhealthy→originalA. Original404 remains; native poll/HTTP counts unknown; one synthetic/Linux/stdio installation |
| #16 actual positive, PR58 | `7268621486df9bb3b90d78d10cc3b88f9c3b5b82` / [packet](reports/issue16-attempt03-20261024/README.md) | Actual source75a77/2docs/14source incidences/desired stored+MCP path/exact original quotes/numeric8,50ms,2. Unknown MCP original hints; no live UUID literal or general accuracy/causal comparison |
| #57 targeted negative, PR61 | `e4d7ffe81ea30f198a5989d3360431e246153806` / [packet](reports/issue16-negative-replay-20261024/README.md) | Exactly2frozen existing candidates/2HTTP200: dependency accepted, false-absence contradiction explicitly semantic-rejected; no new build/general semantics claim |
| #59 software identity, PR62 | `0cd432b95033903c1f2bde4e293b2b11a77525df` / [acceptance](https://github.com/xcosmosbox/Cairn/issues/59#issuecomment-6386162924) | Shared stamps/devel/buildinfo with source fingerprint retained; actualGo1.27.3 DB-free/focused checks. Planned1.0.0/rc labels are not a release |
| #56 corrected offline integration, PR60 | `1f49dc0af50bc4619ae8b2a888f6589351f7a144` / [acceptance](https://github.com/xcosmosbox/Cairn/issues/56#issuecomment-6386219941) |1MiB overlay/stage+disposition/aggregate0.95doc-member audit, rejected-edge falsepass fixed with controls; v1 unchanged/v2 added, no paid success |

Task acceptances cite separate reviewer sessions, exact heads and current checks. Shared
account/model limits are disclosed. Initial reviewer CI-pending observations must be read
with the later explicit final-CI/root acceptance, not silently relabeled.

## Current unaccepted evidence and access

[#63](https://github.com/xcosmosbox/Cairn/issues/63) is the one actual12 calibration on
source1f49/officialGo1.27.3, started2026-10-24T15:51:33.473946Z. Executor/root handoff
reports natural completion with only11/12accepted/materialized: largest71,119B original
failed all three annotation/GateB rounds for incomplete long-list coverage/fidelity.
The first [hard failure checkpoint](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6386370522)
predates downstream completion. Final stage/usage/disposition denominators, immutable
public/private packet and independent review remain pending. No rerun/full609/QA result
is admitted; the original run is not a capacity pass.

#54's answer evaluator/scientific ADR/redirect-ledger safeguards remain unmerged local
synthetic implementation under review. Adopted numerical policies are preregistration,
not measured answer utility. QA/gold/HOLDOUT remain unopened; semantic visible support
is not the old exact-original-span FactRecall. No original-source credit for explicitly
unbound hierarchy results; leaf ownership failures halt.

Full raw construction/provider artifacts may require restricted storage; public packets
must expose sufficient hashes/method/aggregate limits without redistributing article
bodies, secrets or private cases. A missing archive identity is UNKNOWN, not complete.
Pinned official PyPI tokenizer distribution/function tests do not establish that the
native wheel was rebuilt from the source pin, or that configured model alias identifies
a backing served revision. Failed usage/billing/response IDs stay unknown when unretained.

## Historical implementation and restricted archive register

| Historical evidence | Identity / source | Recorded boundary |
| --- | --- | --- |
| Original implementation/CI | `01968d608707730af458ff9c4e6b487c83ade1d3`; [run37264544835](https://github.com/xcosmosbox/Cairn/actions/runs/37264544835) | Successful historical source/CI, not a current environment/candidate check |
| F01–F18 | [PR1](https://github.com/xcosmosbox/Cairn/pull/1), CORRECTNESS.md | Merged fixes/regressions, not exhaustive correctness |
| Original real build/publish/consume | [PR2](https://github.com/xcosmosbox/Cairn/pull/2) | Reported69-document Actions-credential run; distinct from newer native-App evidence |
| Rank/provenance and literal/Chinese experiments | [PR3](https://github.com/xcosmosbox/Cairn/pull/3), [PR4](https://github.com/xcosmosbox/Cairn/pull/4) | Historical low grounded rates/incidents; no established general graph benefit |
| Original App local tests | [PR5](https://github.com/xcosmosbox/Cairn/pull/5) | Local signature/protocol scope, superseded only within #15/#44's narrower accepted live surfaces |
| Source Backfill | [PR6](https://github.com/xcosmosbox/Cairn/pull/6), round3README | Evaluation adapter, not production hydration/online quality; pilot run stays disabled under#13 |

Restricted repository `xcosmosbox/kd_manifeat`, historical branch
`eval/cairn-quality-20261004` at `181b2f6ef4b87a6558cbc8662e3218df164d68cc`,
was observed to contain directory entries:

- `evaluations/cairn-nicedata-20261004/`
- `evaluations/cairn-retrieval-round2-20261004/`
- `evaluations/cairn-source-backfill-20261005/`

Directory listing did not verify full bytes/manifests/multipart reconstruction or remote
completeness; #11 audit remains deferred. Immutable earlier archive commits linked by
original PRs must not be replaced with a newer branch head. Offline runner repair #12
is complete; old pilot #13 still needs control identity/access/freeze/independent go-no-go.
These are not universal requirements for new public experiments, but old-control reuse
requires them. No inaccessible archive is inferred absent or accepted.

## Add an evidence record

Record observation date, source/data/protocol identities, actual command/exit, immutable
artifact commit/size/SHA256/access, reviewer and limits. Distinguish author report,
observed remote receipt, current rerun and acceptance. Retain full denominators, negative
results, unknown usage and failed attempts; exact source/citation support is separate
from actually visible facts and final answer correctness.
