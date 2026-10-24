# Real two-document execution: consumption observed, required relation failed

Session `cairn-realmodel-20261024-live16`; source main
`c6a2b16f21434147947f4ed30d65a06321571546`; isolated branch
`experiment/real-smoke-20261024-live16`. Tasks [#16](https://github.com/xcosmosbox/Cairn/issues/16)
and bounded helper correction [#42](https://github.com/xcosmosbox/Cairn/issues/42).
[Execution ACK](https://github.com/xcosmosbox/Cairn/issues/16#issuecomment-6384913280),
[configuration treatment decision/checkpoint](https://github.com/xcosmosbox/Cairn/issues/16#issuecomment-6384955400),
[helper ACK](https://github.com/xcosmosbox/Cairn/issues/42#issuecomment-6385020068).

**The required smoke does not pass.** The official-endpoint canary succeeded.
Attempt02 actually consumed both frozen documents and retained capacity 8, wait
50 milliseconds and at most 2 retries in final descriptions. The required direct
`重试预算 → 阻塞队列` `depends_on` edge was removed before materialization. No
12-document calibration, benchmark answer run, HOLDOUT, publication or release
was started. No product source, original input or old evidence was changed.

## Actual runs and retained negative results

| Result | Canary | Attempt01 | Attempt02 |
| --- | --- | --- | --- |
| Purpose | Connectivity only | Reviewed one-round smoke | Separately authorized default-retry configuration treatment |
| Actual UTC interval | 13:40:51–13:40:59 | 13:41:11–13:42:18 | 13:43:55–13:46:16 |
| Endpoint/model | Official DeepSeek / requested and returned `deepseek-flash` | Same configured endpoint/model; returned model not retained | Same configured endpoint/model; returned model not retained |
| Settings | JSON, thinking disabled, output64 | JSON, thinking default enabled/high, output16384, HTTP180s, outer600s, retries1, rollback rounds1 | Same except retries3, rollback rounds3 |
| Exit / elapsed | 0 / 8.037s | Builder0 / 67.383s; hard audit failed | Builder0 / 141.211s; original hard audit failed; corrected audit still1 |
| Documents accepted / skipped | None | 1 / 1 | 2 / 0 |
| Nodes / edges / source rows | None | 7 / 8 / 3 | 11 / 14 / 7 |
| Observed usage | 45 prompt + 5 completion | 9438 prompt + 11603 completion,13 successful response logs | 11806 prompt + 26625 completion,18 successful response logs |

All times are 2026-10-24. Python receipts use UTC; original Go logs preserve their
local wall-clock rendering. Coordination uses GitHub event times. The canary's
HTTP200 response ID is `cb977f4b-20bd-4411-98b6-58ad42e913d7`, finish reason `stop`,
JSON `ok=true`; no request-id header was supplied. This establishes only the
observed alias response, not a separately identified provider revision.

Attempt01's timeout document genuinely distorted the dependency subject and
introduced unsupported downstream/caller detail. Gate B rejected it. The chosen
`max-rollbacks=1` means one total annotation round, so it was skipped. The builder
continued with only queue.md and exited0. The historical audit correctly failed
the two-document requirement. The negative result was accepted before root
explicitly authorized exactly one fresh attempt02 with production default retry
counts. It was a deliberate configuration treatment, not an automatic rerun or
repetition of an accepted paid control. Both documents passed their first Gate B
round in attempt02, so it does **not** prove that rollback retries rescued the
first failure. No prompt, gate or input was relaxed.

Attempt02 reused the original canary receipt with an explicit
[reuse identity](attempt-02/canary-reuse.json); that copied receipt is not a second
HTTP call. Per-build provider model/response IDs and error usage were not recorded
by the existing client. The token counts above cover successful logged responses
only; unobserved attempts/usage and final billing remain unknown. No money amount
is inferred from incomplete receipts. Raw prompts, reasoning and bearer headers
are not published.

## What the actual graph preserves and loses

Both originals reached [04](attempt-02/dump/04_extract_input.json). Seven
per-document annotations share six unique member IDs: the retry-budget member is
present in both documents. All seven `(UUID,member,path)` rows agree with the
final candidate. A shared retry-budget primary and two mirrors were written.
Final descriptions preserve all three numeric facts and textual dependency.
The retry-budget summary alone omits50ms/2; the fuller description contains them.
This is synthetic fact retention, not answer quality or solver-visible evidence.

The original [05 extraction](attempt-02/dump/05_extract_result.json) contained the
intended direct dependency and three other cross-subdomain edges. Existing
`repair.go` validates endpoints only within the owning subdomain; all four were
cleaned. Both repair rounds proposed patches outside that scope, all rejected;
[repair receipt](attempt-02/dump/05c_repair_report.json) records `Degraded=true`,
zero applied patches and four cleaned relations. No later stage restored the
direct queue dependency. The final graph has four semantic edges, all local.
An initially unsupported `fixed fake contradicts retry budget` edge was also
removed; permitting cross-subdomain binding alone would not make that claim true.
Endpoint existence and source membership are not relation entailment.

The earlier per-subdomain limitation is explicit in `repair.go`, `relate.go` and
`uuid.go`; this report does not silently widen it. Any new global binding and
semantic verification require their own architecture decision and review.

## Original hints, generated ranges and corrected free audit

The original attempt02 [audit.json](attempt-02/audit.json) failed because the
prepared helper demanded non-null **rewritten** source spans. That expectation
was wrong. Production's annotation wire/parser does not populate the optional
`SourceSpan` type. `buildMemberSources` copies optional original hints into the
DB before writeback; sidecars copy the same hints. No code recomputes those
positions against generated text. All seven DB spans are NULL, meaning exact
position unknown. A supplied hint would still be unverified original metadata.
Sidecar `doc_hash` covers rewritten Markdown, not an original citation.

The corrected helper preserves the original failed audit and writes separate
[audit-v2-final.json](attempt-02/audit-v2-final.json) and
[audit-v2-evidence/](attempt-02/audit-v2-evidence/). It requires complete final
candidate member ownership, both original paths, matching generated sidecar
hashes, matching UUID marker pairs, SQLite integrity, actual CLI/MCP known-node
queries for each document, unknown-UUID behavior and unchanged DB bytes. Its
measured generated block ranges are explicitly local diagnostics, never stored
MCP/original citations. Mirrors may display less text than the shared primary.
No generated quote/span is relabeled as an original location or fact proof.

The corrected free audit observes document/member consumption and actual MCP
metadata, but **still exits1**: `direct_budget_depends_on_queue_edge=false`.
The three numeric checks are true. It does not accept the complete smoke by
renaming the span requirement. The final output remains `fail_required_semantics`.
The [command receipt](attempt-02/audit-v2-command-receipt.json) hashes the exact
helper and original/copied DB and records zero new model requests. Original raw
CLI/MCP receipts remain separate; the corrected audit refuses existing v2 output.

## Identity, reproducibility and checks

The live runs reused binaries whose SHA256 exactly match the earlier accepted
PR38 environment receipt. Their compiled source baseline was `18e7e7b`; all
`build`, `core`, `service`, `go.mod` and `go.sum` bytes were unchanged through the
current main. [Binary/source proof](attempt-02/binary-provenance.json) records the
hashes and exact empty-diff command. This is explicit reuse, not a claim of a new
compilation for those calls. Go1.22.12 was subsequently restored from the official
archive with accepted SHA256; free current-source `make verify` and race outcomes
are recorded in [verification](verification/commands.json).

Both complete public synthetic original/rewritten trees, dumps, usage summaries,
raw errors, source/graph diagnostics and CLI/MCP receipts are retained here. Exact
SQLite binaries remain ignored local artifacts with measured hash/byte size in
`database-identity.json`. Public `knowledge.sql` exports reproduce logical tables
and FTS content without committing DB binaries. Restore into a fresh SQLite with
FTS5 using `sqlite3 restored.db < knowledge.sql`, then inspect `PRAGMA quick_check`
and graph/source counts. Export verification compared all user/visible-FTS table
rows with the actual DB and ran FTS integrity checks. An unused empty sequence
entry and physical/index layout need not reproduce; no byte-identity restoration
claim is made. The failed standard Python FTS shadow-table export was replaced
before publication by this verified logical export; actual run DBs were untouched.

Focused offline tests exercise unknown original hints, complete member ownership,
wrong/missing/duplicate UUID blocks, isolated v2 receipts and failure when textual
facts lack the required directed edge. No product test or paid control was changed.
[artifact-manifest.json](artifact-manifest.json) pins every published evidence byte.
Independent review and root acceptance remain separate from this author's report.

Artifact whitespace check reports four original generated Markdown blank lines at
EOF (exit2). They are retained byte-for-byte rather than altering measured output.
The four helper/document files pass scoped whitespace validation (exit0); link,
scope, credential-exclusion and original-audit checks are recorded in
[verification/integrity.json](verification/integrity.json).
