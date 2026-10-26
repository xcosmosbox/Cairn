# Source-grounded retrieval evaluation

The native batch solver and durable ledger are restored from evaluation
checkpoint `5c84d7c8482806a9355e4e7ecc918bccb82ac88d`. Their prompts, tools,
budgets and grading rules stay unchanged. Historical questions, source excerpts,
answers and audit records are in the [complete approved archive](https://github.com/xcosmosbox/kd_manifeat/tree/ab0d7d4bfe55a88532fce6ac1cdd7c1011db9036/evaluations/cairn-nicedata-20261004).
That repository currently requires repository access. The archive preserves
historical publication-status text; its outer README records the newer consent
and verified upload. It is not a complete frozen-workspace backup.

The existing 200 paid runs are historical evidence, not jobs to rerun. Restore
question files from the archive and provide explicit input paths. Never rebuild
an old baseline with the current adapter and label it the historical baseline.

## Controlled second round

[protocol.json](round2/protocol.json) and its pre-results
[amendment](round2/protocol-amendment-001.json) freeze comparisons and spending.
The offline legacy control uses the existing rankfix binary, isolating the query
experiments from the previous BM25 correction. Native solver and judge source
hashes are in [freeze-receipt.json](round2/freeze-receipt.json).

Four offline factors compare legacy MATCH, safe literal AND on the original
index, safe literal AND on a trigram copy, and Han-character phrase AND on a
transformed unicode61 copy. Both retrieval arms use the same FTS seed query,
top10, evidence formatter and graph/ranking settings. No OR, LIKE, stopwords,
new extraction or query-specific exceptions are added.

`cairn-eval-index-copy` changes only FTS tables in a new DB file. It rejects an
existing destination, a wrong frozen source hash, and a nonempty WAL/journal.
Its manifest binds source/output bytes, every non-FTS table and schema, index
and query profiles, and toolchain versions. Production schema and bundle format
are not migrated. The Han copy is static and read-only: normal production index
rebuilding writes raw fields.

Build and inspect the tools from the repository root:

```bash
go build -o /tmp/cairn-eval-retrieve ./service/cmd/cairn-eval-retrieve
go build -o /tmp/cairn-eval-index-copy ./service/cmd/cairn-eval-index-copy
/tmp/cairn-eval-index-copy --help
/tmp/cairn-eval-retrieve --help
python3 e2e/quality/retrieval_round2.py --help
```

Create a copy with `--source`, `--output`, `--profile` and `--manifest`.
The adapter's `--text-profile han-v1` requires the matching `--index-manifest`.
The adapter verifies the bound DB before and after retrieval, including active
WAL/journal rejection. Production keyword queries default to safe literal AND;
`--query-syntax fts5` explicitly requests an unmodified advanced MATCH expression.

The offline roster uses only previously executed DEV queries. Budget-denied
calls remain separate audit events. Evidence review sees exactly the text
visible within 10,000 characters per search and 20,000 per original trace.
Source-path/line overlap is provenance localization, not semantic correctness.
Fixed-query replay is not a new adaptive Agent run.

Trigram changes Latin matching and cannot match queries shorter than three
Unicode characters. Han normalization preserves original query atoms as ordered
phrases and supports shorter Han fragments, but can join characters across
punctuation or serialized synonym boundaries. Both change BM25 normalization,
including for ASCII queries. Tests and reports preserve these limitations.

## Paid solver and grading

Current execution follows [#105](https://github.com/xcosmosbox/Cairn/issues/105) and the [paired public evaluator](../benchmarks/multihop_public/answer_eval/README.md). `agent_eval_native.py`, the JSON-action runner and `grade.py judge` use ordinary request IDs without cumulative call/token/cost quotas or repeated paid-admission flags. Raw request/response/outcome JSONL records have an in-memory fallback; missing usage is unknown, and a failed sink never cancels independent model work. Per-request timeout/context/output bounds and endpoint/credential redirect isolation remain.

Schema, source/citation and provider failures are local outcomes. The paired evaluator retains all intended question×arm slots. Judge results remain AI judgments, never human review. Interrupted attempts are not silently resent. Historical pilot JSON/results stay unchanged; executable pilot archive copies and the plan-only Source Backfill admission shell were removed. Old controls and archive audits are optional historical work, not dependencies of a new diagnostic.
