# v0.1: source-to-graph semantic completeness

Planning task: [#107](https://github.com/xcosmosbox/Cairn/issues/107). Research and
implementation baseline: `d2fa342312809b1d632308433ec507b523d86655`; comparison before
PR106: `b850ee4e4bf1074713c10b58b2a963b50640b1b9`. Date: 2026-10-26 UTC.
This document is a finite implementation and acceptance plan, not evidence that its
product changes are implemented. Product v0.1 and every child remain unaccepted.
Product parent: [#108](https://github.com/xcosmosbox/Cairn/issues/108).
Children: [#109](https://github.com/xcosmosbox/Cairn/issues/109),
[#110](https://github.com/xcosmosbox/Cairn/issues/110),
[#111](https://github.com/xcosmosbox/Cairn/issues/111),
[#112](https://github.com/xcosmosbox/Cairn/issues/112),
[#113](https://github.com/xcosmosbox/Cairn/issues/113),
[#114](https://github.com/xcosmosbox/Cairn/issues/114).

## Product contract and boundary

v0.1 covers the ordinary production path from supported authoritative originals to
persisted graph and generated Markdown/sidecars, and the same construction helpers used
for changed documents in incremental builds. It preserves meaningful source knowledge:
facts, conditions, exceptions, negation, time, uncertainty, disagreements, explicit
relations and their direction, and every valid source association. Summaries may abstract
but must remain faithful, without broadening qualified claims; graph detail and explicit
relations collectively preserve complete source semantics. Rational deduplication
and fusion may change node counts, but may not erase these distinctions. Keeping raw
originals alone does not establish graph coverage. If substantive knowledge is absent
from the graph's detail/relations, calling it a warning or known limit does not repair
the loss; a faithful abstract summary is valid when its omitted detail survives there.

Source relations explicitly stated by the originals are in scope. If an existing edge enum cannot
represent a supported relation, its semantic content and provenance must still survive
in graph detail; unsupported edge kind is not permission to discard the fact. If inferred
relationships are retained, they remain distinguishable from explicit original assertions
with correct source bindings; they cannot masquerade as source-stated facts. Retaining
only explicit relationships is also valid. Enumerating every
possible inferred relationship is not required. Repeated identical input must converge
without semantic loss, duplicate growth, UUID churn, or damage to human-curated material;
byte-for-byte equality of nondeterministic model prose is not the criterion.

One retryable or repairable failed document receives finite, configured in-place retries or critique-driven
semantic revision as appropriate to the failure. Transport retries do not substitute for
semantic revision. Successful independent documents retain their results and wait for
the required joins. Only after exhaustion may the build explicitly report partial or
unavailable completion. Cancellation and definite permanent source/configuration errors
may terminate directly, with truthful reasons. A normal semantic-success acceptance run cannot pass with
substantive omissions, distortion, or unexplained missing incidences. Forced-failure
tests establish honest partial behavior separately; they do not certify full coverage.

This decision supersedes #105's one-shot/no-semantic-revision and omission-as-warning
acceptance direction for v0.1, including the conflicting portions of its rows14,15,19
and selected-subset interpretation of row21. #105's actual code and experiment remain
historical facts. Its deletion of repeated technical GO, cumulative cost/time admission,
archive admission, full609/recovery prerequisites and retired harnesses stands. No old
governance, staged-history runner, capability ledger or full-recovery system is restored.

v0.2 owns retrieval/ranking/context packing; v0.3 the official MCP Agent; v0.4 fair effect
evaluation and its judge; v0.5 the daily update lifecycle; v0.6 packages/support. v1 follows
acceptance of those six main stages. v0.1.x are task milestones, not release tags. A
semantically complete graph does not itself demonstrate retrieval or answer benefit.

## Known evidence and unresolved attribution

PR106 removed semantic annotation/verification repair loops. The completed #105
diagnostic used six original support documents: five materialized, with173 annotation
rows,172 distinct accepted document–member incidences and171 materialized incidences.
All six initial annotations total205rows: the failed original29, the other five176
before assessment and173 after assessment; the3 removed rows also require semantic
disposition against originals, not automatic exclusion from the v0.1 denominator.
The missing association is already absent in the earliest available05 extraction result;
the final database matches that171-incidence final selection. `NodeSourcesInserted=172`
counts the raw input slice including a duplicate row; it is not172 distinct committed
associations. These are different quantities. The exact LLM-versus-parser/filter origin of the
early missing association remains unresolved; do not claim an entire graph node was
lost, a persistence regression is proven, or semantic retries were exhausted.

The failed original had29 annotation items. Its assessment returned HTTP200/finish=stop,
`supported=false` and `unsupported_items=[29]` although zero-based legal indices were0–28.
The gate rejected this malformed assessment and the current path did not semantically
retry. This establishes a mechanical rejection, not that the source fact was false.
These are new read-only diagnoses of the saved packet, not additional experimental runs.
The researcher matched the archive manifest at private commit
`1552707b04679ea0617732c104e377f311fde37e` (2073985bytes, SHA256
`cdf5f96ae41bee23ff16eba3e690bf392606691e5f0acf22de3d05c501c86921`)
and23 relevant retained entries; public output omits corpus identities/content.

The six originals, not the two used DEV questions or gold facts, define the source-first
semantic acceptance corpus. The old two-question utility outcome remains inconclusive.
Restricted raw originals/requests/DB remain at their existing authorized destination;
public records give sanitized methods and item-level conclusions. No historical result
or evidence packet is edited to fit the new method.

## Fixed-source problem evidence

All code links below refer to the baseline, not an implementation produced by this plan.

| Owner | Observed current behavior / concrete gap | Fixed source |
| --- | --- | --- |
| .1 | Unreadable discovery directories/walk errors may only log; accepted-file roster is insufficient as original-content denominator | [scanner.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/discovery/scanner.go#L133) |
| .2 | Empty items can bypass assessment; omission only warns; malformed item filtering can erase knowledge; ordinary full/incremental paths call once | [semantic_gate.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/annotation/semantic_gate.go#L27), [parser](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/annotation/llm_annotator.go#L145), [annotate.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/incremental/annotate.go#L118) |
| .3 | Name-derived member collisions; coverage counts unique member IDs; descriptions check structural presence, not semantic distinctions; late UUID/candidate filtering follows coverage | [identify.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/extract/identify.go#L27), [describe.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/extract/describe.go#L219), [candidates.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/extract/candidates.go#L31) |
| .4 | Early dangling-edge cleanup precedes repair; triple dedup drops differing qualifiers; unchanged cross-domain/touched-edge paths can retain stale or delete omitted valid edges | [completeness.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/extract/completeness.go#L28), [relate.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/extract/relate.go#L211), [reflow.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/incremental/reflow.go#L340) |
| .5 | Unknown member mapping may skip provenance; source insertion failure logs; raw input-row count differs from distinct SQL associations; generated Markdown paths cannot certify original spans | [ingest_domains.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/ingest/ingest_domains.go#L289), [node_source_repo.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/core/storage/node_source_repo.go#L64), [writeback.go](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/writeback/writeback.go) |
| .6 | Full/C4 orchestration must consume semantic outcomes; accepted annotation items alone cannot prove all changed prose was absorbed; existing CLI flags advertise ignored stage retries | [pipeline](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/pipeline/orchestrator.go#L398), [C4 pruning](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/internal/incremental/orchestrator.go#L1306), [ingest CLI](https://github.com/xcosmosbox/Cairn/blob/d2fa342312809b1d632308433ec507b523d86655/build/cmd/cairn-ingest/main.go#L44) |

## Finite milestones and dependency graph

| Milestone | Deliverable | Depends on | Initial state / integration boundary |
| --- | --- | --- | --- |
| v0.1.1 | Supported-input/source inventory, finite semantic evidence cases, known-loss localization | No product prerequisite | blocked until planning #107 accepted; then ready |
| v0.1.2 | Annotation fidelity, semantic verification and finite document revision | v0.1.1 | blocked; may develop after shared handoff is accepted |
| v0.1.3 | Member identity, mapping, deduplication, node fusion and summaries/details | v0.1.1 | blocked; disjoint from annotation/storage |
| v0.1.4 | Explicit intra/cross-domain relation preservation and bounded repair | v0.1.1, v0.1.3 | blocked; serialized `reflow.go` ownership after v0.1.3 |
| v0.1.5 | Persisted sources/materialization/writeback preservation and true counts | v0.1.1 | blocked; disjoint storage/helper work |
| v0.1.6 | Production orchestration wiring and integrated v0.1 acceptance | v0.1.1; final acceptance also v0.1.2–v0.1.5 | blocked; may start wiring after v0.1.1, cannot accept early |

The six table milestones map in order to #109–#114 above; their Issue bodies contain
the actual blocking/interface links. “May develop” is not
an active claim or permission to overlap another writer. Ready/blocked labels follow
the latest accepted dependency evidence. No product executor is started or assigned by
this plan. Each future contributor reads live claims, posts its own bounded CLAIM, and
uses an isolated branch under the existing protocol.

The minimum handoff is authoritative original identity, candidate result, explicit
`complete` / `partial` / `unavailable` / `cancelled` outcome and reason, plus enough
stage-specific identities to trace the result. Helper owners emit factual stage outcomes;
v0.1.6 alone aggregates the product complete/partial/failed conclusion, retaining
unavailable/cancelled reasons distinctly. v0.1.1 defines this contract once using
existing types where practical. It is not a mandatory new production per-fact ledger.
Each algorithm owner implements its own result/helper; v0.1.6 integrates the handoffs
and public status. Shared aggregate type/export edits are serialized through v0.1.6.

### v0.1.1 — authoritative input and finite semantic evidence

Goal: enumerate all supported source files and explicitly classify unsupported or
unreadable inputs; trace the six-original known losses without guessing their cause.
Create a compact source-first evidence inventory that accounts for the complete
substantive content, including conditions, negation, time, uncertainty, disagreements
and explicit relation evidence. Do not select only facts needed to answer the old QA.
The supported-input rule must explicitly address SKILL.md body versus reference files.
Navigation/boilerplate exclusions require an explicit reason; empty legitimate sources
are distinct from failed reads. A source's UUID-looking text remains ordinary content.

Unique writes: `build/internal/discovery/{interface,scanner}.go`, their affected tests,
and compact fixture/report inputs in an agreed existing build test location. No
annotation, extraction, storage, orchestration, model-response or historical packet
rewrites. Acceptance cases and stage correspondence are evidence inputs, not prompts
that leak the answer inventory into the builder.

Acceptance: every supported original is discovered exactly once with stable identity;
unsupported/unreadable/cancelled cases are explicit; no silent truncation; the missing
172→171 incidence and the rejected original each have the earliest available failure
boundary and honest unresolved attribution; all six originals' substantive distinctions
are represented in the finite expected-evidence inventory. Define deterministic small
fixtures for collision/fusion/relation/failure/identity cases not adequately covered by
those originals. Publish the small handoff contract and evidence mapping used by all
remaining tasks. Run affected discovery tests and fixture/inventory checks only.

Failure handling: retain source and unresolved rows. This task can complete once the
authoritative input/evidence inventory, reproducible earliest-stage attribution and named
downstream owner are accepted. Precise within-stage LLM/parser cause and repair belong
to v0.1.2/v0.1.3; they are not prerequisites to v0.1.1, which those tasks depend on.
Any inventory/discovery defect owned here remains unaccepted. If originals cannot be
accessed, this task's inventory evidence remains unknown. Downstream unresolved semantic
loss blocks its owning task and parent, not an independently completed inventory.

### v0.1.2 — annotation and semantic revision

Goal: preserve source meaning during annotation and verify semantic coverage/factuality
against the authoritative original. Separate malformed/transport/verifier-unknown,
unsupported claims, and omitted substantive knowledge. Apply finite configured in-place
semantic retries or feedback revision to the failed document; preserve valid siblings.

Unique writes: `build/internal/annotation/{format_gate,llm_annotator,semantic_gate}.go`
and annotation helper files/tests; `build/internal/incremental/annotate.go` only for
reuse of the same helper. No `pipeline/orchestrator.go`, incremental orchestrator,
extract aggregate types or storage edits. v0.1.6 owns callers and final result reporting.

Acceptance: each v0.1.1 annotation evidence case retains its substantive meaning; distinct
same-name items and source UUID literals survive; unsupported additions cannot become
accepted facts; critique repairs the intended document; a recoverable first failure
later completes; configured exhaustion becomes explicit partial/unavailable; cancellation
stops dependent work without erasing successful siblings. HTTP retry-only behavior fails
the semantic-revision case. Full and changed-document helpers obey the same contract.
Define each finite retry/revision parameter including whether its first attempt counts;
retain caller cancellation and avoid retrying known source/configuration errors blindly.
Retry the failed step: malformed/out-of-range verifier feedback retries assessment of
the same candidate, while actual semantic omission/false claims revise the offending
document using original, candidate and concrete feedback, then recheck the complete
candidate. Service unavailability is distinct from correctness feedback. Successful
siblings are not re-extracted. Use existing finite parameters with explicit total-attempt
versus extra-retry semantics; do not restore the historical layered72-wire worst case
wholesale or create economic/time admission.
Test nonempty-source/empty-items, malformed/null/contradictory verifier fields, filtered
bad rows, truncated output and unknown assessment separately. Reverify the entire revised
candidate so repairing one omission cannot erase another. Three-document success/repair/
exhaustion preserves the immediate success without rerunning it, waits all terminal states,
and reports the batch partial. C4 keeps human blocks outside model input and preserves
unresolved prose/baseline. Run affected annotation/incremental annotation tests with deterministic model fixtures;
retain actual attempt outcomes and reasons. Do not require paid reruns or full609.

### v0.1.3 — node mapping, fusion and semantic material

Goal: map accepted members to stable nodes without dropping source incidences or merging
different facts because of names, normalization or aliases. Fusion summaries must be
faithful; node details and explicit relations collectively retain conditions, exceptions,
negation, dates, uncertainty and source disagreements, even when summaries abstract.
Both new and existing-target paths obey the same checks and human-edit protections.

Unique writes: node-related `build/internal/extract/{identify,fusion,describe,
extract_from_annotations,candidates,completeness,coverage,uuid,file_slug}.go` helpers and
their tests; `build/internal/incremental/align.go` and node sections of `reflow.go`.
No relation helper edits, no orchestration. `reflow.go` is exclusively owned by this task
until an explicit handoff to v0.1.4; shared aggregate types/export wrappers are requested
from v0.1.6 rather than concurrently edited.

Acceptance: all intended document–member contributions map to retained node semantics
and valid sources; same-name/different-meaning and normalization collisions do not erase
knowledge; true duplicates combine deterministically while retaining each source;
invalid aliases cannot target another persistent UUID; merged targets retain UUIDs and
human content; incomplete or unknown fusion output cannot mean an empty replacement.
The earliest05 missing accepted member triggers repair where first detected; after
description, UUID and candidate filters, recompute final member/source and semantic
coverage rather than reusing an earlier count. Finite targeted revision precedes explicit
partial status. Repeated identical input has stable semantic identity and no duplicate growth. Both
ordinary full construction and shared incremental node-helper cases pass the finite
inventory checks. Run focused extraction/node/reflow tests and changed build targets.

### v0.1.4 — explicit relations and repair

Goal: preserve explicitly supported intra-domain and cross-domain edges, directions,
conditions and evidence; repair genuinely dangling or omitted required endpoints/edges
with finite semantic work. Distinguish an explicit negative/contradiction from absence,
unknown or a time-qualified change. This task does not enumerate speculative inferences.

Unique writes: `build/internal/extract/{relate,repair,relation_endpoints,cross_relations}.go`
and relation tests; relation sections of `build/internal/incremental/reflow.go` only
after v0.1.3 releases that file. No node-fusion algorithm rewrite, storage or orchestration.

Acceptance: each explicit relation evidence case survives full and changed-document
helpers with supported endpoints/direction/qualifiers/source binding; retained inferred
relationships are distinguishable from explicit original assertions and cannot inherit
false explicit-source credit. This owner defines/verifies that distinction and binding,
without requiring a new schema or an inference enumeration. The same triple with
different valid time/condition/source distinctions must retain them; malformed aliases,
unsupported claims and verifier-unknown outcomes cannot be certified; missing output
does not delete existing edges; finite repair recovers a fixable case and exhaustion
reports the failed dependent scope; human-curated edges remain protected. A touched valid machine edge is not deleted merely
because a response omits it; new evidence affecting an existing cross-domain relation
triggers revalidation. Preserve good
independent relations and successful documents while waiting for valid joins. Run the
affected endpoint/cross-relation/repair/human-edge regression cases.

### v0.1.5 — persisted provenance, materialization and writeback

Goal: ensure accepted distinct source associations and semantics reach SQLite and
Markdown/sidecars consistently; report attempted rows, distinct selected incidences and
actually committed associations separately. Do not infer persistence loss from unlike
counters. This owner supplies factual stage counts/status; .6 alone aggregates product
completion. Preserve original identity and every valid source when many documents share a
node or a source is moved, replaced or removed through existing construction helpers.

Unique writes: affected `core/storage/{node_source_repo,source_document_move,
incremental_mutation_repo,node_repo,edge_repo,file_state_repo,manifest_repo}.go` helpers;
`build/internal/ingest/ingest_domains.go`; `build/internal/writeback/` helpers;
`build/internal/incremental/{ingest,rewrite,
managed_moves,writeback_recovery}.go` only for affected construction helper fixes and
regressions; no new move/delete/lifecycle features (v0.5), plus focused regressions. No
production orchestrator/controller edits and no new general recovery system. Concrete
schema changes, if needed, must be declared with compatibility evidence in this task.

Acceptance: selected distinct associations equal committed selected associations on a
normal successful case; every dropped/duplicate/failed row has the correct classification;
the172 raw-row/171-distinct historical observation is not misreported; raw origins and
shared-node refcounts survive repeated/changed/moved/deleted-source helper cases; human
edits and clean documents survive; summary and description remain separate and preserve semantics through SQLite, complete
blocks, shared primary/mirror Markdown and readback; DB and generated file pairs represent
the same selected graph; generated Markdown offsets are never falsely labelled original
evidence locations; retained explicit/inferred relationship distinctions and source
bindings survive persistence/readback using a suitable existing representation, without
mandating a new schema; injected write failure leaves the previous usable state or explicitly
reported failed scope without advancing its baseline. Reuse atomic publication/WAL,
pair-write/path/later-edit and transaction regressions only where changed.

### v0.1.6 — sole wiring and integrated acceptance

Goal: wire the accepted stage helpers into the ordinary production entry paths and run
one fixed finite source-to-graph acceptance campaign. Correct failures through their
owners and rerun the affected cases without sample reselection or denominator shrinking;
do not rerun unrelated historical paid controls. This task owns integration and status, not
all unfinished algorithm changes. Any discovered defect returns to its existing owner
within this finite partition; it does not silently become another hidden prerequisite.

Unique writes: `build/internal/pipeline/{orchestrator,materialize}.go`;
`build/internal/incremental/orchestrator.go`; shared extraction aggregate
types/parsers/export wrappers (`extractor.go`, `export.go`) when required, applying
v0.1.3/v0.1.4 narrow proposed patches under this sole writer; existing `build/cmd/cairn-ingest/main.go`,
`build/cmd/cairn-incremental/main.go` and
`build/internal/controller/runner/runner.go` entry/config/report fields and integrated build test/report inputs. Exact entry file
amendments are declared before editing. Algorithm helpers stay with v0.1.2–v0.1.5;
integration coordinates their interfaces without overlapping writes.

Acceptance: run ordinary full construction on the six authoritative originals and the
finite supplement; inspect graph semantics/provenance against the source-first inventory,
not QA answer success. Validate a repeated unchanged build and shared changed-document
construction path. Every normal-success expected substantive distinction and explicit
relation must be retained without unsupported additions or unexplained source loss.
Read back persisted explicit/inferred distinctions and source bindings: an inferred
relationship must never be reported as an explicit source assertion.
Separately exercise first-failure recovery, exhaustion, cancellation, two independent
documents with one failure, and dependent joins: successful siblings remain, finite
attempts and statuses/reasons are visible, and no “complete” result masks partial work.
Run focused changed build/incremental integration checks, changed binaries and normal CI.
Actual model-based construction records inputs/config/attempts/raw outcomes once under
standing authorization; this planning turn performs no paid run. Public sanitized results
and exact source/commit evidence support a final parent acceptance decision.

## Contract → task → evidence closure

| Contract obligation | Owning milestone(s) | Required acceptance evidence |
| --- | --- | --- |
| All supported authoritative input and meaningful content accounted for | .1, .6 | Discovery roster, source-first finite inventory, full production comparison |
| Annotation fidelity, coverage, no unsupported claims | .2, .6 | Source-backed annotation cases and integrated graph comparison |
| Finite document retries/semantic revision, successful siblings wait | .2, .6 | Recoverable failure, exhaustion, cancellation and join cases with attempts/status |
| Member/alias/UUID identity and rational dedup without fact loss | .3, .6 | Collision, duplicate, same-name, existing-target and repeat cases |
| Faithful summaries; details and explicit relations preserve full semantics | .3, .6 | Complete source-first distinction checks through final graph |
| Explicit intra/cross-domain relations, correct endpoints/evidence | .4, .6 | Positive/negative/time/unknown/dangling/human-edge cases |
| Retained inference distinguishable from explicit original assertion | .4 defines/verifies; .5 persists; .6 reads back | Source-bound distinction survives graph/storage/writeback; no false explicit-source credit |
| Every valid distinct source association and original identity retained | .1, .3, .5, .6 | Stage incidence reconciliation and committed provenance checks |
| Atomic selected graph and Markdown/sidecar consistency | .5, .6 | Focused transaction/WAL/pair/later-edit failure cases |
| Stable repeat and shared changed-document construction | .2–.6 | Semantic repeat/idempotence and changed-document helper/integration cases |
| Honest complete/partial/unavailable/cancelled outcomes | .1 contract; .2–.5 results; .6 wiring | Normal success distinct from injected operational failure reports |
| Query read-only/zero LLM/zero embedding and human data preserved | All touched owners; .6 checks | Relevant existing invariants and human/identity regressions |

Let `Ai` mean that child v0.1.i has its implementation integrated and its stated evidence
accepted on the integrated source, not that its Issue was created or plan approved.
The finite closure is `A1 ∧ A2 ∧ A3 ∧ A4 ∧ A5 ∧ A6 ⇒ v0.1 contract accepted`.
v0.1.6's final evidence includes the earlier helper changes, so stale unit evidence cannot
substitute for the integrated result. Acceptance still records the ordinary maintainer
decision under the existing protocol; it adds no secret admission stage, extra benchmark,
mandatory new ledger, universal recovery/scale suite or new task series.

Any missing substantive distinction, unsupported distortion or unexplained required
association keeps the responsible child and parent unaccepted. Forced partial execution
can pass its operational regression while the normal full-coverage case remains failed.
Acceptance binds this exact commit, supported-input contract and finite original/fixture
corpus; it does not prove absolute completeness for every arbitrary natural-language
input. An in-scope defect found before acceptance returns to its named existing owner
and blocks the parent; one found later is a v0.1.x correction preserving the dated
original evidence. This finite evidence boundary is not permission to waive a known loss.
Known limitations may describe out-of-scope retrieval, inference enumeration, effect and capacity;
they may not redefine an in-scope semantic defect into success.

## Publication and execution handoff

Planning #107 completes only after the reviewed finite plan, product parent/child Issues,
dependencies, labels, ROADMAP/#8/#7 and current normative instructions agree, the docs PR
is merged and the default branch is readable. Product parent and children remain open
until their own acceptance. #14 is future v0.2/v0.4 backlog, blocked on the relevant main
stage, with the inconclusive #105 outcome preserved; it is not dispatched by publication.

Four read-only research sessions reviewed this same complete plan and all seven Issue
drafts: `research_retry`, `research_fusion`, `research_storage`, `research_acceptance`.
All passed after one fusion correction and the coordinator's circular-dependency
clarification. The [coordinator accepted content for publication](https://github.com/xcosmosbox/Cairn/issues/107#issuecomment-6415175290),
not product implementation. Publication author: `cairn-v01-plan-publish-20261026`;
coordinator: `cairn-v01-plan-20261026-root`. The four researchers used the same
gpt-6-astra/xhigh research model and all sessions publish through the same GitHub account;
separate-session review is not independent empirical replication or model diversity.
The author does not accept its own work as independent evidence.

Accepted pre-publication SHA256: plan
`a571b6104d471b6f0956c5775613778f76288557aae85f04a41352de39eb9a33`;
exact parent+six-child draft JSON
`bec2d4a6503b3eabf61957f7e0b1c42f70d15abbb03f70a8fadaa7e2f8360298`.
Publication binds actual Issue numbers and these record links without changing reviewed
scope or acceptance. The docs PR/default-branch check and final planning acceptance
remain separate evidence. No background job is implied.
