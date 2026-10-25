# Corpus-only capacity instrumentation

Task [#41](https://github.com/xcosmosbox/Cairn/issues/41). This opt-in experiment
calls the unchanged production orchestrator and OpenAI-compatible client. It
does not implement a substitute extractor, open QA/HOLDOUT, resume a previous
run, admit full609, or establish graph quality. The two-document prerequisite
has separate semantic/relation verification; paid12 admission remains separate.

All added code is in this directory and
`e2e/benchmarks/multihop_public/capacity/`. No product API, prompt, semantic gate,
retry policy, dependency, Bundle, or query-side behavior changes. Concurrency8 is
an experiment-wide wrapper around actual logical calls, including their nested
HTTP retries. Stage2 may still create one goroutine per document; actual model
calls are bounded without editing that production fanout.

## Free preparation and validation

From the repository root, with Go1.22+:

```sh
go test -race -count=1 ./build/test/e2e/corpus-capacity
go build -o /tmp/cairn-capacity ./build/test/e2e/corpus-capacity
/tmp/cairn-capacity --mode prepare --corpus /data/corpus.json --out /data/capacity-attempt01
sha256sum /data/capacity-attempt01/selection.json
```

Preparation accepts only the exact609-document corpus bytes pinned by the
existing public baseline manifest. It has no QA argument or benchmark-loader
import. Sort by Python-compatible Unicode whitespace body length, then the
baseline doc_id (SHA256 of exact URL); divide at floor(k*N/4), and select the
first, lower-middle and last item in each slice. The12 IDs, source/body/wrapper
hashes, sizes, stratum/rank and original-body byte/codepoint offsets are frozen
before calls. A fixed metadata wrapper and single `SKILL.md` are copied into
separate `original/` and mutable `source/` trees. Originals are never rewritten.
The exact selection SHA is mandatory in later modes.

The official tokenizer is an environment-only prerequisite. No script installs
or downloads dependencies during a request. Provision a reviewed official
`deepseek-recipe` wheel matching commit
`935817c66f5d6e64838f419692545f06787e485f`, its isolated Python executable, and
the matching V41 tokenizer with SHA256
`81f64d1248a68ce3663e07ab3ee48b851e5df0e32d27cb98e4c9a268151e8d99`.
Pin an external runtime receipt with these fields:

```json
{
  "recipe_commit": "935817c66f5d6e64838f419692545f06787e485f",
  "python_sha256": "SHA256 of executable bytes",
  "wheel_path": "/absolute/path/official.whl",
  "wheel_sha256": "SHA256 of wheel",
  "tokenizer_path": "/absolute/path/v41/tokenizer.json",
  "tokenizer_sha256": "81f64d1248a68ce3663e07ab3ee48b851e5df0e32d27cb98e4c9a268151e8d99",
  "package_root": "/absolute/path/site-packages/deepseek_recipe",
  "package_files": {"relative/file": "SHA256 of each installed package file"}
}
```

`package_files` is the complete recursive regular-file inventory excluding
`__pycache__` and `.pyc`; symlinks are rejected. Runtime provenance must also
establish that the wheel matches the pinned official source; a self-authored
receipt is an identity record, not independent supply-chain proof.
The adapter uses official request conversion, the V41 encoding and its special
token ranges, and `resolve_max_tokens`. It never estimates tokens by dividing
JSON bytes. Input plus exact requested output must fit conservative context
1,000,000; max output393216. Missing runtime, count failure, identity mismatch,
or overflow rejects dispatch. Actual offline normal/special-spelling/overflow fixtures are recorded in
`e2e/benchmarks/multihop_public/capacity/offline-tokenizer-validation.json`.
The official0.2.0 native wheel was pinned, not rebuilt from the source commit.
A free `--mode preflight` uses the same runtime
flags as `run` and a synthetic local request; no model call occurs.

## Separately admitted execution

Only after root's explicit admission and independent exact-head review, use
`--mode run --allow-paid --selection-sha256 SHA
--runtime-overlay PATH --runtime-overlay-sha256 SHA --token-python PYTHON
--token-python-sha256 SHA --token-runtime-receipt RECEIPT
--token-runtime-sha256 SHA --out PREPARED_DIRECTORY`, with the existing runtime
`CAIRN_LLM_API_KEY` environment variable. The key is never a command argument.
Defaults are65536 output tokens,600s per HTTP request,5400s per stage/run,
annotation3 attempts ×3 total annotation rounds, HTTP3 retries, no whole-run
retry. The runtime flags must match the separately hash-pinned immutable overlay; changes need
a new reviewed profile. The effective settings, executable SHA, compiled capacity-driver
digest and production source digest are frozen in `run-start.json`. Other production
stage limits remain unchanged.

Each actual serialized HTTP attempt passes the token guard, then receives an
fsync'd pre-dispatch record before any network send. SHA256 and exact wire,
system/user byte/codepoint sizes bind the real generated request, not dump04.
The observer never changes prompts, headers, payload or the returned response
bytes/read errors. It records safe settings, stage/logical/provider attempt,
status, response identity hashes, recognized UUID/model identities, finish,
timing and reported usage, including429, truncated/malformed responses and
unknown usage. An unexpected/absent model identity on a decoded200 response
cancels further admission. Local versus provider input counts remain explicit;
configured alias does not prove a backing model revision.

The ledger is shared and sticky: a failed pre/post receipt cancels the run and
prevents later admission; already admitted calls are drained and remain in the
denominator. Thirty-second progress records and counts retain liveness during
long requests. An abrupt kill can leave an admitted request without an outcome;
the standalone `--mode trace-audit --out DIRECTORY` records that unknown attempt
even without selection, stage or DB artifacts. Full audit records this forensic
receipt before opening graph artifacts; failed builds do so too. Existing `run-start.json`, ledger and
final receipts are never overwritten. There is no resume mode or background
retry. Preserve the directory and request a separate decision after a failure.

`private-builder.log`, source/article bodies and stage dumps are restricted local
artifacts and must not be uploaded as public reports. Product error logs may
contain provider response text. Public receipt fields exclude auth headers,
prompts, generated text and reasoning; unrecognized model/ID text is hashed.

## Audit and admission evidence

The driver runs the free audit after a successful build; it is separately
available as `--mode audit --out DIRECTORY --selection-sha256 SHA`. It verifies
original identities, all12 accepted nonempty documents, actual unique member IDs,
per-document incidence, confidence-selected05h UUID/member mappings, durable DB
nodes and exact `node_sources` triples. Explained omissions never count as
materialized coverage. Missing source rows, unknown UUIDs and invalid nonnull original span hints
fail. Optional NULL hints remain explicit unknowns, following the root amendment
after the initial incorrect NULL-span gate. It records original body, annotation and rewritten hashes separately.
Optional original hints remain unverified and do not locate rewritten Markdown;
this audit does not establish original quote
grounding, semantic fact retention, or answer quality.

Request audit separately exposes unknown usage, length responses and unmatched
admission/outcome/logical events. Report the full denominator even after a later
success. All aggregate usage is provider-reported only; unknowns remain unknown,
and no currency charge is invented. The12 measurements do not guarantee that
any full609 extraction, supplement, description or relation request will fit.
Every future actual request still needs the guard and separate root admission.

## Cross-verifier integration profile (task #56)

The new immutable `runtime-cross-relations-v1.json` extends the original protocol and
selection by hash; it never replaces them. Both `preflight` and `run` require its exact
`--runtime-overlay-sha256` (and optional explicit `--runtime-overlay` path). Preparation
still creates the original identical selection. Use a new prepared attempt directory;
previous preflight/run/roster/evidence files remain immutable. Audit reads the run's
saved `runtime-overlay.json`, `runtime-freeze.json`, run-start and run-finish receipts.
It does not reinterpret an old run using whichever configuration is current.

This profile passes1048576bytes through `pipeline.Options.CrossRelationEvidenceBytes`.
The independently checked12 original wrappers total166300bytes (bodies162880bytes),
including a71332-byte largest wrapper. The driver rechecks actual bytes/hashes and
rejects a source union or source-only JSON envelope above the bound before any paid
call. This is a lower bound: generated endpoint/member/relation framing is checked by
the actual production verifier, and every actual wire attempt still requires the
unchanged official rendered-request token counter plus exact output reserve. Neither
byte preflight nor the synthetic tokenizer probe establishes generated-request fit.

The additional stage is `cross_relation_verify`. Logical/wire events retain all
exposed source document IDs as `document_ids`; the legacy singular field alone is not
per-document cost or coverage. Full stage counts, typed candidate dispositions and
artifact hashes are retained. Required cross artifacts are `00_frozen_sources.json`,
`05e_extract_related.json`, `05e_cross_relation_report.json` and
`05e_cross_validated.json`. Missing best-effort production dumps fail this audit.
Original source/quote/member/eligible-edge bindings are independently rechecked for
accepted candidates, including original byte/line offsets, final UUID/candidate/DB
edges, and complete original neighborhoods. Semantic support remains model-judged;
these mechanical checks are not semantic proof.

All eight dispositions are explicit, including zeros. Semantic, invalid-evidence,
endpoint and invalid-relation rejections are visible safe rejections/model-quality
issues; they receive no supported credit. Invalid ACCEPTED bindings hard-fail.
Insufficient evidence (including overflow), invalid/incomplete verdicts, context or
logical execution errors, unknown stages, truncated responses, missing reports/ledger,
and unmatched attempts prevent capacity readiness. A matched response or fallback
cannot erase those boundaries. Root uses the full counts for its scientific decision;
audit success alone grants no execution or full-corpus admission.

The new profile intentionally changes the historical strict100% member audit to a
minimum0.95 actual incidence rate, with all12 documents nonempty in the materialized
source ledger and every claimed candidate/source mapping valid. The authoritative
prefilter denominator is **distinct(original_doc_id,accepted04member_id)** from the
accepted04 input, before fusion, confidence filtering or drops. Candidate/materialized
incidence totals and per-document missing members expose each loss. Global unique
member counts remain supplemental; shared IDs cannot hide lost document incidences.
Old evidence and the default direct audit helper retain their historical strict rule.

Paid12 additionally requires actual attempt03 semantic PASS, merged independently
reviewed integration, a final executable/source/configuration freeze and root's
separate decision. No QA/HOLDOUT, paid12 or full609 call is authorized by this profile.

The v1 offline runtime receipt preserves its original compiled-source identity.
`offline-cross-relations-runtime-v2.json` records the corrected auditor's new
source/binary preflight after review found a rejected-edge retention false pass.
The auditor now compares the complete eligible validated semantic projection with
both final candidate and DB inventories. A rejected-only identity cannot persist;
an independently accepted duplicate or legitimate validated local edge can justify
that same identity. Original v1/profile/roster evidence remains unchanged.

### Full-rebuild local contradiction verification (report policy v2)

The legacy `cross_relation_verify` stage and `05e_cross_relation_report.json`
filename also cover **new same-subdomain `contradicts` candidates** under
`validation_policy: cross-subdomain-and-local-contradicts/v2`. Each decision records
`scope` (`cross_subdomain` or `same_subdomain`) and `kind`, checked against the
pre-verifier graph. The auditor includes both kinds of scope in the candidate
denominator, reports counts by scope/kind/disposition, verifies accepted original
quotes and all five same-context contradiction predicates, and checks the complete
validated/candidate/DB projection. A rejected local contradiction cannot remain in
the validated graph, final candidate or DB without independently accepted support
for that identity. Other same-subdomain relation kinds retain their prior behavior;
cross-subdomain contradictions are verified once. This is full-rebuild verification
of generated candidates, not a contradiction-discovery or semantic-accuracy claim.

Missing `validation_policy` in a historical report, or explicit
`cross-subdomain/v1`, is interpreted and labeled **historical cross-only v1**.
Those immutable packets are readable without rewriting them, but their zero cross
candidate count gives no evidence about local contradictions. Unknown policies fail
the audit. Future source/runtime freezes must state their actual policy; historical
v1 results cannot establish v2 coverage or admit a new run. Existing runtime profiles,
offline receipts, roster and failed #63 evidence have not been replaced by this change.

Incremental I-7 can generate new local relations but has no authoritative frozen
original neighborhood at that call site; it is **not covered** by this correction.
Existing human-curated edges are not inputs to the full-build candidate verifier.
The no-op incremental compatibility test proves only preservation of an already
materialized curated edge in that scenario. It does not prove dirty-subdomain/new-edge
verification or a general preservation guarantee. See
[ADR0006](../../../../docs/decisions/0006-local-contradiction-verification.md).
