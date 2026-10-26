# Concurrent work and handoff protocol

## Task lifecycle

Each task has `coord:task`, exactly one `status:*`, `priority:P0/P1/P2` and `phase:M0/M1/M2/M3`.

| Status | Meaning | Evidence to leave |
| --- | --- | --- |
| backlog | Idea or task not yet scoped/scheduled | Goal, unknowns, next decision |
| ready | Bounded, dependencies satisfied, available | Acceptance criteria and write scope |
| active | Confirmed current session working | Claim/lease, branch, checkpoint |
| review | Deliverable ready, acceptance pending | PR or report, exact SHA and checks |
| blocked | Cannot proceed on the next dependent step | Reason, dependency/owner, unblocking action |
| done | Accepted and integrated | Merged PR or accepted report, tests and residual limits |

Closed Issues without both `status:done` and a completed resolution are shown separately,
not silently counted as done. Cancelled/duplicate work records its reason and successor.
Priority: P0 immediate correctness/data-loss incident; P1 blocks a stage; P2 improvement.
No new P0 is presumed merely because a task is unverified.

## Claim, heartbeat and concurrent sessions

1. Read latest task comments, PRs and linked dependency Issues. Check active claims
   across related tasks for intersecting files, schema, fixture or freeze identities.
2. Post a `CLAIM` using TEMPLATES.md with agent/runtime, unique session ID, base SHA,
   branch, intended write set, dependency check and UTC lease-until (at most 24h).
3. A maintainer or designated coordinator confirms `CLAIM-ACK`, registers the session
   in the task body/claim comment and sets `status:active`. Confirm only one writer
   for an overlapping scope; read back the acknowledgement before starting writes.
   Independent read-only analysis may start while a claim is pending.
   If no coordinator is available, a contributor may proceed provisionally in an
   isolated branch/fork after posting and re-reading the claim, provided no conflicting
   current claim exists. Mark it `provisional`; do not claim exclusive ownership,
   edit shared artifacts or merge. Resolve competing proposals in PR review. This
   exception lets an unfamiliar external agent contribute without a live dispatcher.
4. Assignees represent GitHub accounts; session IDs represent actual workers.
   Even if all agents use one account, include the session ID in every record.
5. Publish heartbeat/checkpoint at least every two hours while executing, after a
   recoverable milestone, and before interruption. Renew explicitly, at most 24h
   forward. A lease does not imply an agent runs continuously until it expires.
6. Before takeover, re-read the latest heartbeat/PR head, post a takeover request,
   and obtain coordinator confirmation. Expiry makes a task eligible for reassessment,
   not permission to overwrite work. Preserve the predecessor branch/commits.
7. Split shared files/contracts into an integration task or serialize their writers.
   Reviewers use a separate branch/session and report findings rather than editing
   the author's working tree. Do not auto-spawn other agents from a task claim.

This is a cooperative, advisory lease protocol. GitHub comments/labels do not offer
an atomic lock. If two acknowledgements conflict, stop overlapping writes and let a
coordinator choose a canonical session. Do not resolve a race by last-writer-wins.
Agents without write permissions can fork, propose a claim in comments, submit a PR
and ask a maintainer to mirror metadata; they need no access to private chat history.

## Checkpoints and handoff

Use append-only comments for CLAIM, CLAIM-ACK, CHECKPOINT, BLOCKED, HANDOFF, REVIEW,
ACCEPTED and RELEASE. Correct an erroneous record with an explicit amendment.
The latest confirmed lease identifies the writer; labels identify the current stage;
task body fields at creation are a snapshot, not a replacement for later events.

Every checkpoint contains timestamp, session, task, base/result SHA, branch/PR,
completed/in-progress/blocked/unverified, commands + exit codes, artifact identifiers,
remaining diff and next command. Before stopping, push the recovery commit where
possible. If only local changes remain, say they are unavailable to another agent.
Report the environment block; do not imply a background job exists without its ID.

Preserve the selected raw inputs, requests, responses and errors privately; public
reports contain sanitized methods/results. Upload is best effort and is not an
experiment admission condition. Keep old evidence packets unchanged.

## Review, acceptance and decisions

One bounded Issue per PR; link `Refs #N` while work is incomplete and `Closes #N`
only if the entire task is satisfied. PRs describe changed behavior, compatibility,
tests with exact head SHA, negative results, known limits and rollback.
Before integration refresh dependencies and base; do not silently include unrelated work.

Implementation/review/acceptance are different events. Another session reviews high-risk
changes (credentials, provenance, recovery, ledger and experiment semantics). Disclose
shared account/model or other independence limits. No agent approves its own work as
independent evidence. Maintainers accept after relevant CI/review; code tasks reach
done after merge, report-only tasks after an explicit acceptance comment.

No benefit claims from build success, fake mode or offline fact support alone.
Do not rerun paid controls, alter gold/holdout, or relax architecture checks to produce
a pass. Existing authorization remains usable; a new task record is not itself spending
authority. Existing authorized experiments proceed after minimal affected checks. Record the
selected inputs/resources and actual outcomes; do not add source/preflight/live GO,
cost/counter/time, archive or full609/recovery prerequisites. Missing usage stays
unknown, individual failures remain in full denominators, and independent work continues.

Record intentional contract/experimental-method changes in the relevant existing
design document and task decision with compatibility and evidence. #105's finite
simplification is completed history. Current #107/v0.1 replaces its one-shot/no-semantic-
revision and omission-as-warning product direction, retaining necessary finite semantic
correction without restoring removed peripheral gates. The six-child plan uses this
existing protocol, not a new ADR/governance series.

## Keeping records fresh

Task owners update labels after transitions; coordinators reconcile conflicting labels,
expired leases and unresolved dependencies during dispatch and project reports.
Each report in #9 has four evidence-based sections: completed, in progress, blocked,
unverified. Show time since the last evidenced update for blocked work; never infer
progress from elapsed time. Update CURRENT_STATE.md only when its evidence changes,
and retain the as-of date and exact baseline. The board is a generated view of Issues.
