# ADR 0007: Bounded fresh-generation construction, offline checkpoint planning first

Status: root-adopted DESIGN under #75/#14. This phase implements only pure legacy
checkpoint verification and deterministic planning, not the future builder or live admission.

## Context and evidence

The #63 actual calibration is historically FAILED: only eleven of twelve original documents
were accepted. Accepted 04 input retains 298 unique member IDs and 298 source incidences;
those denominators do not recover the missing document. #64 corrected feedback wiring and
#68 tightened local contradiction verification, but neither implementation establishes semantic
coverage or a full609 construction result. Retained artifacts can avoid unnecessary repeated
annotation only when their original source, accepted gate and method identities are verified.

A whole-corpus extraction or whole `incremental.Run` is not an input/output-bounded construction
method. The proposed fresh-generation design needs independent bounds for routing, descriptions,
relation candidates, outputs and recovery. A candidate-count ceiling alone cannot prove wire or
output fit. Root adopted a finite offline first phase before implementing any of those calls.

References: [#75](https://github.com/xcosmosbox/Cairn/issues/75),
[failed calibration packet](../coordination/reports/calibration12-20261024/README.md),
[semantic revision ADR](0005-semantic-annotation-revision.md).

## Options and decision

1. Reuse arbitrary existing graph/04 records or a `passed` assertion without verifying their
   dependencies. Rejected: missing provenance and incomplete accepted denominators could become
   silent full-corpus coverage claims.
2. Run full extraction or unbounded incremental integration as a fallback. Rejected: this
   changes the task into an unbounded model/build operation and conflates existing graph identity
   with a new coherent generation.
3. First verify retained accepted legacy 04 bytes, then produce a deterministic structural plan.
   Chosen. No model/client/network/DB call, persistence or dispatch occurs in the package.

### Trust boundary and legacy import

The caller supplies raw manifest/dependency bytes and an archive descriptor separately from a
`TrustPolicy`: externally expected manifest/archive/corpus/roster pins, a complete frozen roster,
a historical-method compatibility allowlist and the required gate policy. `RosterDigest` sorts
by frozen docID and hashes the roster's JSON metadata; it does not establish roster authenticity.

A successful byte/hash check proves **internal consistency relative to supplied expectations**.
An attacker or mistaken caller can invent both bytes and expected hashes. This pure API does not
verify a remote Git commit, signature, archive reconstruction or the correspondence of every
roster row to an independently authenticated corpus. A trusted external frozen manifest/roster
root and an explicit compatibility decision remain required for any future admission. Archive
commit is retained as a provenance pin, not claimed as cryptographically verified here.

Phase1 supports the actual `cairn-restricted-calibration-archive/v1` legacy manifest and its
capacity-v1 protocol/selection/runtime/run records. It verifies only required dependency bytes;
manifest metadata may include unrelated files, including empty historical logs/WAL. It does not
claim all 143 archive contents were reverified. Required dependencies must be nonempty and match
manifest byte lengths/hashes. New formats, including future #73 revision receipts, are unsupported
and fail closed; they need a separately scoped adapter after existing ID assignment if accepted.
This importer must never fabricate a passed legacy manifest to accommodate new output.

The verified dependency graph includes protocol, selection, runtime overlay/freeze and run-start
identities, original wrappers, 00 frozen sources, the corresponding 02 document, its matching
03 explicit `passed:true` artifact, and aggregate 04. URL SHA256 docID, source path/skill, original
wrapper hash and exact UTF-8 content/body hash must agree with selection/freeze/roster. Exactly
one accepted pass artifact binds the declared document/round; missing/null/false/error verdict
artifacts cannot qualify. Historical source/builder fingerprint, annotation method, gate policy
and protocol/profile hashes are retained, not relabeled as bounded-v1 generated output.

04 IDs are checked against existing `extract.Build04ID`; no replacement ID is minted as repair.
Only the derived 04 item ID is removed for exact 02 comparison. All other document/item fields,
including detail, confidence, SourceSpan and item order, must match. Unknown annotation fields
fail rather than disappearing through decoding. Raw comparison retains absent/null distinctions
and numeric spelling; duplicate JSON keys fail instead of using the decoder's last value.
Duplicate accepted documents/member IDs within
one document fail closed. Shared IDs across documents remain legal and retain every distinct
(document,member) incidence with its complete annotation and original identity.

### Deterministic structural plan

Sort frozen documents by docID and their units by frozen member ID. Schedule each unique member
once at first occurrence and attach all distinct source incidences in the same canonical order.
Batch at most **32 unique units**. K=15 per unit gives a per-batch candidate-union **upper bound
480**, not an actual candidate set, recall guarantee, prompt-size bound or capacity pass.

The plan lists exact missing document identities against the entire expected roster. A cache
with eleven accepted documents and a 609-document roster is missing 598 and is NOT_ADMISSIBLE.
It creates no records for missing documents. Even a complete annotation roster is still only an
offline structural plan: live admission is always false and future wire/output/routing fit is
UNKNOWN. Full detail and per-source incidences are retained; no truncation, source substitution,
completion-order scheduling or hidden unbounded fallback is introduced.

## Future builder requirements, deliberately not implemented

Root adopted the following prospective mapping/relation contracts. These are implementation
requirements for a separately scoped builder, not observed token fit, provider support, capacity
or quality results. This package implements none of their retrieval, serialization or dispatch.

### Mapping and taxonomy routing

Begin with at most32 unique units in canonical order. For literal FTS retrieval only, project each
unit to its full name plus the first40 Unicode content runes, without an ellipsis. For a shared04
unit, choose the first canonical(docID,memberID) incidence for this projection. Filter eligible
Entity/Concept, Subdomain and Domain candidates **before LIMIT**, with per-unit limits15/4/2.
Rank first, then break ties deterministically using persisted first-derived node UUID/birth
membership. Union every selected hit with its required ancestors. Hard candidate caps are480
Entity/Concept,64 Subdomains and32 Domains. An overflow triggers deterministic ordered-batch
bisection and recomputation against the **same catalogue snapshot**; singleton overflow or a
missing ancestor fails closed. Full-taxonomy/full-graph fallback is forbidden.

The model receives EVERY complete source incidence's name/content/detail and full selected candidate summaries. The
retrieval projection is never the model material. Existing `buildAliases` truncates at120 runes
and `buildAlignPrompt` omits detail; neither can be directly reused for this contract. Each encoded
name is at most256 bytes and summary at most1024 bytes. Canonical mapping decisions and groups are
at most2048 bytes each, group count is at most the unit count, every group must be referenced, and
every unit must occur exactly once. Whole canonical mapping work, including overhead, is capped
at32768 bytes: the per-record ceilings usually permit only about7 units, with no32-unit fit promise.

`max_tokens=65536` includes a32768-token reasoning planning allowance; that allowance is not a
provider-enforced reasoning/content split. Actual stopping, parsing, complete coverage and all
limits must be checked and fail closed. At most2 logical attempts may use fixed sanitized feedback.
Early persisted UUIDs are never reassigned; every source incidence and its material is retained.

### Description batching

Root adopted an engineering output cap that creates compression pressure, not a semantic-completeness
or quality-equivalence guarantee. For each node let `M` be the UTF-8 byte length of canonical JSON for
its exact ordered complete `(docID,memberID,path/hash,name,content,detail)` source-incidence array.
Set `C = min(24576, max(2048, 2*M + 512))`. The cap counts the encoded description string, including
JSON quotes and escapes. Model input retains every incidence and all fields, including legitimately
optional empty Detail. A valid accepted Content unit must not be rejected solely because its Detail
is optional/empty; a generated description must be nonblank and source-incidence binding validated.
This future annotation adapter's optional-field contract does not relabel the legacy Gate A method.

Use node aliases `n1` through `n8`, at most8 nodes in canonical greedy order within one Subdomain.
Before dispatch require `sum(C + 256) + 512 <= 32768`; actual per-record non-description overhead
must be at most256 bytes and envelope/separators at most512. Assert the whole canonical result size
independently. Full-input overflow triggers deterministic bisection; singleton overflow stops.
Each finite batch has at most2 logical attempts with fixed sanitized repair; no partial accumulation
or fallback is allowed. The model sees every full incidence even when the engineering output cap
requires compression.

### Relation pair work and output

Cover all same-Subdomain unordered pairs, including across batch blocks. For cross-Subdomain work,
retrieve eligible Entity/Concept candidates in OTHER Subdomains with K15, filtering before LIMIT
and applying the same deterministic ties; deduplicate unordered pair ownership. This cross routing
restricts cross-candidate recall and requires disclosure/measurement rather than an all-pairs claim.
Every selected pair permits both directions × six relation kinds, at most12 direction/kind triples.

Pack at most48 pairs **and64 distinct nodes**, not64 blocks. Pair aliases are `p1` through `p48`.
Each result row is `[direction0or1, kind, confidence]`, with finite confidence in[0,1]. Each canonical
tuple including its separator is at most48 bytes; per-pair envelope/separators at most96 bytes;
the whole envelope at most512 bytes. Assert the whole canonical response is at most32768 bytes:
`48 * (12 * 48 + 96) + 512 = 32768`. Every requested pair needs an explicit result, including an
empty result where appropriate; unknown or duplicate pair/triple results fail closed.

The model emits no edge-description prose. The adapter derives the typed description from full
names as `sourceName --[kind]--> targetName`. Lost model rationale and the new method must be
disclosed; deterministic text is not scientifically equivalent to historical model descriptions.
The model still receives full selected node descriptions. Exact serialized wire tokens plus65536
reserved tokens must fit within1M; deterministically bisect before dispatch, stop on singleton
overflow, and never trim material to fit. Cross relations and new local `contradicts` receive one
source verification. Report actual catalogue pair counts and call counts before a paid relation
stage. No such counts or successful wire/output receipts exist in phase1.

### Recovery and publication

Construct a fresh generation with explicit immutable source/checkpoint identity, preserving
legacy/new-method provenance separately. Persist immutable per-stage receipts and safe UUID/alias
mappings, preserving source/member/human-curated identity and rejecting incompatible or incomplete
recovery states. Validate a complete coherent candidate generation before atomic publication.
Schema/Bundle/query compatibility and actual production serialization/token
accounting need their own implemented and reviewed contract. No estimate from32/480 or the canonical
byte ceiling constitutes actual wire-token/output admission or a provider capacity pass.

## Validation, compatibility and rollback

Tests cover required missing/tampered dependencies, explicit pass qualification, counterfeit
rehashing with inconsistent content/identity, method/round/source mismatch, exact metadata/span
preservation, shared members, 32/33 boundaries, input order, incomplete609 rejection and output
copy isolation. An optional read-only local test ingests actual legacy dependencies and the
frozen original-only corpus; public CI uses synthetic format-compatible fixtures. Required full
race/make-verify and independent engineering/provenance/scientific review are recorded on #75/PR.

No existing production path calls this new package. No schema, existing API, Bundle, query,
dependency, defaults, model runner or release behavior changes. Rollback removes these six new
files; it neither migrates stored data nor overwrites historical evidence. Offline acceptance
is not source-quality improvement, future provider authorization or construction admission.
