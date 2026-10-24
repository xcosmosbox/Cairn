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
| M2 Useful retrieval | Diagnose retrieval/summary loss and test bounded improvements | Reviewed runner durability; fixed protocols; honest paired results on new tasks | #12 #13 #14 |
| M3 Community release | Operable, compatible and measurable distribution | Resource baseline, release/rollback matrix and evidence-based release decision | #17 #18 |

Stages are evidence gates, not promised dates or blanket claims of completion.
Research, restoration, audit and measurement may proceed independently; production
promotion follows the relevant dependencies. Negative results can justify redesign,
simplification or stopping a candidate. More graph structure is not itself a benefit.

Task Issues own status and acceptance criteria. This document owns the stable direction;
avoid maintaining duplicate status tables here. New work should include a falsifiable
goal, module boundaries, dependencies, compatibility impact and an acceptance test.
