# Maintainer execution policy

Accepted direction: [2026-10-24 decision in #8](https://github.com/xcosmosbox/Cairn/issues/8#issuecomment-6382509063).
This records the maintainer's execution preferences; live assignments, claims and results
remain in task Issues and PRs. It does not introduce a second task database or change
the existing [claim/review protocol](PROTOCOL.md).

## Goal and evidence gates

Advance the Alpha toward its first stable release. Stable release is a goal, not a
status already achieved; no version number or release date is assigned by this policy.
Preserve the M0–M3 roadmap and require evidence for the relevant gates:

- A credible, reproducible engineering baseline, including the free end-to-end path.
- Retrieval comparisons with the same resources, a new holdout and intact source
  provenance; distinguish retrieval support from answer quality and retain negative results.
- Verified critical update, recovery, authentication and MCP scenarios, plus the
  applicable compatibility, resource and rollback evidence.
- A maintainer release decision after the required evidence is accepted. Passing CI
  alone does not establish stable-release readiness.

Existing query-side read-only, zero-LLM and zero-embedding constraints, immutable Bundle
identity and human-curated/source provenance protections remain applicable. Architecture
changes still need an ADR and an explicit decision.

## Division of work and model defaults

For work dispatched under this maintainer policy, the main coordinator makes decisions
and delegates research, implementation, documentation and verification to execution agents.
It checks dependencies and evidence rather than directly writing product changes.

| Work | Model | Reasoning |
| --- | --- | --- |
| Research | `gpt-6-astra` | `xhigh` |
| Other execution, including implementation, environment recovery, records and review | `gpt-6.1-sol` | `high` |

These defaults apply to this maintainer's dispatched agents. They are not prerequisites
for outside contributors and do not grant credentials, model spending or permission to
override repository constraints. Separate sessions and reviews retain the independence
limits required by PROTOCOL.md.

## Reuse before building an evaluation environment

Research mature benchmarks, official implementations and reusable environments first.
Compare coverage, licensing/access, reproducibility, adaptation cost and the cost of a
new environment. Use a small supplemental suite for Cairn-specific scenarios that an
existing benchmark does not cover. A custom benchmark or framework needs a concrete
gap and cost justification, not merely a preference for a local implementation.

Freeze the data revision and identity, split/holdout boundary, resource budgets, metrics
and provenance mapping before implementation or results are interpreted. A dataset's
per-question gold contexts are an oracle input, not a complete retrieval corpus. Store
download instructions and hashes when publishing source text is outside the intended
scope. Quality evaluation and performance/resource measurement are separate tasks.

## Durable state and authorized continuation

The repository's current Issues, PRs, commits and evidence are the sole dynamic project
state. Chat context can convey a maintainer decision, but record that decision and the
task's CLAIM/ACK before shared writes. A started session is not proof of continued
background work; a plan is not an execution receipt. Use recoverable checkpoints and
handoffs, and do not repeat an already satisfied approval merely to proceed with routine
authorized work.

This policy does not start agents or publish a release by itself. New scope, architecture
changes and acceptance remain explicit decisions supported by the existing protocol.
