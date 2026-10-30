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


## v0.1.4 source-aware relation helper and handoff

The [dated relation report](../../../../docs/coordination/reports/2026-10-27-v014-relation-fidelity.md)
records the new #112 execution group and helper acceptance separately from the
ordinary six-original product campaign. The cancellation suspension above is dated
history: PR121 was reaccepted and the coordinator
[renewed the explicit release](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6432388051).
The [acknowledged .4 claims](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6433074114)
name the exclusive writers and frozen source-aware interface.

`ProcessRelations` consumes complete frozen originals and accepted member material;
new selections require exact original evidence, direction, qualifying meaning and
explicit assertion credit. Supplied old candidates remain auditable without promotion
to original assertions. The source-aware changed-document adapter uses the same helper
and retains full persistent edge objects separately on absence, unknown or failure;
human data remains protected. Unknown assessment repeats the same candidate; actual
semantic defects revise the full candidate with original material and critique.
Positive N means N shared extra recoveries per source scope, zero defaults to three,
negative disables. Factual per-scope outcomes and attempts remain visible.

The original fixture/expectation inventory stays read-only and outside prompts.
Deterministic helper cases do not certify a real model's reconstruction of all six
private originals. The dated report records actual combined tests and source fixes.
Unsupported predicates retain their exact source-bound graph detail rather than false
edge-enum claims. Verified relation facets use `cairn_relation_assertions_v1` in the
existing description string, with explicit sources/evidence and separately labeled
`legacy_unverified` text. Prior JSON labels are not trusted: only exact current
verified `Selected` description/source/evidence matches keep explicit credit;
unmatched prior facets are downgraded and unknown-shape raw content stays intact.
Distinct full legacy duplicate rows defer their affected
replacement scope as unavailable, preserving all rows and reporting selected facts as
unapplied there. #113 owns complete persistence/readback; #114 owns ordinary entry
wiring and final aggregate/source-to-graph acceptance. The narrow terminal structural
reselection after node filtering preserves early repair inputs and starts no #114
work. Missing in-scope relation behavior still blocks #112 and the parent.

Fresh research after PR122 found an accepted whole-set revision could leave an obsolete
new assertion selected when another revised candidate terminated unknown/exhausted.
The [partial revision repair receipt](../../../../docs/coordination/reports/2026-10-27-v014-partial-revision-repair.md)
records the current correction and independent actual changed-helper/SQLite regression.
At accepted complete+supported replacement coverage, only exact still-present identities
retain current selection/cache credit; removed assertions remain audit history and need
a fresh verdict if reintroduced. Exact good siblings, distinct valid same-triple qualifiers
and other sources survive. Persistent old text remains separately `legacy_unverified`;
its retention cannot grant a superseded newly selected facet explicit credit.

A separate unchanged failing SQLite control localizes old confidence `0` → `0.8` to
pre-existing storage normalization despite exact adapter retention. #113 owns that
repair and complete persistence acceptance; prior .4 full-row evidence covers tested
nonzero-confidence rows only. This .4 repair starts no .5 execution or six-original
actual-model campaign and does not alter frozen inputs or historical reports.

## v0.1.5 persistence handoff

