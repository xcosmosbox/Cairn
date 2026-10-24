# Real two-document preparation

This opt-in helper calls the existing `cairn-ingest`, CLI and MCP binaries. It
does not implement a second builder or change the fixed fake. Its frozen inputs
are the existing [Skill](../SKILL.md), [queue](../references/queue.md) and
[timeout](../references/timeout.md) files. Only the two reference documents are
annotated; the Skill file supplies discovery metadata. [input-freeze.json](input-freeze.json)
pins all three original byte identities at `18e7e7b`.

Current evidence: the renewed authorized official-endpoint canary succeeded. The
first real attempt skipped one document after a genuine semantic-gate failure;
the separately authorized second attempt consumed both documents. Its generated
descriptions retain capacity 8, wait 50ms and retry 2, but its required direct
`重试预算 → 阻塞队列` dependency was removed by the existing subdomain-local relation
contract. **The complete smoke remains a semantic failure.** See
[verification.md](verification.md) and the retained
[run report](../../../docs/coordination/reports/issue16-realmodel-20261024-live16/README.md).
No publication or benchmark/holdout evaluation ran. The earlier credential-egress
rejection remains historical evidence; no further approval was requested after
the user's renewed authorization.

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

## Opt-in live commands

With applicable credential-use authorization, supply the private runtime file path
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

`audit` requires both nonempty reference documents in actual
`04_extract_input.json`, exactly the final candidate's document/member/UUID source
ownership in `node_sources`, matching generated sidecar hashes, SQLite integrity,
real CLI status, real MCP searches/resolves for a node from each reference,
unknown-UUID handling, and unchanged DB bytes after queries. It writes a new
`audit-v2.json` and new `audit-v2-evidence/` CLI/MCP receipts, preserving historical
`audit.json` and raw CLI/MCP files unchanged. It separately checks
the required direct dependency and numeric facts. A missing edge remains a failing
exit even when document consumption and MCP metadata pass. Reviewers must still
inspect final graph faithfulness; the fixed checks do not certify every relation.

Original inputs have independent frozen SHA256 receipts. The type permits an
optional original annotation `source_span` hint; the current annotation wire/parser
does not populate it. `node_sources`, MCP line metadata and sidecar spans copy
those optional pre-writeback hints, without recomputing generated locations.
NULL therefore means exact position unknown, and a supplied hint is still
unverified. Document/member lineage remains required. Sidecar `doc_hash` hashes
the rewritten document, which does not validate any original hint.

The corrected audit independently measures generated block ranges using matching
`kg:uuid` markers, recording generated document hashes. These local diagnostics
are neither stored MCP citations nor original-body positions. Shared mirrors can
contain less text than their primary. Neither original hints, block ranges nor
document membership prove that a source fact was preserved or visible to a solver.
Both trees and the member-to-UUID ledger remain available for separate review.

This feature smoke is separate from the public corpus-only 12-document benchmark
admission, the complete 609-document build, and all DEV/HOLDOUT answer comparisons.
It reads no benchmark questions, gold or HOLDOUT, and publishes nothing. Sandbox
Bundle upload/catalog publication requires a separate bounded scope and review;
the accepted App-auth evidence alone does not prove Bundle publication. #16 stays
open/partial until the original unmet runtime criteria are actually run and accepted.
