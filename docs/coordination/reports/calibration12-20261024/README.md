# Frozen12 actual calibration: failed coverage, retained diagnostic

Task [#63](https://github.com/xcosmosbox/Cairn/issues/63). Sole actual attempt on
source `1f49dc0af50bc4619ae8b2a888f6589351f7a144`, 2026-10-24. Public archival
report prepared by session `cairn-calibration63-recovery-20261025` on 2026-10-25;
report base `a1a016f92cd2883675ed54f9f6afcbefa4a823d4`.

**Result: FAIL.** All twelve frozen originals reached annotation, but only eleven
received accepted annotations and materialized members. The largest original,
`6f388affbc74a3719f00167cd84e7a04981655e63c7acdab04d0c82d57ebb92b`, exhausted
all three annotation/semantic-gate rounds. Its 71,119-byte body describes a long
78-offer list. Each rejection identified substantial missing content; some also
identified fidelity or classification concerns. These are fallible model judgments,
not an independently complete factual-recall score. The document was skipped and
has no accepted member or source row. Retaining all 298 accepted member incidences
from the other eleven documents does not repair the missing twelfth document.

The process finished naturally at `2026-10-24T16:04:50.897136Z`, exit **1**, after
797.424 seconds. The underlying builder returned no execution error and the ledger
reconciled; the capacity audit failed. There was no observed context overflow,
output-length finish, HTTP failure, missing usage, outer timeout or whole-run retry.
This diagnoses a construction/coverage failure under the frozen profile. It does
not establish sufficient capacity or general semantic correctness.

## Public packet and historical identity

| File | Meaning |
| --- | --- |
| [result.json](result.json) | Per-stage usage/size/timing extrema, all twelve per-document counts, audit dispositions and read-only diagnostic summary |
| [execution-manifest.json](execution-manifest.json) | Historical source/tree/binary/compiler/counter pins and exact adopted profile, with local paths omitted |
| [restricted-archive-manifest.json](restricted-archive-manifest.json) | Metadata-only remote commit, ordered parts and all 143 file sizes/hashes; no article or model text |
| [recovery-verification.json](recovery-verification.json) | Actual read-only recovery checks, claim/ACK identities and checks not reexecuted |

The actual source tree was `6b75632af16c00f25066f43e4e8264d931ae45ee`, clean at
admission. The historical binary SHA256 is
`08751f0fed7d819b2b0aa579590f11b5400799e6d94fcac7c8aefba6d35062ef`;
the build used pinned official Go 1.27.3 linux/amd64. Pre-dispatch identities and
independent structural-pin review were recorded before the sole invocation:
[claim](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6386239878),
[root ACK](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6386245957),
[admission](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6386261318),
[actual process start](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6386266878).
The process receipt starts at `2026-10-24T15:51:33.473946Z`; the driver run-start
receipt follows at `15:51:33.803685253Z`.

The frozen profile requested `deepseek-flash` at the official chat-completions
endpoint, JSON object output, thinking enabled, effort omitted, no sampling
overrides; all 385 responses returned that model alias. A specific served backing
revision is unknown. Output reserve was 65,536 tokens; HTTP timeout 600 seconds;
stage/overall timeout 5,400 seconds; logical concurrency 8; configured HTTP retries
3, annotation rounds 3 and rollbacks 3; node confidence minimum 0.7; cross-evidence
limit 1,048,576 bytes. Acceptance required aggregate actual incidence at least 0.95,
all twelve documents nonempty, and valid claimed bindings. The exact per-wire
official-counter input plus reserved output guard was 1,000,000 tokens. Source-only
body/wrapper/JSON sizes (162,880 / 166,300 / 170,541 bytes) were lower-bound diagnostics,
not token-fit proof.

The archive retains freeze/run/ledger pins and parsed artifacts. It does **not**
contain the original executables, standalone compiler receipt, standalone tokenizer
runtime receipt/native wheel, or full raw HTTP bodies. Recovery verified the retained
bytes and internal consistency; it did not rebuild the historical binary or rerender
requests with a newly installed tokenizer. Historical pre-dispatch review and archived
385-attempt counter comparisons are separate evidence from this recovery.

## Actual requests, usage and coverage

There were 385 logical calls, 385 admitted wire attempts, 385 HTTP 200 responses and
385 `stop` finishes, with one wire attempt per call. Every response reports usage;
provider input counts equal the official local input counts on all 385 attempts.
Totals are **466,741 input + 547,983 output tokens**. These are reported token counts,
not a fabricated monetary cost. Peak logical concurrency was 8.

| Stage | Calls | Input tokens | Output tokens | Maximum input | Maximum output |
| --- | ---: | ---: | ---: | ---: | ---: |
| Annotation | 19 | 96,451 | 213,260 | 17,115 | 25,922 |
| Semantic gate | 19 | 193,238 | 89,156 | 35,349 | 8,330 |
| Extract | 1 | 15,367 | 46,296 | 15,367 | 46,296 |
| Describe | 24 | 64,509 | 57,272 | 7,035 | 6,124 |
| Relate | 24 | 42,700 | 129,296 | 4,357 | 13,711 |
| File slug | 298 | 54,476 | 12,703 | 193 | 223 |
| Total | 385 | 466,741 | 547,983 | 35,349 | 46,296 |

The whole-run maximum output was **46,296 at extract call 39**, not the annotation
maximum of 25,922. Maximum input was 35,349 at semantic-gate call 36. All observed
requests fit the frozen wire guard; normal `stop` and fit do not prove factual
coverage. Per-stage HTTP durations and byte extrema are in result.json; concurrent
durations must not be summed as wall-clock elapsed time.

The failed original's annotation calls 10/29/37 have identical system/user hashes
and input counts of 17,115. Parsed rejected rounds contain 72, 136 and 57 items,
respectively. The old orchestrator sent the same original-only request after each
semantic rejection; the previous candidate and critique did not reach the next
annotation request. The gate critique itself can name unsupported specifics, so
neither its omissions nor the article title's count is a gold annotation denominator.
The failed candidates, GateA results and all three gate rejections remain nested
under their original 01/02/03 stage paths in the private packet.

The corrected audit defines incidence as distinct `(original_doc_id,
accepted04member_id)` pairs from `04_extract_input` before fusion, confidence filtering
and drops. That accepted set contains 298 pairs, all present in the candidate and
database: **298 / 298 = 1.0**, with zero missing accepted incidences in each of the
eleven accepted documents. result.json supplies each document's original wrapper/body
hashes, round disposition, accepted/candidate/materialized counts, including the
twelfth document's zeros. A zero accepted count does not mean that failed original
had no factual content to preserve. The audit retains four hard errors: missing
annotation, no materialized member for that original, incomplete original-document
denominator, and skipped/failed selected documents.

## Graph, source evidence and semantic limitations

The retained diagnostic database has 331 nodes, 822 edges and 298 source rows;
SQLite `quick_check` is `ok`. Its SHA256 is
`53a44663ff1d82477a9e29b5a2114c72e1432033f954e9b75a6b5869169ac901`.
The accepted members have matching stored source bindings. All 298 original-span
pairs are NULL, meaning **unknown**, with zero invalid non-NULL spans. Document/hash
ownership does not establish exact original positions or factual recall.

Cross-subdomain candidates were **0**. Every disposition is therefore zero:
accepted, execution error, insufficient evidence, invalid verdict, rejected endpoint,
rejected evidence, rejected relation and rejected semantic. Supported/materialized
supported candidates and mechanically verified cross quotes are also zero. This is
no positive cross-subdomain semantic evidence. The cross audit also preserves the
incomplete original-document denominator error.

There are twelve stored same-subdomain `contradicts` edges. Independent inspection
identified two semantic type errors in this frozen database: edge 615 relates
refurbished-purchase savings to avoiding older Intel Macs, compatible purchasing
dimensions; edge 782 relates meditation relief to anxiety under other conditions,
not strict logical incompatibility. These brief descriptions explain the errors
without redistributing originals. The other ten edges remain unadjudicated. The
existing cross-subdomain verifier did not evaluate these local edges; zero cross
candidates cannot certify them. The inspected originals are pinned respectively by
wrapper hashes `b9231dc6caba2e549d80f473e9fbbff0c407ea90ffe2e4be1d3936a221b337c1`
and `70d7eea69421ada6d275df421870f34796c98c1752df54eb340651dc37746ac2`.
Diagnostic source offsets are not stored native span evidence.

Historical free native read-only checks retained CLI status plus 16 MCP RPC receipts
covering eleven document source-path resolutions and a name search, process exit 0.
The database hash/bytes/inode/mtime were unchanged before and after; these prove
bounded read-only mechanics on this failed diagnostic, not answer quality. The first
wrapper incorrectly assumed `source_metadata.locations` lived at the content root
and failed its assertion. Its available receipts remain; the initial raw RPC dump
was not written before that assertion and is unavailable. The corrected schema-aware
lookup used a separate `read-only-v2` namespace, with no product edit or model call.
Recovery did not repeat those native calls.

## Immutable private archive and actual recovery

The authorized restricted archive is in `xcosmosbox/kd_manifeat`, commit
`1a44fa27db4221778da3986efa5f4414c6f27611`, prefix
`calibration12-20261024/attempt-01`. Access is required to retrieve raw originals,
parsed annotations, graph/source files, stage dumps and RPC payloads. The public
manifest exposes only names, byte lengths and hashes. Original articles, model
payloads, credentials and headers are not published here. Raw response bodies were
hash-only in the reviewed trace; parsed stage outputs are retained.

The gzip archive is **1,684,762 bytes**, SHA256
`90d58abe40b6eb29c2234578ba8bae08a9be93dd3d53460114c673faac36b278`, transported
in 26 ordered parts. The 25 first parts are 65,536 bytes each; the last is 46,362.
All per-part SHA256/Git blob SHA1 identities and all 143 extracted file hashes are
in the public metadata manifest. This is a derivative manifest with additional
remote-location fields; its bytes differ from the original private manifest, whose
own Git blob/SHA256 identities are also recorded.

After environment loss the old calibration worktree/runtime were absent. At
`2026-10-25T01:07:44.631664Z`, this recovery fetched **the existing** 26 parts at the
pinned commit, decoded base64, verified each part's byte count/SHA256/Git blob SHA1,
concatenated in ascending order, verified archive bytes/SHA256, extracted regular
files safely, and verified every one of 143 file byte counts/SHA256. Local Python
verification exited 0. SQLite inspection used `mode=ro&immutable=1`; per-document
counts, ledger event matching and the database identity were reconciled. No private
archive object was replaced or uploaded, and no provider request was made.

Authorized reproduction follows that same order: fetch the original manifest at
the pinned commit; fetch each named part at that commit; check part hashes before
concatenation; check the full archive before decompression; reject absolute/traversal
or non-regular tar members; check all file hashes before opening the database
read-only. Preserve the failed outcome and original files. The ledger is 806,077
bytes, SHA256 `dcf65fc30f454f8e0d499b33ee4b868344e77e8fee06087060fb971e3d2249b2`.

Recovery ownership:
[takeover claim](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6391754253),
[coordinator ACK](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6391759171),
[append-only lease correction](https://github.com/xcosmosbox/Cairn/issues/63#issuecomment-6391760692).
The interruption and prior archival receipt remain in
[root checkpoint](https://github.com/xcosmosbox/Cairn/issues/9#issuecomment-6386677971).
Archival review/acceptance is separate from the failed calibration outcome.

## Bounded follow-up proposals, not new run admission

[#64](https://github.com/xcosmosbox/Cairn/issues/64) separately scopes semantic
feedback correction: unchanged first request and finite limits, then full-original,
fallible-candidate/critique replacement after a semantic rejection. A minimal future
live check can reuse a hash-verified retained failed GateA candidate and its exact
rejection, run one bounded corrective annotation plus one semantic gate, and retain
both results without rerunning the twelve-document paid control. Candidate round 2
(136 items) is an available checkpoint, not accepted truth. Exact corrected source,
request hashes/token guard, chosen checkpoint, two-call budget and failure rules
must be frozen and reviewed before a separate technical GO. No such calls are part
of this report; even a later success cannot change this historical failure.

A separate narrow relation task can extend the existing post-relate/pre-UUID
verifier to all **new** `contradicts` candidates, including same-subdomain endpoints,
using its subject/property/time/condition/incompatibility and original-quote checks.
It should validate cross contradictions once, preserve human-curated existing edges,
identify scope/kind in reports, cover true-contradiction and compatible-dimension /
changed-condition fixtures, and explicitly address incremental new-edge paths.
This proposal does not widen validation to every local relation or auto-relabel a
mitigation as another relation kind; no implementation is included here.

Full609 construction, QA/gold/HOLDOUT inspection or scoring, answer-quality claims,
remote Bundle publication, a candidate freeze and stable release remain outside
this attempt and this archival report. No completed paid control was rerun.
