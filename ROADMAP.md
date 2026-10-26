# Cairn Roadmap

Live roadmap: [#8](https://github.com/xcosmosbox/Cairn/issues/8).
Task board: [#7](https://github.com/xcosmosbox/Cairn/issues/7).

Cairn turns scattered domain documents into a traceable, evolvable knowledge graph,
distributes immutable versions, and serves agents through read-only MCP. The current
version sequence is finite; a published plan is not an implemented or accepted product.

| Version | Product outcome / acceptance boundary | Current task entry |
| --- | --- | --- |
| v0.1 | Source-to-graph semantic fidelity, complete meaningful knowledge and provenance, finite document revision, stable repeats | [#108](https://github.com/xcosmosbox/Cairn/issues/108); [finite plan](docs/coordination/reports/2026-10-26-v01-build-completeness-plan.md) |
| v0.2 | Useful retrieval, source visibility, ranking and context packing | [#14](https://github.com/xcosmosbox/Cairn/issues/14), future retrieval scope after v0.1 |
| v0.3 | Official MCP Agent using the accepted product paths | Future bounded task after v0.2 |
| v0.4 | Fair effect evaluation with comparable resources and trustworthy judgments | #14 research continuation; future evaluation task after v0.3 |
| v0.5 | Daily update lifecycle | Future bounded task after v0.4 |
| v0.6 | Packages, compatibility and support | Future bounded task after v0.5 |
| v1 | Stable release after acceptance of all six main stages and the actual release decision | Not accepted; no release/tag is published by this plan |

The versions order product acceptance. Independent read-only research and disjoint
preparation may proceed without pretending a later product stage is ready or accepted.
Existing `phase:M0/M1/M2/M3` labels remain engineering categories, not version numbers.
Historical M0 reproducibility, M1 reliability, M2 quality and M3 release evidence retains
its tested scope; historical done Issues do not establish current v0.1 or stable readiness.

## v0.1.x: six implementation milestones

| Milestone | Unique responsibility | Dependencies |
| --- | --- | --- |
| [#109](https://github.com/xcosmosbox/Cairn/issues/109) — v0.1.1 | Supported originals, source-first finite evidence inventory, known-loss localization and compact stage handoff | Planning #107 published and accepted |
| [#110](https://github.com/xcosmosbox/Cairn/issues/110) — v0.1.2 | Annotation fidelity/coverage, finite failed-step retries and semantic revision | v0.1.1 |
| [#111](https://github.com/xcosmosbox/Cairn/issues/111) — v0.1.3 | Member identity/mapping/deduplication, faithful summary and complete detail semantics | v0.1.1; agreed annotation output interface |
| [#112](https://github.com/xcosmosbox/Cairn/issues/112) — v0.1.4 | Explicit intra/cross-domain relations, evidence and finite repair | v0.1.1 + v0.1.3; serialized `reflow.go` handoff |
| [#113](https://github.com/xcosmosbox/Cairn/issues/113) — v0.1.5 | Persisted sources, true distinct counts and Markdown/sidecar preservation | v0.1.1; agreed node/relation candidate interfaces |
| [#114](https://github.com/xcosmosbox/Cairn/issues/114) — v0.1.6 | Sole shared orchestration/entry wiring, status aggregation and integrated acceptance | Wiring after v0.1.1; final acceptance after v0.1.2–v0.1.5 |

All six children must deliver integrated implementation and accepted evidence. Their
contract→task→evidence matrix is the complete v0.1 closure: all six accepted implies
v0.1 accepted under the existing protocol, without an unnamed later acceptance project.
v0.1.x are task milestones, not release tags. Issues own live status, claims and evidence;
this document owns direction. Publication starts no execution agent or background job.

Meaningful source facts, conditions, negation, time, uncertainty, disagreements, explicit
relations and valid sources must survive. Summaries may abstract faithfully; graph detail
and relations collectively preserve complete semantics. Rational fusion may reduce node
counts, but raw retention or a warning cannot excuse substantive graph knowledge loss.
Normal coverage success is distinct from forced-failure partial-result correctness.

Current [#107](https://github.com/xcosmosbox/Cairn/issues/107) supersedes #105's one-shot,
no-semantic-revision and omission-as-warning product direction. It does not rewrite #105's
actual inconclusive diagnostic or restore removed cost/time/archive/full609/recovery/
repeated-approval frameworks. Use affected checks and normal CI. Standing authorization
and evaluation/source isolation remain in [EXECUTION_POLICY.md](docs/coordination/EXECUTION_POLICY.md).
