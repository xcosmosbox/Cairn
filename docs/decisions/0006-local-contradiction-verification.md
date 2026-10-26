# ADR 0006: Verify new full-build local contradictions from frozen originals

Status: proposed implementation under [#68](https://github.com/xcosmosbox/Cairn/issues/68);
root approved this bounded method, integration and actual validation remain separate.
Date: 2026-10-25. Baseline: `a1a016f92cd2883675ed54f9f6afcbefa4a823d4`.

## Current applicability — 2026-10-26

The incremental verification limitation below describes the tested historical/current
implementation boundary; it is not a permanent exception to v0.1. [#107](https://github.com/xcosmosbox/Cairn/issues/107)
and its [finite plan](../coordination/reports/2026-10-26-v01-build-completeness-plan.md)
assign explicit-relation/source-authority closure to v0.1.4 and integration/readback to
v0.1.6, preserving stable identity and human content. This plan does not claim those
changes are implemented or extend into the v0.5 daily lifecycle. Historical evidence and
source/quote truth checks remain; no cost/archive/full609/repeated-GO framework returns.

## Context and evidence

The sole actual [#63 calibration](https://github.com/xcosmosbox/Cairn/issues/63)
failed all12 document coverage: only11 originals were accepted and materialized.
The preserved [research checkpoint](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6386648735)
also independently identified two generated same-subdomain `contradicts` semantic
type errors (DB edges615/782). Buying refurbished hardware and avoiding an older
processor are compatible decisions; meditation relief and adverse effects under
different conditions are not strict same-context contradictions. This finding does
not adjudicate the other10 local contradiction edges or alter the failed run.

`ValidateCrossRelations` already verifies generated cross-subdomain candidates after
relation generation and before UUID assignment. Its old local-endpoint fast path
bypassed every source/semantic check for local `contradicts`. Endpoint resolution
establishes identity, not contradiction. The current source-quote verifier already
requires same subject, attribute, time, conditions and incompatible explicit claims.

## Options and decision

1. Leave local contradictions unverified: retains the observed bypass.
2. Verify every local relation kind: broadens model workload and method beyond the
   two diagnosed type errors; requires separate evidence and cost decisions.
3. Ban or automatically relabel contradictions: loses valid explicit contradictory
   claims or invents a different relation without source-supported adjudication.
4. Reuse the existing strict verifier for all new local `contradicts`: adopted.

The full-build candidate selector now includes every cross candidate and every new
local `contradicts` candidate, with cross contradictions processed once. It uses the
same complete, frozen endpoint-member original neighborhood, request-local aliases,
exact quote ownership/offset checks, five typed contradiction predicates,
cancellation handling and fail-closed dispositions. It neither truncates sources nor
substitutes descriptions, adds new relation kinds, relaxes gates or relabels a reject.
The extra prompt sentence makes local scope explicit while retaining the existing
stage-identification prefix. Extra local candidates incur additional build-side
verifier calls; actual request overflow fails that candidate locally. No model call or
capacity/quality improvement is established by the offline implementation/tests.

## Evidence format and compatibility

The legacy function, report field, stage and artifact names remain stable. New reports
record `validation_policy: cross-subdomain-and-local-contradicts/v2`; each decision
records `scope` and `kind` as well as the full candidate. The capacity auditor checks
scope/kind against the pre-verifier graph, enumerates the correct denominator, and
checks accepted outcomes against the validated graph and complete eligible final
candidate/DB projection. Rejected-only local identities cannot persist; independently
accepted duplicate support can still justify an identity. Counts by scope, kind and
disposition expose what was actually judged.

Historical missing-policy reports and explicit `cross-subdomain/v1` are read as
cross-only v1, labeled with the local-verification gap. They remain byte-for-byte
unchanged and cannot earn local-verification credit. Unknown policies fail closed.
Existing frozen profiles, rosters, offline v1/v2 runtime receipts and #63 packets
remain historical evidence; their older `v2` receipt filename is not this report's
validation-policy version. A future run records its actual source/runtime and method. There is no storage-schema, Bundle, query-side,
public CLI, retry-cap, concurrency, dependency or existing input/profile change.

## Scope and limitations

This entry point consumes new extraction results and frozen annotation inputs, not
existing stored human-curated edges. Existing edges are not retroactively rewritten
or relabeled by this change. The compatibility test exercises a no-op incremental
run over an already materialized human-curated local edge; it makes no claim about
all dirty-subdomain update scenarios.

Incremental I-7 `recomputeRelations` can generate new local relations using current
descriptions, but receives no authoritative frozen original snapshots or full accepted
document/member bindings. New contribution source rows alone do not recover immutable
originals for existing members: referenced Markdown may already have been rewritten.
Consequently incremental new-edge verification remains **uncovered**, and no added
source reread is permitted to impersonate original evidence. Integrating that path
requires a separately scoped source-authority/alias/ownership design and tests,
including preservation of existing and human-curated identities. This ADR does not
establish a general incremental preservation guarantee.

## Validation and rollback

Offline fixtures exercise an accepted same-context contradiction; compatible-dimension
and changed-condition rejections; incomplete predicates, invalid quotes/member ownership,
missing originals, actual request overflow and cancellation; other local kinds unchanged and
cross contradictions once. Production full-build integration checks actual SQLite
persistence/absence and frozen-source reuse after disk changes. Adversarial audit
fixtures cover missing pre/decision/post/final/DB evidence, scope/kind lies, rejected
identity retention and immutable historical interpretation. Semantic decisions remain
model-judged; mechanical tests prove enforcement, not model accuracy. Run affected checks/builds and preserve normal CI. #105 removes universal local
fullrace/verify, archive/counter and repeated live-admission prerequisites.

Rollback reverts this bounded code change and returns new builds to cross-only
verification; preserve all already emitted reports with their original policies.
Do not rewrite v2 evidence as v1 or rerun the failed calibration as routine validation.
No calibration/full609/QA/gold/HOLDOUT/provider/release execution is admitted here.
