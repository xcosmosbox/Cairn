# ADR-0003: source-grounded validation of explicit cross-subdomain relations

Status: root-approved design; implementation acceptance pending independent review.
Date: 2026-10-24. Task: [#45](https://github.com/xcosmosbox/Cairn/issues/45).
Baseline: `c6a2b16f21434147947f4ed30d65a06321571546`.

## Current applicability — 2026-10-26

This record preserves the original bounded design and evidence. #105 removed its old
64KiB evidence microbudget, cumulative stage deadline and fullrace/verify/repeated-live-GO
admission as universal experiment prerequisites. Actual request constraints, source/alias/
quote ownership and relevant checks remain. [#107](https://github.com/xcosmosbox/Cairn/issues/107)
and the [v0.1 plan](../coordination/reports/2026-10-26-v01-build-completeness-plan.md)
now require explicit source-relation coverage and finite repair in full and shared changed-
document construction. That work is planned, not accepted implementation; the earlier
no-recall-guarantee/candidate-only scope cannot excuse in-scope v0.1 semantic omissions.
No retired harness or new admission framework is restored.

## Context

A two-document real smoke extracted an explicit dependency between nodes assigned to
different subdomains. Repair treated each subdomain as the only endpoint namespace and
removed that candidate. UUID translation also built only a local alias map, and full
ingest attempted semantic edges before nodes in later subdomains existed. Fixing only
endpoint resolution would admit unsupported candidates as well: the same output included
a purported contradiction based on something absent from a fixture. Endpoint existence
establishes identity, not truth. Historical smoke receipts remain failures, unchanged.

## Options and decision

Keep local-only edges, force related nodes into one subdomain, or accept every globally
resolvable edge were considered. The first loses existing explicit candidates; the second
changes taxonomy to mask the failure; the third promotes hallucinated semantics. A new
global discovery pass is unnecessary for repairing this bounded failure and would add an
unmeasured discovery/cost problem.

Use one exact alias resolver across repair and first-build UUID assignment. A unique
local match wins for compatibility; only when no local match exists may a globally unique
match resolve the endpoint. Missing and ambiguous matches fail closed with separate
observable dispositions. The internal key includes domain, subdomain and temporary alias;
no names, fuzzy matches, fixture heuristics or last-write-wins selection resolve identity.

Retain resolvable cross-subdomain candidates through repair. After ordinary per-subdomain
relation identification, validate each surviving cross candidate against complete original
documents contributed by both endpoints' verified annotation members. The pipeline captures
the exact annotation input bytes and SHA256 before any write-back; it does not reread later
generated documents, trust generated descriptions as source evidence, invent source spans,
or use the generated `source_skills` label to establish document ownership. The input and
report retain the member-to-source binding. No global crossedge discovery is added.

Each candidate gets one independent verifier request through the existing client and global
bounded parallel primitive. Every finite candidate receives an outcome; there is no count
cap. Candidates sharing documents may repeat input. That is the deliberate small initial
implementation, not a batching framework or a corpus-scale efficiency claim. The stage
inherits the existing stage context/deadline; cancellation is checked before and after
the client call and again before accepting a verdict, so a late valid response cannot
masquerade as completed validation. A complete serialized source-view neighborhood is
limited to 64KiB by default, configurable through `Options.CrossRelationEvidenceBytes` /
`CrossRelationOptions.MaxEvidenceBytes`. Oversized neighborhoods are reported as insufficient
without truncation or a model call. Larger callers must choose a capacity appropriate to
their model context; this byte limit is not a spending authorization or a token guarantee.

Original managed anchors and UUID-valued source facts also obey R1. A small deterministic
per-request source view replaces every canonical UUID-form literal in the entire input
with collision-free opaque aliases. Identical byte spellings reuse one alias across the
candidate's documents and metadata; exact case variants retain distinct reversible spellings.
The chosen alias prefix is absent from the original input, so existing marker-like source
facts cannot collide with inserted tokens. The mapping remains private in memory; frozen
bytes/hashes do not change. Evidence must first occur in the actual supplied view, then
aliases decode before member ownership and exact-original quote/offset checks. Invented,
malformed or non-round-tripping aliases reject that candidate. This does not rewrite the
existing annotation pipeline or allow raw-source UUIDs to bypass the new verifier's R1 gate.

The verifier must explicitly support the relation and direction, give a reason, and cite
exact original quotes owned by both endpoints' members. Code checks source hashes, member
ownership, exact quote presence and computes byte offsets (end exclusive) and 1-based line
positions from the frozen bytes. Repeated quotes use their first exact occurrence. Missing,
malformed, incomplete or invented evidence cannot accept a candidate. Invalid evidence
rejects only that candidate. Semantic rejection, insufficient evidence, malformed verdict,
endpoint failure and execution/context error remain distinct in the per-candidate report.
The report preserves verifier predicates and source identities; no metadata-only result is
advertised as proof that model semantics are correct.

For `depends_on`, source requires target as a prerequisite or constraint; correlation or
mention order does not prove dependency. For `contradicts`, the verifier must affirm two
incompatible claims about the same subject, attribute, time and conditions, with evidence
for both. Omission, a fixture's lack of coverage, and differently qualified claims do not
establish contradiction. These are explicit source-judging requirements, not deterministic
natural-language theorem proving. A model can still make a mistaken semantic judgment;
exact quotes make that judgment inspectable rather than infallible.

Only accepted cross candidates survive to UUID translation. Build the entire qualified
alias-to-first-UUID binding before changing any node IDs, translate endpoints against the
frozen identity index, then mutate IDs. Finish all LLM validation before that translation
(R1). First-build UUID derivation and namespace stay unchanged. Incremental existing
identities do not use this path and remain pinned (R2). Full ingest materializes all nodes
and hierarchy edges before its separate semantic-edge pass, so later-subdomain targets
are present. Confidence-filtered/missing targets remain rejected by existing selection /
application-level endpoint checks.

## Compatibility and limits

The ordinary source-relation helper supplies the exact focus original's current
neighborhood-local ID, path and SHA-256 to each new-candidate verifier. Source IDs
are rebuilt per neighborhood; a global `sN` is not reused by position. The complete
relationship assertion, including direction and qualifiers, must be explicit in
that focus original, subject to the existing strict contradiction rule below.
Other originals may establish endpoint identities; their
assertions or a focus quote naming an endpoint alone do not establish focus support.
Exact member/source/quote checks and the runtime focus binding still apply.

Generation and whole-set coverage use that same complete-focus-assertion contract.
Each source's round preserves its own explicit qualifiers and contributions; other
originals establish endpoint identity and reference context, not extra predicates,
qualifiers or attribution comparisons. Another source's independent assertion is
checked in its own round, rather than counted as missing from this one. Required
edges and source-bound node detail still need complete preservation. If verification
rejects focus support, the existing revision feedback retains the verifier's concrete
reason, focus identity and failed alias-wire candidate. This makes the same finite
repair loop actionable without adding requests, stages or recovery allowance.

The existing `contradicts` contract permits two originals to state the two opposing
assertions. The focus must explicitly contribute an actual conflicting assertion,
not just an endpoint name. Both sides need exact member/source/quote evidence and
all five existing predicates (same subject, attribute, time, conditions, and
incompatibility) must be true, preserving attribution and uncertainty. Absence,
unknowns and changes in time, conditions or versions remain insufficient. The
shared generation/coverage/verifier contract preserves this narrow rule; it does
not permit other relation kinds to borrow qualifiers from another source.

Full construction explicitly sets `SuppliedCandidatesAreNew`; the default remains
false for persistent incremental candidates. Only a definite `rejected_semantic`
audit of a new seed absent from current `Selected`, after its budget-owning source
has completed whole-set coverage and explicit-candidate verification, remains a
candidate-local rejection. Its partial decision, reason and attempts remain visible;
the rejected seed is not inserted and positive seed audits gain no explicit-source
credit. This does not repair or promote previously partial source work. Unknown,
malformed, evidence, transport, permanent and cancellation outcomes retain their
existing finite failure behavior, as do rejected persistent candidates by default.
The existing shared audit-budget owner is bookkeeping, not proof that the relation
is asserted in that source; a multi-source neighborhood's first source must not be
reported as semantic provenance. No independent audit scope or new budget is added.

No DB schema, Bundle format, UUID namespace, dependency, query API or query-side behavior
changes. Same-subdomain relation generation and valid local alias behavior are unchanged;
this ADR does not claim those relations now receive the additional source verifier.
Incremental relation replacement, human-curated protections and source ledger ownership
are unchanged and covered by existing regression gates. Original optional source spans
remain unknown when absent; verifier quote offsets are separate audit evidence and do not
backfill annotation positions.

Only actual extraction/repair candidates can become new cross edges. There is no crossedge
recall guarantee, no result for a candidate extraction never proposed, and no live-model
accuracy/cost or full609 feasibility claim. Missing original input, an oversized evidence
neighborhood or verifier failure can reduce the accepted graph; reports expose that loss
instead of claiming a successful semantic judgment. Completed-extraction replay still
requires callers to verify checkpoint origin and is not a substitute verifier.

## C4 source namespace continuity

Managed writeback omits discovery frontmatter. Before annotating C4 prose or assigning
member IDs, incremental Run resolves the exact source path against persisted node_sources
and validated caller frozen sources. Their unique namespace takes precedence over the
scanner's directory fallback. Conflicting or missing registered identities remain a local
unavailable document; no member-prefix guessing or generated Markdown substitutes for raw
source evidence. A genuinely new unregistered path keeps normal discovery. Current raw
snapshot checks and all later member/source joins remain required; stale or absent raw
originals cannot advance a changed document's baseline.

## Source-internal references and fused node prose

Node-prose coverage reads the complete frozen source, including its title and adjacent
sentences. A unique compatible reference may restore an omitted subject or version owner
without adding a fact. Multiple plausible owners, wrong entities/values or attributes
borrowed from another source remain unsupported. This permission does not establish a
`generalizes` edge or relax any candidate-edge focus, kind, direction or contradiction gate.

For fused node prose, each assertion must be checked through the actual node/member/source
joins against its corresponding raw original. Compatible contributions with unambiguous
ownership need no mechanical source-ID label in every sentence. Speaker disagreement,
conflicting values, conditions, time and uncertainty must still be preserved explicitly;
member material or a binding alone cannot substitute for correct surviving node prose.
Relation-only revision cannot repair immutable node text, and unresolved prose still fails
under the existing finite budget. Offline controls verify this request contract and failure
handling; actual model effectiveness requires a separately identified affected mini run.

## Candidate support eligibility and uncertainty

The relation verifier judges whether the exact candidate has explicit support for its
kind, direction and full qualifiers in the supplied originals. It does not decide whether
the relation exists in the world. Complete, usable, unambiguous sources that establish only
cooccurrence, inference or a mismatched predicate/direction can reject the candidate's
source-support eligibility without proving that the real-world relationship is false.
Missing/unusable necessary originals, unresolved reference or genuinely undecidable support
still produce `insufficient_evidence`. No program parses the reason into a different verdict.

This clarification does not change the evaluator, evidence/focus/contradiction gates,
Full-only new-seed rejection exception, persisted-edge defaults or finite shared budgets.
Historical unknown verdicts and their partial runs remain unchanged. Offline seed-shaped
controls establish the request contract and separate rejection/unknown handling only;
live effectiveness and separate subject/fact reachability gaps require their own evidence.

## Evidence and acceptance

Offline regressions cover reversed scope order, duplicate/local/global aliases, missing
and filtered endpoints, two-pass SQLite persistence, source ownership/hash integrity,
absence versus contradiction, direction/time/condition predicates, invented quotes,
incomplete/malformed verifiers, execution cancellation/errors, oversized evidence, more
than 64 candidates, unchanged local relations, managed UUID anchors, literal UUID facts, repeated identifiers,
marker collisions and exact decoded original positions. A full pipeline fake test changes disk
content after annotation and proves validation still consumes the captured input; it also
checks final UUID edge persistence and actual write-back. Fakes prove enforcement and
wiring, not semantic model quality.

Required gates: focused extract/pipeline/ingest tests; `go test -race -count=1 ./...`;
`make verify`; independent exact-head review and root acceptance. Any later real-model
verification needs a newly identified source/run and separate admission after review.

## Rollback

Revert this bounded implementation to return to local-only behavior without migration.
Already produced Bundles keep the same schema and identities; they are immutable and must
not be rewritten to alter historical evidence. A subsequent rebuild changes the candidate
edge set and must retain its own report/source identity. Do not undo incremental human or
source metadata as part of this rollback.
