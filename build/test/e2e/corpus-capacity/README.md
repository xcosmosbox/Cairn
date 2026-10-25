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

## Fixed-R3 seeded qualification/revision driver (task #73)

Separate `seed-preflight`, `seed-run`, and `seed-audit` modes test the production
revision API without invoking the full pipeline. This is a NEW experiment, not a
fourth #63 round or paid control repeat. #63 remains FAIL. This implementation and
free tests grant no live admission; root must freeze/review/admit separately.

Inputs are fixed to LAST R3 GateA's57items from private archive commit
`1a44fa27db4221778da3986efa5f4414c6f27611`, source
`6f388affbc74a3719f00167cd84e7a04981655e63c7acdab04d0c82d57ebb92b`.
The complete71332B original wrapper is authoritative, exactly as historical
`os.ReadFile` supplied it. Original/candidate/rejection hashes and sizes are
checked; body-only substitution, R2 selection and candidate union are unavailable.
Original spans remain unknown. The old rejection is wrapped prose: because the
old parser defaulted absent flags to false, it cannot prove current typed false.
It is provenance only, never cast into a new `SemanticRejection`.

1. Current GateB qualifies fixed R3/full original. Explicit typed false plus NEW
   nonblank reason permits revision. A pass STOPS as
   `QUALIFICATION_PASS_no_revision_effect`, a stochastic recheck without revision
   effect. Untyped rejection, transport/parse failure or unknown result stopsFAIL.
2. Production Revise uses THAT new typed reason, same full original and fixed R3.
   Candidate/critique remain fallible data under #64 projection/alias rules.
3. Unchanged FormatGate(default100runes) must yield a NONEMPTY replacement, then
   current GateB checks it against the same full original. No pipeline follows.

Hard cap: THREE actual wires total, ONE per call, concurrency1, annotation/gate
inner-attempt1, no outer retry/canary/alternate candidate. Production HTTP
`MaxRetries:1` permits an additional retry in that library; the stricter transport
wire cap prevents its dispatch, and bad outcomes cancel before the retry loop.
Historical capacity modes retain their previous settings and nil seed policy.

A separate plan has this exact schema; it never replaces the capacity overlay:

```json
{
  "format": "cairn-seeded-revision-plan/v1",
  "mode": "fixed-r3-qualification-revision-validation/v1",
  "archive_commit": "1a44fa27db4221778da3986efa5f4414c6f27611",
  "document_id": "6f388affbc74a3719f00167cd84e7a04981655e63c7acdab04d0c82d57ebb92b",
  "original_sha256": "96c295b30c38e1cf2421ca4615ce375a7012a68b3f81ec05947fa4a03a519309",
  "candidate_sha256": "7b7a3d15e711f0857d3899c981e79b17eb7f47a5a7b694ec59d1e983e63de34a",
  "archived_rejection_sha256": "c2d503378f06c25940c2e698dd00b78763fafeb582d7de9a00734e73a018b735",
  "max_tokens": 65536,
  "request_timeout_seconds": 600,
  "run_timeout_seconds": 1800,
  "max_actual_wires": 3,
  "max_wires_per_call": 1
}
```

Use a fresh output directory and pinned official runtime:

```sh
cairn-capacity --mode seed-preflight --seed-input-root /private/restored/raw/attempt-01 \
  --seed-manifest /private/new-plan.json --seed-manifest-sha256 PLAN_SHA \
  --token-python PINNED_PYTHON --token-python-sha256 PYTHON_SHA \
  --token-runtime-receipt PINNED_RECEIPT --token-runtime-sha256 RECEIPT_SHA \
  --out /private/new-seeded-attempt
```

Seed modes forbid overrides of capacity timing defaults; the separate plan fixes
HTTP600s/overall1800s. Free preflight captures the production qualification builder
and real client serialization with an in-memory transport: zero network/model
access and no model-key lookup. It also counts a SYNTHETIC-critique revision-builder
probe, not a future live request or semantic verdict. Actual revision depends on
NEW typed qualification feedback; final GateB depends on the cleaned replacement.
Each actual request uses production builders, capture hashes and the official
per-wire counter. Overflow stops; no truncation. Only separate frozen rootGO may
use `seed-run --allow-paid` with the same identity arguments and environment key.

Preflight binds binary/plan/compiled-driver/production digests. Exclusive start,
finish, capture and ledger files preserve failures. Explicit phase/document/SHA
attribution binds decoded revision `original` to the frozen source; GateB must
match its production capture using that same original. It never guesses identity
from a path inside critique/candidate. Capture hashes include projected candidate
and critique, system/user and exact wire. Optional policy on the existing trace
transport enforces phase order, output65536, official model, JSON/thinking and no
extra sampling overrides; there is no alternate live transport.

