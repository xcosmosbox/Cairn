# v0.1.1 authoritative sources and finite evidence

Task [#109](https://github.com/xcosmosbox/Cairn/issues/109), parent
[#108](https://github.com/xcosmosbox/Cairn/issues/108). Date: 2026-10-26 UTC.
Base: `d7e7c761d48fe2c79aefc7547d4ef49191abeef3`, tree
`e2bebdb3f9fc257ec30c59ce61e3b8f07e0ae2ef`. The execution group used a full authentic
GitHub clone and isolated worktrees, with clean initial status. A prior synthetic
documentation checkout was not source proof.

## Delivered boundary

Discovery now provides an additive `ReportingScanner` / `FSScanner.ScanReport` API,
with stable normalized repository-relative source identity, exact supported source
bytes, sorted/deduplicated records and explicit `supported`, `unsupported`, `excluded`,
`unreadable` and `cancelled` dispositions. Caller repository identity supplies the
namespace. Supported sources include complete SKILL marker frontmatter/body and
configured UTF-8 reference files (Markdown by default), each exactly once. Scope/navigation exclusions require
reasons; no semantic navigation heuristic drops content. Empty legitimate sources are
supported with zero bytes. Failed reads and cancellation retain healthy siblings and
return an error; that error means the scan cannot certify a complete roster. Source
UUID-looking text remains content. Symlinks/non-regular entries are classified explicitly.

The legacy `Scanner.Scan` and reference-only `Skill.ReferenceFiles` remain usable.
The ordinary full/incremental builders still need #114 wiring to consume supported
report sources, including SKILL bodies, exactly once. This delivery does not claim
those paths have already acquired complete source-to-graph coverage.

The [finite evidence package](../../../build/test/e2e/semantic-evidence/README.md)
separates synthetic source text from expected observations and defines the small shared
handoff. Existing `dktypes.AnnotatedDocument{Skill,FilePath,Items}` supplies source-scoped
candidate items; candidate revision and index/member mappings remain traceable through
node selection and committed provenance. Helper outcomes remain factual
`complete` / `partial` / `unavailable` / `cancelled` with reasons; #114 alone aggregates
product completion. No production per-fact ledger, Bundle migration or downstream
algorithm change is introduced.

## Six-original inventory

All six originals remain in scope, including the rejected original and three rows
removed by old assessment. The original semantic denominator is independent of old QA,
annotation item counts and later accepted subsets. The exact reconstructed original
Markdown totals 51,183 bytes across 288 lines. The retained diagnostic's working input
also contains generated writebacks; those cannot stand in for authoritative originals.
Private reconstruction checks bind source bodies to raw DB/corpus and the five frozen
inputs plus rejected-original input. Historical packets are unchanged.

The public [inventory summary](../../../build/test/e2e/semantic-evidence/testdata/v011/inventory_summary.json)
uses S01–S06, counts, classifications and a private evidence receipt: 360 substantive
units, 16 explicitly excluded units and 194 directed relation evidence records. The authorized
private inventory retains exact original identities/bytes, span partition, all substantive
units and explicit reasons for exclusions. Source-span accounting and semantic review
are separate: a valid partition alone cannot prove faithful expected semantics.

The [public supplement](../../../build/test/e2e/semantic-evidence/testdata/v011/supplement.json)
adds eight substantive synthetic originals, two explicitly empty SKILL markers and
nineteen cases for collision/fusion,
conditions/negation/time/uncertainty/disagreement, explicit relation direction/qualifiers,
inferred distinction, multi-source identity, stable repeat, human data and finite failure
handling. Cases are evidence inputs for named downstream helpers; no model-based
construction or integrated graph pass is reported here.

## Known losses and downstream ownership

The [read-only reconciliation](../../../build/test/e2e/semantic-evidence/v011_known_losses.md)
binds consumed saved files to the unchanged 2,073,985-byte private archive, SHA256
`cdf5f96ae41bee23ff16eba3e690bf392606691e5f0acf22de3d05c501c86921`.

- Six initial annotations contain 205 rows. Five accepted originals have 176 initial
  rows and 173 after assessment; three removed occurrences remain visible for semantic
  disposition by #110. The failed original's 29 rows remain in the source inventory.
- The failed original's assessment returned HTTP 200/stop, supported=false and
  unsupported index 29, outside legal 0–28. The gate rejected malformed feedback and did
  not semantically retry. This does not establish a false source fact or exhausted
  semantic revision; #110 owns repair.
- There are 172 distinct accepted document–member incidences. The earliest retained
  05 extraction already has 171; subsequent selected stages and SQLite agree. The
  exact LLM/parser/filter origin within that boundary remains unresolved with #111.
  A whole-node loss or persistence regression is not established.
- `NodeSourcesInserted=172` is a raw input-row count containing a duplicate; selected
  distinct and committed associations are 171. #113 owns truthful production counts
  and persisted provenance, while #114 owns full integrated semantic acceptance.

#109's acceptance concerns authoritative discovery, complete expected inventory and
reproducible stage localization with named downstream owners. It does not certify #110–#114
repairs, parent v0.1 semantic success, retrieval/answer benefit or a release.

## Validation and review

Affected discovery/evidence checks, saved-packet reconciliation and source-span checks
are recorded at the exact PR head in the task checkpoint and PR. Normal CI remains
enabled. No new model/API construction, paid control rerun, sample reselection,
historical packet rewrite, annotation/extraction/storage/orchestration change or
query-side LLM/embedding/write behavior is introduced.

Execution sessions: `cairn-v011-discovery-20261026`,
`cairn-v011-inventory-20261026`, `cairn-v011-losses-20261026`,
`cairn-v011-integrate-20261026`; coordinator `cairn-v011-root-20261026`.
Separate code and original-semantic review sessions inspect the combined source/evidence.
All use the shared GitHub account; separate-session review is not independent empirical
replication or model diversity. Final acceptance/merge belongs to the coordinator.

Rollback reverts this task's additive discovery API and evidence/docs commits while
preserving the original historical packet and private inventory receipt. Downstream
implementation starts only under its own claim and existing dependencies; #112 still
requires #111's serialized `reflow.go` handoff. #111 may develop independently against
the agreed annotation interface; #113 helper work and #114 wiring can proceed after
#109 acceptance, while final integration awaits their actual accepted helper evidence.

Local affected checks used verified official Go 1.22.12 (archive 68,995,422 bytes,
SHA256 `4fa4f869b0f7fc6bb1eb2660e74657fbf04cdd290b5aef905585c86051b34d43`).
Actual commands completed successfully on the authentic combined tree:

```sh
go test ./build/internal/discovery -count=1
go vet ./build/internal/discovery
go build ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental
python3 -m unittest discover -s build/test/e2e/semantic-evidence -p '*_test.py'
python3 -m unittest discover -s .github/scripts -p 'test_*.py'
python3 build/test/e2e/semantic-evidence/v011_reconcile.py /authorized/diagnostic105
```

The last command used the existing authorized local packet, matching 23 consumed entries
against its fixed archive. Its path above is a portable access placeholder; it makes no
model call and does not modify that packet. Final inventory checks and CI bind the PR
head separately; no local full-suite/race, paid experiment, recovery or capacity rehearsal
is claimed by this report.
