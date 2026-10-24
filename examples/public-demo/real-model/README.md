# Real two-document preparation

This opt-in helper calls the existing `cairn-ingest`, CLI and MCP binaries. It
does not implement a second builder or change the fixed fake. Its frozen inputs
are the existing [Skill](../SKILL.md), [queue](../references/queue.md) and
[timeout](../references/timeout.md) files. Only the two reference documents are
annotated; the Skill file supplies discovery metadata. [input-freeze.json](input-freeze.json)
pins all three original byte identities at `18e7e7b`.

Current status: **free preparation only; no successful real-model request,
extraction, publication or quality measurement**. The previous automatic approval
review rejected credential egress. Do not run live commands until the applicable
current authorization clears that review. A CLI opt-in is an execution safeguard,
not evidence of authorization. See [verification.md](verification.md).

## Free preparation

With the documented Go toolchain and cached dependencies available, run from the
repository root:

```bash
GOFLAGS=-buildvcs=false make build
python3 -m unittest discover -s examples/public-demo/real-model -p 'test_*.py' -v
python3 examples/public-demo/real-model/run.py prepare --out .local/real-model/attempt-01
```

`prepare` reads no credential and makes no network request. It creates a fresh
directory, copies only the frozen three files into `source/`, keeps an untouched
`original/` copy, verifies SHA256/bytes, and records the exact builder commit and
helper hash. Existing run directories are refused. The explicit VCS option avoids
the known worktree VCS stamping failure; it does not modify product source.

## Reviewable live commands — not executed in this preparation

After credential use has been authorized, supply the private runtime file path
only as a process argument; the helper loads the value without printing it:

```bash
python3 examples/public-demo/real-model/run.py canary --out .local/real-model/attempt-01 --key-file /path/outside/git/deepseek-key --authorize-deepseek
python3 examples/public-demo/real-model/run.py smoke --out .local/real-model/attempt-01 --key-file /path/outside/git/deepseek-key --authorize-deepseek
python3 examples/public-demo/real-model/run.py audit --out .local/real-model/attempt-01
```

The single canary uses only the official
`https://api.deepseek.com/chat/completions` endpoint, exact `deepseek-flash`, JSON
mode, thinking disabled, 64 output tokens and a 45-second timeout. It retains
provider response model/ID/usage, refuses redirects and does not retry or fall
back. The [official model documentation](https://api-docs.deepseek.com/api/responses)
states that `deepseek-flash` is served by DeepSeek-V4.1-Flash-1017 as inspected on
2026-10-24. An unexpected returned model stops admission for reconciliation.

The real builder uses the same endpoint/model, product JSON mode and enabled
thinking with the provider's default high effort. Its existing client has no
per-run thinking/JSON switch. The reviewed command uses 16,384 output tokens per
call, 180 seconds per request, client/annotation retry setting 1 and rollback
setting 1, plus a 600-second outer process deadline. Extraction, coverage, fusion
and slug stages retain their own existing internal limits. These settings do not
promise an exact number of calls. Successful response logs supply actual observed
call/token totals; failed requests may consume additional provider usage. Failed
or timed-out attempts are preserved and never automatically rerun.

The product client does not retain each response's returned model field; its
model log is the configured ID. The separate canary receipt establishes only its
own observed provider response. Per-build returned-model tracing would need a
separately scoped client change; this helper does not silently add it.

## Acceptance and provenance

`audit` requires both nonempty reference documents in the actual
`04_extract_input.json`, both paths in the built `node_sources` ledger, member
links back to original annotation IDs, valid rewritten line bounds, sidecars,
SQLite integrity, real CLI status, real MCP searches/resolves for a node from
each reference, unknown-UUID handling, and unchanged DB bytes after queries.
It records the returned nodes/edges and expected-name presence for manual semantic
review. Structural success does not certify the intended `重试预算 → 阻塞队列`
dependency or retention of the capacity 8 / wait 50ms / retry 2 facts. The reviewer
must inspect those outputs and retain missing/wrong relations or facts as failures.

Original inputs have independent frozen SHA256 receipts. Annotation `source_span`
is a model-generated, best-effort location on those originals, not a verified fact
citation. The 04 dump retains that original annotation and member identity.
After structured writeback, `node_sources` and MCP locations address rewritten
Markdown; sidecar `doc_hash` hashes the rewritten document. They must never be
reported as original-body spans or proof that a fact was preserved. The helper
retains both trees and the member-to-UUID ledger to permit a separate audit.

This feature smoke is separate from the public corpus-only 12-document benchmark
admission, the complete 609-document build, and all DEV/HOLDOUT answer comparisons.
It reads no benchmark questions, gold or HOLDOUT, and publishes nothing. Sandbox
Bundle upload/catalog publication requires a separate bounded scope and review;
the accepted App-auth evidence alone does not prove Bundle publication. #16 stays
open/partial until the original unmet runtime criteria are actually run and accepted.
