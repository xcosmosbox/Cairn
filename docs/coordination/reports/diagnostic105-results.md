# Fixed DEV support-enriched diagnostic — 2026-10-26

Task [#105](https://github.com/xcosmosbox/Cairn/issues/105). The
[accepted plan](2026-10-26-simplification-plan.md) and
[selection fixed before outcomes](diagnostic105-selection.json) define this run.

The first two existing selected DEV qids (both inference questions) determine a complete
six-original support-document union. MultiHopRAG revision
`71ac0d0bd1f951d2d6b70311f7d2ae404e1ffa82` and original source hashes are pinned.
This is support-enriched exploratory given-source utility. It does not measure full-corpus
retrieval superiority, new-question/document generalization, null refusal, Chinese quality,
HOLDOUT performance, hosted MCP answer quality or stable-release readiness.

The ordinary production `cairn-ingest` built a fresh isolated DB from original-only wrappers
with `--no-evolve`, official `deepseek-flash`, output65536 and per-request1800s timeout.
`--max-retries=-1` disables physical request retries. No cumulative time/cost/counter/archive
or repeated technical admission controls preceded it. Source and human/UUID/alias/atomic
integrity protections remain. Raw source/request/response/error bytes are retained privately;
no article text, question/gold or credentials appear in this public report.

Both paired arms use exactly those six source originals and questions, safely quoted OR,
topK10, serialized context10000 codepoints and one disabled-thinking deepseek-flash JSON
answer request with output4096. Raw FTS uses256-token chunks/224 stride (39chunks;253952B).
Graph calls actual production SearchPipeline via cairn-eval-retrieve, bypassing MCP and
ResponseBuilder/token packing. Solver input is qid/question only; judge alone receives the
selected gold projection. No HOLDOUT projection or scoring occurs.

## Observed result: inconclusive

All intended work ran:43 construction,4 solver and7 judge requests (54 total). Observed
responses were HTTP200 with stop completion and reported usage. No retries, sample changes
or post-outcome tuning occurred. Missing archive/billing information did not block work.

| Construction observation | Result |
| --- | ---: |
| Intended originals / accepted and materialized originals | 6 / 5 |
| Accepted annotation rows / distinct(document,member) incidences | 173 / 172 |
| Materialized distinct source incidences | 171 / 172 accepted |
| Nodes / edges / node-source rows | 173 / 424 / 171 |
| Construction wall time | 497.856 seconds |
| Raw index / graph DB | 253952 / 765952 bytes |

One document failed factual assessment locally; the other five continued. One further
distinct accepted incidence was not materialized. The complete intended document denominator
remains6. The raw index retains all6 originals. The earlier checkpoint's173 incidence wording
counted annotation rows; distinct incidence accounting is172, without altering raw evidence.

| Fixed DEV qid prefix | Raw solver | Graph solver |
| --- | --- | --- |
| 02f2a372b510 | Substantive answer,2 citations | Explicit abstention |
| 040bf93af9d8 | Substantive answer,3 citations | Substantive answer,1 citation |

All four solver outputs passed their schema/original-binding/citation checks. Six emitted
citations resolved; no exact quotes were declared, so a quote-check rate is undefined.
This mechanical validity does not establish correct or supported answers.

Of seven blinded semantic judgments, three passed schema/provenance validators and four
failed: one top-level schema mismatch, one missing authoritative evidence reference and two
missing original-bound cited substantive support. The two validated graph evidence judgments
also flagged gold/rubric ambiguity. The remaining usable raw answer judgment satisfied its
answer target, but its paired evidence judgment failed schema validation.

| Blind case | Qid | Arm / target | Outcome |
| --- | --- | --- | --- |
| case-00001 | 040bf93af9d8 | raw / answer | validated; answer strict-pass |
| case-00002 | 040bf93af9d8 | raw / evidence | judge verdict top-level schema mismatch |
| case-00003 | 040bf93af9d8 | graph / answer | answer claim lacks original-bound cited substantive support |
| case-00004 | 02f2a372b510 | raw / evidence | supported verdict lacks authoritative evidence references |
| case-00005 | 02f2a372b510 | graph / evidence | validated, rubric/gold ambiguity |
| case-00006 | 02f2a372b510 | raw / answer | answer claim lacks original-bound cited substantive support |
| case-00007 | 040bf93af9d8 | graph / evidence | validated, rubric/gold ambiguity |

Consequently all four paired slots remain unresolved under the existing conservative policy:
provisional grounded-correct0/2 per arm, with no formal statistic. These are **not measured
zero accuracies** or proof of equality. The run establishes no reliable paired utility estimate
or demonstrated graph benefit. Judge failure is part of the retained result; no case was
rejudged, rescued, dropped or used to tune the method.

The solver phase took8.424s and judge phase23.478s including harness/retrieval overhead;
these are not product SLOs. Reported totals are244774 prompt and318076 completion tokens
(including build thinking). Fees remain unknown rather than inferred. Build cost is separate
from online utility; no amortized efficiency claim is made.

## What the existing results locate

| Qid prefix | Construction support loss | Raw visible support documents | Graph visible support documents | Solver behavior |
| --- | --- | ---: | ---: | --- |
| 02f2a372b510 | None: all3 support originals materialized | 1 / 3 | 1 / 3 | Raw names the expected target string; graph abstains |
| 040bf93af9d8 | Failed original3d08… plus omitted member from original129e… | 3 / 3 | 1 / 3 | Both name the expected target string; only raw answer judgment validates |

The failed original and omitted accepted member belong to the second question, so they
cannot explain the first question's graph abstention. For the first question, both contexts
show one of three support documents despite all three being constructed; graph used9053
characters and raw10000. For the second, both contexts used10000 characters but graph shows
one support document versus raw's three. These are observed source-visibility differences,
not proof that a missing gold fact caused an answer or that a target-string match is correct.

The useful next question from this completed run is where representation/ranking/packing
loses usable evidence, alongside the separately observed source omission and judge schema/
cited-support binding failures. No such investigation, tuning or new run was started here.
Single valid answer observations stay valid; evidence-judge failures make their own paired
comparison unresolved, without cancelling unrelated calls or erasing the raw strict-pass.

## Source and validation

The actual build binary came from local `0ef43b32401e6a5511b6ddf1e06eed68840c6954`, whose
exact tree was published as `ad7ab5b9b2cc550bdb3543404236d27ec26d3173` in
[PR106](https://github.com/xcosmosbox/Cairn/pull/106). A later three-line test-fixture correction
`baba7a1` changed no experiment implementation. The declared graph SHA256 is
`dc5b264fb199cd65a486f6b7c65f6237c25c548e716ed149fa47f0b2dae4f00e`.
[Sanitized structured observations](diagnostic105-results.json) contain selected identities
and counters; exact raw bytes/QA projections/model bodies remain private.

Affected author checks passed for production build, runtime, incremental, service and paired
Python adapters. Actual combined/no-overlay pipeline tests passed0.398s; focused annotation/
extraction checks passed0.020s/0.012s and incremental conflict/partial-result checks passed.
Ingest and production retrieval binaries built successfully. Normal CI initially found two
old-response-shape test fixtures; the test-only correction's two combined regressions passed
0.012s. No local fullrace/verify, full609, recovery or archival rehearsal gated this diagnostic.
The build, solver, blind projection, judge and summary commands all exited0; individual
construction/judge failures above remain failures despite process exit0.

Normal CI on published `c223` passed Go/race/build, Viewer and MCP checks; its remaining
failure was an obsolete sourcecap historical current-file hash admission. That inactive
execution gate is now retired without changing historical results/hashes; six focused
sourcecap tests passed in the quality executor. No experiment was rerun.

The selected raw evidence packet's32 parts were saved to restricted `xcosmosbox/kd_manifeat`,
branch `evidence/diagnostic105-20261026`, commit
`1552707b04679ea0617732c104e377f311fde37e`,
`eval/diagnostic105-20261026/README.json`. This records successful storage, not a new
reconstruction audit or experiment admission requirement.
