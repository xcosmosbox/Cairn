# Real DeepSeek build recovery

This fixture-specific command continues the nicedata end-to-end test from verified,
previously accepted per-document annotations. It executes the production
`pipeline.Orchestrator`: global extraction, coverage, relationship repair,
descriptions, relations, slugs, SQLite ingest and Markdown/sidecar writeback.
It does not replay extraction or later model responses.

The original attachment and request bodies are kept outside this repository.
`accepted_checkpoint.go` checks the fixture commit, current input bytes, original
209 evidence files, current format validation, and compatibility of the annotation
and ID implementation. The saved semantic-gate pass is reused as a historical
verdict; it is not represented as a new model judgment.

Build with `go build -o real-build ./build/test/e2e/real-build`. First run without
`--allow-paid` to verify the checkpoint without network requests:

```sh
./real-build \
  --repo /path/to/clean/input-worktree \
  --out /path/to/new/preflight \
  --accepted-dump /path/to/original/full/stages \
  --historical-code-root /path/to/original/Cairn
```

For a real run, provide `CAIRN_LLM_API_KEY` in the process environment, choose a
new empty output directory and add `--allow-paid`. Defaults are `deepseek-flash`,
393216 maximum output tokens, 8 concurrent requests, a 30-minute request/stage
limit and a 90-minute overall limit. The maximum is a ceiling, not a requested
output length. File-slug generation retains its production 120-second stage
limit and deterministic fallback.

Before the first new API call, the command writes the effective configuration,
actual compiled builder identity, frozen publisher fingerprint and checkpoint
proof. Its HTTP observer records request/response hashes, status, finish reason
and token usage, including usage from rejected `length` responses. It never
writes API keys, prompts, reasoning or response content to that ledger. Requests
without usage remain explicitly unknown; historical annotation costs are separate.

Success requires all 69 documents to be accepted, no skipped documents, the
production completeness gates, a readable graph, and production source-closure
validation. Publish only the new successful output through `actions-publish`
with its matching `frozen-builder-fingerprint.json`.
