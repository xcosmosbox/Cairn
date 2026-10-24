# Frozen attempt02 cross-candidate replay

The single admitted real replay passed its two-candidate gate: production accepted the original directed `retry-budget → blocking-queue depends_on` candidate and explicitly rejected the original `free-fixed-fake → retry-budget contradicts` candidate on semantic grounds. The latter is a difference in fixture coverage and conditions, not two incompatible assertions about the same subject. There was no reannotation, relation discovery, taxonomy change, graph materialization or whole-run retry.

This supplements the accepted attempt03 positive full-build packet. It exercises the negative candidate that attempt03 did not propose. It is a targeted correctness observation on two retained synthetic candidates, not an estimate of general graph quality, retrieval benefit, calibration performance or causal improvement. UUID masking remains unexercised because these frozen original sources contain no UUID strings. No 12-document/full-corpus/QA/HOLDOUT run is included.

## Authorization and identities

Task #57, related #16; session `cairn-cross-negative-20261024-live16`, branch `experiment/cross-verifier-replay-20261024`. Root authorized exactly one direct production verifier replay after independent offline guard review. Scope ACK [6385782572](https://github.com/xcosmosbox/Cairn/issues/57#issuecomment-6385782572), HTTP guard amendment [6385879593](https://github.com/xcosmosbox/Cairn/issues/57#issuecomment-6385879593), offline checkpoint [6385961656](https://github.com/xcosmosbox/Cairn/issues/57#issuecomment-6385961656), pre-dispatch [6385975057](https://github.com/xcosmosbox/Cairn/issues/57#issuecomment-6385975057). An independent shared-account/model session reviewed the actual driver, hashes, restored scopes, input rows, transport/durability limits and offline regressions before dispatch; outcome review is separate.

| Identity | Frozen value |
| --- | --- |
| Production source | `75a77a0d5d1e1d5077c4758e56f2bc7fd3ac62a8` |
| Driver source commit | `b3c94909da60491282b933e36cf6b67eb1fd4fa1` |
| Production verifier SHA256 | `0dad47ace8c9fd8192c99ac271f9ecacbbc40752c06b3735c81be1dfd1fe8914` |
| Actual executable SHA256 | `e5590aba62c9f4744057c5f5ea2a61231cf40f6af143a6cf5ccc788688bc4775` |
| Actual preflight freeze SHA256 | `1af86c8bfa20d06085c84891e3937f85c6800a77ac3b7bba45693ca0d26ec722` |
| Projected graph SHA256 | `bc11e8662b69ba6c54478d5e34747919ee2380aae8c16bd2ffa30c1d15a9c44e` |
| Non-relation inventory SHA256 | `c2c1c1615e9994afaab591f70da869f45674abd28ec9983f034cae9d4ed703c8` |

`attempt-01/freeze.json` retains every source hash, option and exact selected relation row. The driver records the literal `git rev-parse` newline in `driver_source_commit`; this is a raw receipt detail. Later report-only commits do not change the measured executable. Production build/query/schema/Bundle code is unchanged from the stated base. `verification/binary-buildinfo.txt` records actual Go build metadata.

Inputs are the existing immutable packet `../issue16-realmodel-20261024-live16/attempt-02/`: `dump/04_extract_input.json`, `dump/05_extract_result.json`, and `original/references/{queue,timeout}.md`. Their four SHA256 pins are in the freeze and checked before any credential lookup or client construction. The original two rows, including description/confidence, are selected without alteration; all six nodes, taxonomy, member IDs and other non-relation fields are unchanged. The historical dump omits `Domain.Slug` and `Subdomain.Slug` (`json:"-"`). The driver restores only production-derived `extract.Slugify` values, validates the original one-domain/three-subdomain ordering and resolved cross-scope endpoint identities, and asserts exactly two candidates. This restores derived identity; it does not regroup nodes.

## Single actual result

