# PR46 split-primary fixture clock diagnosis

Task [#50](https://github.com/xcosmosbox/Cairn/issues/50), session
`cairn-mcp-fixture-20261024-live16`, fresh main baseline
`2d87bbdf075960eade455fac2aa2f5148ee31530`, isolated branch
`fix/mcp-source-fixture-clock-20261024`.
[CLAIM](https://github.com/xcosmosbox/Cairn/issues/50#issuecomment-6385237274) /
[ACK](https://github.com/xcosmosbox/Cairn/issues/50#issuecomment-6385241055).

The source-metadata test assumed that all lineage inserts shared one timestamp.
Its assertion requires split successor `a`, but the documented resolver chooses
newest `created_at` first and lexical UUID only on a tie. A newer `b` correctly
returns `b` and `b`'s source. This change fixes that test precondition and adds an
explicit newer-`b` case. **No production resolver, schema or query behavior changes.**

## Original failed CI remains evidence

- PR46 exact head `ce5aa5c1f34a22d99b1045c425866f6816e91b16`.
- [Correctness40100963020](https://github.com/xcosmosbox/Cairn/actions/runs/40100963020),
  [job120892832098](https://github.com/xcosmosbox/Cairn/actions/runs/40100963020/job/120892832098),
  Go stable resolved to1.27.3; Ubuntu24.04.5.
- Actual checkout merge `0571249743221020a45abc77b8279d5670f12417`, combining that
  head with main `2d87bbdf075960eade455fac2aa2f5148ee31530`.
- Race step failed `TestResolveNodeSourceContract` at
  `zz_source_citations_test.go:115`: split returned `NodeUUID:b`, member-b,
  first-skill, first/b.md and unknown positions. The test demanded `a`/first/a.md.
  Go1.22 and Viewer jobs passed. No `SQLITE_BUSY` appeared in this failure.
- [Sanitized failure excerpt](diagnosis/ci-failure-excerpt.log) retains the exact
  test payload and checkout/toolchain identities without setup credentials/headers.
  [PR checkpoint](https://github.com/xcosmosbox/Cairn/pull/46#issuecomment-6385216179).

The actual failing runner's lineage row timestamps were not retained. The cause
mechanism below matches the trace; it is **not** a recovered CI timestamp record
or a claim to have reproduced the entire Go1.27 runner environment.

## Bounded mechanism check

The old fixture calls `UUIDLineageRepo.InsertBatch` for old→a, split→a, split→b.
It executes individual prepared statements whose `created_at` defaults use
`datetime('now')`. A transaction does not establish the tie required by this test.
`ResolveLatest` explicitly orders `created_at DESC, new_uuid ASC`. `GetNode` uses
that primary, and MCP obtains source metadata for that same primary UUID.

Three focused local Go1.22.12 race commands ran once each before this fix; all
production/tracked source bytes remained unchanged. Only ignored test overlays
changed explicit fixture timestamps:

| Variant | Expected by old test | Actual result |
| --- | --- | --- |
| Unmodified fixture | Primarya | exit0 in this bounded observation |
| Explicit equal timestamp for all lineage rows | Primarya by UUID tie break | exit0 |
| Explicit split→b one second newer than split→a | Old test still demandsa | exit1, same returnedb/member-b/first/b.md payload as CI |

[Exact commands, times and outputs](diagnosis/commands.json),
[equal overlay source](diagnosis/tied-fixture.go.txt),
[newer overlay source](diagnosis/b_newer-fixture.go.txt) and their overlay JSONs
are retained. The extra injected lines move the assertion from115 to117; the
failure body is otherwise the same. Overlay paths identify the actual local run.
For reproduction, copy the corresponding `.go.txt` outside the Go package tree,
point Go's `-overlay` Replace mapping from the original test's absolute path to
that copy, and run the recorded focused command. `.go.txt` prevents evidence from
becoming an unintended executable Go package during `go test ./...`.

## Fix and validation boundary

`sourceFixture` now pins explicit equal timestamps after inserting its lineage
rows. The existing strict test still requires primarya, onlya's source, siblingb,
no stale source, correct KG routing and unchanged DB bytes. No assertion was
weakened and no timeout, scheduler delay or retry was added.

The new parallel `TestResolveNodeSplitSourcesFollowExplicitNewerSuccessor` sets
only split→b to one second newer. It requires primaryb, member-b/newer/b.md,
unknown positional metadata for that member, siblinga in the human-readable
redirect, no a/stale source in b's metadata and unchanged DB bytes after queries.
Both timestamp orderings are now deterministic.

[Validation receipts](verification/commands.json) record focused race, full
`go test -race -count=1 ./...` and `make verify`, exact times/statuses/logs.
The local toolchain is Go1.22.12; exact-head CI must separately establish both
configured Go versions and Viewer. [Integrity receipt](verification/integrity.json)
and [artifact manifest](artifact-manifest.json) verify the bounded test/report
scope and published bytes.

The original red PR46 run is not retried, overwritten or reclassified as green.
This independent correction requires separate review and CI. Root's intended
sequence is acceptance/merge here, then incorporating changed main into PR46 to
obtain a new head and legitimate new CI. This is not a stable-release decision or
a resolution of the separate real graph dependency failure. No model, benchmark,
HOLDOUT, publication or deployment was run for this task.