[#113](https://github.com/xcosmosbox/Cairn/issues/113) supplies bounded persistence
helpers and deterministic SQLite/Markdown readback controls; its
[report](../../../../docs/coordination/reports/2026-10-27-v015-persistence-fidelity.md)
maps each obligation to evidence. This does not perform #114's ordinary six-original
construction campaign or aggregate product completion.

`NodeSourceRepo.InsertBatchWithResult` separates attempted raw rows,
`DistinctSelected`, `DuplicateRows`, and verified `Committed` selected associations.
A repeated selected association remains committed without becoming a new source.
Repeated positions of the same source incidence select a real deterministic span
(known complete, then earliest), rather than fabricating a bounding range. Different
skill identity at one physical key fails explicitly; SQL failures return zero
committed for that transaction. `IngestDomains` exposes the corresponding
`NodeSources*` counts and exact missing-member mappings rather than skipping them.
The synthetic 172-attempt/171-distinct control demonstrates the arithmetic on real
SQLite; it does not replay or relabel the historical diagnostic's 172 raw rows.

Changed construction can opt in to `fuseDescriptionsWithVerifiedContent` and carry both
returned maps into `reflowResult.descriptions` / `verifiedNodeContent` before I-8.
Only complete current outcomes with exact matching ID and Description authorize the
atomic Summary+Description pair. The legacy `fuseDescriptions` entry keeps its truthful
summary-repair deferral until #114 switches the ordinary caller. Storage protects human
content and pinned fields, and repeated equal pairs do not change the node timestamp.

`WritebackContextWithRelations` accepts actual stored edges for optional schema1
`graph_metadata`; incremental writeback reads the stored graph directly. Exact source
associations carry `source_document` coordinates; generated block offsets carry
`generated_document` coordinates. Relation snapshots retain every stored row and full
Description assertion envelope, including source hashes, quotes and explicit versus
unverified classifications; serialization grants no new source credit. Relations remain
outside editable Markdown. Legacy sidecars and the old writer API remain usable, with
unknown relations represented as unknown rather than a fabricated empty graph.

Focused controls (from the repository root):

```sh
go test ./core/storage ./build/internal/ingest ./build/internal/writeback ./build/internal/incremental ./build/test/e2e/semantic-evidence
```

The composed tests exercise actually verified .3/.4 candidates, I-8 SQLite application,
I-9 primary/mirror/complete-block readback, repeated selections and zero confidence.
Separate injected failures prove their operational behavior; they do not replace the
normal selected-graph retention checks or the later source-first semantic campaign.


Fresh .5 research reopened #113 after an actual last-source deletion left stale
incident relation snapshots in independently sourced survivor sidecars, causing a
later human edit to be overwritten as C3. The
[neighbor writeback repair receipt](../../../../docs/coordination/reports/2026-10-28-v015-neighbor-writeback-repair.md)
retains the original failure and new actual-Run incoming/outgoing/shared-primary/
mirror/human/clean/repeat controls. Its injected postcommit sidecar failure also
checks the old baseline and a human edit made before retry. Both actual invalidation triggers (C2 deletion and source-aware
semantic I8 relation application) now propagate changed incident endpoints to existing I9 publication.
Guarded existing Run recovery refreshes only a derived stale relation cache after
exact core/source/hash proof, preserving Markdown and original editable baselines;
read-only Detect and the exact matcher are unchanged. The preserved failed retry
now recovers the later human edit normally, and same-Run pending human edits plus
shared/clean/repeat and affected package checks pass. New review, CI and
acceptance remain live #113 events; #114 is still unstarted.

## v0.1.6 ordinary integration and fixed campaign

The [dated integration report](../../../../docs/coordination/reports/2026-10-29-v016-integrated-acceptance.md)
supersedes the earlier unstarted snapshot: #109–#113 are accepted on main
`2ae4eef654e99e5d84de43b72e7dc65247c18bf1`; the newly acknowledged #114 group
owns ordinary wiring/status, integrated controls and the fixed actual-model campaign.
Its implementation/checks and provider outcomes remain separate from parent acceptance.
Frozen v0.1.1 sources, inventory and all 19 supplement cases remain unchanged.

`corpus-capacity --mode v016` consumes only a prepared Markdown tree containing
the exact six originals, `supplement.sources[].content` and separately empty
discovery markers. Do not point it at a directory containing inventories, cases,
expectations or other judge material. Each phase uses a new output directory.

```sh
go build -o /tmp/cairn-v016 ./build/test/e2e/corpus-capacity
/tmp/cairn-v016 --mode v016 --phase full --source-tree /authorized/fixed-source-tree --out /private/full-01 --max-tokens 65536 --semantic-retries 3
/tmp/cairn-v016 --mode v016 --phase repeat --source-tree /authorized/fixed-source-tree --previous /private/full-01 --out /private/repeat-01 --max-tokens 65536 --semantic-retries 3
/tmp/cairn-v016 --mode v016 --phase unchanged --source-tree /authorized/fixed-source-tree --previous /private/repeat-01 --out /private/unchanged-01 --max-tokens 65536 --semantic-retries 3
/tmp/cairn-v016 --mode v016 --phase changed --source-tree /authorized/fixed-source-tree --previous /private/unchanged-01 --change-path fixtures/operations/references/sibling.md --change-file /authorized/changed-addition.md --out /private/changed-01 --max-tokens 65536 --semantic-retries 3
```

Full/repeat call ordinary `RunFullRebuild` on pristine frozen bytes. Unchanged/changed
call ordinary incremental `Run` on a copied existing DB/generated tree. The latter
receive the caller-held full raw snapshots and accepted annotation documents from
the preceding phase. The declared changed prose updates its complete raw snapshot
at the same skill/path; generated blocks are never claimed as originals. Changed
source span evidence must match the actual raw revision or remain unknown.

Source/config hashes, actual attempts, reasons, raw transport outcomes, reports and
SQLite graph/source readback stay in the private evidence directory. A first actual
authentication denial stops repeating identical provider requests and keeps local
unavailable errors for other sources; it never supplies a fake model verdict. A
missing DB/tree/raw context prevents unchanged/changed phases from claiming a usable baseline.
An independent raw full repeat can run even after partial prior construction.
A partial but usable DB/tree/context may support in-place work; prior incompleteness
remains explicit in the phase aggregate even when that new operation succeeds.
No runtime key or provider success is assumed. Source-first semantic comparison and
independent review are required after an actual successful graph; runner/unit success
does not establish the 360-unit/194-context or all-case semantic acceptance.

The [2026-10-30 authenticated campaign report](../../../../docs/coordination/reports/2026-10-30-v016-actual-model-acceptance.md)
is the current actual-result record. The 2026-10-29 request-start-only automatic
approval interruption and earlier source-free401 remain dated unknown outcomes.
After explicit corpus/destination authorization and successful model listing, the
initial 32768 full ended partial: mapping truncation plus JSON-mode400 left only
three Skill shells. Three65536 corrected full runs also ended partial/exit1,
persisting respectively172/170/175,179/180/172 and161/159/160 nodes/edges/source
bindings. All bytes,360source units,194contexts and19cases remain in scope.

The last two corrected full runs retained34 and53 HTTP402
`insufficient_balance_error` responses. Independent source review still found
accepted-annotation false passes and persisted losses; complete annotation/node
helper status does not prove faithful final selection or provenance. Repair01's
amended supplement review is 6 PASS / 4 FAIL / 9 live UNKNOWN: enum-unsupported feed/shutdown
knowledge may remain in source-bound Detail, while supported dated dependency
edges and faithful summary conditions still matter. Final repair03 and repair02 S01–S03/supplement audits confirm losses;
repair03 drops28 same-UUID/different-text concept groups after completed descriptions.
Repair02 S04–S06 final audit confirms 20 missing members before selection; all360/194
expectations remain accounted for, including failed ones. Unforced failure controls, live human seed and stability unknowns remain
separate from existing deterministic acceptance controls.

Current published PR126 code3da0f4e5 includes opaque-node-alias, existing-human
preservation, relation enum/detail wiring and final human-overlay status corrections. None changes earlier frozen binaries.
The approved local 35-node check used exact original mapping/accepted04 material;
all 35 hashes matched production caller material offline. Actual execution reached
8 scopes, with27 unsent and no final semantic verdict; four `Forbidden` transport
errors contain no HTTP status/body. A separate tool poll explicitly returned
automatic approval rejection. No bypass or restart followed, and all model work
is stopped. Balance exhaustion is also a paid-run blocker.

Released same-domain exact duplicate calibration keeps the first existing traversal
classification and full semantic key equality, preserves metadata and qualified
endpoints, and rejects malformed-label merging and literal-frame hijacking. An
offline production-Go208→176 check preserves194 members and69 relation assertions;
there is no model generation, persisted graph repair or semantic acceptance credit.

Repeat/unchanged/changed are unrun and unknown. The examples above describe the
existing runner; they do not authorize a new call. No raw repeat on known-bad
annotation, fourth correction full or blanket paid human-preservation rerun was
added. PR126/#114/parent#108 remain unaccepted/unmerged.

Budget routing: the campaign flag governs annotation, mapping and source relations.
Full/changed node fusion uses maxRetries0 → fixed default3 in every recorded run,
including flag1 experiments. Current flag default3 aligns other stages; HTTP
retries remain disabled in these paid experiments.
