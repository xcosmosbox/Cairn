# ADR 0005: Annotation revision history and v0.1 fidelity contract

Status: current v0.1 product contract adopted through [#107](https://github.com/xcosmosbox/Cairn/issues/107)
on 2026-10-26; implementation remains pending in its finite child tasks. #105 earlier
superseded the historical revision method and shipped single-pass behavior. The failed #63 calibration remains failed: 11 of12 originals materialized.
The later typed-critique revision implementation was software evidence, not improved coverage.

At the PR106 baseline, the production path makes one original-source annotation attempt, validates actual
source/alias/schema properties, and keeps valid independent material. It does not perform
semantic-idempotence Gate B, losslessness rewrite, feedback repair or repeated annotation
rollbacks. An omitted description or failed document is counted locally, without erasing
successful siblings. Original UUID-form literals are source text; machine identities still
use request-local aliases and stable owned UUIDs. Human-curated edits remain protected.

Earlier method details and test receipts remain in git history and #64/PR72. They cannot be
relabelled as results of this simplified method. New evidence records the actual source and
raw outcomes. Run affected annotation/incremental checks; fullrace, full609, counter receipts,
archival reconstruction and repeated live GO are not prerequisites for a small experiment.

No schema, immutable Bundle or query-side write/LLM behavior is introduced. Rollback reverts
the implementation commits while preserving historical inputs/results and human data.

## Current v0.1 applicability

The [v0.1 plan](../coordination/reports/2026-10-26-v01-build-completeness-plan.md)
supersedes single-pass/no-semantic-revision and omission-only-success as product goals.
Finite configured retries target a failed step; malformed assessment retries verification
of the same candidate, while source-semantic defects revise the offending document with
originals/candidate/feedback and recheck the whole candidate. Successful siblings remain;
exhaustion is explicit partial/unavailable, cancellation/permanent errors have reasons.
Meaningful omissions or distortions remain unaccepted; empty output is not proof that a
nonempty original contains no knowledge. HTTP retries do not replace semantic revision.
This documents the new intended behavior, not its completion. #63/#64/#105 results and
the removal of peripheral archive/cost/time/full609/repeated-GO gates remain unchanged.

## Source subjects and their direct predicates

The fixed public mini repeat exposed a subject-binding gap: annotation retained a dated
duration in a separate attribute Concept, and a dated quantity in separate version/date
Entities, while the original named subjects retained only definitions or duties. Full
mapping preserved those member divisions; description fusion received only each node's
declared members. All source incidences and numeric strings survived globally, but the
subjects lost their required facts or source-bound reachable paths. This is not semantic
completion, and later relation-audit uncertainty does not explain away the earlier gap.

Generation and the existing semantic assessment now share an explicit boundary: when the
original directly states attributes, states, relations or rules about an explicitly named
subject, retain those facts and their complete arguments and qualifications in that
subject item's content/detail, whether dated or undated. Additional attribute/date or
statement/rule units may coexist but cannot replace that subject's material or leave its
assertion only in another relation participant. Preserve roles, direction, speakers,
quoted stance and negation rather than reversing the predicate or promoting a disputed
claim to an author-confirmed fact.
This does not require copying a sentence's predicate indiscriminately to every mentioned
object or generating an inverse relation that the source did not assert.
A temporal qualifier alone does not establish a new subject identity. Preserve genuinely
distinct objects and explicitly distinct version objects, different subjects, dated values,
negation, conditions and exceptions. Source uncertainty is distinct from assessment unknown:
an explicitly unknown value or unspecified subject may be faithfully retained and verified
as supported. Only an assessor unable to determine whether the candidate's attribution or
uncertainty is faithful returns unknown. Never guess an owner to create a connection the
source did not specify. Neither similar names, dates, values nor entity/concept style
authorizes a programmatic merge.

The existing document processor supplies the original, whole candidate and concrete
ownership omission to finite revision, then reassesses the whole replacement. There is
no new schema, model call, retry loop, allowance or invented graph relation. This helper
is shared by Full and C4. Its assessment is also reused for single-node fusion; that
caller checks only the node's supplied member material and attribution, without inventing
an absent parent, changing members or importing unsupplied facts. Whole-document items
assessment remains responsible for the annotation subject boundary. Mapping and alignment
are unchanged; the clarification cannot guarantee that a model will choose a connected
final representation.

Scripted controls establish omission-feedback flow, finite repair/exhaustion, preservation
of independent identities and dated/negative facts, and same-candidate unknown handling.
They already pass on the unchanged processor and do not prove prompt effectiveness. The
actual failed repeat remains failed; validation after the change must use the same fixed
sources and unchanged subject/source/reachable-value acceptance. Prior successful Full
and changed results, earlier failures and all planned cases remain historical evidence.

A subsequent fixed repeat completed production with all three relation scopes complete;
the ticket/count values and source bindings were retained, while acceptance still failed:
an undated dependency existed only in a standalone statement unit and the target ticket's
material. The named entry subject had only its SKILL member, without the log source. This
showed that date/version qualifiers must not be a prerequisite for direct-subject material.
The same generation/assessment boundary now covers undated predicates; no proposition is
mechanically merged into an action and no edge is forced. Both failed repeats and their
different outcomes remain unchanged. New scripts exercise this exact omission shape and
wrong-subject/quoted-claim feedback through the existing finite processor, not model
effectiveness or a guarantee of later mapping membership.
