# v0.1.5 persistence fidelity

Task [#113](https://github.com/xcosmosbox/Cairn/issues/113), parent
[#108](https://github.com/xcosmosbox/Cairn/issues/108). Baseline main
`c9e5872d750582e9de4c98ded0492814850b26cf`, tree
`632cc2509085a415ed95682bea9252d7e6531220`.

This report describes bounded persistence/application helpers and deterministic
acceptance controls. It does not accept the parent, claim six-original semantic success,
or perform #114's ordinary entry wiring or provider campaign. No paid model call,
full609/history rerun, frozen-input edit, SQLite migration or general recovery framework
was added. Normal CI and separate-session review remain required live PR events.

## Implementation and compatibility

`NodeSourceRepo.InsertBatchWithResult` reports raw `Attempted`, physical-key
`DistinctSelected`, `DuplicateRows`, and readback-verified `Committed` selected
associations. Repeat writes count the selected associations still committed, rather
than claiming new sources. Failed transactions report zero committed. Full materialization
exposes the same facts; an exact missing node/member mapping or failed SQL provenance
write returns an error before the graph version advances. Accepted contributions from
multiple extracted entries sharing a persistent node ID are accumulated.

`UpdateMachineContent` writes Summary and Description in one SQL statement, protects
`human_curated` identities and preserves UUID/name/source/ownership/properties/file
identity. Equal repeat pairs leave their node timestamp unchanged. The changed helper's
explicit `fuseDescriptionsWithVerifiedContent` method returns the complete verified pair;
I-8 rejects non-complete, wrong-ID or mismatched-Description payloads before writes.
The legacy `fuseDescriptions` entry still truthfully defers existing Summary repairs.
#114 must choose the opt-in method and carry both returned maps into the reflow result;
this report does not claim that ordinary callsite is switched.

Semantic edge replacement preserves legitimate confidence zero in storage and I-8.
For multiple legacy machine rows of one triple, the selected assertion aggregate updates
one deterministic row's Description/confidence while preserving its other fields; every
other original row and ID remains intact. The existing assertion coalescer retains all
old facets as unverified history and grants explicit source credit only to current
Selected assertions. This permits removing the old protective duplicate-scope deferral.
An updated aggregate Description is intentionally different bytes; lossless retention
means preserved semantic facets and row metadata, not byte identity for that field.
Human edges remain authoritatively protected and replacement failure rolls back.

Source deletion removes actual contributions/refcounts while preserving human-curated
nodes and curated incident/provides edges. A machine endpoint needed by an independent
human relationship survives without a fabricated source association. Unreferenced plain
machine nodes still delete through the existing helper. Existing move/recovery helpers
retain original member identity and source coordinates.

Sidecar schema1 gains optional `graph_metadata` version1. `SourcesKnown` and
`RelationsKnown` distinguish known-empty from unavailable caller context. Each original
source reference carries `source_document` coordinates; generated block offsets carry
`generated_document` coordinates. Legacy `span` remains an ordering hint and local
source spans are selected per document. Full `RelationSnapshot` includes the stored
ID, timestamps, endpoints, kind, Description envelope, properties, provenance, confidence,
source refs, direction and cardinality; `Edge()` reconstructs it exactly. Serializing a
snapshot grants no new explicit credit. Relations stay outside editable Markdown (R4).
Old schema1 sidecars/readers and old writer API remain usable. The new bounded
`WritebackContextWithRelations` API accepts actual SQLite readback; incremental writeback
loads the stored graph and refreshes moved-source metadata without replacing human
hash baselines or later edits.

## Acceptance map

| Obligation | Actual deterministic controls |
| --- | --- |
| Attempted/distinct/duplicate/committed and missing mappings | `TestV015StorageIngestSourceAccounting`, `TestV015StorageSourceBatchCountsAndRollback`, `TestV015Raw172Distinct171CommittedSQLite` |
| Full source failure, no false successful count or version advance | `TestV015FullSourceWriteFailureNoBaselineAdvance`, source-accounting write-failure case |
| Original source identity, repeat/change/move/delete and shared refcounts | `TestV015StorageOriginalSourceLifecycle`, `TestV015StorageRepresentativeSourceSpan`, `TestV015StorageAcceptedRepeatedOriginalOccurrence`, existing `TestSourceDocumentMoveRollsBackWholeBatch`, `TestV015WritebackMoveRefreshesSharedMetadata`, `TestV015WritebackTwoSharedMovesConverge`, `TestV015WritebackTwoSharedMovesRetryMetadataFailure` |
| Human nodes, human relations and clean data | `TestV015StorageSourceDeletionKeepsCuratedContent`, `TestV015StorageSourceDeletionKeepsCuratedRelationship`, `TestV015WritebackLegacyAPIHumanEditSurvives`, composed clean-file assertions |
| Complete verified Summary+Description through SQLite and shared primary/mirrors | `TestV015VerifiedNodePairSQLiteAndNoStalePair`, `TestV015StorageMachineContentPair`, `TestV015WritebackSQLiteGraphReadback` |
| Legitimate old and newly selected zero confidence | `TestV015OmittedZeroConfidenceOldEdgeSQLite`, `TestV015NewSelectedZeroConfidenceSQLite`, `TestV015SemanticReplacementPreservesZeroConfidence` |
| Actual apply of duplicate legacy rows, exact other metadata, current explicit/source/evidence distinction, stable repeats | `TestV015DuplicateLegacyRowsActualSelectionRepeatSQLite`, amended `TestV014RelationReflowDuplicateRowsApplyLosslessly`, storage duplicate/permutation/rollback controls |
| SQLite selected graph equals serialized sidecar snapshots; generated locations cannot impersonate original evidence | The pair/duplicate composed tests invoke actual I-9 and compare every incident full row; `TestV015WritebackSQLiteGraphReadback`, `TestV015FullSQLiteSelectedGraphWriteback`, `TestV015WritebackRawOccurrencesMatchPersistedRepresentative`, `TestV015ResearchSharedSourceSpans` |
| Legacy schema and known-empty/unknown compatibility | `TestV015WritebackMetadataPresenceAndLegacy`, `TestV015WritebackKnownEmptyDistinctFromUnknown`, `TestV015WritebackRefreshPreservesHumanBaseline` |
| Pair-write failure and later-edit preservation | `TestV015WritebackFailureRetainsPairAndBaseline`, existing `TestPairJournalRecoversCrashAndPreservesLaterEdit`, `TestConflictedJournalKeepsLaterEditAndAdvancesIndependentDocument` |

The synthetic 172-row test creates 171 valid distinct source incidences and one exact
duplicate, checks all 171 in actual SQLite, then repeats the source helper. It does
not replay the historical packet, assert historical semantic completion, or call the
full insert-only materializer twice on one live DB. The old 172 raw/171 distinct
observation remains unchanged and is not called a persistence loss.

## Preserved failures and review boundary

The original confidence probe from #113 comment6434350672 (file SHA256
`7ca29514220edaeab5951e725eb8fa65f378a3bd2b2b5cd6f7f9a913b909eed8`)
was durably adapted as `TestV015OmittedZeroConfidenceOldEdgeSQLite`. On c9e it actually
failed after I-8 changed an unchanged retained row from confidence0 to0.8. The captured
sanitized log SHA256 is `d73a276ffccfcf8c924284444eb738f672831937a012e73207f589d8c3c1dc5a`.
The new test passes only after the storage and I-8 fixes.

Independent baseline probes also actually failed for raw duplicate counts, swallowed
source-write failure/version advancement and shared-document local source span. Their
unchanged baseline probe SHA256 values are
`71e2e95994360329bfe6d0b2b061a82bf9842ccd4d429350238d6bf08494957a` (ingest),
`266c2aec86d0dd0288d98ca842fde529d77eadcc60ab9a55eee6cc6cf99328c4` (writeback);
combined log `95f72e2571a0a2472f29072f2f510d0692dd1df0c16324fe780da9e73af08116`.
The separate I-9 readback probe is
`4c5a5d94bdb590b1c1713a699999e4e05efcb05f9c248dae51acef7b39007e81`.
These are synthetic public fixtures; no restricted originals or credentials are in the PR.

Review found a further legitimate same-member/same-file multiple-original-span case
that the first candidate rejected. Acceptance/publication were held for correction;
a green initial suite did not waive this normal-case failure. The coordinator chose
a real representative span under [decision6442584342](https://github.com/xcosmosbox/Cairn/issues/113#issuecomment-6442584342),
without a schema/DTO/mandatory occurrence ledger. The earlier blanket claim that
multiple positions necessarily constitute an invalid metadata conflict was too broad;
these normal same-source repeats must succeed with raw2/distinct1/duplicate1/committed1.
Final corrected checkpoint and validation are recorded below before publication.

## Exact source and validation

Initial combined code checkpoint `7f2ec650c6afd17b275dbf43bfb7a070105105dd`, tree
`2dcc4bae6bdb9ec39499505d7791e7a1eba0eff8`, serially combines edge66bc0ddb,
storage02f0f40 and writebackddfd522 plus the bounded pair/duplicate adapter.
On that exact code, Go1.22.12 (`GOTOOLCHAIN=local`, `-buildvcs=false`) ran:

- `go test ./core/storage ./build/internal/ingest ./build/internal/writeback ./build/internal/incremental ./build/internal/pipeline ./build/test/e2e/semantic-evidence -count=1`: exit0.
- `go vet` on those same packages: exit0.
- `go build ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental`: exit0.
- `python3 -m unittest discover -s build/test/e2e/semantic-evidence -p '*_test.py'`: exit0.
- `git diff --check`: exit0.

Corrected code checkpoint `e42479669df4dfaf6c7ce21d8213c53bbd895a71`, tree
`82db3240d49fbb63ac95eed24ff6f4d0b8e33b02`, additionally integrates the genuine
same-source occurrence correction8d2900b and writeback correction982c3d, plus the
full SQLite-selected-graph → bounded writer composition test. The first writer
candidate also failed a two-shared-source batch rename: refreshing one destination
prematurely erased the other move's old sidecar proof. The correction attests all
committed moves before refreshing any metadata, refreshes sharing documents/primary
as one batch, and preserves retryability after injected metadata-write failure.
Full/raw writeback uses the same real representative span as storage, so the first
I-9 repeat does not churn source metadata. Mandatory path/identity/hash/member checks
and relation snapshots remain enforced; only a stale optional source cache after a
committed path move is refreshed from the authoritative ledger.

On this exact corrected code, the six-package tests, same-package vet and both CLI
build commands listed above all returned exit0. Test log SHA256:
`8d20bcad60138d88ca380fee656d7be82c4ff39b548256c5855e63a04a7dbb09`.
The successful vet/build logs contain no output (empty-file SHA256
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`).
The unaffected ten Python evidence tests passed at the earlier checkpoint; their
log SHA256 is `2d407504d003cfe404f23be48347e686ae1f67393860d4067d9a0bf06b1ddd60`.
All relative link targets in the three changed Markdown files exist, and diff whitespace
validation passes.

Separate-session research reproduced the old failures, checked permutation/repeat,
actual pair verification followed by a concurrent human edit or injected SQL abort,
and the corrected real AssignIDs multi-occurrence path. The pair protection probe
SHA256 is `a6acd39bd2bc82683c28a90c45cddbde3b3247c5a63f9d46292d6f07310db286`,
its successful log `9c528a556fdd10abcf44d18ed973ea710d4295a7ade5eaad03318df106f31ceb`.
The unchanged two-shared-source move probe passed at baseline c9e, failed at the
first writer candidate ddfd522, then passed at corrected writer982c3d3. Its baseline,
negative and corrected logs have SHA256
`7258ae8f1baa84c43390ff169892b9935fe50609735aec6670e2e606c27b4afb`,
`c8f6c683370474bc311f88aad51e4bb3a37983d915b1bebab84b7c7ca051b62c`,
`c01521f4238fd105f13f5611661bfe7c797fc0d565fe3949a62c9ccd87dfeb96`.
The corrected independent six-probe roundtrip check also covers post-commit metadata
obstruction/retry with a later human primary edit, shared-source deletion/conversion
to a complete block, known-empty edge metadata and repeat stability. Separate
contract controls on combined e424 passed (log SHA256
`25e0fffcf3ec9df3bd838ee6a184f3a7bc262e8af745ee37e51273fe5dfbeedf`).
These are bounded synthetic empirical controls, not a whole-original model result.

Final exact remote head review, normal CI, PR publication and maintainer acceptance
are separate live Issue/PR events. All sessions share the GitHub account/environment;
separate sessions do not establish account/model diversity. Author checks are not
independent review or whole-product acceptance.
