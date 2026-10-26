# v0.1.2 local endpoint configuration repair

2026-10-26 UTC (2026-10-27 in the maintainer's timezone). Task
[#110](https://github.com/xcosmosbox/Cairn/issues/110), exact main baseline
`b3bb2c3f7c5fa902503b0af784c03483af6a2ffd`, tree
`a37faa012337c798b05a2405d5015f35f415305e`.
New author sessions: `cairn-v012-repair-core-20261027` and
`cairn-v012-repair-integration-20261027`; disjoint scopes acknowledged in
[core ACK](https://github.com/xcosmosbox/Cairn/issues/110#issuecomment-6421018564)
and [integration ACK](https://github.com/xcosmosbox/Cairn/issues/110#issuecomment-6421042698).

## Reproduced defect and bounded change

The [fresh core review](https://github.com/xcosmosbox/Cairn/issues/110#issuecomment-6421003577)
reproduced a known-permanent-configuration failure with the actual production
OpenAI-compatible client: missing protocol, unsupported `ftp` protocol and missing
HTTP host were rejected locally, but the processor spent all extra recoveries and
reported exhaustion. This blocks fresh reacceptance under the existing contract;
the previous PR118 acceptance remains a dated historical event.

The existing annotation `modelError` classifier now recognizes only the specific
`*url.Error` inner causes `unsupported protocol scheme ...` and
`http: no Host in request URL` as permanent. Provider HTTP status retains precedence;
other wrapped URL/network failures remain unavailable. The original error chain
and concrete reason survive. Generation, assessment and revision use this same
existing boundary. No new URL validator, provider/transport policy, retry loop,
schema or orchestration change is introduced.

Core commit `2eacbee4280d8807f3ddaf26445836030806fb68` was cherry-picked as
`a80380fe329cb37292bba84fc9ce11f7523da8ac`; both have core result tree
`b7d11f287b457efcdee0ca591f12ae2e6a41183a`. Integration adds independent actual-client
controls and this dated report/current snapshot. The final published head/tree are
recorded in the live PR and task checkpoint; publication and acceptance remain
separate events.

## Actual deterministic evidence

| Case | Actual work and outcome |
| --- | --- |
| Actual production client with `ftp://invalid.invalid/chat/completions`, `api.invalid/chat/completions`, `http://`; processor extra allowance 2, provider retry disabled | Before repair: all three cases fail the regression, each with three unavailable annotate attempts and exhaustion. After repair: each has one permanent annotate attempt, unavailable final outcome, no exhaustion and the original reason. Local rejection makes no wire/model call. |
| Wrapped error classification controls | Known local causes remain permanent with original chain; URL-wrapped unexpected EOF/timeout, plain configuration phrases and status503/429 text remain unavailable. |
| Actual local HTTP503 / HTTP429 during assessment; processor extra allowance 1 | Each makes three wire requests: successful annotate, unavailable assessment, successful identical assessment. Candidate identity and assessment request bytes are unchanged; generation is not repeated. The503 body includes protocol/host/key/token decoys. The actual429 path returns its typed rate-limit error. |
| Actual client timeout30ms under a live caller; extra allowance 1 | Two local HTTP annotate attempts, both unavailable with concrete timeout reasons; unavailable/exhausted. Caller context remains live. |
| Caller cancellation during actual local HTTP work; extra allowance 2 | One annotate attempt, cancelled outcome, no exhaustion or dependent assessment. |

Tests use the production client against locally rejected endpoints or loopback
`httptest` servers; provider HTTP retries are disabled to expose the document
allowance. No paid/external model request is made. The new HTTP test fixture's first
run waited indefinitely for server cleanup; explicit fixture release channels
corrected cleanup, and the final focused, affected and race runs below completed.
This was a test fixture issue, not an additional product finding.

Fresh [C4 scope review](https://github.com/xcosmosbox/Cairn/issues/110#issuecomment-6421025670)
and [source-semantic contract review](https://github.com/xcosmosbox/Cairn/issues/110#issuecomment-6420966613)
passed their respective baseline scopes. The core researcher also verified actual
production-client truncation recovery; the unavailable-versus-invalid attempt label
is a recorded nonblocking observation, with no truncation change in this repair.
These scoped findings do not substitute for reviewing the repaired exact head.

## Validation and remaining acceptance

Go1.22.12 linux/amd64, `GOTOOLCHAIN=local`, integration runs with `TZ=UTC`.
Go commands use `/workspace/scratch/17701a3d35cd/toolchain/go/bin/go`.
Core old-fail/fixed-pass evidence is in its
[checkpoint](https://github.com/xcosmosbox/Cairn/issues/110#issuecomment-6421067721).
The combined core patch and final boundary test file passed:

| Command | Result |
| --- | --- |
| `go test ./build/internal/annotation -run '^TestV012ProductionClient' -count=1 -timeout=10s` | exit0 |
| `go test ./build/internal/annotation ./build/internal/incremental ./build/test/e2e/semantic-evidence -count=1` | exit0 |
| `go vet ./build/internal/annotation ./build/internal/incremental` | exit0 |
| `go build -buildvcs=false -o /tmp/cairn-v012-repair-integration-bin/ ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental` | exit0 |
| `go test -race ./build/internal/annotation -run '^TestV012(ConfigError\|ProductionClient)' -count=1 -timeout=30s` | exit0 |
| `git diff --check`; targeted `gofmt -l`; new report local links | pass |

The build retains the existing linked-worktree VCS-stamping workaround. Normal CI
uses its ordinary checkout and remains required. Exact-head research/CI, root
publication approval and eventual acceptance/merge are pending at this local
checkpoint. Separate sessions share GitHub account/provider infrastructure; none
claims provider-diverse independence. Root alone accepts and merges.

The five-file allowlist is `build/internal/annotation/document_processor.go`,
`build/internal/annotation/zz_v012_config_error_test.go`,
`build/internal/annotation/zz_v012_client_boundary_test.go`, this report and
`docs/coordination/CURRENT_STATE.md`. Connector publication must match the tested
local tree and have actual main as its single parent; no generic history upload.
Original reports, all source/gold fixtures and historical packets remain immutable.
No paid originals/full609 replay is required or claimed. Full-entry wiring, final
product aggregation and integrated source-to-graph acceptance remain #114; #111
has not started. This repair does not establish graph utility or release readiness.
