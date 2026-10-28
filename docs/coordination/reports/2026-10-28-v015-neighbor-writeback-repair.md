# v0.1.5 neighbor writeback repair evidence

As of 2026-10-28 UTC. Task [#113](https://github.com/xcosmosbox/Cairn/issues/113),
new sessions `cairn-v015-r2-repair-20261028` and
`cairn-v015-r2-integration-20261028`. Starting main:
`69bd583426b578d103468e0fa55e1673d2d8f7c8`, tree
`f17fcc2d6e9dce2999f9f2e5e9b59e08d1b612b5`.
The [new research decision](https://github.com/xcosmosbox/Cairn/issues/113#issuecomment-6473317956)
reopened .5; PR124 and its dated acceptance remain unchanged history.
This report does not accept .5 or parent #108. Exact-head review, normal CI and
maintainer acceptance are separate live events. #114 ordinary wiring, aggregate
status and the six-original actual-model campaign remain unstarted.

## Reproduced failure and repair boundary

The actual incremental `Run` initializes a clean baseline for two independently
sourced nodes A and B. Deleting A's last original document removes A and A→B in
SQLite. The old bypass marks only A's source documents; I9 skips B's independent
source document, leaving a dead relation snapshot in its sidecar. B's next legal
Description edit is then classified C3 `sidecar_metadata` instead of C1, and the
old machine detail overwrites it. Both fresh roundtrip and contract research
sessions reproduced the failure on the exact starting main.

The original initial probe SHA256 is
`6c28e52577bb600f62e8e201141138497d7555047cb377aad26636f1a46831a5`.
The stronger complete-layer probe SHA256 is
`2a5650a8d8d306e16dea27e4f469b62f457f7427e307ab5f446142976cd4cda5`;
its preserved failed-before log SHA256 is
`c6b57840122c27a260f729c8018ffdbf07d59deab2e1e8442805d8953cbec73e`.
The first function in
[`zz_v015_delete_neighbor_test.go`](../../../build/internal/incremental/zz_v015_delete_neighbor_test.go)
is retained byte-for-byte from the complete-layer probe before additional focused
controls. These deterministic fixtures contain synthetic data only.

The narrow storage change captures distinct original source documents for surviving
incoming and outgoing incident endpoints before relation identities disappear. The
read happens inside the same deletion transaction, including phantom cleanup, and
is returned as `SourceDeletionResult.AffectedSourceDocs`. `applyBypassD` marks
these paths for existing I9 publication. Neighbor nodes are not marked semantically
dirty/touched, so this correction does not expand relation recomputation or model
work. Human-edge endpoint retention, source refcounts, no-contribution fail-closed
behavior and transactional rollback remain in force. No schema or format change.

An independently reproduced second invalidation trigger is source-aware semantic
relation application. Actual .4 verified selection reaches I8 SQLite, but its existing
context publishes only A's document, leaving independently sourced B stale.
[Scope amendment6473474620](https://github.com/xcosmosbox/Cairn/issues/113#issuecomment-6473474620)
assigns the actual I8 graph-change affected-document closure to the same production
owner. Research probe SHA256
`aed1f469988da99fa1ff1b9c30f175cfa76e724f6f57621ba0be7aecee8c0305`,
negative log SHA256
`01d77cd5dabbd3c7c558a20d28243646c2ea62d9e2c2116314cd2c56d7a554e4`.
The repair compares complete stored rows by ID before and after completed semantic
replacement, then marks source documents of changed old/new endpoints. Protected
human edges and exact no-op repeats do not expand the publication set. The actual
verifier-selected regression and changed/removed/retargeted/no-op/human-protected/
SQL-failure controls pass; ordinary .6 wiring remains separate.

## Actual composed acceptance and intermediate failure

The new
[`zz_v015_delete_neighbor_acceptance_test.go`](../../../build/internal/incremental/zz_v015_delete_neighbor_acceptance_test.go)
uses real SQLite, generated Markdown/sidecars and ordinary `Run`. A has the only
node in its subdomain; survivors occupy another subdomain. It covers:

- An independently sourced incoming endpoint and an outgoing shared endpoint,
  with both mirrors and its complete primary checked against every actual stored
  incident relation field after deletion.
- A human-curated neighbor reached by a machine edge, an unrelated clean document,
  exact survivor node/source-ledger preservation and a retained full-field uncertain
  edge with legitimate confidence zero.
- Later ordinary Description edits in the independent complete block and shared
  primary, C1 classification, human-curated SQLite values and a byte-stable repeat.
- Existing `beforeWriteback` injection after deletion commits: a directory at the
  incoming survivor's sidecar path forces the actual I9 metadata refresh to fail;
  its Markdown remains usable and an old sentinel file-state timestamp/hash must
  remain unchanged. The old valid sidecar is restored and a human Description edit
  is made before the next `Run`, testing preservation across retry.

Acceptance test checkpoint `a62b14337be79df7836d4eb2806a7f49b2d77a53`
on old production code fails on stale incoming relation metadata. Its negative log
SHA256 is `f47c166d8a32906ae299da1802cc34351e3c75a0f33e0eabd2af2edc6bc4eec0`.
Repair `ac9b74252a574e531e198f11fa574d9dda51176d` was integrated serially as
`a4c4c3f710c56318a0e72cbaf9f6dce52384e7d3`. The unchanged normal composed
acceptance and original complete-layer regression pass there.

The stronger failure/retry control still **fails** at this intermediate checkpoint.
I9 correctly reports the forced sidecar failure and preserves the old file baseline.
After restoring the old valid sidecar and editing B's Description before retry,
`Run` returns success with C1=0/C3=1 and overwrites that human edit. The combined
targeted command exits1; log SHA256
`08767b7ecd6ab5c8824b357fd24eb9c4a5dcb38274a0933af35f6a7e3f459de7`.
[Checkpoint6473486272](https://github.com/xcosmosbox/Cairn/issues/113#issuecomment-6473486272)
records this owned failure. The normal pass does not waive it. The later correction
and passing rerun are recorded below; this failed intermediate outcome remains history.

## Corrected recovery and final local result

The final correction uses the existing mutating `recoverIndependentWritebacks` phase
after journal recovery and shared failure expansion. For each owned source/file-state/
expected primary path, it requires the existing exact metadata matcher to prove every
schema/document/UUID/member/source/core read-only/editable-baseline field when only
new authoritative relation context is omitted. Unknown or corrupt identity does not
qualify. A qualifying stale relation cache is refreshed through existing
`RefreshGraphMetadataContext`, which preserves Markdown, `DocHash` and editable hashes;
the exact full matcher is re-read afterwards. C1 then sees a human edit against its
original baseline. Refresh failure retains an explicitly failed scope and baseline.

No `SidecarMetadataMatches` weakening, `orchestrator.go` change, read-only Detect
mutation, new journal or schema change was made. Canonical shared primary views receive
explicit actual SQLite incident rows during this recovery; legacy primary callers are
unchanged. Identity/source/read-only/hash mismatch, wrong repository and damaged
sidecars retain the existing protected classification path. This correction repairs a
derived cache; it does not grant source-assertion credit or create graph knowledge.

Final code checkpoint `92c5c0ffe376864c9adb591941e27718695b00da`, tree
`2b4e10562bc8bd294acc7149c823cd22408b258d`, integrates the three released repair
commits serially: `ac9b74252a574e531e198f11fa574d9dda51176d` (C2),
`78e35bcece4616ef948b4986070ac884893b6ff9` (I8), and
`db714c5d759ef3461df3b77bc33bfe54e17d99d8` (guarded recovery).
All targeted controls pass at this combined checkpoint, including the unchanged original
C2 regression and actual source-aware I8 regression; the previously failing postcommit
publication case now preserves the later human detail as C1/human-curated and settles.
The same-Run pending shared-primary edit plus C2 deletion also passes. Original negative
receipts and the intermediate failed retry remain retained above.

The retry fixture independently proves its prior sidecar differs only in derived relation
context: suppressing only that comparison in an in-memory diagnostic copy makes all
remaining authoritative metadata checks pass. The diagnostic is a test assertion;
production still reuses the unchanged exact matcher before and after refresh. A separate
failed-before identity-proof log is SHA256
`55b721262af65735c2fa103d531e7cce21cd94541cc879a8c0acff88acaa1754`.
The original acceptance expectation was retained: this unambiguous case must recover
normally, rather than being recast as permanently failed. Guard controls additionally
cover shared primary pending Description, immutable Detect/repeat, core/source/UUID/
editable-hash mismatch and explicit refresh failure. These failure controls establish
bounded preservation behavior, not general crash recovery or a model campaign.

## Separate-session review at the exact code checkpoint

Both fresh research sessions reviewed the exact combined `92c5c0f`/tree `2b4e1056`
above. Roundtrip ran 42 affected tests, including the unchanged strongest retry,
original C2, same-Run pending human, actual I8, prior six roundtrip controls and
independent wrong-repository/Detect-purity/member/hash/source guards: exit0; log SHA256
`bf5558a3c2bba85b00f68bcb50ed43648d9639cfcaea4d716a04338a618c0cb2`.
Its added independent probe SHA256 is
`c66fb767105a46291b17587b37d64af247a14875390829aa1bd80631175d20aa`.
Contract ran 17 top-level durable tests and three independent overlay probes: both
exit0; respective log SHA256
`3fce512eb5b28218cf2a1bf89353cc569f4e12252d77a39bcf38596dcaf04125`
and `2ccbea07f51f605acb1d67df81439f61d6c790001af57118ba86cbcf9707d338`.
These separate sessions share account/model/environment. They made no production
edits and reported scoped PASS; formal published-head reviews, normal CI and root
acceptance remain live PR/Issue events.

The original I8 test function is byte-identical in the retained research file
SHA256 `aed1f469988da99fa1ff1b9c30f175cfa76e724f6f57621ba0be7aecee8c0305`
and the new durable regression. Its function-body SHA256 is
`a930fcf1998f24c4e9e1ddcfae1f081e6c882a7936c9baa69b562a233539886d`.
The author's unchanged old/new I8 replay logs are SHA256
`e8a5004b0d201e10e560bd812f9b4946c3e974f825e471d56d0e72ee6f011846`
(exit1) and `aa6991f309a76f4b17639ba359fd1a9e3e99d314358a6d544bf755b811e55efa`
(exit0). The original complete-layer C2 probe is likewise an unchanged byte prefix
of its durable regression, verified locally. Earlier failures retain their original
source and outcomes; neither is relabeled as ordinary .6 model success.

## Commands and evidence limits

Commands use the restored Go toolchain with `GOTOOLCHAIN=local` and
`-buildvcs=false`; no provider request is needed. From repository root:

| Source | Actual command | Exit / meaning |
| --- | --- | --- |
| a62b143 (old production) | `go test -buildvcs=false ./build/internal/incremental -run '^TestV015DeleteNeighborAcceptanceRun$' -count=1` | 1, stale independent incoming snapshot |
| a4c4c3f | `go test -buildvcs=false ./build/internal/incremental -run '^(TestV015DeleteNeighborAcceptanceRun\|TestV015R2RoundtripDeleteEndpointRefreshesSurvivor)$' -count=1` | 0, normal deletion/human/repeat controls |
| a4c4c3f | `go test -buildvcs=false ./build/internal/incremental -run '^TestV015DeleteNeighborAcceptance' -count=1` | 1, pending human edit lost after injected publication failure |
| 92c5c0f | `go test -buildvcs=false ./build/internal/incremental -run 'TestV015(DeleteNeighborAcceptance\|R2RoundtripDeleteEndpointRefreshesSurvivor\|R2ContractI8IncidentNeighborRefresh\|Relation\|Recover)' -count=1` | 0, all targeted normal/failure/recovery controls |
| 92c5c0f | `go test -buildvcs=false ./core/storage ./build/internal/ingest ./build/internal/writeback ./build/internal/incremental ./build/test/e2e/semantic-evidence` | 0, five affected packages |
| 92c5c0f | `go vet -buildvcs=false ./core/storage ./build/internal/writeback ./build/internal/incremental` | 0 |
| 92c5c0f | `go build -buildvcs=false ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental` | 0 |

Go is `go1.22.12 linux/amd64`. Final targeted log SHA256
`f9d6413475f932b1ef14fa35c06281159e5b7000d3a89d14a0742e19e32ab524`;
five-package test log SHA256
`49efbbc818a75d737c462a6996f075399de6fc008e70665d15b653bad6c5d7b9`.
Vet/build produced no output (empty-file SHA256
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`),
with observed exit0. Changed-document local links, 12 coordination tests and diff checks
also pass. Final documentation-only commits do not change the checked Go source/tests.

Logs above are local receipts identified by content hash; the public synthetic tests
and recorded commands make their behavior reproducible without restricted artifacts.
No new paid/full609/historical model run, frozen-input mutation, generic recovery
system or release/tag is part of this repair. Separate sessions share the account,
model and environment; broader independence is not claimed. Passing bounded helper
controls do not establish whole-original semantic completeness or final v0.1 success.
