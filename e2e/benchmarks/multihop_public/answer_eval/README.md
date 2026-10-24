# Frozen public paired-answer evaluator

Task [#54](https://github.com/xcosmosbox/Cairn/issues/54); method: [PR37](https://github.com/xcosmosbox/Cairn/pull/37).
This implementation has been exercised with synthetic fixtures only. It has not
opened actual QA/gold/HOLDOUT or called a model. The existing retrieval baseline,
old freezes, product/query algorithms and defaults remain unchanged.

`protocol.json` records the root-adopted comparative/evidence policies. Relevant
source/construction ADRs and the root-adopted scientific
[ADR0004](../../../../docs/decisions/0004-public-graph-evaluation.md) are listed explicitly. Final code/runtime/config/input hashes and separate stage admission are
required before any DEV/HOLDOUT execution. CLI opt-in flags do not grant admission.

## Reuse and resources

`adapter.py` calls the existing original-source FTS retriever and the existing Go
adapter's actual SearchPipeline. Both receive exactly the original question's
safely quoted whitespace OR expression, top10, and an exact10000-Unicode-codepoint
serialized evidence budget including all shown metadata, sources and relations.
There are no hidden source/relation count caps. Final raw body clipping shrinks
its original offsets; generated text gets no invented original interval. Adapter
scope bypasses ResponseBuilder/token packing/MCP and is not a hosted answer SLA.

`run.py` precomputes and snapshots every context and deterministic paired job order
before reading a provider key. It uses the existing native runner's shared
NativeDeepSeekClient/RunDurability/JSONLedger admission, retry, fail-stop and drain
transaction through a narrow per-payload method. Historical native request bytes
are regression-tested unchanged. The worker-pool pattern is the existing bounded
pattern, default8 for both arms. One4096-token disabled-thinking JSON response per
arm, no HTTP retry, further search or source backfill. Case start/end timestamps,
exact payload/context/source identities, all attempts and failures remain recorded.
Queued cases receive zero-call failure results after a stop; admitted calls drain.
A shared stateless no-redirect helper fences all3xx in both solver and legacy
judge paths; this intentional redirect behavior change preserves ordinary wire
bytes. The two existing phase transactions/ledgers stay separate and are
independently verified/namespaced; no nested admission or global callback patch.

## Judge and source contracts

`prepare_judge.py project` makes separate solver-only and judge-only projections
from the unchanged frozen roster after a separately admitted stage. Solver input
contains only question ID/text; the projection receipt links both files. `blind`
requires complete actual solver ledgers and exact question/context/run hashes.
Original excerpts are independently verified against pinned corpus bodies. Optional
original line hints stay unknown/unverified and never locate generated Markdown.

Two positive blind case kinds reuse unchanged `grade.judge`, its durable scheduler,
validation/recovery machinery and schema. A judges the actual answer against one
expected-answer target. B judges the exact packed visible context against unique
(originalURL,gold fact) propositions, not whether the solver restated every gold
background fact. B scores only complete `met` facts; partial/missing/contradicted
all receive0. Adapter guards require original URL/hash binding and actually visible
substantive support. Metadata/IDs/titles alone cannot prove a proposition. A claim's
retrieved references must belong to the solver's own declared citations. No method
labels enter judge inputs, although evidence style/structure can reveal a method.

Nulls have no judge and no invented placeholder fact. Valid abstention is exactly
`abstain:true, answer:"", citations:[], quotes:[]`. Every other output, malformed
response and transport/retrieval error counts as an attempted failure. Declared
quotes require exact original Unicode offsets and verbatim original/visible/answer
text. Unresolved/ambiguous judging remains provisional0 in the fixed denominator
and prevents formal acceptance; no selective successful rerun repairs it.

Judge input is bounded before requests, with no silent original-source clipping.
The existing judge's one validation/length retry and ledger-bound recovery policy
apply. Ambiguity is retained, not retried merely to obtain agreement. Model-based
semantic judging has same-model bias and is not independent human gold.

## Statistical outputs

`summarize.py` uses NumPy PCG64,20000 accepted positive global support-group
resamples (seed20261024), carrying both arms and cross-type groups together. It
redraws/counts empty-type replicates, computes each positive-type question mean,
and equally averages the three types. All positive vectors are generated first;
independent null singleton vectors use seed20261025. Exact vectors are persisted
before emitting the final summary. Lower bounds use the5th linear percentile.
Undefined precision replicates contribute0; no Wilson replacement or pseudo-count.
Degenerate endpoints remain explicitly nominal bootstrap bounds.

Infrastructure requires all609 actual documents and≥95% distinct(document,
accepted04member) incidence coverage, actual UUID/source ownership, valid emitted
citations/quotes, resolved judges, paired grounded-correct lower difference>−5pp,
and graph semantic VisibleSupportedFactRecall lower≥30%. The latter is an explicit
minimum evidence-discovery policy. It is a different metric from the earlier raw
exact-span39.76%; do not compare their numbers as a gain.

Separate cited-answer demo floors are50% grounded-correct lower,95% attempted
precision point/90% lower,90% null point/80% lower, and100% observed citation/quote
checks of declared quotations. Zero declared quotations have an undefined observed
rate and are explicitly reported; this is not a natural-language quotation detector.
Neither a noninferiority result nor both arms failing establishes answer
utility. Output separates infrastructure, absolute demo floors and answer
integration; it makes no superiority, hosted SLA or stable-release claim.

## Offline validation and later invocation

```sh
python3 -m unittest discover -s e2e/quality -p 'test_public_answer_transport.py'
python3 -m unittest discover -s e2e/benchmarks/multihop_public/answer_eval -p 'test_*.py'
```

NumPy is an explicit analysis-runtime prerequisite; no product dependency/lock is
changed and no alternate RNG runs if it is absent. Synthetic tests cover paired
failures, source/quote/citation guards, metadata-only false support, null abstention,
cluster sampling and drained401/ledger failures. Repository CI discovers this
package through its existing recursive public-benchmark unittest command.

Later commands, only after separate admission: `prepare_judge.py project` writes
solver/judge projections; `run.py` consumes pinned solver projection, full corpus,
raw receipt/index, graph binary/DB, construction proof and protocol; then
`prepare_judge.py blind`, `prepare_judge.py judge` and `summarize.py`. Each exposes
`--help`. All output paths are new and immutable. Inputs/contexts/answers may contain
copyrighted or restricted text; retain them in their authorized evidence packet,
not in public source commits. The checked-in tests contain synthetic data only.
