# Historical preparation and subsequent real execution

The sections below preserve the earlier preparation/rejection record. They are
historical, not the current authorization or execution state. The newly retained
[real execution report](../../../docs/coordination/reports/issue16-realmodel-20261024-live16/README.md)
records the actual passing canary, negative first attempt and two-document second
attempt. Both original `audit.json` failures remain unchanged. A separate corrected
free `audit-v2.json` accepts optional original position hints as unknown, verifies
document/member ownership and generated-block diagnostics, and still **fails the
required direct dependency**. No graph-benefit, exact-original-citation or stable
claim follows from consumption. Current helper checks and this correction are
covered by focused offline tests; original preparation receipts remain unchanged.

# Preparation evidence and outstanding execution

Session `cairn-realmodel-20261024-prep`, baseline
`18e7e7bfc989fc0216625b0851e105690edd3950`, branch
`experiment/real-multidoc-smoke-20261024`.
[Claim](https://github.com/xcosmosbox/Cairn/issues/16#issuecomment-6384277024),
[timestamp amendment](https://github.com/xcosmosbox/Cairn/issues/16#issuecomment-6384283201),
[ACK](https://github.com/xcosmosbox/Cairn/issues/16#issuecomment-6384283657).
GitHub event times supersede manually entered future timestamps; ACK's timestamp
was separately amended in comment6384287627. These are preparatory write records,
not model spending authorization or result acceptance.

## Completed free checks

[commands.json](verification/commands.json) records exact arrays, actual UTC
times, exit statuses and logs for four offline failure/contract tests, the free
prepare command, and diff whitespace validation. All three commands exited0.
The tests check frozen input mutation/extra-file/reuse rejection, credential opt-in
before key read, and an explicitly mocked unexpected-model response. That mocked
response is never counted as an external request or successful canary.

[preparation.json](verification/preparation.json) freezes the originals and planned
model settings, with `model_calls=not_run`. [environment.json](verification/environment.json)
records Go1.22.12/Python3.12.14 and SHA256 for the three real binaries. Earlier in
this session, `GOFLAGS=-buildvcs=false make build` completed all eight binaries
with exit0 using the previously verified toolchain and existing module/build
caches. This is a build observation, not a graph, model or stable-release result.

The isolated runtime contains untouched originals plus a mutable copy of exactly
one Skill and two reference documents. Existing public inputs, historical receipts,
benchmark corpus, questions, gold and HOLDOUT were not changed. No real sandbox
mutation or model retry was performed after the rejection below.

## Blocked live canary

The earlier attempted action was a single JSON connectivity request to the official
`https://api.deepseek.com/chat/completions` endpoint with exact `deepseek-flash`,
thinking disabled and 64 output tokens. The automatic approval review rejected
credential egress. Its sanitized rejection text was:

> The request transmits a bearer API key to an external DeepSeek endpoint, but trusted user text does not authorize using or disclosing that credential to this destination.

The rejection further instructed:

> Do not bypass this rejection through a workaround or indirect execution. Continue with a safer alternative, or carry out checks to prove that the action is authorized or low risk before trying again. Complete unaffected work without asking for confirmation. Report anything that remains blocked, clarify why it was blocked by auto-review, inform the user of the risk and ask for approval.

No HTTP/model completion, returned model ID, usage receipt or graph outcome exists
for that attempt. It must not be labeled a successful call or measured provider
failure. The key remains only in the private runtime destination outside git;
its value and location are omitted here. Applicable earlier broad user authorization
was acknowledged, but the execution agent did not retry through another agent,
endpoint or transport after this rejection. Current external credential authorization
must clear the review before the concrete live commands in [README.md](README.md)
can run.

## Not run and next evidence

Real canary, two-reference ingestion, query/provenance audit, semantic relation/fact
review and Bundle publication are all **not run**. The helper's unexecuted audit
path is code awaiting the real artifact, not runtime evidence. #16 remains partial
and open. No GraphRAG accuracy, cross-document relation preservation, fresh answer
benefit or first-stable readiness is claimed.

After authorization is resolved, use a new runtime directory, freeze the inputs,
run exactly one canary, inspect the returned model identity, then run the bounded
smoke and free audit. Preserve all failed stages and original/rewritten span
semantics. A separate scope would be needed for native controller/Bundle sandbox
publication and per-call returned-model tracing. The planned corpus-only12-document
admission and larger benchmark remain separate and HOLDOUT stays closed.
