# v0.1.1 saved-packet loss reconciliation and finite supplement

Task: [#109](https://github.com/xcosmosbox/Cairn/issues/109). Session:
`cairn-v011-losses-20261026`. Date: 2026-10-26 UTC. Source baseline:
`d7e7c761d48fe2c79aefc7547d4ef49191abeef3`. This is new read-only evidence from the
retained #105 packet. It neither changes the historical report/packet nor proves
that the downstream repairs or the v0.1 product contract have passed.

## Fixed inputs and reproducibility

The restricted packet is identified by the
[existing private manifest](https://github.com/xcosmosbox/kd_manifeat/blob/1552707b04679ea0617732c104e377f311fde37e/eval/diagnostic105-20261026/README.json):
archive size `2073985`, SHA256
`cdf5f96ae41bee23ff16eba3e690bf392606691e5f0acf22de3d05c501c86921`.
Original identity/content, requests, provider reasons and databases stay at that
authorized destination. Public source IDs S01–S06 follow sorted original relative
paths, matching the separate source-first inventory. A pseudonym is not a replacement
for its private stable source identity.

From the repository root, with `PACKET` set to the authorized retained directory:

```sh
python build/test/e2e/semantic-evidence/v011_reconcile_test.py
python build/test/e2e/semantic-evidence/v011_reconcile.py "$PACKET"
```

Both commands completed with exit 0 on 2026-10-26; the first ran four offline
multiplicity/set regressions. The second matched all 23 consumed unpacked files to
the fixed archive bytes before reconciling sets. It reads SQLite with
`mode=ro&immutable=1`, makes no model calls and emits only pseudonyms, counts,
indices, stage names and hashes. Missing/inaccessible/mismatched inputs make the
command fail; a missing private packet is unknown, never a passing audit.
The command does not export raw names, member IDs, prose, paths or reasons.

Key consumed bytes:

| Input | SHA256 |
| --- | --- |
| `04_extract_input.json` | `a8e9769b29bbdbd52cac17df2231ae7940aabe5290fb6aa21055a59dd75e23d4` |
| `05_extract_result.json` | `4f57187a79cb0e934f0551676a95694ef1bdc98cbff0624653cd252a924e9969` |
| `05h_materialization_candidate.json` | `9b76e26c40f40a54dd7c9c3ea3ec8af118d1b3a5819b9a20556fd99ff18fe0e9` |
| trace | `f909748b2ec7f9d5b98d2b8d02f19e7be0f3a156db642a88e2cc80c6b4606fd7` |
| database | `dc5b264fb199cd65a486f6b7c65f6237c25c548e716ed149fa47f0b2dae4f00e` |

## Rejected document and three removed annotation occurrences

| Source | Initial rows | Accepted rows | Removed initial zero-based indices |
| --- | ---: | ---: | --- |
| S01 | 32 | 31 | 2 |
| S02 | 29 | none: document rejected | none accepted |
| S03 | 38 | 38 | none |
| S04 | 34 | 34 | none |
| S05 | 33 | 33 | none |
| S06 | 39 | 37 | 17, 18 |
| All six | 205 | 173 across five | 3 across accepted documents |

The accepted five began with 176 occurrences, then became 173. The script compares
complete annotation row contents with occurrence multiplicity, ignoring only IDs
assigned after assessment. Saved accepted rows and 04 input rows match exactly
under that comparison. S01 assessment attempt 7 returned unsupported index `[2]`;
S06 attempt 8 returned `[17,18]`. These returned indices match the actual removed
occurrences. This proves the deletion boundary, not that those three deletions
were semantically justified. Their source-backed meaning remains in the six-source
inventory and requires annotation-owner disposition/repair under
[#110](https://github.com/xcosmosbox/Cairn/issues/110). The private inventory binds
these occurrences to original evidence; none leave the denominator.

S02's sole matching assessment is attempt 9: HTTP 200, finish `stop`,
`supported=false`, `unsupported_items=[29]` for a 29-item candidate. Legal indices
are 0–28. The request matches the complete saved initial array, not just a name or
request order. Request SHA256:
`7869cc8155ac9449bee877e4e5b913510b7a0134a789828483bdea4e36f74585`;
response SHA256:
`02a46a043e2d5132008f3881500efb12868ece0eaa0bd4d0697eacfc9230ea9b`.

The earliest established rejection boundary is malformed assessment validation in
[the fixed semantic gate](https://github.com/xcosmosbox/Cairn/blob/d7e7c761d48fe2c79aefc7547d4ef49191abeef3/build/internal/annotation/semantic_gate.go#L64).
The fixed code calls assessment once and rejects an out-of-range index before any
item removal. There is no saved assessment retry of that candidate. This does not
establish source falsehood, a valid unsupported-item decision, or exhausted semantic
revision. #110 owns finite reassessment of malformed feedback, source-semantic
annotation fidelity and any required subsequent revision. The entire S02 source
remains an unresolved construction obligation.

## Earliest accepted-member loss and unlike counters

An incidence here is `(original file_path, accepted member ID)`. A persisted
association is `(node UUID, member ID, original file_path)`. Raw rows preserve
multiplicity; neither a node count nor a distinct member count is either set.

| Saved stage | Annotation rows | Distinct members | Distinct document–member incidences | Raw expanded source rows | Distinct selected/persisted associations |
| --- | ---: | ---: | ---: | ---: | ---: |
| 04 input | 173 | 165 | 172 | not yet selected | not yet selected |
| earliest 05 extraction | — | 164 | 171 | 172 | 171 selected |
| 05b repaired | — | 164 | 171 | 172 | 171 selected |
| 05d described | — | 164 | 171 | 172 | 171 selected |
| 05e cross-validated and related | — | 164 | 171 | 172 | 171 selected |
| 05f UUID, 05g slug, 05h candidate | — | 164 | 171 | 172 | 171 selected |
| SQLite `node_sources` | — | — | 171 | — | 171 persisted |

Every saved 05 stage has 157 leaf nodes, one missing accepted incidence belonging
to S01, no unknown member, and no unexpected incidence. The precise same missing
incidence is absent at the earliest saved `05_extract_result.json` and remains
absent through final selection. Its exact private member/source tuple can be
recomputed as the 04 incidence set minus expanded stage incidences. The 05h selected
association set equals SQLite in both directions; there is no additional persistence
loss in this packet's selected set.

`NodeSourcesInserted=172` equals the expanded input list length. That list has one
duplicate association; its set has 171 elements. The
[fixed ingestion counter](https://github.com/xcosmosbox/Cairn/blob/d7e7c761d48fe2c79aefc7547d4ef49191abeef3/build/internal/ingest/ingest_domains.go#L566)
and [SQLite batch insertion](https://github.com/xcosmosbox/Cairn/blob/d7e7c761d48fe2c79aefc7547d4ef49191abeef3/core/storage/node_source_repo.go#L73)
therefore use different counting units. A raw input counter of 172 does not certify
172 distinct committed source associations.

The earliest reproducible boundary is accepted 04 members → saved 05 extraction.
The retained evidence does not isolate raw LLM output, parser behavior or completeness
filtering within that boundary. It does not prove loss of a whole graph node or a
SQLite insertion regression. [#111](https://github.com/xcosmosbox/Cairn/issues/111)
owns first-detected member mapping/fusion repair and final post-filter semantic/source
coverage. [#113](https://github.com/xcosmosbox/Cairn/issues/113) owns truthful raw,
distinct-selected and committed counters and persistence correctness. #113 cannot
repair meaning already missing before ingestion. The source/member set check remains
separate from full semantic coverage: correct counts cannot certify preservation of
conditions or distinguish different facts sharing a member key.

## Finite supplement and downstream handoff

[testdata/v011/supplement.json](testdata/v011/supplement.json) fixes eight substantive
synthetic source files, two legitimate empty marker sources and 19 expected cases
before downstream implementation. It is a compact
fixture, not a new execution framework, production per-fact ledger or builder prompt.
To prepare ordinary construction input, under a fresh root create each normalized
relative path in `empty_skill_markers` with exactly zero bytes, then create each
`sources[].path` containing the exact UTF-8 `sources[].content`. Parent directories
are created as needed. Point ordinary discovery at that fresh root; do not bypass
discovery with prebuilt annotation rows. The two markers are
`fixtures/operations/SKILL.md` and `fixtures/control/SKILL.md`; they make the two
synthetic skills discoverable. Their empty bodies are legitimate successful reads
with no substantive semantic expectations, not failed reads or silently excluded
knowledge. Thus the supported fixture roster is ten files: eight substantive
originals and two empty structural markers. No generated `_shared` files or sidecars
are input. The synthetic Markdown headings identify their following assertions;
they add no separate assertions beyond those bodies.

Only `empty_skill_markers` and `sources[].{path,content}` prepare construction input.
Expected assertions and failure
scenario instructions remain in the acceptance channel. No old QA/gold is used.
Normal cases and injected failures are separate evidence kinds. Passing schema checks
or offline reconciliation does not mark any graph-semantic case as passed.

| Case group | Required distinction | Owner |
| --- | --- | --- |
| annotation/qualifiers; malformed and semantic recovery; exhaustion/unavailable | Complete source meaning; reassess malformed same candidate; revise actual omission; explicit total-attempt budgets and terminal reason | #110 |
| same-name; normalized-name; qualified fusion; UUID literal | Pump versus library; radio versus bracket; conditions/negation/date/uncertainty/disagreement; source UUID stays text | #111 |
| qualified and cross-domain relations; explicit versus inferred | Direction, time/condition, negative versus unknown, supported endpoints, no false explicit-source credit | #112 |
| shared fact/multisource; relation readback; write failure | Distinct sources survive rational fusion and DB/Markdown; separate actual counters; usable state/baseline survives failure | #113 |
| repeat; human preservation; cancellation; sibling/dependent join | Stable UUID/source/meaning, curated/clean data preserved, terminal sibling outcomes and honest aggregate completion | #114 |

The shared handoff identifies the authoritative original, candidate result and factual
`complete` / `partial` / `unavailable` / `cancelled` outcome with reason and enough
stage identities to trace it. Stage owners report facts; #114 alone aggregates product
complete/partial/failed. #109 can accept this inventory/localization work without
pretending #110/#111/#113 repairs are already done. Unresolved downstream meaning
still blocks its owning task and parent #108.

The fixed corpus remains all six originals plus this supplement. Normal full coverage
requires every substantive distinction, explicit relation and valid source association
to survive without unsupported additions; forced-failure success only establishes
truthful operational behavior. Query/utility benefits, arbitrary-language completeness,
full609 capacity and release readiness are outside this finite evidence claim.
