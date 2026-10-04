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

Only `agent_eval_native.py --allow-paid` and `grade.py judge` call the provider.
Keys come from runtime environment variables, never committed files or logs.
Imports, unit tests and offline replay make no provider requests.

The solver uses `deepseek-flash`, thinking disabled, max4096 output tokens,
at most three searches/four model rounds, and top10. Native search batches run
in provider order; the final answer must be one separate submit call. Mixed,
duplicate and invalid calls fail explicitly. Assistant prose is never repaired
into a tool action. Errors and JSON framing consume the same evidence budget.
All requests, responses, retries, failures and actual usage remain in the fsynced
ledger and result records.

```bash
python3 e2e/quality/agent_eval_native.py --help
python3 e2e/quality/grade.py --help
python3 -m unittest discover -s e2e/quality -p 'test_*.py'
python3 e2e/quality/export_receipts.py --self-test
```

The judge retains thinking enabled/low, max32768 output tokens and at most two
attempts per case. Every attempt consumes the frozen global budget. Automatic
errors are not passes; residual AI adjudication is separately labeled. The
benchmark and grading are AI-produced, not human gold. The ten-question holdout
has prior aggregate results and is not an untouched generalization set.
Protocol success, nonempty retrieval and source overlap alone do not establish
answer correctness or GraphRAG effectiveness.
