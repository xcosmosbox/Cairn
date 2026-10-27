# v0.1.4 relation helper fidelity and finite repair

Task: [#112](https://github.com/xcosmosbox/Cairn/issues/112). Date: 2026-10-27 UTC.
Starting main `f92d7dfa87cba5c51b12ad38265b05ddc828ec3f`, tree
`48de892e1b614bc7c6c26f5f3e7dfe8ee2fa340d` (878 tracked leaf entries).
The [fresh v0.1.3 recheck](https://github.com/xcosmosbox/Cairn/issues/111#issuecomment-6432972299)
and [renewed whole-file release](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6432388051)
satisfy dependencies. The [acknowledged execution and interface](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6433074114)
separates relation, whole-file incremental and integration ownership.

The implementation and local helper evidence below are ready for review. Normal CI,
exact published-head independent review, merge and maintainer acceptance remain live
Issue/PR events; this report does not predeclare them. No new paid construction,
full609 experiment or historical packet rerun occurred. The unchanged six-original
inventory remains 51,183 bytes, 360 substantive units, 16 reasoned exclusions and
194 explicit relation cases. These are the original denominator, not a claim that
this deterministic helper campaign reconstructed all six originals.

## Implementation and factual boundary

`(*Extractor).ProcessRelations(ctx, candidate, docs, originals, RelationProcessOptions)`
consumes complete `FrozenSource` bytes, accepted document/member material and exact
candidate nodes. Generation and completeness assessment see full original material;
new selection also requires source-supported endpoints, direction, qualifiers and
`explicit_source_assertion=true`. Scopes retain source/member identity, exact
material/candidate identities, actual step attempts, factual outcomes/reasons and
exhaustion. Decisions retain exact source hashes and computed quote byte/line positions.
Expected inventory/cases remain outside construction prompts.

Supplied candidates remain auditable without promotion to original assertions.
Only newly verified explicit decisions enter `Selected` and the candidate graph.
The source-aware changed-document adapter passes a candidate clone and separately
protects full persistent old edge objects. Missing output, unknown support or transport
failure does not authorize deletion. Human content stays outside model input and
retains every field. A retained old inference receives no false explicit-source credit.
Source-less legacy entries preserve old content, make no model call or unverified
addition, and expose unavailable original support. No schema/shared extraction DTO
or ordinary entry source-plumbing change was made.

Positive `MaxRetries=N` means N EXTRA recoveries; zero defaults to three, negative
means none. A source shares one allowance across generation, coverage, verification,
revision and deferred supplied-candidate audits. An identical regenerated candidate
does not reset its audit allowance. Unknown/malformed feedback repeats the exact
candidate/request; actual semantic defects revise full originals + whole candidate +
critique and recheck the affected set. A completed revised set supersedes its obsolete
prior selection while retaining exact unchanged successful siblings and other sources.
Cancellation and permanent input/configuration failures terminate with concrete reasons.
A recovered audit clears stale unavailable status without replenishing its allowance.

Released owner commits were integrated without manual production overlap: source
`63ba0a8`, `f711968`, `9f351839`, `76357f6`; incremental `874758f`, `0ded7b7`.
The last production-code checkpoint is local `ccf61fd99313764d825da655200aa2aae6be371d`,
tree `8c20053b1eda9a1a3225b2cb760c181f3ad720ad`; final documentation/publication identities
are recorded in #112 and its PR rather than self-referenced in this file.

## Finite source-derived evidence

Independent acceptance reads the unchanged public
[supplement originals](../../../build/test/e2e/semantic-evidence/testdata/v011/supplement.json).
Scripted assessors inspect actual candidates and deliberately reject concrete negative
controls. This verifies helper behavior/evidence binding, not a real model's semantic
judgment quality or ordinary whole-original production completeness. The independent
acceptance files contain nine extract and two incremental test functions with subcases;
author regressions and separate reviewer probes supply complementary cases.

| Source obligation | Actual control |
| --- | --- |
| F03/F04 conditional feed and F01/F05 shutdown signal | Exact source-bound node detail survives; unsupported predicates are never relabeled as false `references` edges |
| F04 pump dependency on C from 2025-06-01 and earlier D dependency | Genuine `depends_on` edges exercise cross-domain endpoints/direction/time, source binding and unchanged repeat |
| F03/F04 uncertain 3/5 percent disagreement | Attributed same-date contradiction material; existing negative controls reject different times/conditions and absence as contradiction |
| F05 inference/unknown | Logger co-location dependency and Sensor unknown have real F05 member bindings, remain auditable and receive no explicit credit even from positive old-candidate audits |
| Same-triple qualifier/source facets | Author/reviewer controls preserve distinct actual conditions, times and source evidence through full/shared changed helpers and reordered repeat |
| Invalid endpoints/evidence | Wrong direction, explicit predicate false/missing, invented quotes, wrong member/source and invalid aliases cannot produce accepted additions |
| Finite correction | Actual empty first candidate triggers omission critique/revision/recheck; unknown repeats unchanged assessment; mixed unknown/rejection shares one allowance; dangling recovery uses existing valid nodes only |
| Failures and siblings | Exhaustion retains all source scopes and successful independent relations; pre-cancel makes zero calls; real local production-client invalid protocol stops permanently after one attempt |
| Changed-document composition | Incoming cross owner revalidates when only target changes; missing/new cross joins use both originals; omission/unknown/transport preserve full old rows through I8; human rows stay exact and outside prompts |

Unsupported edge predicates survive in previously verified source-bound node detail;
these helper controls do not re-prove .3 body semantics or claim the new relation
helper generates unsupported kinds. The initial acceptance draft incorrectly used
`supports`, then an inappropriate `references` representation for feed/shutdown.
Review corrected those fixtures to genuine supported kinds and explicit graph-detail
checks, without changing source expectations or production enum semantics. Other
fixture corrections decoded JSON originals and cited all contributing endpoint sources.

Real implementation defects found and corrected remain visible:

- Separate source review and integrator probes reproduced audit/generation allowance
  reset (four unknown assessments instead of two with MaxRetries1) and a permanent
  old-audit error followed by another generation call (two local invalid-protocol
  failures instead of one). Original assertion limits now pass on the repaired helper.
- Source review found stale selection after whole-set semantic revision, and later
  found recovered deferred audits leaving unavailable state/skipping subsequent audits.
  All six unchanged reviewer probes pass after the source follow-ups.
- The original pipeline materialization test failed after early dangling cleanup was
  deferred: a node removed at the terminal description gate left a selected dangling
  edge, reported as two selected but only one stored with one skipped. The
  [exact six-line amendment](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6433315852)
  reruns the existing structural selector after terminal node filtering. Early repair
  candidates stay intact; the original failing test is unchanged and passes.
- A final independent real SQLite probe found forged prior machine-envelope
  `explicit` labels/invented sources without evidence surviving coalescing. The
  incremental follow-up now derives explicit authority solely from current verified
  `Selected` facets with an exact canonical description/source/evidence match.
  Unmatched prior facets become `legacy_unverified` with all known fields retained;
  unknown/extra-field JSON retains the whole old raw text. Two-pass real SQLite and
  reordered repeat regressions prevent false credit, lost old content or growth.

## Actual local verification

Go1.22.12, `GOTOOLCHAIN=local`, all Go commands use `-buildvcs=false`.
The combined code checkpoint above passed:

| Command | Exit/result |
| --- | --- |
| `go test -buildvcs=false ./build/internal/extract ./build/internal/incremental ./build/internal/pipeline ./build/test/e2e/semantic-evidence -count=1` | 0, all four packages |
| `go test -buildvcs=false ./build/internal/pipeline -run TestMaterialization -count=1` | 0, unchanged terminal regression |
| `go build -buildvcs=false ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental` | 0 |
| `go vet -buildvcs=false ./build/internal/extract ./build/internal/incremental ./build/internal/pipeline` | 0 |
| `go test -race -buildvcs=false ./build/internal/extract ./build/internal/incremental -run 'TestV014\|TestCross\|TestNodeReflowExistingRead\|TestV013CancellationAcceptance' -count=1` | 0, rerun after the final incremental provenance correction |
| `python3 -m unittest discover -s build/test/e2e/semantic-evidence -p '*_test.py'` | 0, 10 tests |
| `python3 -m unittest discover -s .github/scripts -p 'test_*.py'` | 0, 12 tests |
| `make check-invariants`; `git diff --check`; 18 local Markdown target checks | 0 |

Separate sessions share the GitHub account/environment; deterministic controls are
not independent empirical model replication. Source reviewer repeated its six probes,
source acceptance and unchanged materialization test on the combined code checkpoint,
all exit0. Incremental reviewer checked real same-triple time/source facets, repeat,
full row protection and missing/cancelled scope behavior. Final exact-head formal
reviews/normal CI remain separate. No blanket local full verify/race suite was run. The I-6 node body from `reflowNode`
through its node helper functions is byte-identical to starting main (SHA256
`19c2c80e77cc1080e4ab7efc759cdbadf54c8f3c0eda666fb6ee8cb5f4376c26`),
including the repaired caller cancellation behavior.

## Persistence and ordinary-entry handoff

The existing relation description string contains JSON under
`cairn_relation_assertions_v1`. Each facet has `classification` and `description`;
newly verified `explicit` facets additionally carry exact `sources` and `evidence`.
Old plain text is separately `legacy_unverified`, never promoted by coexistence.
Parsing prior JSON labels is not verification: only exact canonical matches to current
verified `Selected` facets retain explicit credit; unmatched prior facets are downgraded,
and unknown-shape/extra-field JSON preserves the whole raw old description.
Canonical ordering/dedup retains distinct qualifying/source/evidence facets without
one-triple row growth. Focused real SQLite helper tests exercise successful normal
single-old-row application and repeat while preserving old edge fields.

The [explicit duplicate-row guard](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6433183250)
addresses existing I8 collapse of distinct full legacy machine rows sharing a triple.
The adapter withholds that affected source-subdomain replacement key, retains all
original rows and reports unavailable/deferred application with exact scope/triple.
Verified decisions remain selected evidence, explicitly unapplied there; independent
scopes still apply. This is a factual dependent application failure, not normal
semantic/persistence success. No storage/schema change or broad ownership transfer
is implied. [#113](https://github.com/xcosmosbox/Cairn/issues/113) owns complete
persistence/Markdown/readback of distinctions, source binding and this precise shape.

[#114](https://github.com/xcosmosbox/Cairn/issues/114) remains sole owner of ordinary
full/changed source plumbing, shared aggregate/export changes and product
complete/partial/failed aggregation. The narrow terminal compatibility repair is handed
back without starting that task. It must preserve surviving semantic/UUID correspondence,
consume actual scopes/attempts/reasons and perform the fixed ordinary six-original plus
finite-supplement construction/repeat/changed campaign and persisted readback. These
boundaries waive no missing .4 algorithm obligation and establish no parent acceptance.
Refs #112.