Private `*-response.private.json` retains content parsed by the gates. Observed
wire final-content hashes bind those saved bytes to the response. Offline
`seed-audit` reconciles exact phase/wire/usage/source identities, capture/actual
wire hashes and parsed-content/usage hashes, then replays saved content through
production parsers/gates. A finish string alone cannot prove typed qualification.
Pass-at-qualification uses exactly1call; a completed revision path uses exactly3.
Missing usage, counter delta, non-stop/error, unknown attempts or persistence
failure prevent acceptance. No charge is invented for unknown usage.

Source/model payloads, candidate/revised documents, typed reason and private logs
remain restricted. Public reports contain sanitized method/counts/hashes/negative
results only. No seeded result establishes causal improvement, factual recall,
full12/full609 capacity, graph/answer quality, QA/HOLDOUT or stable readiness.

The counter freeze includes Python executable SHA, adapter SHA, runtime receipt
SHA and official counter ID. `seed-run` rejects a different/missing configuration
before the first wire, rerenders/recounts the identical qualification request and
compares it with preflight. Each wire's token receipt must match that identity;
standalone audit compares every request with start/preflight. A coincidentally
equal token count cannot authorize changing runtime. A nonempty environment key
is required before seeded dispatch, matching normal capacity admission.

A current qualification or final GateB pass retains
`accepted-cleaned.private.json` plus `accepted-checkpoint.json`, bound to the
actual pass response, full-original hash, skill/path, accepted bytes and complete
run/runtime/counter identity receipt. Offline audit reproduces that accepted
candidate. No IDs/04 are assigned here. A future separately accepted checkpoint
adapter must use existing AssignIDs semantics; a failure creates no accepted04.
The accepted checkpoint is explicitly provisional until a matching seed-audit
PASS; a parsed gate pass during a transport/counter/ledger failure is unusable.

## Known negative-parent receipt continuation (task #85)

`receipt-preflight`, `receipt-run`, and `receipt-audit` are separate opt-in modes.
They do not modify the fixed-R3 loader, its three-call policy, production prompts,
or historical capacity defaults. This implementation grants no live admission.
Source acceptance, exact zero-key preflight/freeze and a separate root technical
GO are required before an actual model request. Existing specific user DeepSeek
paid/network/public-source and machine-feedback disclosure authorization persists.

Only the complete negative #73 attempt02 is eligible: private archive commit
`2da71d37dfda6db29a25445283f205b90d873334`, ledger
`d5a06026a0c51bea166c0bf3dbb8dc445ee8cf71714c0dd62a90b4d13e7ff51d`,
cleaned163-item candidate
`01abf76117282c668a413d890f561733323565ba0687ed7d2ef63226be543d5c`,
final response file
`09cf12e276f2d6e3ec77572b46837fefb5bea9261b1b511670cc0cb274b912ad`,
and exact current typed reason
`4188a394eb8ec982eac02deaa8deafeeb5a60ef2412bfec721c726f633d57746`.
The complete71332-byte original wrapper remains authoritative. Original spans
remain unknown; no graph IDs are introduced.

The new strict JSON manifest is independently pinned, and all its values must
equal `knownReceiptPlan()` in the reviewed source. Its snake-case fields include
format/mode, parent archive/manifest/ledger/start/finish/binary/driver identities,
production source digest, parent plan/preflight, document/skill/path,
original/seed/candidate/final-response/typed-reason hashes and lengths/item counts,
the complete counter identity, and fixed budgets. Caller-supplied self-consistent
hashes cannot select another parent or reason. The format is
`cairn-parent-receipt-plan/v1`, mode `parent163-revision-validation/v1`, and budgets
are output65536, HTTP600s, overall1800s, at most TWO actual wires, ONE per call.
No extra manifest fields or capacity timing overrides are admitted.

`--parent-root` is a separately restored read-only archive root containing
`manifest.json`, `run/`, `evidence/`, `fixed-inputs/` and `runtime/`. First verify
the immutable transport archive and all members as documented by its manifest.
The adapter rechecks the independent manifest SHA and every declared regular
member; traversal, duplicate members, escaping links and missing/changed files
are rejected. It then independently proves the parent sequence
qualification→revision→validation with exactly3admissions/3outcomes, expected
model, HTTP200/stop, reported usage, exact local/provider token equality and
capture/observed-content bindings. All three requests and saved responses are
replayed through unchanged production builders/parsers, including the initial
typed false, revision/nonempty FormatGate and final typed false. The original
candidate, cleaned bytes and both reasons must reproduce exactly. The parent's
acceptance-only seed-audit FAIL is expected and is neither an integrity proof nor
permission to accept its candidate. Incomplete attempt01, passing/untyped/blank
or foreign parents, manual critique/candidate substitution and changed production
semantics are rejected. No new qualification model call occurs.

After a separately reviewed strict manifest is prepared, free preflight uses:

```sh
cairn-capacity --mode receipt-preflight --parent-root /private/restored-attempt02 \
  --receipt-manifest /private/receipt-plan.json --receipt-manifest-sha256 PLAN_SHA \
  --token-python PINNED_PYTHON --token-python-sha256 PYTHON_SHA \
  --token-runtime-receipt PINNED_RECEIPT --token-runtime-sha256 RECEIPT_SHA \
  --out /private/fresh-continuation
```

