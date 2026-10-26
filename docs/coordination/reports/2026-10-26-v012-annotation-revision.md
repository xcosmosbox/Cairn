# v0.1.2 annotation fidelity and finite revision

2026-10-26 UTC. Task [#110](https://github.com/xcosmosbox/Cairn/issues/110), base
`c78497e3787de49be2187b4b21895f85f66b4d4d` (accepted v0.1.1 / PR117).
Sessions: `cairn-v012-annotation-20261026` (core),
`cairn-v012-incremental-20261026` (C4 adapter),
`cairn-v012-integration-20261026` (acceptance/evidence/publication).
[CLAIM-ACK](https://github.com/xcosmosbox/Cairn/issues/110#issuecomment-6417783317)
freezes their disjoint write sets and helper interface; the
[fixture-only amendment](https://github.com/xcosmosbox/Cairn/issues/110#issuecomment-6417842933)
covers two affected pipeline test verdicts. Review/CI/acceptance remain separate events.

## Implementation boundary

`annotation.DocumentProcessor.Process(ctx, skill, path, original)` is the shared
full/changed-document helper. `DocumentResult` records original skill/path, the last
candidate/document digest, actual ordered annotate/assess/revise attempts, concrete
reason, exhaustion and `complete` / `partial` / `unavailable` / `cancelled` outcome.
Only a complete document is eligible for downstream acceptance; an incomplete last
candidate is trace evidence. No shared aggregate schema, extraction/storage or
production orchestrator changes are included.

`MaxRetries` is a shared **extra recovery** allowance for the whole document:
positive N allows N recoveries, zero uses default 3, negative disables recovery.
Initial generation and assessment are actual attempts but consume no recovery.
Malformed, explicitly unknown or unavailable assessment retries that exact candidate; a genuine omission
or unsupported claim revises the offending document from the original, whole candidate
and concrete feedback, followed by whole-candidate assessment. A semantic recovery
can generate and assess, bounding logical model calls by `2 + 2N`; an assessment-only
retry uses one call. Processor primitives do not nest standalone wrapper retry loops.
Provider HTTP retries remain their existing separate transport behavior; these are
logical helper-call bounds, not a wire-request measurement or restored stacked loop.

Assessment requires an explicit support boolean (legacy `idempotent` accepted only
with consistent explicit fields), reason and non-null `unsupported_items`/`omissions`
arrays. Malformed, null, contradictory or out-of-range feedback cannot establish a
semantic rejection. Unsupported additions and omitted substantive meaning cannot
pass. An explicit verifier inability uses valid JSON `{"status":"unknown","reason":"concrete inability"}`
with no correctness fields. Its attempt outcome is `unknown`, retaining the reason;
exhaustion is document-unavailable. This differs from invalid/malformed feedback
and transport-unavailable attempts, and never invents semantic critique. Missing or
null support remains invalid rather than an implicit unknown declaration.
Invalid/filtered rows and truncated output are explicit failed work. A nonempty
source with empty items requires full-source assessment; only explicit no-substantive-
omission feedback can complete it. Legitimate empty originals remain distinct. Cancellation and
known permanent source/configuration errors terminate with a reason.

The incremental adapter feeds only C4 outside-block text to the same processor,
retains ordered per-document results, and passes only complete nonempty candidates
to the existing downstream interchange. Unresolved scopes remain skipped, keeping
the existing prose/baseline protection contract. Successful siblings do not rerun.
The adapter reports factual document outcomes; it does not invent final product
aggregation. [#114](https://github.com/xcosmosbox/Cairn/issues/114) still owns ordinary
full-entry wiring, aggregate status and persistence-level C4/baseline acceptance.

## Finite acceptance evidence

The immutable [v0.1.1 handoff](../../../build/test/e2e/semantic-evidence/README.md)
retains all six complete originals: 51,183 bytes, 360 substantive units, 16 reasoned
exclusions and 194 explicit relation contexts. The eight substantive synthetic
originals, two empty markers and 19 declared cases are unchanged. Expected inventories,
case instructions and gold never enter builder prompts. No paid calls or new private
original annotation run is required or claimed by these deterministic tests.

The [removed-row review](../../../build/test/e2e/semantic-evidence/v012_removed_rows.md)
compares all three deleted historical rows with full original paragraphs and attached
inventory meaning. Each combines supported substance with an invented role or billing
interval: finite revision must remove those additions and retain the complete supported
topic, qualifications and attribution. Public invented equivalents exercise that
repair contract. The historical packet is immutable; its failed S02 assessment still
does not establish factual falsehood or exhausted revision.

| Required behavior | Concrete evidence |
| --- | --- |
| Historical 29-item/index29 failure retries the same candidate | `TestV012AcceptanceHistorical29ItemAssessment`: one generation + two identical assessments, 29 facts retained |
| Repair cannot erase previously covered meaning | `TestV012AcceptanceRevisionRechecksPreviouslyCoveredFact`: prohibition repair loses threshold, next whole-candidate check catches it, final candidate retains both |
| False detail does not erase true row substance | `TestV012AcceptanceMixedTruthRowsAreRevisedNotDeleted`: role and billing-interval equivalents use original/candidate/critique and whole replacement assessment |
| Qualifiers, dates, uncertainty, disagreement, same-name items and UUID source text survive helper processing | `TestV012AnnotationSupplementSourceBoundaryAndPreservation`: unchanged F01–F08, explicit source/path identity and exact retained text, distinct F02 rows sharing a name; fixture expectations absent from prompts |
| Malformed/null/conflicting verifier retries the same candidate | `TestDocumentProcessorInvalidVerdictReassessesExactCandidate`: 18 invalid variants including duplicate fields, null entries, contradictory flags and indices |
| Explicit unknown retains its reason separately from malformed and transport feedback | `TestDocumentProcessorDeclaredUnknownAssessment`: unknown-to-known recovery, unknown exhaustion, 11 invalid/conflicting declarations; no semantic rewrite |
| Invalid rows cannot disappear through filtering | `TestDocumentProcessorMalformedRowsAreNotFiltered`: bad row rejects the candidate, no supported-subset completion |
| Nonempty source/empty items requires truthful coverage assessment | `TestDocumentProcessorEmptySourceAndCandidateRequireTruthfulCoverage`: actual omission repairs; explicitly no-substantive content may complete after assessment; empty source has no calls |
| Truncation/service unavailability differ from correctness feedback; permanent errors stop | `TestDocumentProcessorTruncationAndProviderTimeoutRetryOnlyFailedStep`, `TestDocumentProcessorServiceErrorBodyCannotOverrideStatus`, `TestDocumentProcessorCancellationAndPermanentFailuresStop` |
| Shared finite exhaustion/default/disabled semantics | `TestDocumentProcessorSharedFiniteAllowanceAndExhaustion`, `TestStandaloneGateAndAnnotatorDefaultRecoverySemantics` |
| Three-document success / repair / exhaustion waits terminal states without rerunning success | `TestV012C4ThreeDocumentRepairExhaustionAndJoin`; final batch product conclusion remains #114 |
| Cancellation preserves completed siblings; human blocks remain outside repair input; unresolved zero/unavailable scopes skipped | `TestV012C4CancellationRetainsCompletedSibling`, `TestV012C4HumanBlocksExcludedFromRepair`, `TestV012C4UnavailableAndZeroItemsRemainSkipped` |
| C4 and full helper use the same allowance | `TestV012C4RecoveryAllowanceMatchesSharedProcessor` |

Observed deterministic attempt outcomes are retained as `DocumentResult.Attempts`
and asserted against actual calls, rather than inferred from configured budgets:

| Scenario | Actual work and terminal reason |
| --- | --- |
| Historical index29 recovery | annotate → invalid assess → same-candidate complete assess; 3 calls, complete |
| Explicit unknown recovery / exhaustion | annotate → unknown assess → same-candidate assessment; 3 calls. Known final feedback completes; second unknown remains unavailable/exhausted with both concrete inability reasons in attempt history |
| Repair erases earlier threshold | annotate → omission assess → revise → new omission assess → revise → complete assess; 6 calls, both facts retained |
| Each mixed-support row equivalent | annotate → unsupported-claim assess → revise → complete assess; 4 calls, complete |
| Three C4 documents | Immediate success 2 calls; repaired sibling 4; exhausted sibling 4 with semantic omission reason and `Exhausted=true`; all three terminal results retained, two accepted documents, one unresolved skip |
| Invalid assessment uses sole recovery, next verdict finds omission | 3 calls, partial/exhausted with missing-second-fact feedback; no new nested revision allowance |
| C4 caller cancellation | Completed sibling remains complete after 2 calls; cancelled sibling has its single attempted generation and caller cancellation reason; no dependent assessment |
| Service generation unavailable with negative/1/default allowance | 1/2/4 calls respectively; unavailable retains concrete reason, never factual feedback |

The deterministic source-preservation fixtures control model responses; they establish
helper routing and retention under those responses, not general model semantic judgment,
new annotations of all 360 private units, complete source-to-graph construction, repeat
stability, utility, full609 capacity or release readiness. Node identity/mapping, relations,
persistence and integrated original acceptance retain #111–#114 ownership. Forced failure
robustness is separate from normal semantic coverage.

## Actual validation and recovery

Read-only continuity checks ran with exit 0 on the exact accepted source baseline:

- `python3 build/test/e2e/semantic-evidence/inventory_span_check.py --inventory <authorized>/inventory.json --sources <authorized>/sources --summary build/test/e2e/semantic-evidence/testdata/v011/inventory_summary.json`: frozen inventory SHA256 `f0bfc3a6b8d802f3b15fe7c7e2f6db2e8c09ea4feb7564b241b76d42f78e44d8`; all six source hashes/spans/public totals match. This is structural continuity, with the new three-row semantic review described above.
- `python3 build/test/e2e/semantic-evidence/v011_reconcile.py <authorized-diagnostic105>`: all 23 archive entries match fixed archive SHA256 `cdf5f96ae41bee23ff16eba3e690bf392606691e5f0acf22de3d05c501c86921`; unchanged 205 initial rows, three removals across accepted five, S02 malformed assessment, and downstream 172-to-171 incidence boundary.

Combined core/adapter and acceptance checks ran with Go 1.22.12 linux/amd64,
`GOTOOLCHAIN=local`, on local integration production checkpoint `45a3d19`, including
the final source-reconstruction assertion and explicit-unknown correction. The final
test-only followup `1e60b2b` integrated as `23e06a1` additionally verifies that a valid
whitespace source with a non-nil never-called client reports complete/zero-items while
the C4 adapter still skips it and preserves unresolved pruning protection;
`go test ./build/internal/incremental -run TestV012C4UnavailableAndZeroItemsRemainSkipped -count=1`
then ran with exit 0. Production code was unchanged by that fixture tightening:

| Command | Result |
| --- | --- |
| `go test ./build/internal/annotation ./build/internal/incremental ./build/internal/pipeline ./build/internal/writeback ./build/test/e2e/semantic-evidence -count=1` | exit 0 |
| `go vet ./build/internal/annotation ./build/internal/incremental ./build/internal/pipeline ./build/internal/writeback ./build/test/e2e/semantic-evidence` | exit 0 |
| `go test -race ./build/internal/annotation ./build/internal/incremental -run 'TestDocumentProcessor\|TestStandaloneGateAndAnnotator\|TestV012\|TestAnnotationDisabledRecoveryAssessesOnce\|TestC4SingleAnnotationKeepsHumanBlocksOutsideSource' -count=1` | exit 0 |
| `go build -buildvcs=false ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental` | exit 0 |
| `python3 -m unittest discover -s build/test/e2e/semantic-evidence -p '*_test.py'` | 10 tests, exit 0 |
| `python3 -m unittest discover -s .github/scripts -p 'test_*.py'` | 12 tests, exit 0 |
| `git diff --check`; targeted `gofmt -l` | exit 0; no formatting output |

The local build uses the already diagnosed linked-worktree VCS-stamping workaround
`-buildvcs=false`; no source change addresses that environmental behavior. No default
stamping failure was rerun or claimed in this session. Normal CI remains enabled and
uses its ordinary checkout. Separate review and normal exact-head CI are pending at
this checkpoint. Core adversarial regressions and the provider-status classification
correction are included: a 503 response body mentioning an API key or max_tokens must
remain retryable service unavailability, rather than override its status as a local
configuration error. Local recoverable branch:
`work/v012-evidence-20261026`; core `7ab0ec4` integrated as `f8fbbc1`, adapter `45d57dc`
as `abe258a`, core followups `4cd6aed` as `7b6ebed` and `19ec6ec` as `45a3d19`, initial acceptance commit `8534915`.
Publication must retain this explicit
file scope and match the tested tree; no generic history push or historical cache upgrade.
