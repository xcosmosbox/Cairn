# ADR 0007: Retired bounded experimental construction design

Status: superseded by the accepted [#105 simplification](https://github.com/xcosmosbox/Cairn/issues/105#issuecomment-6406294894),
2026-10-26. The old checkpoint importer, staged mapping/history/counter bridge and bounded
corpus-capacity executables were experimental preparation, not production capacity or
answer-utility evidence. Their historical source, plans and raw result packets remain in git.

The abandoned design imposed complete-roster acceptance, per-record output/description
microbudgets, confidence .7 forecasts, all-pair/leaf completion, counter-equality receipts,
replay authorization, recovery and full609 publication admission. Those requirements do
not precede a small experiment and their inactive executable framework is retired.

Use ordinary production ingest into a fresh isolated DB, with originals only and --no-evolve
when history is unnecessary. Preserve actual alias/parent/source/member ownership, finite
numeric values, human edits, valid selected graph consistency, isolated materialization and
atomic publication. Missing output is a local unavailable outcome, never an empty replacement
that deletes prior data. Preserve raw requests/responses/errors and complete intended counts.
Unknown usage and upload failure are warnings. The #105 implementation removed semantic
repair/retry; this is baseline history, not the current v0.1 acceptance direction.

Current applicability (2026-10-26): [#107](https://github.com/xcosmosbox/Cairn/issues/107)
and the [v0.1 plan](../coordination/reports/2026-10-26-v01-build-completeness-plan.md)
require finite failed-step retry/semantic revision and substantive coverage in ordinary
production construction. Their implementation is pending. This does not restore the
retired bounded/staged/history/counter/importer framework or accept lossy construction
merely because retained items are valid.

Existing product incremental writeback/ownership safeguards and optional evolution features
remain independent of the retired harness. No public API/schema/Bundle/query migration is
required by retiring it. Use affected tests/builds and normal CI; no recovery or full609
rehearsal is an experiment gate. Rollback can recover old source from git without rewriting
historical results or treating an old method as a new measured success.
