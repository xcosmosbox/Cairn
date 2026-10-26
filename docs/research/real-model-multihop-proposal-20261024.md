# Real-model multihop experiment method

Current execution direction: [#105](https://github.com/xcosmosbox/Cairn/issues/105#issuecomment-6406294894),
2026-10-26, replacing this proposal's old smoke→12→609→DEV admission ladder.
The original 2026-10-24 proposal and later stage receipts remain in git/task history.
A small real diagnostic follows minimal affected implementation checks directly.

## Fixed diagnostic

Use the first two existing selected DEV qids, fixed before opening their QA/outcomes,
and their complete original support-document union. See
[selection metadata](../coordination/reports/diagnostic105-selection.json).
This support-enriched sample measures exploratory given-source utility; it establishes
neither full-corpus retrieval superiority nor new-question/document generalization.
HOLDOUT remains unopened and no post-outcome reselection/tuning is allowed.

Build once through ordinary cairn-ingest in a fresh isolated DB with --no-evolve.
Builder receives original source wrappers only; no question/gold. Separate solver projection
contains qid/question; only source selector and judge projection see selected DEV gold.
The original complete609 corpus/revision remains pinned for attribution and selected-byte
verification. A diagnostic subset is declared, not presented as a full-corpus build.

| Resource | Both arms |
| --- | --- |
| Sources | Exactly the selected complete original documents |
| Retrieval | One safely quoted whitespace OR question, top10 |
| Visible context | 10000 Unicode codepoints including metadata/IDs/relations/provenance |
| Solver | deepseek-flash, disabled thinking, one JSON answer, output4096 |
| Follow-up | No query rewrite, extra search, oracle backfill or winning-route selection |

Raw uses existing original-source FTS256-token chunks/224 stride. Graph uses actual
production SearchPipeline through cairn-eval-retrieve, bypassing ResponseBuilder/token
packing/MCP. Record that adapted surface; do not claim full hosted/MCP answer quality.

## Evidence and outcomes

Preserve exact private raw source/request/response/error bytes; publish sanitized methods,
counts and outcomes. Model usage/fees can remain unknown and upload can fail without
stopping independent calls. Source/member/UUID ownership and exact original quote offsets
remain enforced. Generated summaries never become original spans; structural hierarchy
blocks without originals cannot support citations. Actual contamination fails only its
question/arm/derived dependency, while independent cases continue.

Keep all planned question×arm slots, including build/retrieval/solver/judge failure.
Report supported-answer outcomes, abstention, invalid citations/quotes, source retention,
request counts, known/unknown usage and time. Same-model semantic judging is fallible and
is not independent human ground truth. Tiny diagnostics report observations without formal
full256 bootstrap/demo floors or release acceptance. Negative/inconclusive is a completed
useful result, not automatic authorization to tune or rerun.

Existing DeepSeek/network/data-egress permissions are standing; no separate technical GO,
monetary/time ceiling, counter receipt, archive admission, full609/recovery or Viewer replay
precedes this sample. Per-request transport limits and credential/redirect isolation remain.
Larger full-corpus/HOLDOUT or stable-release conclusions require appropriately declared
methods and actual evidence; they are separate from this diagnostic.
