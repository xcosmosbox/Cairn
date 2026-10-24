# ADR-0004: frozen public graph evaluation

Status: root-adopted preregistration in [#54](https://github.com/xcosmosbox/Cairn/issues/54),
2026-10-24; implementation and execution admission remain separate. Scope was
confirmed by [the ADR amendment](https://github.com/xcosmosbox/Cairn/issues/54#issuecomment-6385976516).
The exact executable constants live in
[protocol.json](../../e2e/benchmarks/multihop_public/answer_eval/protocol.json).
This decision is not a quality result, model-spending admission or stable release.

## Context and options

The accepted public baseline fixes all609 original MultiHop-RAG documents,
original chunking, a grouped DEV/HOLDOUT roster and a free lexical reference.
[PR37's method](../research/real-model-multihop-proposal-20261024.md) stages real
construction before paired answers. Production graph construction and adapted
retrieval have different provenance from original-text chunks; generated wording
cannot inherit an original interval merely through a node source row.

Reusing the historical adaptive native Agent unchanged would introduce three
searches/four model turns, different query semantics and a different raw index.
Reimplementing HTTP, ledgers or graph traversal would duplicate reviewed safety
and production algorithms. Superiority, noninferiority and absolute utility are
different claims; selecting whichever route wins after observing results would
not be a preregistered comparison.

## Decision

Reuse the frozen original-source FTS index and the actual production graph
adapter. Both receive the same explicit quoted-whitespace OR question, top10 and
10000 Unicode codepoints of exactly serialized visible evidence, including shown
metadata, relations and provenance. One disabled-thinking deepseek-flash JSON
answer request with4096 output tokens is made per arm. There is no adaptive
retrieval, rewrite, oracle backfill or graph tuning. The existing durable native
transaction is exposed through a narrow payload seam, preserving native default
request bytes; its shared bounded-worker/admission/drain pattern is reused.

Construction requires all609 actual documents and at least95% distinct
(original document,accepted04member) incidences before fusion/confidence/drop.
Shared member IDs across documents remain separate incidences. Explained
omissions receive no materialization credit. Actual UUID/member/document source
ownership is required. Optional original span hints remain unknown/unverified;
source semantics follow [ADR-0002](0002-mcp-source-citations.md) and source-grounded
cross-relations follow [ADR-0003](0003-source-grounded-cross-relations.md).

For positives, judge A evaluates the actual answer against one unchanged
expected-answer target. Judge B evaluates the exact actually visible context
against unique original(URL,gold fact) targets. Both reuse the existing judge,
validator and durable scheduler, with local original/visible/citation guards.
Credited B facts require complete semantic support, verified original URL/hash,
and a quote mapped to visible substantive body/description/relation content;
partial support, metadata/IDs/titles alone and unexplained omissions receive0.
This semantic evidence-discovery metric is not the historical exact-span raw
FactRecall39.76%, and their percentages cannot be interpreted as a gain.

The primary comparative route is noninferiority: graph-minus-raw grounded-correct
one-sided95% support-group-cluster lower bound must be strictly greater than−5pp.
Graph semantic VisibleSupportedFactRecall lower bound must also reach30%. The30%
threshold is an explicit minimum evidence-discovery policy, not a law implying
answer utility. Source/citation validity and actual materialization remain gates.

Freeze20000 accepted global positive support-group bootstrap draws using NumPy
PCG64 seed20261024. A cluster spans all its questions/types and both arms. Average
question outcomes within each positive type, then equally average the three types.
Reject/redraw empty-type samples and record rejections. Generate/store accepted
positive vectors first, then independent64-null-singleton vectors using
PCG64 seed20261025; combine by replicate index. Bounds use the5th linear
percentile. Undefined precision replicates contribute0. No Wilson replacement,
pseudo-count, dropped failure or alternate RNG is allowed. Degenerate endpoints
retain their mathematical value and are disclosed as nominal bootstrap bounds,
not exact finite-sample guarantees.

Null success is mechanical: schema-valid abstain=true with empty answer,
citations and quotes; no null judge or fabricated required fact. Every other
output, malformed/empty/error response is an attempted failure. Attempt precision
uses successful positive answers over all attempted cases across256; a zero point
denominator is undefined/no-pass. All failures remain in the fixed192positive/
64null denominator. Unresolved/ambiguous judging remains provisional0 and blocks
formal acceptance until frozen recovery/adjudication resolves it.

Separate cited-answer demo floors are grounded-correct lower≥50%, attempted
precision point≥95%/lower≥90%, null success point≥90%/lower≥80%, and100% observed
emitted-citation/exact-quote checks. Infrastructure and absolute answer-demo
outputs remain separate. Passing graph noninferiority does not claim an answer
pass when these floors fail; neither route declares a hosted answer SLA or stable
release. There is no post-result switch to a winning superiority/fallback route.

## Evidence, compatibility and rollback

Implementation validation uses synthetic fixtures only. Actual QA/gold/HOLDOUT,
paid construction/evaluation and benefit are not evidence of this ADR at authoring.
Before any admitted stage, freeze source/binary/runtime/config/data/roster/context/
blind-map identities, actual source bindings, judge recovery rules and bootstrap
vectors. Preserve all attempts, unknown usage and negative results. No existing
freeze, product API, Bundle, query-side invariant or graph algorithm changes.

Engineering review reproduced a standard-library redirect path that could forward
an Authorization header after an initial authorized POST. Under the explicit
[#54 transport amendment](https://github.com/xcosmosbox/Cairn/issues/54#issuecomment-6386191624),
a tiny stateless shared no-redirect wire helper now rejects every unexpected3xx
before any forwarded send. Native and legacy direct judge calls both use it;
ordinary request payload/header/model defaults are preserved. Redirect observation
intentionally stops new admission while already admitted calls drain and persist.
The solver's existing http_attempt ledger and judge's existing request_id/reservation
ledger remain distinct, independently validated and namespaced in aggregate
receipts. No transaction is nested or double-admitted. Local transport/validator
callbacks avoid global monkeypatches across workers; helper source hashes are part
of both runtime identities. Only old tests' injected mock targets change, not their
assertions. Offline same-host/cross-host/downgrade/POST fixtures use no sockets,
provider keys or actual QA. This is an intentional transport safety correction,
not unchanged behavior for redirects or evidence that a historical redirect occurred.
The Go retrieval adapter still bypasses ResponseBuilder/token packing/MCP; full
product-surface evidence is a separate requirement.

Rollback the new evaluator and payload exposure without altering historical data
or ledgers. Keep existing native behavior under its exact-wire regression and
retain this decision/history rather than rewriting an unsuccessful experiment.
Any scientific change after observation requires a separately recorded decision
and new implementation/freeze identity before further data.

Under the pre-QA [hierarchy decision](https://github.com/xcosmosbox/Cairn/issues/54#issuecomment-6386430179),
legitimate source-less Skill/Domain/Subdomain blocks retain their exact top10
ranking and visible budget position. An explicit structural_unbound marker and
all displayed text/metadata count toward10000 Unicode characters. They provide
navigation context, without original-source bindings: no original citation,
quotation, grounded claim support or bound-source fact credit. No filtering,
reranking, replacement, backfill or inferred originals is permitted. A solver
citation to such a block fails that answer and the emitted-citation check. A
judge's sole-unbound met verdict fails the existing provenance validator, using
the existing one-validation-retry then unresolved0/no-pass rule. A legitimate
structural block itself is valid product output; Entity/Concept leaf ownership
missing or invalid remains a global provenance halt. This policy was fixed before
QA/model access and does not alter the production retrieval algorithm.
