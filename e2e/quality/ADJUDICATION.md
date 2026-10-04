# Independent AI adjudication

This is an offline supplement for all cases that lack a valid automatic judge
result after the automatic run has stopped. It does not call a paid model, edit
gold, modify the rubric, or convert an automatic failure into automatic success.
A valid automatic judgment whose answer receives `strict_pass=false` is complete
and is not selected for adjudication.

The coordinator confirms the run is closed, then freezes all three inputs:

```bash
python e2e/quality/adjudicate.py prepare \
  --blind /run/blind.json --grades /run/grades.jsonl --usage /run/judge-usage.jsonl \
  --out-dir /run/adjudication --judge-complete --batch-size 4
```

The default rejects unresolved request receipts. Only after confirming process
termination may the coordinator use `--allow-interrupted`; the manifest then
explicitly records those request IDs instead of calling them budget failures.

Give each reviewer only assigned `batch-NNN.json` files. They contain the frozen
rubric and anonymous cases, original source excerpts, actual retrieved text, and
whitelisted failed candidate verdicts. They omit the arm map, question ID, split,
category, result-file paths, model usage and reasoning-chain content. Reviewers
must not open those other inputs. They must recheck all required facts, every
substantive claim, forbidden assertions, and abstention against the evidence.
Fixing JSON or copying a failed verdict alone is not adjudication.

Prior candidate verdicts are visible. This can anchor the separate adjudicator;
the manifest discloses that the review is not independent of previous judgments.
No human review is claimed. Format and length failures may concentrate in complex
cases, so adjudication results do not estimate overall automatic judge reliability.
"Independent" means separate from the solver and the automatic DeepSeek judge.
Some reviewers previously worked on benchmark authorship, original-source review,
implementation, or dev diagnosis. They are not all fresh reviewers with no prior
knowledge. Assigned inputs omit arm/split maps, but this does not establish
complete blinding or independence from prior project context.

Reviewer output is JSONL with `blind_id`, `reviewer_id`, `reviewer_model` (use
`unknown` when not exposed), concise `review_notes`, and a complete `verdict`
under the existing judge schema. No arm/split details should be sent back in
messages. The coordinator can validate drafts without persisting them:

```bash
python e2e/quality/adjudicate.py collect \
  --manifest /run/adjudication/manifest.json --reviews /run/reviewer-1.jsonl \
  --out /run/adjudicated-grades.jsonl --check-only
```

Remove `--check-only` to append validated records. Multiple `--reviews` arguments
are allowed. Duplicate or unassigned cases, changed frozen inputs, bad quotes,
unsupported enum values, or missing semantic review notes are rejected. The same
unmodified `grade.validate_verdict` and `grade.scores` functions are used.

After collecting the entire selected set:

```bash
python e2e/quality/adjudicate.py finalize \
  --manifest /run/adjudication/manifest.json \
  --adjudicated /run/adjudicated-grades.jsonl \
  --out /run/final-derived-grades.jsonl --receipt /run/adjudication-receipt.json
```

The final file is a new derived view; original automatic grades and fees remain
unchanged. Every case has a `grade_origin`. The completion receipt separately
reports automatic coverage, independently adjudicated count and unresolved count,
whose sum must equal the original case count. Never describe combined coverage
as automatic DeepSeek coverage. If explicitly publishing an incomplete result,
use `--allow-incomplete` and report unresolved cases without shrinking the denominator.

Only the root coordinator joins the final file with the private arm/split map
through `grade.py summarize`. Reports must include the completion receipt beside
the summary: the generic summary's `judged` field includes both grading sources.
Detailed bad-case analysis is restricted to dev; holdout is reported in aggregate.
