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
