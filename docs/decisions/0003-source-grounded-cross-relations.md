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
