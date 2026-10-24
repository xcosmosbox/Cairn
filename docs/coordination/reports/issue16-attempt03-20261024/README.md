# Actual two-document semantic regression, attempt03

The single authorized run consumed both original documents, preserved the direct
`重试预算 → 阻塞队列` dependency through original-source verification into SQLite,
and exposed that edge through actual MCP forward impact. Capacity8, wait50ms and
retry2 remain in final descriptions. This is a small synthetic correctness check;
it is not an answer benchmark, a GraphRAG quality gain or stable-release approval.

Session `cairn-realmodel-20261024-attempt03`;
[CLAIM](https://github.com/xcosmosbox/Cairn/issues/16#issuecomment-6385561998),
[ACK/root admission](https://github.com/xcosmosbox/Cairn/issues/16#issuecomment-6385582822),
[pre-call freeze](https://github.com/xcosmosbox/Cairn/issues/16#issuecomment-6385599503).
The earlier [attempt01/02 negative evidence](../issue16-realmodel-20261024-live16/README.md)
is unchanged. Root separately admitted this one regression after accepted #45
implementation; it was not an automatic retry or replay of an accepted paid control.

## Source, binaries, inputs and settings frozen before the call

Exact clean product source: `75a77a0d5d1e1d5077c4758e56f2bc7fd3ac62a8`, the
accepted PR53 merge. The standard binaries were freshly rebuilt by `make build`
(exit0), not reused from the earlier runs. Their `go version -m` records all attest
that revision and `vcs.modified=false` in [attempt03-freeze.json](attempt03-freeze.json).

| Binary | SHA256 |
| --- | --- |
| cairn-ingest |`46aa438c629e047c4f9137099a06c769bc171c348ae89067f442d45ed935bddd`|
| cairn |`6acab2b7ac87c864550f29225c1731a84f0d2e34264977b656edc685267f476d`|
| cairn-mcp |`f420a42bbf56aa79951ebba12a790a7e5d10eefd7eb8bf83733f7c0009f93886`|

The unchanged frozen Skill and two references were copied into a new empty
ignored runtime directory. Original snapshots are retained separately from
rewritten documents. Only the two references are annotated; the Skill provides
discovery metadata. The original successful canary receipt is copied with explicit
[reuse identity](canary-reuse.json), not counted as a new call or evidence of the
new builder's response identity.

Effective standard helper invocation, with private key location suppressed:

```sh
python3 examples/public-demo/real-model/run.py smoke \
  --out .local/real-model/attempt-03 --key-file /AUTHORIZED/RUNTIME_KEY --authorize-deepseek
python3 examples/public-demo/real-model/run.py audit --out .local/real-model/attempt-03
```

Same official `https://api.deepseek.com/chat/completions`, configured
`deepseek-flash`, JSON and enabled production-default high thinking;16384 maximum
output tokens,180-second HTTP timeout, CLI max-retries3/max-rollbacks3, no evolution
sidecar,600-second outer process bound. Other production stage retry constants
remain unchanged. Cross-source evidence limit was the existing65536-byte default.
Generated runtime `preparation.json` records the deliberate3/3 override from the
helper's one-round preparation defaults. No product/helper/prompt or frozen input
was edited. Nine offline helper tests passed before the model call.

Root explicitly scoped this run to correctness with the standard client. No new
trace driver or proxy was introduced. Requested model and successful token logs
are known; provider response IDs, served backing revision, total wire attempts,
failed/503 usage and final billing are **unknown**. This is not permission to omit
full wire/token accounting in a later capacity experiment.

## Actual result and observed usage

Run14:48:02.872714–14:49:29.955336 UTC on2026-10-24:87.065seconds, builder exit0.
Both documents passed their first annotation/semantic-gate round;0 skipped.
The builder materialized17nodes/30edges, including14semantic edges and14source
rows. It rewrote2documents plus3shared primaries, with6mirrors and5sidecars.
Repair reported0dangling/0cleaned/0rounds and no degradation.

[ingest.json](ingest.json) and [ingest.log](ingest.log) retain the actual command,
limits and outcome.25successful response logs sum16407prompt and26482completion
tokens. These are42889reported tokens, not a complete wire-attempt or cost ledger.
No provider503 error line was observed; unlogged usage is still not assumed zero.
[usage-observation.json](usage-observation.json) states those boundaries explicitly.
The copied earlier canary's45+5tokens are excluded from these new-run totals.

## Cross-scope mechanism actually exercised

All required dumps exist, parse and have measured byte/hash identities in
[cross-audit.json](cross-audit.json). The
[frozen source dump](dump/00_frozen_sources.json) is byte-identical to both retained
originals and has their original SHA256. The exact
[candidate report](dump/05e_cross_relation_report.json) contains3decisions:

| Candidate | Actual verifier outcome | Materialized |
| --- | --- | --- |
| Retry budget depends_on blocking queue, across subdomains | supported; correct direction; source and target original quotes/member bindings validated | Yes |
| Cross-document relation check references retry budget, across domains | insufficient evidence, with retained explanation/quotes | No |
| Cross-document relation check references blocking queue, across domains | insufficient evidence, with retained explanation/quotes | No |

The accepted edge's source quote is timeout.md original UTF8 bytes[316,417),
line10; target quote is queue.md bytes[97,223), line5. Independently sliced bytes,
line counts, source hashes and endpoint member ownership match both native
validated records. The complete original sources are retained; generated
annotation/detail text is not substituted as the quote authority. Independent
mechanical checks also locate the four rejected candidates' raw verifier quotes,
clearly labeling those computed offsets separately from native accepted offsets.

The accepted source/target member sets bind through
[05e validated](dump/05e_cross_validated.json),
[05f UUID assignment](dump/05f_uuid_assigned.json) and
[05h materialization](dump/05h_materialization_candidate.json) to the actual final
SQL `depends_on` edge. Its source is retry budget UUID
`c3123633-9fad-5391-8d39-ba4d7b393783`; target is blocking queue UUID
`87177315-8d2f-52d8-9d48-68fcf2afeb4e`. Rejected candidates are absent from the
final graph. No genuine contradictory-facts relation is inferred merely from
fixture scope or absence.

Limits: no `contradicts` candidate was generated in this actual run and none is
stored finally. Therefore this run does **not** demonstrate live rejection of
that specific false-absence candidate; that branch has separate implementation
regressions. The originals contain0UUID strings, so source UUID-masking behavior
is likewise not exercised here. Pre-UUID model aliases and post-last-LLM UUID
assignment were checked, but no standard-client raw prompt/HTTP trace is claimed.
The two insufficient-evidence outcomes are conservative AI judgments, not new
human gold. Local relations were not all subjected to the new cross-candidate
verifier; no complete-graph truth certification follows.

## Real query, source and byte-preservation evidence

The unmodified corrected [audit-v2.json](audit-v2.json) exits0 for both nonempty
extraction inputs, exact candidate/member/UUID source ownership, generated
sidecar hashes and block-marker diagnostics, SQLite integrity, CLI status, actual
MCP searches/resolves, unknown UUID handling and unchanged DB bytes. All four
required checks are true: capacity8, wait50ms, retry2 and directed dependency.
The original14stored span hints remainunknown; they are not generated positions.
The new cross-edge quote offsets above are separate original-source evidence,
not newly populated MCP `node_sources` citations.

Additional actual [MCP depth1 impact](mcp-cross-impact.json) from retry budget
returns blocking queue and renders the direct `depends_on` edge; this verifies
product forward traversal across the taxonomy boundary. Both query sessions
leave exact database SHA256 unchanged:
`d644cb208107dd58aac7b5b74c3b49f0046bbc985d111cfb8918ed19736d8483`.
MCP receipt paths are in a separate audit evidence directory, and earlier paid
attempts/audits were never overwritten. Independent security review recomputed
source/member/quote/UUID/SQL bindings and actual CLI/MCP behavior before this
report; final exact-head review/root acceptance remains a separate record.

## Recovery and publication boundary

All public synthetic original/rewritten documents, stage dumps, verdicts, quote
bindings, log/usage summaries and query receipts are retained. Exact runtime DB
binary remains ignored locally. [knowledge.sql](knowledge.sql) reconstructs logical
schema/user/FTS rows in a fresh SQLite with FTS5; its import was compared against
the actual graph and passed quick_check/FTS integrity. Physical SQLite bytes and
unused sequence bookkeeping need not reproduce. Original DB/hash/size and export
identity are in [database-identity-and-restore.json](database-identity-and-restore.json).
[artifact-manifest.json](artifact-manifest.json) pins the published evidence bytes.

This reports one synthetic positive regression after two retained negative
attempts, not a controlled causal estimate or statistical before/after comparison.
No12-document calibration, full609 build, QA/gold/HOLDOUT access, answer judging,
Bundle/catalog publication, model substitution or stable release occurred. Root
must separately admit any next capacity stage and decide #16 acceptance.

The raw generated Markdown snapshots retain five original blank lines at EOF;
unscoped artifact whitespace validation reports exit2 for those byte-exact files.
Source code is unchanged. Link, scope, credential-exclusion and manifest identity
checks pass; snapshots are not trimmed to make an artifact-style check green.
