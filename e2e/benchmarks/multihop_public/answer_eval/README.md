# Paired original-source / Cairn answer evaluation

Current method: [#105](https://github.com/xcosmosbox/Cairn/issues/105). Use the existing adapter and judge for a fixed DEV diagnostic. Fix the question IDs before outcomes. The complete support-document union is a **support-enriched diagnostic corpus**; it supports exploratory given-source utility observations, not full-corpus retrieval, generalization or release claims. HOLDOUT remains unopened.

Both arms use exactly the same original-only corpus, literal question/quoted-OR query, topK10, 10,000 visible Unicode codepoints, deepseek-flash with disabled thinking, one answer request and 4096 output ceiling. The graph adapter executes the actual production search pipeline, bypassing MCP/ResponseBuilder packing. The builder receives originals only; solver input permits question_id/question only; labels and original support targets enter only the judge.

## Existing workflow

1. Supply a roster with `selected.dev: [{"qid": "existing DEV ID"}, ...]`. `project` verifies membership against the unchanged checked-in split. Pass the manifest-verified `MultiHopRAG.json` and the declared original-only `corpus.json` to `prepare_judge.py project --qa QA --corpus CORPUS --roster ROSTER --stage dev --out PROJECTIONS`. Only selected questions are projected. No stage-admitted, paid or whole-roster-equality flags exist.
2. Build the source index with `../benchmark.py build-index --corpus CORPUS --output RAW_DB --receipt RAW_RECEIPT`. Build the graph once through ordinary production ingestion with `--no-evolve`.
3. Record construction audit `db_sha256`, `documents` (`doc_id`, original `path`, accepted `members`, `materialized_members`) and actual `source_bindings` (`node_uuid`, `member_id`, `file_path`). Include every intended document, including failures/empty results. If accepted-member inventory is unavailable, report it as unknown rather than claiming complete retention.
4. Execute `run.py` using its existing `--questions`, `--projection-receipt`, `--corpus`, `--roster`, `--construction-audit`, `--graph-db`, `--graph-binary`, `--raw-db`, `--raw-receipt`, `--out`, `--stage dev` and matching SHA arguments. Hashes bind setup inputs once; no per-question history replay or repeated whole-file hashing occurs. The output includes contexts, exact private request/response records, results and usage.
5. Use `prepare_judge.py blind` on those actual solver artifacts, then `prepare_judge.py judge` with blind input/private map, `--extra-body` containing `{"thinking":{"type":"disabled"}}`, output/usage paths and corresponding hashes. Use `summarize.py` for the fixed paired summary. Small diagnostics return descriptive counts and null bootstrap weights; the historical full-sample statistic is optional.

`--help` lists exact required arguments. Solver and judge use `CAIRN_LLM_API_KEY` without storing headers or credentials. Every request has its ordinary per-request timeout and output/context limits. There is no cumulative call/token/spending limit, archive prerequisite, all609 requirement, minimum retention fraction or repeated technical GO.

## Local failures and records

Unknown usage is nullable with observed subtotals; it does not reject valid text. Invalid JSON, oversized requests, missing/wrong source ownership and invalid citations/quotes fail the affected item/arm. Credential errors affect only the endpoint/credential cohort. Later independent jobs continue and every intended question×arm stays in the denominator. Missing/ambiguous judgment is unresolved, with provisional zero where the metric needs it.

Raw requests, provider responses and outcomes are retained privately with exact source/item/arm association. Credential-bearing errors are redacted. JSONL I/O failure records an unavailable sink and keeps an in-memory copy; there is no inode/size/fsync admission latch. Original intervals and declared quotations are verified exactly; generated graph wording never becomes an original quotation. Historical controls/results remain preserved, while obsolete executable pilot copies are retired.
