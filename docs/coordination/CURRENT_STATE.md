# Current evidence snapshot

As of **2026-10-24 (Asia/Shanghai)**, inspected through GitHub.
Implementation baseline: `01968d608707730af458ff9c4e6b487c83ade1d3`.
This snapshot predates the collaboration PR; read current Issues/PRs for later updates.

## Verified repository facts

- PRs #1–#6 are merged. Main's last implementation merge was 2026-10-05.
- [Correctness run 37264544835](https://github.com/xcosmosbox/Cairn/actions/runs/37264544835)
  succeeded at that SHA. Existing CI covers Go race/build/vet/invariants, smoke-script
  regressions, Python evaluation regressions and Viewer subpath loading.
- PR #1 supplies F01–F18 correctness changes; #2 real build/publish/consume;
  #3 rank propagation/provenance; #4 literal queries and Chinese evaluation;
  #5 App token cache/protocol fixes; #6 source-backfill evaluation.
- The public round3 README states that the pilot `run` command is disabled;
  authorization-stop and judge durability require reviewed handling first.

## Reported historical results, not rerun here

PR #2 reports a 69-document real build and online publication/consumption using
Actions credentials; it does not establish production App JWT/refresh integration.
PR #3/#4 report very low strict grounded pass rates and no established GraphRAG advantage.
PR #6 is an evaluation adapter and does not promote source hydration into production.
See EVIDENCE.md for exact links, scope and access limits. Do not transfer a measured
result to a different source/data/protocol snapshot.

## Open work

- #10: fresh environment/free end-to-end recovery is unverified. The earlier chat
  request and old CI are not evidence of a current restored cloud environment.
- #11: audit full private archives and remote hashes; directory existence alone is
  insufficient to claim a complete or reproducible evidence bundle.
- #12 → #13: repair runner offline, then independent review before a pilot decision.
- #14: investigate Chinese recall, summary fact loss and cross-document retrieval.
- #15: production App credentials/permissions/refresh still need dedicated integration.
- #16/#17/#18: public example, performance baseline and release acceptance planning.

No additional executing agent or cloud task was observed during initialization.
No paid experiments, production deployment or release were started by the coordination
setup. The latest live claims and checkpoints, not this sentence, identify later work.