The process ran once on 2026-10-24 from `15:24:00.060488892Z` to `15:24:18.925740352Z`, production verifier elapsed 18.865 seconds (wrapper wall 18.893 seconds), exit 0. `attempt-01/command.json` contains the exact safe argv and process result. Both logical calls ran concurrently under the production verifier.

| Original candidate | Final disposition | Meaning |
| --- | --- | --- |
| retry-budget → blocking-queue / depends_on | `accepted`, verifier `supported` | Original timeout text directly states the dependency; queue text explains capacity/waiting. Source and direction predicates true. |
| free-fixed-fake → retry-budget / contradicts | `rejected_semantic`, verifier `rejected` | Fixture omission and synthetic concept definition can coexist under different subjects/conditions. All five contradiction predicates false. |

There were exactly **2 logical calls and 2 admitted transport attempts**, both HTTP 200; no retry, redirect, 503, admission failure or timeout was observed. Requested model was `deepseek-flash` at the official HTTPS chat-completions endpoint. Successful reported usage totals **2,445 input + 3,455 output = 5,900 tokens**. Provider response IDs and returned model identifiers are unavailable through this standard client and remain unknown; billing is not inferred. Failed-request usage is likewise unavailable, though this replay observed no failed transport/HTTP response. The separate later corpus driver has stricter metadata requirements.

`attempt-01/cross-report.json` preserves both actual verifier verdicts, reasons, predicates and original quotes. `attempt-01/filtered-projection.json` contains only the accepted dependency row and identical non-relation content. No SQLite/query claim is made by this direct verifier replay; the separate attempt03 packet covers final UUID translation, ingestion and real read-only CLI/MCP behavior.

`audit.json` checks all five quote substrings against pinned original UTF-8 bytes, member-to-document ownership, unchanged non-relation inventory, final single dependency row and the durable call/transport ledger. The two accepted quote offsets are production-native and independently checked: timeout bytes `[316,417)`, line 10; queue bytes `[97,223)`, line 5. The three rejected quote offsets in the audit are explicitly **independently derived**, since production reports no validated offset array for semantic rejection. These diagnostics do not populate or upgrade unknown original member `source_span` hints.

## Bounds and offline checks

The admitted limits were two logical calls, four transport sends, 16,384 output tokens per call, 180 seconds per HTTP client and 300 seconds overall, 65,536 original-evidence bytes per neighborhood. `MaxRetries:1` permits at most two production attempts per logical call; a separate driver-local RoundTripper guards every HTTP attempt including redirects. It requires exact official endpoint and POST, admits at most four sends under one mutex, persists compact JSONL plus `fsync` before dispatch, syncs the output directory after exclusive ledger creation, and cancels permanently on persistence, destination or budget rejection. It records no authorization headers, key values, foreign redirect locations or response bodies. No product client/default was modified.

The first reviewer identified that retry count alone did not cap redirect sends. The guard correction and offline actual-client retry/redirect tests were completed before the sole paid run. A second tiny correction synced the directory entry before dispatch. Earlier preflight directories were offline only and retained locally; their identities are recorded in `verification/commands.json`. They were never model attempts.

Actual offline `go test -race -parallel 8 -count=1 ./...` and `make verify` both exited 0. The focused tests cover original-row/input/taxonomy mutation, derived scopes and nonzero actual production candidate processing, incorrect outcome gates, logical limits, actual client 429 retry accounting, foreign redirect denial before transport, four-send admission and ledger-persistence fail-closed behavior. Logs and exact command records are in `verification/`. The raw `go version -m` output retains its original trailing tab on the module line; `git diff --check` reports that single evidence-file whitespace warning, which is preserved rather than rewritten.

The provider log is copied byte-for-byte as `attempt-01/provider.log` after exact-key and credential-pattern scans; it contains ordinary production request/usage diagnostics. No credentials, raw headers, private source files or DB binaries are published. `manifest.json` lists every other packet file with size/SHA256. Prior attempt01/02/03 evidence is unchanged. Acceptance belongs to root after independent outcome review and CI; #16 remains open until that disposition.
