# ADR 0005: Bounded annotation revision from typed semantic rejection

Status: proposed for #64; implementation and offline checks do not establish source quality.

## Context and evidence

The actual #63 calibration on source `1f49dc0af50bc4619ae8b2a888f6589351f7a144`
failed its original all12 requirement: twelve originals were presented, eleven documents
were accepted/materialized, and the largest document exhausted its three annotation/Gate B
rounds. The retained rejected candidates contained 72, 136 and 57 items. These counts are
observations, not a correct target count or proof of causal improvement. The three annotation
requests had identical original-only prompts; the successful semantic rejection reasons
and Gate A-cleaned candidates were retained as artifacts but discarded by the next call.
No truncation or provider transport failure explains that observed failure.

See the [read-only diagnosis](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6386648735),
[root recovery checkpoint](https://github.com/xcosmosbox/Cairn/issues/9#issuecomment-6386677971),
and [bounded task/acceptance](https://github.com/xcosmosbox/Cairn/issues/64).
Gate B itself named an object absent from the original, so treating every named omission
as an instruction to add it would introduce unsupported content. Public rationale contains
no article bodies or secret values. The failed historical attempt remains failed and immutable.

## Options

1. Repeat the same independent original-only request. This preserves the existing method
   but discards an available specific critique and can lose correct prior material.
2. Accumulate a conversation, union rejected candidates, add a domain checklist, split the
   source, increase rounds or relax gates. These change the method/budget or treat rejected
   material as truth; they are outside this task.
3. Use only the latest successfully parsed explicit semantic rejection and its cleaned
   candidate in a fresh, bounded full-replacement call, with the same original as authority.

## Decision

Implement option 3 in both full `processDocument` and incremental C4 `annotateOne`.
The first `Annotate` request retains exactly its existing system/user bytes and request
options. Before any qualifying semantic rejection, operational or format failures continue
to use that original-only request.

`SemanticRejection` wraps the existing rollback sentinel and carries a reason. Gate B
creates it only after successful JSON parsing with **explicit `idempotent: false` and a
nonblank reason**. Provider/transport/parse failures, missing/null verdicts and missing/null/
blank reasons never become typed critique. Existing Gate B pass/reject and in-place retry
semantics are preserved: this change does not make a previously rejected verdict pass or
change a missing-field rollback into an extra inner retry.

After a typed rejection, `Revise` receives a JSON data envelope containing the entire same
original, that rejection's Gate A-cleaned candidate, and that specific reason. The system
instruction declares the original authoritative; source, candidate and critique are data,
not task instructions. It asks to preserve supported correct candidate material, repair
omission/distortion/classification, remove unsupported material, and produce a complete
replacement `items` result. The critique can itself hallucinate objects, dates or conditions;
its assertions must be checked against the original before adding anything. This is a prompt
contract backed by the unchanged gates, not a guarantee that a model obeys it.

Only a newer valid semantic rejection replaces feedback. Annotation transport/parse errors,
Gate A failures and unverifiable/untyped Gate B failures retain the latest valid candidate/
reason pair. There is no history, aggregate candidate union, truncation or conversation
state. Feedback is local to a document invocation, including concurrent documents. The
existing three outer rounds (including first annotation), configurable existing inner retry
caps, per-call output cap and failure/skip behavior remain unchanged. Revision uses a fresh
stateless `Complete` for every inner attempt. No additional gate or provider call is added.

The revision candidate projects only `tag`, `name`, `content`, `detail`, and `confidence`.
It excludes IDs, document/skill/path metadata, UUID metadata and `SourceSpan`. UUID-form
text introduced through candidate/critique is replaced with consistent opaque aliases.
The alias prefix is chosen absent from all new prompt data and the original to avoid
collisions. Known aliases are reversed in decoded response strings, including JSON escapes,
before the ordinary annotation parser and both gates; unknown aliases are untouched.
Helpers stay in `llm_annotator.go` rather than adding a shared framework. The authoritative
original is unchanged. This is not a repair or exception for arbitrary pre-existing UUID
text in the original/first request; that broader boundary remains outside this correction.

For incremental work, "same original" means the exact existing line-preserving C4 text
with managed/human blocks blanked out, not the full managed document. Existing ID minting,
member/source ownership, downstream UUID pinning, human-curated protection and dirty-set
rules are unchanged. Neither path changes storage/schema, Bundle identity, extraction,
query-side behavior, defaults or dependencies.

## Validation and limits

Offline regression tests freeze the original first-wire system hash and literal source/user
bytes; exercise explicit typed versus untyped rejection and inner caps; assert complete
original and latest cleaned candidate/reason, replacement instead of history union, feedback
retention across transient failures, concurrent isolation, collision-safe UUID round trips,
metadata exclusion, and full/incremental parity with C4 block exclusion and member IDs.
Required repository checks are `go test -race -count=1 ./...` and `make verify`; exact command
outcomes, source head and independent engineering/security/scientific reviews belong in
the PR/task evidence rather than being inferred from this ADR's presence.

These checks validate software behavior, not improved source coverage or semantic accuracy.
Later requests grow by the complete candidate and critique plus JSON representation overhead;
there is no claim of an exact token/capacity bound. Any actual post-fix validation requires a
separately frozen bounded root admission with exact-wire/token accounting. No paid/model run,
QA/gold/HOLDOUT scoring, full609 construction or release is admitted here.

## Compatibility and rollback

The annotation package gains `Revise` and a typed error while retaining the original rollback
sentinel for existing callers. First-call bytes, result format, storage and accepted Bundle
compatibility are unchanged. Later retry methodology changes deliberately and must be recorded
in future experiment identities; old calibration results cannot be relabeled as corrected.

Rollback reverts the #64 implementation commit and this ADR. It restores original-only retries
without migrating stored data or rewriting historical artifacts. An independently reviewed PR
and root acceptance are required before integration; the author does not self-merge.
