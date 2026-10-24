# Cairn: start here / Agent 接手入口

This file applies to the entire repository. Any human or agent may contribute;
no private chat, particular model, account or orchestrator is required.

1. Read [coordination](docs/coordination/README.md), [current evidence](docs/coordination/CURRENT_STATE.md),
   [architecture constraints](CONTRIBUTING.md) and the live [hub](https://github.com/xcosmosbox/Cairn/issues/7).
2. Read the task Issue and its latest comments, dependencies and linked PRs.
   Refresh `main` and record its SHA; historical snapshots are not current runtime proof.
3. Follow [claim and handoff protocol](docs/coordination/PROTOCOL.md). Declare a unique
   session, branch, write scope and expiring lease. An assignee alone is insufficient.
   Claims are advisory, not an atomic distributed lock. Without acknowledgement,
   isolated branch/fork work is allowed under the provisional rules in the protocol.
4. Use one branch/worktree per task. Do not overwrite another agent's branch,
   force-push shared history, modify shared experiment inputs or silently broaden scope.
5. Preserve query-side read-only, zero-LLM, zero-embedding behavior, immutable Bundle
   identity, source provenance and human-curated edits. Propose architecture changes via ADR.
6. Report evidence at recoverable checkpoints and before stopping. Link exact commits,
   commands with exit status, tests, artifacts and remaining work in the Issue and PR.
   An unpushed local result or chat claim is not a handoff.
7. Follow CONTRIBUTING's required gates for code changes. Documentation-only changes
   need link/template validation; automation changes need their focused tests and CI.
   Report blocked checks explicitly. Do not rerun completed paid experiments as routine validation.
8. Public reports must be understandable without private artifacts. Restricted data stays
   in its authorized destination; missing access is `unknown`, not `pass` or `absent`.

The collaboration system records work; it does not start agents, grant credentials,
approve model spending, or automatically accept/merge their work. Existing explicit
authorization continues to apply within its scope. No approval is needed merely to read,
research, reproduce free local checks, or prepare an isolated fix and PR.
