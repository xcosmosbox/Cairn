# Cairn collaboration / 协作入口

Start with the live [hub and board #7](https://github.com/xcosmosbox/Cairn/issues/7),
[roadmap #8](https://github.com/xcosmosbox/Cairn/issues/8) and
[report index #9](https://github.com/xcosmosbox/Cairn/issues/9).
These are shared project records for any contributor, not a transcript of one conversation.

| Need | Authoritative record |
| --- | --- |
| Understand purpose and modules | Root README and CONTRIBUTING |
| Understand direction | ROADMAP.md and #8; substantial changes have an ADR |
| Learn known facts and limitations | CURRENT_STATE.md and EVIDENCE.md, each pinned and dated |
| Find and claim work | Task Issue (`coord:task`), latest comments, status labels |
| Coordinate concurrent edits | PROTOCOL.md; session, lease, write scope, dependencies |
| Review implementation | PR diff, CI, review threads, exact head SHA |
| Recover unfinished work | Latest task checkpoint, branch/commit, restore commands and artifacts |
| Follow overall progress | #9 reports linking task evidence; #7 is a projection |

Read [PROTOCOL.md](PROTOCOL.md), [RESTORE.md](RESTORE.md),
[TEMPLATES.md](TEMPLATES.md) and [DECISIONS.md](DECISIONS.md).
English identifiers stay stable; Chinese explanations are welcome. Reports may be bilingual.

The maintainer's [execution policy](EXECUTION_POLICY.md) records the v0.1–v0.6→v1
direction, model defaults and reuse-first evaluation approach. It supplements this entry;
live task assignment and acceptance still come from Issue/PR records.

## Current execution

[#107](https://github.com/xcosmosbox/Cairn/issues/107) publishes the finite
[v0.1 construction plan](reports/2026-10-26-v01-build-completeness-plan.md):
[#108](https://github.com/xcosmosbox/Cairn/issues/108) and six bounded children. All product implementation remains unaccepted
at plan publication. Start with the actual ready Issue and latest dependency/claim records.
Source-first semantic coverage, finite failed-step retry/revision, provenance and repeat
stability are required; successful partial handling does not satisfy normal full coverage.
#14 is later v0.2/v0.4 backlog, not current dispatch.

#105's simplification and first real diagnostic are completed historical evidence;
its one-shot/omission-as-warning product direction is superseded by #107. Its deletion of
unnecessary gates remains. #11/#13/#99/#103 are closed superseded work, not accepted
successful experiments. No label implies a running job or a completed result.

Use minimal affected checks and actual raw outcomes. Normal CI remains; small experiments
need no full609/recovery/archive admission or repeated technical GO. Release publication
has its own evidence and decision. Current snapshots belong in CURRENT_STATE.md.

## Board automation and limitations

The `Coordination board` Action projects task labels into the marked region of #7.
It runs on task Issue/comment events and manual dispatch, using default-branch code.
It does not execute Issue text, call models, assign work, alter status labels or merge PRs.
The board is not a native GitHub Projects board and provides no atomic claim service.
If Actions is unavailable, use the live Issue search links in #7 and maintain claims
manually. Bot access only needs repository contents read and Issues write.

For future Projects integration, map its Status/Priority/Stage fields to the same Issue
semantics. A second view must not become a competing source of task truth.
