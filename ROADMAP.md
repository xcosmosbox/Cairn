# Cairn Roadmap

Live roadmap: [#8](https://github.com/xcosmosbox/Cairn/issues/8).
Task board: [#7](https://github.com/xcosmosbox/Cairn/issues/7).

Cairn turns scattered domain documents into a traceable, evolvable knowledge graph,
distributes immutable versions with GitOps, and serves agents through read-only MCP.
Success means reliable operation and measurable usefulness on new tasks.

| Stage | Outcome | Exit evidence | Initial tasks |
| --- | --- | --- | --- |
| M0 Reproducible development | A fresh agent can understand and restore the project | Free end-to-end report, accessible evidence index, public synthetic example | #10 #11 #16 |
| M1 Reliable operation | Correct identities, recovery, publication and consumption | Existing regressions retained; real App permissions/JWT/refresh verified | #15; historical #1 #2 #5 |
| M2 Useful retrieval | Diagnose retrieval/summary loss and test bounded improvements | Fair declared resources; source/gold isolation; honest paired results | #12 #13 #14 |
| M3 Community release | Operable, compatible and measurable distribution | Resource baseline, release/rollback matrix and evidence-based release decision | #17 #18 |

Stages describe evidence for their claims, not a mandatory execution ladder.
Research, restoration, audit and measurement may proceed independently; production
promotion follows the relevant dependencies. Negative results can justify redesign,
simplification or stopping a candidate. More graph structure is not itself a benefit.

Task Issues own status and acceptance criteria. This document owns the stable direction;
avoid maintaining duplicate status tables here. New work should include a falsifiable
goal, module boundaries, dependencies, compatibility impact and an acceptance test.

## 2026-10-24 maintainer direction: Alpha toward first stable release

The goal is the first stable release; no version number is assigned and stable readiness
has not been established. Keep the M0–M3 outcomes above. The release decision needs reproducible engineering,
comparable quality and critical product-path evidence. Small exploratory experiments
proceed independently after affected checks; archival/all609/recovery/fullsuite work
and repeated technical approvals are not prerequisites. #105 owns the current
finite simplification and fixed support-enriched DEV diagnostic. CI alone does not
establish a release or benefit.

Research should first assess mature benchmarks and reusable environments, then justify
small supplements for Cairn-specific gaps. [Quality benchmark task #21](https://github.com/xcosmosbox/Cairn/issues/21)
tracks the adopted public multihop thin adapter separately from #17's performance work;
its live protocol and acceptance remain in that task. See the [execution policy](docs/coordination/EXECUTION_POLICY.md)
for the delegated decision/execution split and model defaults, and [#8](https://github.com/xcosmosbox/Cairn/issues/8#issuecomment-6382509063)
for the recorded maintainer decision. This addition preserves the earlier roadmap history.