The output directory itself must be fresh. Preflight captures and counts the
ACTUAL production Revise request using the complete original, exact163 candidate
and unedited final typed reason; the existing production serializer is captured
offline with zero provider calls. Binary, compiled driver/production digests,
manifest, parent proof and full counter identity are frozen. Actual run rechecks
and recounts the identical first wire before dispatch.
The first transport admission also requires the exact frozen request SHA and
full token receipt; even same-identity counter or byte drift cancels before a wire.
The final GateB request
depends on the new nonempty replacement and remains unknown until its own actual
per-wire guard. No truncation, manual diagnostic checklist or brand correction is
added to the machine critique; unchanged production source-precedence rules apply.

Only a separate root frozen GO may use `receipt-run --allow-paid` with the same
arguments and nonempty environment key. It executes production Revise→unchanged
nonempty FormatGate→current GateB, concurrency1, inner-attempt1, no qualification,
wire/outer retry, canary, alternative transport, pipeline or third revision.
The existing client, durable ledger and official counter remain the single call
path. Its separate two-phase admission policy requires explicit parent/document/
full-original identity on every request and outcome; paths in fallible feedback
never determine document attribution. Failures, missing usage and unknown
attempts stay retained rather than becoming acceptance.
Every nonempty stop response requires positive input equal to the admitted
official count and positive completion tokens at most65536. The new recording
boundary first retains the actual response/ledger, then cancels and returns an
execution error on invalid or missing usage; an unauditable revision cannot
spend the final validation wire. The offline parent/current trace checks enforce
the same bounds, including coherently rewritten response/ledger/finish totals.

`receipt-audit` is offline and binds private parsed content to observed wire
hashes, usage, source/parent, production capture/replay and current run/preflight
receipts. It reports `integrity_pass` separately from `accepted`: a complete
typed-negative two-call run may have valid integrity but is never accepted. Its
exit remains nonzero without a completely audited passing candidate. Passing
GateB produces a checkpoint explicitly `provisional_until_receipt_audit_pass`;
the checkpoint alone is unusable, and a transport/model/counter/persistence error
cannot be hidden by a parsed true verdict. No AssignIDs/04 adapter or construction
follows. Historical #63 remains FAIL11/12, attempt01 UNKNOWN and attempt02 final
semantic FAIL. No item-count-caused benefit, recall, quality, causal effect,
all12/full609, QA/gold/HOLDOUT or release claim follows.
# Opt-in indexed-source experiment (#99)

`indexed-preflight`, `indexed-run`, and `indexed-audit` are a separate source-only
experiment. They require `--indexed-original`, `--indexed-diagnostics`,
`--indexed-manifest` and its independently reviewed `--indexed-manifest-sha256`,
the existing pinned token runtime flags, and a fresh `--out`. The exact manifest
is the finite `knownIndexedPlan()` schema, not a historical seed/receipt manifest.
Only `indexed-run` accepts (and requires) `--allow-paid`; implementation acceptance
does not grant live admission. Audit is offline but recounts both production wires.

The known public original is authenticated independently, with source role
`annotation_input_original`; it is never invented as an existing annotation member.
The accepted pure helper supplies the complete unchanged original and exact block
index, without candidate, critique or diagnostics. One generation response must
pass its strict witness/coverage validation and the same current GateA before one
unchanged full-original GateB. Total maximum: two actual wires, one per call,
concurrency1,65536 output reserve,600s/request and1800s attempt. There is no
qualification, repair, retry, canary or third call. Invalid/missing usage,
uncertain delivery, changed profile/source/runtime, or persistence errors stop.

The first actual generation wire/count is fixed at zero-key/zero-HTTP preflight.
GateB is unknown until the real strict candidate exists; its full request is
reconstructed by the current production gate, rendered by the same production
`RenderRequest`, counted, and checked against the actual forwarded Body. Private
CompleteRequest, exact wire bytes, raw HTTP response and parsed response are
retained for each phase along with the complete durable ledger. Offline audit
reconstructs both builders, verifies raw/parsed/trace bindings, and officially
recounts the complete actual wires. Failed/incomplete attempts retain evidence.

Even complete delivery, strict audit and GateB=true produce only
`PROVISIONAL_GATEB_PASS_REQUIRES_ROOT_REVIEW`. `accepted` stays false, no accepted
checkpoint is written, and AssignIDs04 is never called. Root must review ALL
`no_annotation` rows, separate private A12/B2/C3 source-local diagnostics and
fidelity, and the immutable archive before any separately admitted checkpoint
adapter. Quotes witness occurrence, not entailment or exhaustive recall. The
opaque diagnostic freeze is copied privately and never enters a prompt. This
experiment proves neither quality nor capacity/causality/full609 admission.

