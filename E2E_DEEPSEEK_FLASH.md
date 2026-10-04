# DeepSeek Flash / nicedata end-to-end acceptance

Model run: 2026-10-03. Online publication and consumption: 2026-10-04. Target repository: `xcosmosbox/kd_manifeat`.

## Input and execution scope

The user-supplied `nicedata-skills(2).zip` is 30,473,261 bytes with SHA-256
`a4d5ed6510a7a67667298fb1d40d8c8a7f0ec95ffafc76a390732fe992a62afa`.
The isolated corpus excludes archive/Git metadata. Cairn discovers 27 skills
(26 original skills and a documentation wrapper) and 69 reference documents.
The input fixture commit is `e7d45faa948353b77c6753ae1987f59498df1a8e`.
Published source consists of generated Markdown and sidecars. Original runtime
`SKILL.md` files, the archive, credentials and raw model transcripts are excluded.

The test uses `deepseek-flash` at
`https://api.deepseek.com/chat/completions`. Configuration follows the
[official DeepSeek model table](https://api-docs.deepseek.com/quick_start/pricing/):
the standard thinking budget is 65,536 tokens, and the supported maximum output
is 393,216 tokens. This large corpus uses the maximum for extraction and
downstream requests, concurrency 8, a 30-minute request/stage timeout, and a
90-minute overall timeout. Provider reasoning and sampling defaults are retained.
The file-slug stage has a separate 120-second production deadline and deterministic
fallback names.

This is a checkpointed real-provider test. The first run's 69 accepted real
annotations and recorded semantic-gate passes are verified against original
source bytes and compatible annotation/ID code before reuse. Original raw Gate B
verdict text was not saved; replay therefore represents its recorded pass, not a
new semantic judgment. Extraction, coverage supplementation, repair, description,
relation generation and slug generation were then sent to the real DeepSeek API.

## Bugs found and fixed

- Reject incomplete provider completions even when truncated output parses as JSON.
- Enforce real annotation member IDs and source provenance in repair patches.
- Gate missing member details/descriptions and silent ingest omissions.
- Remove tiny fixed output budgets from slug and PR-summary calls, retain caller
  cancellation, and distinguish successful slugs from deterministic fallbacks.
- Sort accepted documents before fusion for stable source selection.
- Compute lease time inside SQLite statements after lock acquisition; replace
  timing-sensitive renewal tests with controlled ticks.
- Check the frozen original builder fingerprint in publication and check all
  expected source/builder fields during consumer receipt verification.
- Use the same confidence-filtered candidate graph for ingestion and writeback;
  reject eligible UUID collisions before materialization.

The real run exposed the final issue: three repair nodes had confidence zero,
so ingestion excluded them at the 0.7 threshold while
writeback still used them. One duplicated the members and UUID of an accepted
node. The repair prompt did not require confidence, and the saved parsed records
do not distinguish an omitted value from explicit zero. Sidecar validation
blocked publication. Filtering those nodes preserves
all 2,086 unique annotation members. The corrected deterministic materializer
reuses the saved completed extraction, with separately recorded model-builder
and materializer identities and zero additional provider calls.

## Real provider measurement

The fresh downstream run records 648 HTTP attempts, 647 HTTP responses,
1,127,194 input tokens and 1,144,013 output tokens, with no `length` completions.
Eight attempts have no usage record because the slug deadline interrupted reads
or transport. These token figures are a recorded lower bound, not a complete
billed total. Historical annotation calls are outside this measurement.
565 of 879 proposed nodes received successful model-generated slugs; the remaining
names use deterministic fallback.

Coverage was 2,004/2,086 after initial extraction and 2,086/2,086 after coverage
supplementation. All 879 proposed node descriptions completed; all 35 subdomains
completed relation generation with 1,435 added relations and no invalid relation
endpoints accepted at that stage. Confidence filtering retains 876 knowledge
nodes, still covering all 2,086 members.

## Publication and consumption evidence

Local materialization accepted 944 total nodes (876 knowledge nodes), 2,460
edges and 2,200 provenance rows across all 69 source documents. Candidate selection
explicitly excluded three zero-confidence nodes and six incident relations;
ingestion then skipped zero edges. Writeback produced 69 source documents plus
230 shared primary documents, each with a sidecar: 598 reviewed generated files.
The production validator passed SQLite, schema, endpoint, provenance and source
closure checks. Full Go race tests and [fixed-commit CI](https://github.com/xcosmosbox/Cairn/actions/runs/37112860461) passed. Actual CLI, MCP stdio, REST and SSE
passed locally against a production SQLite snapshot, with unchanged DB bytes and
no WAL/SHM creation.

Generated sources: [PR #12](https://github.com/xcosmosbox/kd_manifeat/pull/12),
merged as `801ebed0eb6f1044dfd713d1e76251de39a79f47`.
The frozen prebuilt transport commit is
`43e53b22df2709dec833fd2ead3f8166b6b74a4f`. Its 44 Base64 parts are
verified individually, reassembled and checked against archive SHA-256
`59b9c7a3a8af5ae690e803760f2b4f9d42043c3033c80da43f4c80410b0661e8`
before extraction. This transport contains the unchanged accepted snapshot and
provenance; it makes no LLM calls.

- [Release publication and replay](https://github.com/xcosmosbox/kd_manifeat/actions/runs/37168033699): passed.
- [Accepted Release](https://github.com/xcosmosbox/kd_manifeat/releases/tag/kb-nicedata-e2e-20261003-801ebed0eb6f-1d0f66c50f75): GitHub Release ID `402770491`.
- [Catalog PR #13](https://github.com/xcosmosbox/kd_manifeat/pull/13): merged to main as `54aa89828726f28173b9e3859290b47354aa614a`.
- Bundle digest: `sha256:1d0f66c50f7549da43e3b0f23d8e036ef6084b466ffd4a5f24cb82ebf0ba95da`.
- Release tarball SHA-256: `a3a30473a7e951fa7f27e7c6cb3d30049357599a499869c090c0d33d05463369` (matches GitHub asset metadata).

- [Formal Catalog consumer job](https://github.com/xcosmosbox/kd_manifeat/actions/runs/37168181994): passed. Repeat installation preserved the installed generation.
- All five requested themes were found; CLI checks, 30 MCP stdio calls, REST and four SSE calls passed, including UUID/provenance queries and unknown-KG rejection.
- Downloaded database SHA-256 was `666eba2a529b1777dde6a59f7f641ba890ea1af6047ebe1f8668d878c18b8495` before and after consumption, with no WAL/SHM files.

The publisher creates its own consistent SQLite snapshot before packing. Its
serialized DB checksum therefore differs from the prebuilt input snapshot; these
checksums describe separate stages. GitHub asset metadata, bundle verification
and the installed DB read-only checks all passed.

Machine-readable [acceptance summary](e2e/receipts/nicedata-20261003/acceptance.json),
[publication receipt](e2e/receipts/nicedata-20261003/publish.json),
[consumer receipt](e2e/receipts/nicedata-20261003/consume.json),
[protocol smoke receipt](e2e/receipts/nicedata-20261003/consumer-smoke.json) and
[build report with provenance](e2e/receipts/nicedata-20261003/build-report.json)
are committed alongside this report. GitHub Release publication occurred at
2026-10-04T01:27:34Z; the manifest retains the frozen build timestamp from October 3.

The GitHub connector creates the reviewed source commits and PRs. Dedicated
GitHub Actions jobs use repository-scoped `GITHUB_TOKEN` credentials to exercise
Cairn's real `HTTPForge`, immutable Release publication/replay, Catalog resolution,
bundle verification and installation/reinstallation. This does not exercise
production GitHub App JWT minting or installation-token refresh.

Consumer acceptance covers CLI search/why/traversal, MCP stdio, REST and SSE,
source provenance, UUID lookup, unknown-KG failures and read-only DB integrity.
Publication uses the production source-closure validator. Generated Markdown,
sidecars, source ledger and database node sets are also checked against each other
before upload.

## Reproduction

The fixture runner and checkpoint constraints are documented in
[`build/test/e2e/real-build/README.md`](build/test/e2e/real-build/README.md).
Publication/consumer commands are implemented in
[`build/test/e2e/actions-publish`](build/test/e2e/actions-publish).
The protocol smoke test is [`e2e/real-consumer-smoke.py`](e2e/real-consumer-smoke.py).
Credentials are injected only at runtime. The release publication job does not
have an LLM credential and cannot regenerate paid content.

## Frozen identities

- Real downstream LLM code: `a6fb17cf00de5c0c9e85ec2134a1658e087b8727`.
- Accepted materializer code: `7940e62a71ef0321f2563abcfd1471a55b390f2e`.
- Completed LLM checkpoint SHA-256: `f72fca62bc06f4955f5a7b6c8ac6a4b87bc4fdafababeadea18628d6784677e9`.
- Twenty-file checkpoint manifest SHA-256: `41638f9bf93e2d84be130cfdb3150cc9038adbe5dbe2485e87f9253af54aec5d`.
- Standalone generated DB SHA-256: `6d2d54c533a68317a03a61e6e5291c235a656db71f4408a26fbaf28cf547c828`.

The release build report embeds both complete fingerprints, checkpoint hashes,
reused/fresh stage lists and the original usage summary. Its materializer
configuration digest binds those records to this specific recovery.
