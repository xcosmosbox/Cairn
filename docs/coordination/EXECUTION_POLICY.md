# Maintainer execution policy

Current direction: [#105](https://github.com/xcosmosbox/Cairn/issues/105#issuecomment-6406294894),
2026-10-26. The [accepted finite plan](reports/2026-10-26-simplification-plan.md)
replaces older experiment-admission instructions. Live ownership/results stay in Issues and PRs.

## Work and validation

Keep source/member/alias identity, human edits, atomic writes and query-side read-only,
zero-LLM, zero-embedding behavior. Preserve all intended experimental cases and actual
raw requests, responses and errors. Validate changed behavior with affected tests/builds;
normal CI remains enabled. Then execute the declared experiment. No local fullrace/verify,
full609, archive completion, recovery/Viewer rehearsal, counter equality, repeated technical
GO or cost/time ceiling is a universal prerequisite for a small real experiment.
Missing usage/fees are unknown, never fabricated zero. Missing descriptive metadata or
failed upload is a warning. Malformed model output and source contamination fail their
actual dependent item/arm/graph scope; independent cases continue. A possibly committed
state mutation is not blindly reapplied. Retain negative or inconclusive results without
automatically tuning or repeating the experiment.

Research selected one 38-row simplification plan; fresh executors implement it and run
one fixed small DEV support-enriched diagnostic. No further governance/recovery framework
or recursive preparation phase is requested. Release publication is a separate decision.

## Reuse and fair evaluation

Reuse mature benchmarks and existing production adapters. Declare selected source bytes,
DEV roster, paired resources and metrics before outcomes. Originals go to the builder;
qid/question to the solver; gold to the judge. HOLDOUT stays closed unless separately
requested. Support-enriched samples measure exploratory given-source utility, not full-
corpus retrieval or generalization. Larger studies and release claims need their own evidence.

## Standing authorization

The maintainer permanently authorizes ordinary paid DeepSeek API use and network access
for Cairn at official `https://api.deepseek.com` with `deepseek-flash`, including authorized
public originals, machine annotations and model-derived critiques/feedback. Scoped public
code/sanitized reports may be published to `xcosmosbox/Cairn`; restricted raw evidence may
be archived to private `xcosmosbox/kd_manifeat`. Do not ask again for these permissions.
[Original authorization](https://github.com/xcosmosbox/Cairn/issues/8#issuecomment-6384918656)
and [data-egress confirmation](https://github.com/xcosmosbox/Cairn/issues/8#issuecomment-6396678380).
Credentials and unrelated private data are excluded. Actual platform safeguards/rejections
remain effective; do not route around a rejection or rewrite an unknown historical outcome.

## Collaboration and release

Use bounded claims, isolated branches, exact commits and truthful checkpoints under
[PROTOCOL.md](PROTOCOL.md). Review the actual changed high-risk behavior once, disclosing
shared-account/model limits. Existing authorization needs no new source/preflight/live
approval. Do not infer a running job or accepted result from a label or plan.

The Alpha-to-stable goal and M0–M3 roadmap remain. Publishing a release still needs its
actual support/compatibility/recovery/resource evidence and maintainer decision; that
checklist does not precede an exploratory experiment. For explicitly delegated work,
research defaults are gpt-6-astra/xhigh and execution gpt-6.1-sol/high; outside contributors
need neither these models nor private orchestration. This policy itself starts no agents.
