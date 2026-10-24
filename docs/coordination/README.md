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

The maintainer's [execution policy](EXECUTION_POLICY.md) records the Alpha-to-stable
goal, model defaults and reuse-first evaluation approach. It supplements this entry;
live task assignment and acceptance still come from Issue/PR records.

## Initial parallel work

- #10 environment restoration: documentation/report only; defects become separate Issues.
- #11 archive audit: evidence index and audit report.
- #12 runner repair: exclusive write scope over evaluation solver/judge/ledger code.
- #14 retrieval research: analysis/ADR, read-only inspection of production code.
- #16 synthetic public example: new example assets; coordinate RESTORE.md sections with #10.
- #17 performance baseline: new benchmark harness; no optimization in this task.

#13 reviews #12 after its implementation is ready; #15 needs a dedicated App environment;
#18 plans release gates. Check live Issues before selecting work: this list describes
boundaries and does not assert that any session is running.

## Board automation and limitations

The `Coordination board` Action projects task labels into the marked region of #7.
It runs on task Issue/comment events and manual dispatch, using default-branch code.
It does not execute Issue text, call models, assign work, alter status labels or merge PRs.
The board is not a native GitHub Projects board and provides no atomic claim service.
If Actions is unavailable, use the live Issue search links in #7 and maintain claims
manually. Bot access only needs repository contents read and Issues write.

For future Projects integration, map its Status/Priority/Stage fields to the same Issue
semantics. A second view must not become a competing source of task truth.
