# Reusable records / 可复制记录

## CLAIM / CLAIM-ACK

```text
Event: CLAIM
Time (UTC):
Task: #
Agent/runtime:
Session: unique-stable-id
GitHub account:
Base SHA:
Branch / worktree:
Write scope (files/contracts/inputs):
Dependencies checked:
Lease until (UTC, <=24h):
First verifiable result:
```

Coordinator reply: `CLAIM-ACK <session> until <UTC>; scope <...>; conflicts checked <...>`.
Set `status:active` after acknowledgement. Renewal is a new checkpoint with an explicit
lease-until; takeover names the prior session and coordinator confirmation.

## CHECKPOINT / BLOCKED / HANDOFF

```text
Event:
Time (UTC):
Task / session:
Base SHA / result SHA:
Branch / PR:
Completed (with evidence):
In progress:
Blocked (reason, dependency, unblocking action):
Unverified / not run:
Commands, exit codes and log links:
Artifacts (immutable location, bytes, SHA256, access):
Uncommitted/local-only work:
Recovery commands / next step:
Lease until or explicit RELEASE:
```

## REVIEW / ACCEPTED

```text
Task / PR / reviewed head SHA:
Reviewer session and independence limits:
Behavior and failure cases inspected:
Checks run (command, exit, evidence):
Findings by severity:
Compatibility / rollback / experiment identity:
Decision: accept / request changes / unknown
Remaining limitations and follow-up Issues:
```

## Project report (#9)

Use completed / in progress / blocked / unverified; cite task numbers, exact commits,
CI and artifact receipts. Include as-of time, author/session and next owners. For blocked
work show last evidenced update and actionable unblocking step. Do not call an expired
claim active or a pending PR completed. A report may say no new evidence was available.

## Start an unfamiliar agent

> Work on xcosmosbox/Cairn. Read AGENTS.md, docs/coordination/README.md and live
> hub #7, roadmap #8, report index #9. Read CURRENT_STATE.md/EVIDENCE.md as dated
> evidence, then refresh current Issues/PRs. Select one ready task with satisfied
> dependencies and a nonoverlapping write scope. Post a unique session CLAIM and
> wait for coordinator acknowledgement before overlapping writes. Use an isolated
> branch/worktree, follow CONTRIBUTING, and leave checkpoints and a bounded PR/report
> with exact source/test/artifact evidence. Preserve paid history and frozen identities.
> If access is missing, record blocked/unknown and continue independent free work.

## Coordinator dispatch

Read live claims/dependencies/PRs; choose disjoint scopes; send each worker the repository,
task number and session ID. Confirm leases in the task Issues. Request independent review
for high-risk changes, inspect actual CI/artifacts, accept or return findings, update labels
and #9. Starting workers depends on the invoking environment's capabilities and explicit
authorization; this repository protocol itself starts no sessions.
