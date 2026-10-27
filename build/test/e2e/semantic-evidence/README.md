# v0.1.1 finite source evidence

Task [#109](https://github.com/xcosmosbox/Cairn/issues/109) defines the source-first
acceptance inputs for the remaining v0.1 helpers. These files are evidence inputs,
not a builder, a benchmark score, or proof that the graph already preserves them.
No model calls are made by the checks in this directory.

`testdata/v011/supplement.json` contains small public synthetic originals and separate
expected observations. A construction runner creates the declared `empty_skill_markers` with zero bytes
and writes each `sources[].content` to its declared relative path;
it must never include cases, expectations, original inventory or judge material in
builder prompts. `inventory_summary.json` describes the complete six-original
private inventory using pseudonymous source IDs; its private referenced evidence
retains exact original bytes, identities, spans and semantic expectations.

The frozen original bodies are authoritative. The historical working input tree also
contains generated Markdown written during the old diagnostic; those views cannot
reconstruct the original denominator. The inventory author verifies original bytes
against the frozen annotation input, rejected-original input, corpus and raw DB.
Historical packets remain unchanged.

## Checks

From the repository root:

```sh
go test ./build/internal/discovery ./build/test/e2e/semantic-evidence
python3 -m unittest discover -s build/test/e2e/semantic-evidence -p '*_test.py'
```

The Go test checks public input/expectation identity, case ownership and inventory
summary arithmetic. The Python tools separately reconcile the saved stage sets and
check exact private source spans when the authorized packet is supplied. A passing
span partition establishes accounted-for text; semantic review must still determine
that each expected unit faithfully preserves its substantive distinctions. No public
structural check substitutes for private original access or semantic review.

## Small handoff contract

This is a test/report interchange, not a required production ledger or schema migration.
Existing helper result types should be reused where practical. v0.1.6 alone owns shared
aggregate production types and full/incremental entry wiring.

| Field | Meaning |
| --- | --- |
| `source_id` | Stable identity in one frozen corpus; combine repository identity with normalized repo-relative path in production |
| candidate identity | Exact annotation/node/relation/materialization result or content digest that was assessed |
| `outcome` | `complete`, `partial`, `unavailable`, or `cancelled` for the helper's own scope |
| `reason` | Concrete failure/exclusion/cancellation, empty for a successful scope only |
| stage identities | Source → annotation item/member → selected node/relation → committed association/file identities as applicable |
| attempts | Actual finite failed-step attempts/revisions and exhaustion status, when that helper attempted work |

For annotation → node handoff, each candidate names the authoritative `source_id`
(repository identity + normalized original path), its candidate revision/digest, and
stable item indices or existing member IDs scoped to that candidate. #110 supplies
item text and source association without changing item meaning silently; #111 preserves
the mapping from every accepted `(source_id, candidate, item/member)` to selected node
semantics. The existing `dktypes.AnnotatedDocument{Skill, FilePath, Items}` and
`AnnotatedItem` are the interchange; `FilePath` supplies the repository-scoped
original path. Item indices are scoped to the named candidate, and assigned member IDs
are reconciled after extraction. Malformed or unavailable candidates are explicitly reported, never an empty authoritative replacement.
This permits #111 helper development against this contract before #110 final acceptance.

`complete` requires all obligations in the named helper scope. A valid candidate subset
with a missing substantive distinction remains partial. Missing access or unknown
assessment is unavailable, never a semantic pass. Empty legitimate source content is
supported and empty; a failed read has a reason and is unavailable. Cancellation remains
distinct. Successful siblings are retained while required joins wait. v0.1.6 aggregates
complete/partial/failed without erasing unavailable/cancelled reasons.

`FSScanner.ScanReport` is the additive authoritative source API. Its report preserves
SKILL marker bytes and each configured supported UTF-8 reference exactly once, with explicit unsupported,
excluded, unreadable and cancelled dispositions. `Skill.ReferenceFiles` remains a legacy
reference-only list. v0.1.6 must consume supported report sources to include SKILL body
knowledge in ordinary full and shared changed-document paths; this task does not rewrite
those entry points.

## Downstream mapping

| Owner | Required use |
| --- | --- |
| [#110](https://github.com/xcosmosbox/Cairn/issues/110) | All original distinctions, malformed assessment, removed-row disposition, finite step retry/semantic revision |
| [#111](https://github.com/xcosmosbox/Cairn/issues/111) | Earliest extraction omission, alias/name collisions, fusion, faithful summary/detail and stable UUIDs |
| [#112](https://github.com/xcosmosbox/Cairn/issues/112) | Explicit relation direction/qualifiers, inferred distinction, dangling endpoints and human relations |
| [#113](https://github.com/xcosmosbox/Cairn/issues/113) | Distinct selected versus committed incidences, original provenance and consistent SQLite/Markdown readback |
| [#114](https://github.com/xcosmosbox/Cairn/issues/114) | Ordinary entry wiring, helper statuses, six-original plus supplement integration, repeat and changed-document acceptance |

Case-specific `owner`, `stage` and `evidence_kind` fields identify the named responsibility.
The unresolved exact LLM/parser/filter cause of an already localized historical stage
loss does not block #109's inventory acceptance; it remains with that stage's owner.
These fixtures neither waive that loss nor establish parent v0.1 acceptance.

## v0.1.2 annotation helper evidence

The [annotation revision report](../../../../docs/coordination/reports/2026-10-26-v012-annotation-revision.md)
and [three-row semantic disposition](v012_removed_rows.md) add deterministic helper
acceptance without changing these originals, expectations or historical packets.
`annotation.DocumentProcessor` returns source skill/path, candidate identity, factual
document outcome/reason and actual finite attempts. Only `complete` candidates may
enter downstream node processing; other retained candidates are evidence.

The shared recovery allowance counts extra failed-step recoveries: positive N gives
N, zero defaults to 3, negative disables. Invalid or explicitly unknown assessment
reassesses the same candidate with its own attempt reason; actual unsupported/omitted knowledge triggers original/candidate/feedback
revision and whole-candidate recheck. The C4 adapter uses this same helper with only
outside-block source text. Full-entry wiring and final six-original-plus-supplement
graph/aggregate acceptance remain #114. Source-preservation fixtures do not constitute
a new model judgment of the private originals or general semantic-completeness proof.

## v0.1.3 node helper evidence and integration handoff

The [node fidelity report](../../../../docs/coordination/reports/2026-10-27-v013-node-fidelity.md)
documents exact historical category-validation attribution, bounded synthetic
controls and an opt-in authentic retained-response replay. Frozen v0.1.1 inputs and
historical packets remain unchanged. Run affected node checks with:

```sh
go test ./build/internal/annotation ./build/internal/extract ./build/internal/incremental
CAIRN_V013_HISTORICAL_PACKET=/authorized/diagnostic105 go test ./build/internal/extract -run TestV013HistoricalAcceptedMemberRecovery -count=1
```

`AssignIDs` derives semantic member identity from exact skill/tag/name/content/detail,
independently of path, candidate order and duplicate source additions.
`LookupMemberContributions` separately retains source path, candidate digest, item
index and item material for every matched contribution. Exact accepted annotation
repeat stability does not prove model-paraphrase stability from raw-source reruns.

`ExtractWithCoverageOptions` and `RepairFinalCoverage` repair only unresolved mapping
members and retain good siblings. `CoverageOptions.MaxRetries` and shared
`FuseNodeDescriptions` maxRetries use positive N = N extra recoveries, zero = default3,
negative = none. The initial call is separate; a failed scope shares one allowance
across retries/revisions. `CoverageReport.Attempts` records actual targeted identities,
candidate digests and outcomes. Recovered mapping nodes still need description and
semantic assessment before final acceptance; `CoverageReport.NeedsDescription`
names every new/touched node. Read-only `retained#N` mapping aliases and accepted
alignment group keys join unresolved members to established semantic objects
without rederiving pinned UUIDs or resetting successful sibling work.
When repair consumes a post-UUID selected graph, #114 assigns UUIDs only to newly
created nodes; it must not rerun first-creation UUID derivation over retained
targets whose member sets have grown. Touched retained targets require complete
semantic recheck of their old plus newly contributed material.

`FuseNodeDescriptions` checks complete node Name/Summary/Description against all
source-bound member Name/Content/Detail plus existing machine description. Concrete
semantic critique revises the full candidate; unknown/malformed assessment retries
the same candidate. `NodeDescriptionOutcome` preserves complete/partial/unavailable/
cancelled, reason, actual attempts, exhaustion, and exact candidate/material digests.
Only complete candidates authorize applying both Summary and Description. Human
content stays outside model input; protection alone cannot certify new material.

After description, UUID assignment and candidate filtering, #114 recomputes
`ComputeFinalCoverage` on actual survivors. Its complete outcome is STRUCTURAL only.
Semantic candidate/material identity is recorded before UUID assignment: retain the
temporary-alias→UUID mapping or before/after snapshots and verify checked text,
members and full material remain identical. Combine exact surviving semantic
outcomes with final structural coverage; neither old counts nor a stale whole-graph
digest is a semantic pass. Existing data structures suffice; no new ledger is required.

Existing-target reflow cannot persist a repaired summary through its legacy
description-only boundary. It retains old content and reports partial/unresolved;
the verified repaired Summary+Description remains factual handoff for #113/#114.
Ordinary full/changed entry wiring, persisted graph acceptance and aggregate product
status remain #114. The node report and task acknowledgement record the eventual
serialized `reflow.go` release to #112; no relation work starts from this handoff text.

The explicit #112 handoff preserves .3's node wrapper/shared helper and
`nodeDescriptionOutcomes` interface; I-7 relation code remains byte-identical to
the starting main. After accepted integration, the coordinator records the actual
release in #111/#112. The next relation owner then holds the whole file exclusively;
node corrections require another serialized ownership amendment. Until that event,
#111 retains the file and #112 remains blocked. This text starts no worker.

Fresh cancellation reacceptance [reopened #111](https://github.com/xcosmosbox/Cairn/issues/111#issuecomment-6432223324)
after PR120: caller-cancelled existing-node reads incorrectly reported unavailable,
while preserving stored content and making no model call. The
[narrow repair evidence](../../../../docs/coordination/reports/2026-10-27-v013-cancellation-repair.md)
keeps the earlier acceptance as history. The previous #112 handoff is suspended;
the acknowledged .3 repair owner holds the file until a new explicit accepted
release. I-7 remains unchanged, and no .4 worker starts from this correction.
