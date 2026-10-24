# Evidence register

Checked 2026-10-24. `verified` below is deliberately limited to the named observation.

| Evidence | Identity / link | What was checked | What it does not prove |
| --- | --- | --- | --- |
| Main implementation | `01968d608707730af458ff9c4e6b487c83ade1d3` | GitHub tree/ref and merged PR metadata | Current cloud environment health |
| Main CI | [run 37264544835](https://github.com/xcosmosbox/Cairn/actions/runs/37264544835) | completed / success at that SHA | Production readiness or online quality |
| F01–F18 | [PR #1](https://github.com/xcosmosbox/Cairn/pull/1), CORRECTNESS.md | Merged; fix and regression descriptions exist | Exhaustive correctness under every deployment |
| Real build/publish/consume | [PR #2](https://github.com/xcosmosbox/Cairn/pull/2) | Merged; historical run receipts linked in PR | New rerun here or real App JWT validation |
| Rank/provenance changes | [PR #3](https://github.com/xcosmosbox/Cairn/pull/3) | Merged; paid results and limitations reported | General answer-quality gain |
| Literal/Chinese experiments | [PR #4](https://github.com/xcosmosbox/Cairn/pull/4) | Merged; aggregate cohorts and incident limitations reported | Independent human grading or uninterrupted durable cohort |
| App local verification | [PR #5](https://github.com/xcosmosbox/Cairn/pull/5), core/githubapp/README.md | Merged; local signature/protocol coverage documented | Production App key, install scope, refresh or routing |
| Source backfill | [PR #6](https://github.com/xcosmosbox/Cairn/pull/6), e2e/quality/round3/README.md | Merged; adapter and disabled paid runner documented | Online accuracy or production source hydration |

## Restricted artifacts

Artifact repository: `xcosmosbox/kd_manifeat` (restricted access).
Observed branch `eval/cairn-quality-20261004` at
`181b2f6ef4b87a6558cbc8662e3218df164d68cc` contains these directory entries:

- `evaluations/cairn-nicedata-20261004/`
- `evaluations/cairn-retrieval-round2-20261004/`
- `evaluations/cairn-source-backfill-20261005/`

The directories were listed during setup. Full byte manifests, archive reconstruction,
hashes and completeness were **not** reverified; #11 owns that audit. Do not invent a
README path inside an archive directory: list its actual contents first.
Historical PRs link immutable earlier commits for their evidence. Use those identities
when assessing their exact claims; a newer branch head must not silently replace them.

An external contributor without access can work from public source, CI and synthetic
fixtures. Mark inaccessible raw evidence unknown and scope conclusions accordingly.
Public reports should explain the method, aggregate outcomes and limitations without
requiring raw private data to understand the claim.

## Add an evidence record

Record task, observation date, source/data/protocol identities, command and exit status,
result, immutable artifact URL/commit, size/hash where applicable, access level, reviewer
and limitations. Distinguish author report, observed remote result and fresh rerun.
For unknown usage or partial failure retain the denominator and explicit unknowns.
