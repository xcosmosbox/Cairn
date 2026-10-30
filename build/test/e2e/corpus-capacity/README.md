# Source-only construction diagnostic

For end-to-end integration, first run the reusable
[small production baseline](../mini-baseline/README.md). Its explicit `--source-tree`
option can follow a mini PASS with a separately selected full run; full semantic
acceptance remains independent. There is no default historical/private corpus.

The current driver uses the ordinary production orchestrator with a declared
source-only JSON array. Every supplied row remains in the preparation outcome
roster. Invalid source rows are recorded locally; valid independent rows continue.
There is no fixed12/609 count, minimum coverage, official tokenizer installation,
runtime overlay, paid-admission flag, cumulative wall deadline or archive prerequisite.
Questions, answers and HOLDOUT data are never builder inputs.

From the repository root:

```sh
go build -o /tmp/cairn-capacity ./build/test/e2e/corpus-capacity
/tmp/cairn-capacity --mode run --corpus /data/declared-originals.json --out /data/new-attempt
```

The JSON rows use `title`, `author`, `source`, `published_at`, `category`, `url`
and `body` fields. The supplied set is the complete intended source denominator.
`prepare` remains available as an optional formatting step; subsequent `run`
uses its existing output directory without `--corpus`. New runs refuse to overwrite
old receipts. The credential comes from `CAIRN_LLM_API_KEY` and is never a CLI argument.
The diagnostic client makes one HTTP attempt (`MaxRetries: -1`); ordinary client
defaults retain transient retry behavior. Per-request output/context limits,
provider timeouts and explicit caller cancellation
remain applicable. Concurrency controls scheduling rather than validity.

`original/` preserves source bytes and `source/` is the builder's mutable worktree.
`selection.json` binds URLs, source paths and original-body hashes once.
`preparation-outcomes.json` includes invalid rows; `build-report.json` includes skipped
construction documents. Neither missing material nor a low coverage percentage
invalidates the independent accepted subset.

`requests.jsonl` records attempt IDs, document IDs, request hashes, actual
request and response bodies (base64-encoded byte fields), HTTP status
and explicit errors. The current request hash identifies the parent input of each
response. Credentials and credential-bearing errors are redacted; hashes still
identify observed bytes. Missing usage is null with a warning, never an invented
zero or a stop condition. These files and the builder log are private (0600).
Sink errors warn and observed rows drain from memory to `observed-outcomes.json`
when possible; unavailable evidence is not reported as a successful receipt.
A failed trace never triggers model or mutation replay.

Historical seed/receipt/indexed continuation executables, sealed staged mapping,
full-history checkpoint replay, archival import and their obsolete admission tests
were retired by [#105](https://github.com/xcosmosbox/Cairn/issues/105). Their historical
JSON results and source history remain available; this command does not reopen them.
Construction alone makes no answer-utility or full-corpus retrieval claim.
