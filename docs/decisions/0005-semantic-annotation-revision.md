# ADR 0005: Annotation revision history and v0.1 fidelity contract

Status: current v0.1 product contract adopted through [#107](https://github.com/xcosmosbox/Cairn/issues/107)
on 2026-10-26; implementation remains pending in its finite child tasks. #105 earlier
superseded the historical revision method and shipped single-pass behavior. The failed #63 calibration remains failed: 11 of12 originals materialized.
The later typed-critique revision implementation was software evidence, not improved coverage.

At the PR106 baseline, the production path makes one original-source annotation attempt, validates actual
source/alias/schema properties, and keeps valid independent material. It does not perform
semantic-idempotence Gate B, losslessness rewrite, feedback repair or repeated annotation
rollbacks. An omitted description or failed document is counted locally, without erasing
successful siblings. Original UUID-form literals are source text; machine identities still
use request-local aliases and stable owned UUIDs. Human-curated edits remain protected.

Earlier method details and test receipts remain in git history and #64/PR72. They cannot be
relabelled as results of this simplified method. New evidence records the actual source and
raw outcomes. Run affected annotation/incremental checks; fullrace, full609, counter receipts,
archival reconstruction and repeated live GO are not prerequisites for a small experiment.

No schema, immutable Bundle or query-side write/LLM behavior is introduced. Rollback reverts
the implementation commits while preserving historical inputs/results and human data.

## Current v0.1 applicability

The [v0.1 plan](../coordination/reports/2026-10-26-v01-build-completeness-plan.md)
supersedes single-pass/no-semantic-revision and omission-only-success as product goals.
Finite configured retries target a failed step; malformed assessment retries verification
of the same candidate, while source-semantic defects revise the offending document with
originals/candidate/feedback and recheck the whole candidate. Successful siblings remain;
exhaustion is explicit partial/unavailable, cancellation/permanent errors have reasons.
Meaningful omissions or distortions remain unaccepted; empty output is not proof that a
nonempty original contains no knowledge. HTTP retries do not replace semantic revision.
This documents the new intended behavior, not its completion. #63/#64/#105 results and
the removal of peripheral archive/cost/time/full609/repeated-GO gates remain unchanged.
