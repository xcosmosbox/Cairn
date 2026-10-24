# Real-model multihop experiment proposal — 2026-10-24

Task [#14](https://github.com/xcosmosbox/Cairn/issues/14); research session
`cairn-stable-baseline-research-20261024` (`gpt-6-astra/xhigh`). Inspected source:
`18e7e7bfc989fc0216625b0851e105690edd3950`.
[Scope ACK](https://github.com/xcosmosbox/Cairn/issues/14#issuecomment-6384305954).

**Adopted direction, not an executable freeze or release approval:** real two-reference
workflow smoke, separate corpus-only 12-document capacity calibration, full 609-document
build admission, then paired original-text versus production-graph answers. HOLDOUT stays
closed. Numerical quality/efficiency thresholds and the first-stable support contract remain
pending coordinator decisions before HOLDOUT. This document changes no existing inputs,
freeze, implementation or default. Current delegated credential egress was rejected by
automatic approval review; no paid retry is admitted by this proposal. That is an execution
authorization block, not a bad key, provider failure, exhausted budget or quality result.

## What this experiment can decide

Proposed primary scenario: an Agent answers cross-document factual questions from a fixed,
versioned English document knowledge base with inspectable citations. This matches the
build/Bundle/read-only-query architecture; its quality is not established. MultiHop-RAG is
a reusable public proxy, not evidence of arbitrary skill-document or Chinese performance.
The first-stable support contract is a separate [#18](https://github.com/xcosmosbox/Cairn/issues/18)
decision; graph failure does not silently redefine Cairn as an original-text FTS product.

Reuse the [accepted public protocol](public-multihop-baseline-20261024.md), exact corpus,
DEV128/HOLDOUT256 and thin adapter. Its frozen full-corpus source audit, grouping and
licenses remain authoritative. No new questions, gold, seed or benchmark ecosystem is
needed. [#21 acceptance](https://github.com/xcosmosbox/Cairn/issues/21#issuecomment-6382714099)
establishes free lexical evidence only. [#27](https://github.com/xcosmosbox/Cairn/issues/27#issuecomment-6382804686)
rejected source-cap2: visible FactRecall fell from 39.76% to 37.33%; no further same-DEV
cap tuning. The accepted protocol records earlier raw/graph negative results and their
limitations. Neither source overlap nor more graph structure proves answer benefit.

Two falsifiable representation hypotheses use the same question, sources and online budget:
original text retains needed details but wastes context on repeated/noisy chunks; real
graph summaries/relations may connect relevant facts compactly but can omit, distort or
misrank them. A free same-graph NODE-FTS diagnostic helps locate traversal/ranking effects.
It is not a third paid solver arm or proof of BFS's causal contribution to answer quality.

## Bounded stages and admission evidence

| Stage | Work and fixed sample | Required evidence before advancing |
| --- | --- | --- |
| Workflow smoke | [#16](https://github.com/xcosmosbox/Cairn/issues/16): existing public demo's two references, real ingest then MCP smoke | Actual model receipt, successful build, source lineage and real read-only MCP response; no benchmark benefit claim |
| Capacity calibration | 12 corpus-only documents, no QA access; complete production build | Per-stage calls/attempts, input/output usage, finish reasons, timing, skipped/dropped items, annotation size, largest combined request, graph/source identities |
| Full build | Entire fixed 609-document corpus, once; construction cannot read QA | Coordinator admits measured capacity plan; all 609 processed, no skipped document, actual combined input checked before dispatch, original lineage and ledger verified |
| DEV | Existing smoke8 first, then one DEV128 paired run | Offline-reviewed harness/config/source hashes; defects recorded, fixed once under a new implementation identity; no selective successful reruns |
| HOLDOUT | One paired HOLDOUT256 run only after explicit opening decision | Immutable graph, candidate, solver/judge, scoring and success criteria frozen; independent runner review; complete retained denominators |
| Product verification | Same real graph through MCP and actual response packing | Evidence for the supported end-to-end surface, separate from retrieval-adapter results |

The 12-document selection proposal is deterministic and independent of labels: sort the
609 originals by whitespace-token body length, tie-break by frozen `doc_id`; split at
indices `floor(k*609/4)` for k=0..4. From each disjoint slice select its first, lower-middle
(`floor((n-1)/2)`) and last document. Freeze IDs/body hashes before the first call; this
covers the shortest and longest bodies without using gold-support membership.

Proposed operational bounds to record in the execution manifest: one workflow-smoke
attempt with 30-minute wall limit, one capacity run with 90-minute wall limit, 600-second
build HTTP timeout, and no automatic whole-run retry. Preserve pinned stage retry limits
(annotation/Gate B each at most three attempts, at most three annotation rollback rounds,
extraction at most three attempts per coverage round, at most two coverage rounds);
record other stage limits and actual concurrency rather than assuming all stages share
one retry count. Timeout is a diagnostic stop, not evidence of bad model quality. Resolve
these settings in reviewed executable configuration before paid admission. The user has
not imposed a spending ceiling: no artificial monetary cap is an admission blocker, but
all calls, retries, unknown usage and actual cost must remain auditable.

### Whole-repository extraction is the capacity risk

The current [pipeline](../../build/internal/pipeline/orchestrator.go) annotates documents
individually, then [ExtractFromAnnotations](../../build/internal/extract/extract_from_annotations.go)
serializes **all** accepted annotations into one request. The estimated 1.25M original
corpus tokens are not a measured request size and already exceed the model's advertised
1M context; annotation could shrink or expand it. Measure, per document, original bytes,
codepoints, whitespace count, annotation bytes/items and provider input/output usage;
separately measure combined extraction JSON plus system/template overhead and reserved
output. Record tokenizer identity or explicitly label token counts as estimates.

Admission needs a conservative full-corpus projection by length stratum, the largest
document, expected stage/request counts and finish/skip rates. Before the full combined
request is sent, inspect its actual serialization and verify input plus reserved output
fits the supported context using a justified tokenizer/bound. Unknown fit is not a pass.
If it does not fit, stop at that boundary and propose a bounded batching/staged-extraction
ADR; do not silently truncate annotations, omit difficult documents, use gold-only inputs
or increase output limits as a remedy for excessive input. Preserve expensive intermediate
annotations and failure receipts; their later reuse needs explicit identity validation.

## New paired-answer protocol

This is a proposal separate from the free baseline and old Source Backfill experiment.
The [#13 handoff](https://github.com/xcosmosbox/Cairn/issues/13#issuecomment-6383015868)
permits a new controlled public-benchmark path without replaying historical paid controls;
it does not complete #13 or enable that pilot command.

| Setting | Original-text control | Real-graph treatment |
| --- | --- | --- |
| Knowledge | Existing `raw_source_fts_sqlite`, full 609 originals and frozen chunking | Production construction from those same originals and metadata, with no QA/gold access |
| Query | Frozen safely quoted original whitespace atoms joined by OR | Exactly that MATCH expression via existing explicit `query_syntax=fts5` |
| Retrieval | One original-question retrieval, top 10 | One actual `SearchPipeline` call, top 10; existing BFS/ranking defaults pinned |
| Display | 10,000 Unicode codepoints total, metadata/IDs/citations included | Same total including nodes, edges and all provenance; explicit clipping |
| Solver | `deepseek-flash`, thinking disabled, max_tokens 4096; one answer request | Same prompt/schema/parameters and one answer request |
| Exclusions | No follow-up search, rewrite, expansion, external search or extra evidence | Same, and no source backfill |

Do not mix the control's OR with production's default `text` literal-AND semantics.
[PrepareSearchQuery](../../service/internal/service/search_query.go) passes explicit fts5
through unchanged; ordinary text is a different supported contract. Report this exact
choice, not a claim that default natural-language search was evaluated. One answer call
per arm avoids adaptive query/turn confounds. Record actual token usage and order paired
arms using a frozen deterministic schedule; equal ceilings do not imply equal realized
cost. Build/update cost is separate from online solver/query cost and must be included in
any amortized efficiency claim. Do not add a baseline-only oracle/full-document context.

[cairn-eval-retrieve](../../service/cmd/cairn-eval-retrieve/main.go) calls real production
retrieval but explicitly bypasses ResponseBuilder, service token packing and MCP. Its
answer comparison is therefore an adapted retrieval experiment. Successful #16 transport
smoke is useful but does not prove equivalent answer quality through full MCP packing.

## Original provenance, scoring and stopping

Freeze original corpus bytes, URL/doc_id/body hashes and body offsets separately from the
working Markdown tree. Build rewrites that tree: `node_sources` paths/member IDs/line spans
can describe **rewritten** Markdown, not original article positions. Retain the complete
original-document -> annotation-04/member -> fused-node UUID lineage, original and rewritten
snapshots, and deterministic wrapper/offset mapping. Any quote presented as an original
quote must be checked against the pinned original body; generated annotations or matching
rewritten lines cannot validate it. A cited source containing a gold fact does not establish
that the fact was visible to the solver. An unresolved binding blocks grounded scoring.

Primary proposed endpoint: answerable-question grounded-correct rate, requiring a correct
answer, support for material claims in the **actually visible** evidence, and valid original
source citations. Report original-source support and visible-context support separately.
Keep benchmark-normalized answer match/F1, citation validity, correct null abstention,
empty/error/truncation rates, latency and actual tokens/cost as separate metrics. Exceptions
remain in the original denominator. Original-text exact-span FactRecall cannot be relabeled
as semantic fact coverage for generated graph summaries.

Reuse the repaired ledger and evidence-validation machinery, with reviewed thin adapters
and source hashes in the new freeze. Freeze answer normalization, judge rubric and
conservative failure handling before evaluation. Blind arm labels and randomize judging
order; validate claimed quotations mechanically. A `deepseek-flash` semantic judge remains
AI adjudication with same-model bias, not independent human gold. Report paired differences
with support-group-cluster uncertainty and question-type strata, including null separately.

Stop new admission on authorization, source/hash, read-only, ledger or source-binding
failure; drain admitted calls and retain unknown usage conservatively. Diagnose in order:
construction fact retention, lexical candidates, ranking, clipping, citation binding,
solver, judge/ledger. Retain negative outcomes. Do not reopen source-cap tuning or change
questions/budgets after seeing failures. Weak DEV evidence can stop the candidate without
spending the holdout; HOLDOUT is never a tuning loop.

Before opening HOLDOUT the coordinator must adopt the support contract, an absolute user
utility floor, practical effect/non-inferiority margins, uncertainty rule and abstention/
citation requirements. Research suggestions such as +5 percentage-point quality gain or
20% efficiency gain are **not adopted thresholds**. The sample may leave small effects
uncertain; an inconclusive interval is not a pass. Stable also needs the separate engineering,
compatibility, recovery, authentication, resource and rollback gates in #18. No current
build, negative/positive benchmark or successful CI alone makes that release decision.

## Provider contract checked against official documentation

Checked 2026-10-24: [Models & Pricing](https://api-docs.deepseek.com/quick_start/pricing/)
lists `deepseek-flash` as DeepSeek-V4.1-Flash-1017, 1M context, up to 384K output, JSON and
tool calling support. Treat the name as a moving alias: capture returned model identity,
request ID/date, finish reason and usage where supplied; configured model logs alone do
not prove the served revision. Do not substitute another model without coordinator decision.

[Chat Completions](https://api-docs.deepseek.com/api/create-chat-completion/) specifies
`thinking` enabled by default, default high effort, `max_tokens` up to 393216; ordinary
thinking defaults to 64K output. JSON mode also requires an instruction to emit JSON;
length termination is not a successful complete extraction. Current construction uses
JSON plus enabled thinking and 65536 output; the proposed solver explicitly disables
thinking with its separate 4096 cap.

[Thinking Mode](https://api-docs.deepseek.com/guides/thinking_mode/) says temperature is
ignored while thinking is enabled; xhigh maps to high. Thinking tool conversations must
return prior `reasoning_content`; the existing native solver's disabled-thinking contract
avoids that requirement. Preserve these distinctions in request receipts rather than
assuming temperature zero establishes determinism. No provider request was made by this
research session.
