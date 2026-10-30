# v0.1.6 ordinary construction integration and acceptance

Task [#114](https://github.com/xcosmosbox/Cairn/issues/114), parent
[#108](https://github.com/xcosmosbox/Cairn/issues/108). As of 2026-10-29 UTC.
**Overall: BLOCKED / unaccepted.** This is an execution report, not product acceptance. The ordinary wiring and
deterministic checks are distinct from actual-model semantic evidence; the latter
remains unavailable until an authenticated construction produces a reviewable graph.

## Source and ownership

Starting main `2ae4eef654e99e5d84de43b72e7dc65247c18bf1`, tree
`0be7e07577fbe94ebc189e2c03e522423bc845c6`, follows PR125 and the
[fresh .5 acceptance](https://github.com/xcosmosbox/Cairn/issues/113#issuecomment-6486028430).
The [wiring/test ACK](https://github.com/xcosmosbox/Cairn/issues/114#issuecomment-6486072351),
[integration ACK](https://github.com/xcosmosbox/Cairn/issues/114#issuecomment-6486090346)
and [runner-test amendment](https://github.com/xcosmosbox/Cairn/issues/114#issuecomment-6486149311)
define disjoint sessions. Integration session `cairn-v016-integration-20261029`
uses `work/v016-integration-20261029`; writers release commits for serial integration.
Root alone reviews publication, merges and accepts. Sessions share account, model
and environment; their separation is not independent empirical replication.

## Ordinary construction behavior

Full construction consumes supported `ScanReport.Sources` once, including nonempty
SKILL body/frontmatter, and joins every finite `DocumentProcessor` outcome before
aggregating product status. Unsupported, unavailable and cancelled scopes retain
their concrete reasons. Empty legitimate markers remain separately accounted for.
Only complete annotation candidates enter extraction; source/candidate attempts are
kept without shrinking the discovered denominator.

Accepted node helpers perform finite coverage repair and whole-material description
verification. The integration records checked alias-to-UUID correspondence, recomputes
coverage after actual final filtering and applies only complete verified content
pairs. Current source-aware relation `Selected` facets carry their classification,
Sources/Evidence through the existing assertion representation into SQLite. Full
writeback uses the actual persisted edge list, preserving unknown versus known-empty
relation context rather than fabricating explicit-source credit.

Changed construction receives complete caller-held current originals plus accepted
member material. Missing source context keeps dependent node/content/source/relation
work coherent before I-8: it cannot commit a new pair that its following writeback
does not represent, advance the failed baseline or turn the next retry into C3.
Successful independent work and human edits remain. Factual attempted, distinct
selected, duplicate and committed source counts are separate; C1 detection is
separate from actual committed progress when deciding failed versus partial.

CLI/controller entry fields preserve these results and accept existing source-context
JSON inputs. Source context lives outside the mutable managed tree; generated blocks
never become originals. Public status omits raw originals and candidate documents,
while private dumps preserve the source/candidate evidence needed for review.

## Fixed input and method

The six authoritative originals retain exact v0.1.1 bytes. Inventory SHA256:
`f0bfc3a6b8d802f3b15fe7c7e2f6db2e8c09ea4feb7564b241b76d42f78e44d8`.
The unchanged [supplement](../../../build/test/e2e/semantic-evidence/testdata/v011/supplement.json)
defines eight public source bodies, two empty skill markers and all 19 cases.
Source preparation adds a separate empty `originals/SKILL.md` marker for discovery;
it adds no substantive knowledge. The builder tree therefore has 14 substantive
sources and 3 empty markers. The original paths are prefixed `originals/`; a private
roster binds each S01–S06 ID to its exact path/hash. Frozen inputs are untouched.

The existing `inventory_span_check.py` exits 0 on the exact inventory: 51,183 bytes,
288 lines, 360 substantive units, 16 justified exclusions and 194 relation contexts.
This proves source/span accounting. It does not assess graph semantics.
Inventory, cases, expected observations and judge material stay outside the source
tree and are never sent to the builder. No QA answer success is used as acceptance.

The bounded `corpus-capacity --mode v016` adapter uses the ordinary orchestrators
and `RawTraceTransport`. Every phase writes a fresh private directory containing
source hashes, immutable originals, mutable generated source tree, request/config
identity, actual raw requests/responses/errors, stage/build reports and a read-only
SQLite node/edge/source snapshot when a usable DB exists. Missing usage/cost is unknown.
Positive semantic retry N means N extra recoveries per failed scope; the declared
campaign uses 1 extra recovery and provider retries disabled (`MaxRetries:-1`).
Per-request output 32768 tokens, timeout 600 seconds and concurrency 8 govern individual
requests/scheduling; they are not cumulative admission or completeness thresholds.

| Operation | Ordinary path and comparison |
| --- | --- |
| full | Full construction of all fixed source bytes; source-first graph/provenance review |
| repeat | Independent full construction again from pristine identical raw bytes in a fresh DB; compare semantics, identities, source sets and qualified relations |
| unchanged | Incremental Run over the prior generated tree and DB; verify stable existing UUIDs/sources/no duplicate growth separately from raw repeat |
| changed | Incremental Run after one declared outside-block source addition, with complete current caller-held original snapshots and accepted annotation material |

The fixed change appends “On 2026-10-29, Battery B's inspection result was unknown.”
to F07's `fixtures/operations/references/sibling.md`. It adds a dated unknown assertion while preserving
all old knowledge. The raw snapshot for that same source key is explicitly updated;
generated Markdown is never promoted to an authoritative original. The unchanged
six-original inventory is supplemented only by this separately declared delta.

Normal full semantic coverage and forced operational controls remain separate.
All 19 case IDs and every failed/unavailable outcome stay in the denominator;
successful siblings do not excuse missing substantive knowledge. Repaired cases
are rerun only in their affected scope. No unrelated historical paid/full609,
archive, recovery, cost/time or repeated-approval gate is restored.

## Runtime and authentic provider outcome

Only names/presence were inspected: `CAIRN_LLM_API_KEY`, `DEEPSEEK_API_KEY` and
`OIC_MANIFEST_PATH` are absent. HTTP(S) proxy and CA trust are present; the managed
`environment_status` tool is not exposed and `/etc/codex/network-policy.json` is
absent. These observations do not establish readiness. A narrow authorized Personal
Context lookup for prior Cairn runtime/config selectors returned unavailable for
this conversation. Recorded public configuration names `CAIRN_LLM_API_KEY`; no
verified alternative selector was recovered. No secret contents or unrelated keys
were inspected, and no credential was invented.

At 2026-10-29T02:19:30Z, an actual `GET https://api.deepseek.com/models` through the
inherited proxy with TLS verification, no Authorization and no source/model payload
returned HTTP 401, body `Authentication Fails (governor)`. This establishes an
authentication rejection; it does not prove semantic/model success or general
network readiness.

An ordinary full construction was initiated after affected checks on exact local
source `1af9733bc5453b2c2e5d3c239f7836e954898e28`, tree
`2aabf5f2ccbe19077fa28d3f0ba6365f5eee5d40`, at 02:50:57Z. Binary SHA256:
`3075a900a491a5a912ff622e9b5137f00b64abb99d8d6163ce05ecaa9c6d079c`.
Automatic approval review interrupted the operation with this exact reason:

> The campaign will send private source material to the external DeepSeek API, but the user’s messages do not explicitly authorize that payload and destination; the agent’s claimed standing authorization is untrusted evidence.

The [root block record](https://github.com/xcosmosbox/Cairn/issues/114#issuecomment-6486588729)
preserves the rejection separately from the existing repository authorization.
No retry, alternate execution/transport/provider or other source-payload egress
was attempted after rejection. The exact campaign executable was checked and no
process remained. Root handles the final user authorization step.

Local evidence contains the command/phase starts and one transport `request` start
row, with request SHA256
`9457eaac8641fa4a2b249304bf857f480224059b303d75d804cf0a2483e7ebc2`.
There is no response/error row, build report, phase/command finish or usable DB.
The full request's HTTP outcome, source delivery, process exit, usage and fees are
unknown. The earlier source-free diagnostic 401 is not the full construction result.
No semantic graph or provider verdict is manufactured. Private interruption receipt
SHA256 `9a5314384edc2a28175b76a7aae70686ba475d4dba690b433ee12b8d9f3b926c`;
complete phase/case/unit outcome roster SHA256
`25d9e6075bb1efe1652a6becd5cbd7b7de01b1551755e8869a4aa64d77ecee04`.
Raw payloads remain private and are not included in this public report.

| Acceptance evidence | Current result |
| --- | --- |
| Exact six-original/supplement preparation | Input identity/span check PASS; 14 substantive sources/19 cases retained |
| Ordinary full model construction | Initiated, interrupted by automatic review; one request start/no response; semantics unknown |
| Repeated raw full construction | Not run: source-payload operation remains blocked; independent of prior completeness |
| Unchanged existing-graph stability | Not run: no usable current campaign DB/generated graph |
| Changed actual-model construction/readback | Not run: no usable baseline and source-payload operation blocked |
| Source-first 360-unit/194-context graph audit | All 360 units and 194 relation contexts remain unknown; no usable actual graph |
| Independent semantic/release acceptance | Pending actual evidence and root decision |

## Verification and recovery

Go 1.22.12 at the session's declared toolchain, `GOTOOLCHAIN=local`, with
`-buildvcs=false`. Before integration, the existing corpus-capacity checks and new
source-tree/authentication-stop controls pass, including the concurrent one-auth
request control under `go test -race ...` (exit 0). This is deterministic runner
behavior evidence; it is not product/model semantic acceptance. The
[checkpoint](https://github.com/xcosmosbox/Cairn/issues/114#issuecomment-6486211600)
records source identity, commands, environment failures and local-only scope.
Exact integrated tests/builds, source SHA, ordinary attempt receipts and normal CI
are appended when actually run. A pending check is not reported as passed.

On local checkpoint `efd1540a3f3d971a1e2c34245775355311178976`, all seven affected
packages pass (exit 0): pipeline, extract, incremental, controller runner/reconcile,
corpus-capacity and dkconfig, using `go test -buildvcs=false ... -count=1`.
Log SHA256 `2eac69f9a4cfb529fd556cca1ef6923887952be8d2fff2a73aa95c02a50c8638`.
The durable controls retain full/changed graph-file-source readback, metadata-only
SKILL intake, finite recovery/exhaustion/cancellation/sibling joins, exact raw source
SHA/byte windows, supplied-input immutability, missing-context unchanged retry,
C1 first-write rollback and successful-sibling/second-failure status. Preserved
independent probes caught missing-context pair publication and premature C1 status
that the first focused suite missed; their intermediate failure history remains.
The source-context coherence and committed-readback corrections are now integrated.
The final controller release retains private factual build reports before terminal
transition, classifies proven local configuration failures permanently once, keeps
retryable HTTP 503 and actual caller cancellation distinct, and preserves the underlying
failed/partial receipt. Final production release is `18d098f`; final test release
is `62b4696057b362dd005bae5e4f221eea97677e37`. The
[author](https://github.com/xcosmosbox/Cairn/issues/114#issuecomment-6486580905) and
[test](https://github.com/xcosmosbox/Cairn/issues/114#issuecomment-6486579713)
releases retain intermediate failures and actual independent review limits.

On integrated `908726a2be418c98e0fa69cd5e6095551b77a87f`, all four controller
packages pass (exit 0), including real outer Step→runner→DocumentProcessor outcomes,
negative-token/typed-nil zero-call failure, finite HTTP 503 retries and actual cancellation.
Log SHA256 `bcd1d81603b87b92045fe643ae52093b7eb789cf109913146ced70a284e65b28`.
Both CLI binaries plus cairnd build (exit 0), and vet on the nine affected packages
passes (exit 0). Corpus-capacity builds on the exact attempted source; its concurrent
authentication/source-input guards also pass under race. The final docs only change
this sanitized evidence; normal CI remains enabled for the draft PR. No known code
defect remains unresolved in this bounded implementation. The remaining product
obligation is the blocked actual-model full/repeat/changed campaign and source-first
semantic/readback acceptance; a passing deterministic suite cannot replace it.

Preserve the interrupted attempt and all raw traces; do not reuse its output
directory or replay a possibly committed mutation blindly. Supply a supported
official provider runtime binding through the existing configured environment.
Only unchanged/changed phases require usable preceding DB/tree/source-context data;
a partial but usable baseline may proceed and its incompleteness remains in the
phase aggregate. Independent raw repeat has no prior-completion gate.
The automatic-review rejection must be resolved through the supported user/platform
authorization path before any resumed source-payload operation.
No experiment or model job is implied to run in the background by this report.

#114 and parent #108 remain unaccepted until their stated implementation, normal CI,
actual full/repeat/changed source semantics, persistence/readback and independent
review are fulfilled. The environment limitation does not reopen already accepted
.1–.5 helper scopes, and deterministic integration success cannot waive it.
