# v0.1.3 node helper fidelity and retained-response evidence

Task: [#111](https://github.com/xcosmosbox/Cairn/issues/111). Date: 2026-10-27 UTC.
Starting main: `413cb6045d6cfd0c893624dc87277dffb04e683a`, tree
`6e50d90478f3a6ae64b28599353e279f1b7ec36d`.
[Scope/interface acknowledgement](https://github.com/xcosmosbox/Cairn/issues/111#issuecomment-6431600098).
This is helper implementation and bounded deterministic evidence, not ordinary
full-entry construction, six-original semantic certification, parent acceptance or a release.

## Exact historical extraction loss

Read-only inspection of the fixed #105 packet refines the earlier earliest-stage
attribution without rewriting any historical input, response, dump or result. The
existing `v011_reconcile.py` audit verifies 23 consumed entries against archive
SHA256 `cdf5f96ae41bee23ff16eba3e690bf392606691e5f0acf22de3d05c501c86921`.

The accepted04 input has 173 annotation occurrences, 165 distinct members and 172
distinct document–member incidences. The missing member is present in request13
AND its HTTP200/stop raw response. That response contains 158 nodes and all165
members. Its affected row is inside `entities[]` but declares `label:"Concept"`,
with confidence0.95 and one valid member. The parser requires the array's matching
label and drops that row. The earliest saved05 result therefore has157 nodes,
164 members and171 source–member incidences. Later selected/persisted sets agree.
This is a raw-response/category-validation boundary loss; no whole-node insertion
or SQLite loss follows. Why generation chose the inconsistent category remains unknown.

| Retained identity | SHA256 |
| --- | --- |
| 04 input bytes | `a8e9769b29bbdbd52cac17df2231ae7940aabe5290fb6aa21055a59dd75e23d4` |
| earliest05 bytes | `4f57187a79cb0e934f0551676a95694ef1bdc98cbff0624653cd252a924e9969` |
| request trace bytes | `f909748b2ec7f9d5b98d2b8d02f19e7be0f3a156db642a88e2cc80c6b4606fd7` |
| request13 body | `1badf10e718d8cd8e9ed246047792451c7f8715e8d33d6de291c91aafd080add` |
| response13 body | `fecf5f9e6da66d4e1125e2e17c5d0d864e8f9fa51e511df19069579743cbbf90` |
| affected member ID (hashed) | `97444630490e3fb40af72d589c3b0e4f368dbba05ade60247a75263773f3bf99` |

The authorized private replay uses authentic04 and response13, followed by one
scripted correction of ONLY that row's array/category correspondence. It exercises
the actual mapping recovery helper, retains successful siblings and recomputes the
172 distinct incidences. The always-on public equivalent uses synthetic names.
Neither replay is a new provider request or a semantic judgment of the originals.
Private prose and source paths remain in the authorized packet.

## Acceptance boundary

The independent tests exercise the actual owned helpers. Deterministic generation
and semantic feedback are test controls, with deliberately false candidates and
concrete source-defined critique; they do not measure real-model semantic accuracy.
Expected evidence remains outside builder prompts. Repaired candidates are fully
reassessed so restoring one qualifier cannot silently drop another.
The existing pipeline relation controls were adapted under the
[test-only scope amendment](https://github.com/xcosmosbox/Cairn/issues/111#issuecomment-6431828343)
for hashed member identities, call-scoped node generation aliases and required
summary/detail fields; their frozen-source/verified-edge/human assertions remain.
The [writeback-fixture amendment](https://github.com/xcosmosbox/Cairn/issues/111#issuecomment-6431899705)
adapts the same generation shape while retaining its injected sidecar failure,
ingest/error and unchanged-original-byte assertions.
Those routing fixtures are compatibility evidence, not semantic-model judgments.

`ComputeFinalCoverage.Outcome == "complete"` means structural member/source
coverage of the named actual survivor graph only. It is never a semantic verdict.
The semantic report binds checked Name/Summary/Description to candidate/material
digests before UUID assignment. #114 must retain the existing temporary-alias to
persistent-UUID mapping or matching before/after node snapshots, and confirm that
the checked text, members and full materials remain unchanged through UUID and
candidate filtering. It must combine those exact surviving semantic outcomes with
the recomputed structural report, rather than compare a stale whole-graph digest
blindly or treat equal counts as semantic success. No new ledger is required.

Deterministic identity here covers identical accepted annotation inputs. A new
model paraphrase of the same raw source can change the exact item signature; stable
raw-source reconstruction and model-mediated repeats remain #114's actual campaign.
Newly created members therefore use the readable legacy prefix plus an exact
semantic discriminator, rather than preserving the old name-only collision.
Existing merge targets retain their stored UUIDs through alias lookup; no schema
migration or automatic rewrite of existing persisted identities is introduced.

| Case | Required evidence |
| --- | --- |
| Same name / normalization / detail-only differences | Distinct semantic identities and all source content reach grouping |
| True duplicates / unchanged repeat / candidate reorder | Stable first-created UUID, all distinct source incidences, no duplicate growth |
| Category-mismatched accepted member | Earliest detected omission receives missing-only finite repair; valid siblings retained |
| Qualified summary and detail | Conditions, maintenance exception/prohibition, chronology, uncertainty, attributed disagreement and source UUID literal survive; summary cannot broaden claims |
| Description/UUID/candidate filtering | Actual final member/source counts are recomputed, never copied from early coverage |
| Unknown / malformed assessment | Same candidate reassessed; no false correctness feedback or empty replacement |
| Semantic defect / exhaustion / cancellation | Concrete critique revises the affected node only; actual attempts and reasons remain visible |
| Existing target / human protection | UUID pinned, existing content preserved on unresolved work, human content outside model input |

## Verification and remaining handoff

Identity/mapping and shared node-semantic helpers are implemented, with independent
deterministic acceptance controls. Mapping repair exposes `NeedsDescription` for
every new/touched candidate; read-only `retained#N` aliases let unresolved members
join proven retained objects without changing their UUID or losing earlier prose.
Alignment presents all source-bound Content/Detail, full existing machine detail,
and no curated content. Literal FTS query compilation prevents punctuation from
silently removing an existing merge target from recall. Harmless extra generation
metadata is tolerated; strict alias/required-field/truncation/duplicate/assessment
validation remains.
When post-UUID final coverage repair touches a retained target, its UUID stays
pinned. #114 must derive UUIDs only for first-created repair nodes, never blindly
rederive the retained target from its expanded member set. `NeedsDescription`
requires semantic recheck of the whole touched node, including old material.

Actual affected checks below passed; exact final-head receipts and normal CI belong
to the task/PR. Review and acceptance remain separate events. The independent
review sessions share the same GitHub account and execution environment; separate
sessions are not independent empirical replication or model diversity.

| Actual check | Result |
| --- | --- |
| `go test -buildvcs=false ./build/internal/annotation ./build/internal/extract ./build/internal/incremental ./build/test/e2e/semantic-evidence -count=1` | pass |
| Two affected full-pipeline frozen-source/local-contradiction regressions | pass; original assertions retained |
| Full pipeline package including injected writeback failure regression | pass |
| Opt-in `TestV013HistoricalAcceptedMemberRecovery` with exact authorized packet | pass; two mapping attempts,165 members/172 incidences |
| `go test -race -buildvcs=false ./build/internal/extract ./build/internal/incremental -run 'TestNode\|TestV013Acceptance' -count=1` | pass |
| `go build -buildvcs=false ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental` | pass |
| `go vet -buildvcs=false ./build/internal/annotation ./build/internal/extract ./build/internal/incremental` | pass |
| semantic-evidence Python checks / coordination Python checks | pass;10 /12 tests |
| local Markdown links / `make check-invariants` | pass;13 local links / all architecture checks |

Local environment: Go1.22.12, `GOTOOLCHAIN=local`, linked-worktree `-buildvcs=false`.
No paid call, full609 rerun or redundant archive campaign is part of this evidence.

Existing-target reflow's legacy persistence boundary cannot apply a repaired Summary.
The helper can return verified Summary+Description; the wrapper must retain old
content and explicitly report unresolved/partial when the summary changes. Necessary
persistence/application belongs to #113/#114, with exact factual helper outcomes
retained. Whole-product status and ordinary entry wiring belong to #114.
`reflow.go` remains exclusively #111 until its explicit accepted handoff to #112;
this report starts no relation worker.

For the serialized #112 handoff, the relation section of `reflow.go` is unchanged
from the starting main (I-7 onward SHA256
`4f043628cbe327a6906c40002656e14eee877c91c8048c7b48df4679f204429d`).
The receiving relation owner preserves the shared node helper and factual
`nodeDescriptionOutcomes` boundary. The coordinator records actual acceptance and
explicit release in #111/#112; only then may #112 become the file's next sole writer.
This prepared handoff grants no concurrent writes and does not start #112.
