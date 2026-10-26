# ADR-0004: frozen public graph evaluation

Status: root-adopted preregistration in [#54](https://github.com/xcosmosbox/Cairn/issues/54),
2026-10-24, simplified under #105 on 2026-10-26. Scope was
confirmed by [the ADR amendment](https://github.com/xcosmosbox/Cairn/issues/54#issuecomment-6385976516).
The exact executable constants live in
[protocol.json](../../e2e/benchmarks/multihop_public/answer_eval/protocol.json).
This decision is a method, not a quality result or stable release. Existing spending authorization persists.

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

Construction records the intended original set and all accepted/member/source incidences,
including omissions and failures. A declared support-enriched DEV subset is permitted and
labelled accordingly; all609 and95% retention are not experiment admission conditions.
Actual UUID/member/document ownership remains required for cited graph evidence. Optional
span hints remain unknown; source semantics follow [ADR0002](0002-mcp-source-citations.md).

For positives, judge A evaluates the actual answer against one unchanged
expected-answer target. Judge B evaluates the exact actually visible context
against unique original(URL,gold fact) targets. Both reuse the existing judge,
validator and durable scheduler, with local original/visible/citation guards.
Credited B facts require complete semantic support, verified original URL/hash,
and a quote mapped to visible substantive body/description/relation content;
partial support, metadata/IDs/titles alone and unexplained omissions receive0.
This semantic evidence-discovery metric is not the historical exact-span raw
FactRecall39.76%, and their percentages cannot be interpreted as a gain.

For a small DEV diagnostic, publish observed paired supported-answer/abstention/source
retention outcomes with all intended question×arm slots. Unresolved/ambiguous judgments
are unknown/provisional0, never passes. Failures do not disappear from denominators.
No full256 bootstrap, all-type sampling or demo floor is required to publish this sample.
Tiny given-source results make no full-corpus retrieval, generalization or release claim.
The historical full-HOLDOUT statistical protocol remains available in git history; opening
HOLDOUT or making those claims requires an appropriate separately declared study.

Null success remains schema-valid abstain=true with empty answer/citations/quotes. Other
malformed/empty/error responses count as attempted failure. Quoted originals require exact
source/offset/text checks; generated descriptions receive no invented original intervals.

## Evidence, compatibility and rollback

Implementation validation uses synthetic fixtures only. Actual QA/gold/HOLDOUT,
paid construction/evaluation and benefit are not evidence of this ADR at authoring.
Bind selected source/DB/context and declared roster/resources once. Preserve raw
requests/responses/errors, all attempts, unknown usage and negative results; no repeated
whole-file hashes, source/preflight/live GO, counter/fee or archive admission is required. No existing
freeze, product API, Bundle, query-side invariant or graph algorithm changes.

Engineering review reproduced a standard-library redirect path that could forward
an Authorization header after an initial authorized POST. Under the explicit
[#54 transport amendment](https://github.com/xcosmosbox/Cairn/issues/54#issuecomment-6386191624),
a tiny stateless shared no-redirect wire helper now rejects every unexpected3xx
before any forwarded send. Native and legacy direct judge calls both use it;
ordinary request payload/header/model defaults are preserved. Redirect observation fails the affected endpoint request without forwarding credentials;
independent contexts continue and observed outcomes persist.
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
missing or invalid fails only the affected evidence/question/derived graph scope. This policy was fixed before
QA/model access and does not alter the production retrieval algorithm.
