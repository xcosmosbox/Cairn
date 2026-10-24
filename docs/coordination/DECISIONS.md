# Decision log

## ADR-0001: repository-backed, tool-neutral collaboration (2026-10-24)

Status: proposed for acceptance with the collaboration PR.

Context: implementation and experiment history exists in merged PRs, while new work
has no Issue queue and private conversations are not a durable handoff mechanism.

Decision: stable directions and protocols live in versioned docs; task state and execution
events live in Issues; review and integration live in PRs; artifacts use immutable identities.
Issue #7 is an automatically refreshed view of task labels. A native Projects board may
later mirror this model. Session-specific advisory leases with coordinator acknowledgement
address shared-account agents without claiming atomic locking.

Alternatives: a private chat-only plan is inaccessible to new contributors; a standalone
task database creates an additional service and competing state; Projects-only governance
depends on a capability not exposed by the current connector.

Consequences: any human/agent can onboard from the repo, but owners must maintain labels
and checkpoints. The automation updates a board, not workers or approvals. Expired leases
need reconciliation. Restricted evidence remains restricted with clear access limitations.

Validation: focused board rendering/API/idempotence tests, repository templates and live
Issue readback; CI verifies the collaboration PR. Rollback: disable the board workflow;
Issues and their source metadata remain usable through search and manual maintenance.

## Future ADR format

Use `docs/decisions/NNNN-topic.md`: status/date, context, options, decision, evidence,
compatibility, consequences, rollback and links to task/PR. Reference accepted decisions
here without copying their task status. A proposal is not an accepted architecture change.
