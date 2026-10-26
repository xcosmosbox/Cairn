# ADR 0005: Annotation revision history and current single-pass behavior

Status: historical revision method superseded by [#105](https://github.com/xcosmosbox/Cairn/issues/105)
on 2026-10-26. The failed #63 calibration remains failed: 11 of12 originals materialized.
The later typed-critique revision implementation was software evidence, not improved coverage.

The current production path makes one original-source annotation attempt, validates actual
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
