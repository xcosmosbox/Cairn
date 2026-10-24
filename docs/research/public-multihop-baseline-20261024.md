# Public multihop baseline protocol — 2026-10-24

Status: maintainer-adopted **free baseline** protocol; implementation in [#21](https://github.com/xcosmosbox/Cairn/issues/21).
Research: read-only baseline_research session (`gpt-6-astra/xhigh`), recorded by
`cairn-coordination-20261024-alpha-records`. [Decision](https://github.com/xcosmosbox/Cairn/issues/8#issuecomment-6382563312).
This freezes the free baseline only; future paid/graph comparisons require separate decisions.
It is a new protocol, not a continuation of a historical experiment freeze.
No GraphRAG benefit or stable-release readiness is established by this document.

## Reuse decision and boundaries

| Layer | Chosen approach | What its evidence can establish |
| --- | --- | --- |
| Engineering contracts | Small deterministic public fixtures/mocks; no LLM | Recovery, incremental/Bundle/MCP behavior |
| Retrieval quality | MultiHop-RAG complete fixed corpus; MuSiQue-Full v1.0 external protocol | Same-corpus new-question retrieval; external checks after execution |
| Agent usefulness | Same Cairn task through MCP with fixed solver | Actual task/answer effects only after a controlled run |

Use mature data and thin adapters rather than a new broad framework. [MCPMark](https://github.com/eval-sys/mcpmark/tree/cd45b7f57923b9b3985467f5139927575f83141c)'s isolated
environment/state/trace approach may inform layer 3; its leaderboard is not GraphRAG
evidence. [HotpotQA](https://hotpotqa.github.io/) dev distractor is a useful later diagnostic, but its ten supplied
paragraphs encode candidate priors; unioning them is a pooled-corpus adaptation, not
fullwiki. [LongBench v1](https://github.com/THUDM/LongBench/tree/2e00731f8d0bff23dc4325161044d0ed8af94c1e/LongBench) DuReader is a later Chinese diagnostic, without sentence-level support
labels for direct fact recall; upstream data licensing needs separate checks.

Historical motivation from PRs [#3](https://github.com/xcosmosbox/Cairn/pull/3),
[#4](https://github.com/xcosmosbox/Cairn/pull/4), [#6](https://github.com/xcosmosbox/Cairn/pull/6):
original text 32/50 versus rank-fixed graph 1/50; later Han FTS 3/50 versus graph 2/50.
Used holdout, AI adjudication and ledger reconstruction limit independence. These results
do not establish graph advantage. Paid Source Backfill stays disabled pending #12/#13.

## Source identities and access

Primary sources: [MultiHop-RAG paper](https://arxiv.org/html/2401.15391v1),
[official implementation at c1c1287](https://github.com/yixuantt/MultiHop-RAG/tree/c1c1287aa60a94acf9c4d20c891c9cd611a0f6e8),
[fixed dataset revision](https://huggingface.co/datasets/yixuantt/MultiHopRAG/tree/71ac0d0bd1f951d2d6b70311f7d2ae404e1ffa82).
Researcher downloaded and checked the following bytes; the recorder did not independently
re-download them. Implementation must verify these identities before accepting inputs.

| File | Bytes | SHA256 |
| --- | ---: | --- |
| corpus.json | 6785567 | `20b61b5ab84de84a927420c5d265b7ec8d859ae49980699958a787ade9e4d28f` |
| MultiHopRAG.json | 5171312 | `03cfb4926461f868684903aadc8024447bdda5bb3f6804741424cce338515bff` |
| README.md | 1312 | `f671db44cf1802f359451e466d82c68c5059d58451b8976ce8329b74d8b89001` |

The fixed corpus has 609 unique article URLs; 2556 questions include 301 null cases.
Researcher found all 6084 labeled facts as exact body substrings, with zero unmapped facts.
These are source-audit observations, not retrieved-answer results. The HF README declares
[ODC-BY](https://opendatacommons.org/licenses/by/1-0/), whose database license does not
grant separate news-article copyright. Publish downloaders, hashes and aggregate metrics,
not the article bodies.

[MuSiQue official revision](https://github.com/StonyBrookNLP/musique/tree/922ac98f19a201998dbdae6d7f2887a5258dbdeb)
and [v1.0 download script](https://github.com/StonyBrookNLP/musique/blob/922ac98f19a201998dbdae6d7f2887a5258dbdeb/download_data.sh)
use Drive file ID `1tGdADlNjWFaHLeZZGShh2IRcpO6Lv24h`; repository license is CC BY 4.0.
[Paper](https://arxiv.org/html/2108.00573v3): answerable dev 2417, Full dev 4834,
2–4 hops with paired missing-evidence questions. ZIP bytes/hash are **unknown** because
not downloaded here. This round freezes only the external-validation protocol: retain each
question's original contexts and paired answerability; do not union contexts while keeping
unanswerable labels. Download, identity audit and execution are later dependent work.

## MultiHop split and retrieval freeze

1. Index the full 609-article body corpus once. Questions/gold/evidence never enter the
   retriever or graph construction. `doc_id=sha256(exact URL)`; pin body hashes separately.
2. Normalize a query with NFKC, casefold, whitespace collapse and strip; qid is SHA256
   of its UTF-8 bytes. Non-null group is `docs:` plus sorted unique evidence URLs joined
   by newline; null group is `null:` plus normalized query.
3. SHA256 of `cairn-mhr-v1\0` plus group: first 8 hex digits modulo 5 equal to zero
   means DEV, otherwise HOLDOUT. Keep groups together; use no alternate seed.
4. Within each type/grouped pool sort by qid, select DEV32 and HOLDOUT64 per type:
   128 DEV, 256 HOLDOUT. Smoke uses the first two DEV per type (8 total). Fail if quotas
   are insufficient. Split manifest contains only qid/type/group_hash.
5. Research audit DEV available counts are 141/160/115/69, HOLDOUT675/696/468/232 for
   inference/comparison/temporal/null. Regex `\w+` normalized tokens, five-token
   shingle Jaccard >=0.85 found zero cross-split near-duplicate pairs in both the full
   data and selected384. This does not eliminate semantic leakage or training contamination.
6. Chunk bodies at256 whitespace tokens, stride224, preserving original body codepoint
   offsets. One original-query retrieval per question, top10; all displayed content shares
   10000 Unicode codepoints, with explicit tail truncation and visible offsets.
7. Free raw literal OR is a sanity control. `raw_source_fts_sqlite` uses SQLite unicode61
   FTS5/BM25, safely quoted original whitespace atoms joined by OR, no expansion or
   stopword removal, chunk_id tie-break. It is not Cairn's production node FTS.

This holdout tests new questions over one fixed knowledge base, not new-document
generalization. The runner defaults to DEV and refuses HOLDOUT until a separate frozen
evaluation decision. DEV allows at most two explicitly identified single-factor candidate
rounds, followed by a freeze or retaining FTS; do not switch datasets/budgets/gold after
seeing weak results.

## Metrics, real graph and stopping rules

Report pre-truncation Doc Recall@5/@10 and AllGoldDocs@5/@10. Budget-visible FactRecall
and AllGoldFacts require each complete labeled body span to be covered by the union of
visible intervals from that same document; title/path text is not evidence, repeats count
once. Stratify by type and 2/3/4 evidence documents. Null cases do not enter positive-recall
denominators: report their retrieval counts/empty rate separately. Also record errors,
empty results, truncation, p50/p95 latency and index bytes. No solver means no answer
accuracy, correct abstention or grounded-pass claim.

A real graph requires production construction over the whole corpus without QA access.
Paper-average sizing609×2046 is about1.25M source tokens before prompts/extraction/fusion/
retries; this is an estimate, not a cost or usage receipt. Measure a small build and actual
tokens, then obtain the applicable coordinator decision before a full paid construction.
The no-key path can complete fixtures and raw/FTS. Node FTS/graph/backfill must say
`not_run_no_real_graph`; synthetic nodes or hand-authored gold edges cannot prove benefit.

Future graph comparisons need the same source corpus and equal solver/search/context/output
budgets, plus independently reviewed runner durability. No four-arm paid run or production
source hydration is enabled here. Original-text FTS in #21 is an evaluation adapter, not
an existing production product feature. Graph-default or accuracy claims need controlled
benefit evidence; the maintainer has not adopted numerical benefit/non-inferiority
thresholds or opened the holdout. Statistical power, user scenarios and the actual baseline
must inform a later pre-registration. An experimental graph may coexist with a stable
tool, subject to a separate release decision; this protocol does not reposition Cairn.

Stop on input-hash, source-span, read-only-invariant or ledger errors. Fail quota or
cross-split near-duplicate checks instead of silently dropping cases. Diagnose in order:
gold/body mapping, graph fact retention, lexical candidates, ranking, serialization
truncation, source binding/backfill, solver reasoning/citation, judge/ledger.
