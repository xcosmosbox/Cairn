# Issue #33: bounded lock observation

**Root cause remains unknown. No product fix or stable-readiness conclusion.**

Session `cairn-runtime-stability-20261024`, current source base
`18e7e7bfc989fc0216625b0851e105690edd3950`, Go1.22.12 linux/amd64.
[Claim](https://github.com/xcosmosbox/Cairn/issues/33#issuecomment-6384266457)
and [ACK](https://github.com/xcosmosbox/Cairn/issues/33#issuecomment-6384283418)
authorize only this report directory, three original targeted race iterations and
at most one controlled case within180 measured seconds. No controlled case was needed.

## Distinct evidence

| Observation | Result and scope |
| --- | --- |
| Original uninstrumented push CI, head `a4621b569d30da0ec3631117945fb9f0670372c6` | [Job120845608409](https://github.com/xcosmosbox/Cairn/actions/runs/40084863404/job/120845608409), Ubuntu24.04.5, Go1.22.12, `go test -race -count=1 ./...`, exit1. The concurrent test took8.85s and failed at `zz_readonly_test.go:272`: Record BeginTx returned `database is locked (5) (SQLITE_BUSY)`. [Retrieved original log](original-ci-job-120845608409.log). |
| Earlier author isolated test | Same head/toolchain, race count30,0fail; [original receipt](https://github.com/xcosmosbox/Cairn/pull/32#issuecomment-6382989072). Not rerun here. |
| Single authorized CI retry | Attempt2/job120846345632 success, [preserved disposition](https://github.com/xcosmosbox/Cairn/issues/33#issuecomment-6382996773). Does not supersede failure. |
| New instrumented isolated test | Current base, original test/assertions, race count3, exit0,5.695s wall.27Records (3seed+24concurrent),0Record errors. [Full log](targeted-race.log), [summary](summary.json). Does not establish original failure cause. |

## Measured lock boundaries

Across24 concurrent Records, the largest successful BeginTx call took
**1238.178ms**. The largest interval from successful BeginTx return to Commit return
was **132.182ms**. The latter is an upper interval for the transaction holding the
write lock after BeginTx returned, including commit work; it does not expose the
internal SQLite lock acquisition/release instants. All24 BeginTx requests began
while at least one ReadManifest call was active. The existing assertions passed:
each iteration observed9 final changesets and matching metrics/lineage/snapshot paths.

These intervals show actual serialization and wait, while remaining below the
default5000ms busy timeout in this isolated run. They cannot explain which CI
writer/operation held the lock, establish fairness, or prove that broader pressure
caused the original failure. The report is a bounded negative reproduction with
new timing evidence, not a flaky/test-only classification or repair.

## Connection and transaction semantics inspected

`openEvolutionDB` uses a separate SQLite handle per Record, maximum one open
connection, WAL and IMMEDIATE transactions. `storage.OpenSQLite` supplies
`busy_timeout(5000)` on the driver connection. Record begins before selecting
the parent and holds its transaction through source snapshot/validation, diff,
metrics and changeset inserts until commit. `ReadManifest` opens mode=ro and
query_only, using one read-only transaction for both lists. The pinned modernc
v1.30.1 driver sends `begin immediate` for non-readonly `_txlock=immediate`,
and plain `begin` for read-only transactions. Existing Go `database/sql` transaction
semantics and per-connection timeout were retained. Candidate long-hold or scheduling
paths remain hypotheses because no failing lock trace was observed.

## Reproduce this diagnostic

Run from the pinned repository with the accepted Go toolchain/dependency cache.
`instrument.py` rejects a changed source hash, produces the exact retained
[overlay source](instrumented_record.go.txt) and refreshes local absolute paths in
`overlay.json`. No production/test source is edited. It adds clock observations,
atomic manifest counters and one end-of-Record JSON line; this overhead may change
timing and must be considered when comparing with uninstrumented CI.

```sh
python3 docs/coordination/reports/issue33-lock-20261024-runtime/instrument.py
go test -race -c -overlay docs/coordination/reports/issue33-lock-20261024-runtime/overlay.json -o /tmp/cairn-runtime33-diagnostic.test ./core/evolve
python3 docs/coordination/reports/issue33-lock-20261024-runtime/run_diagnostic.py
```

Compilation used `GOFLAGS=-buildvcs=false`, `GOTOOLCHAIN=local`, and the previously
verified Go1.22.12 executable at
`/workspace/scratch/110c4593aeb1/cairn-env/.local/toolchain/go/bin/go`;
GOPATH and GOCACHE point to that restored environment. Compile exit0, outside
the measured run. The runner executes exactly count3, with170s test timeout and
180s process wall limit. These bound diagnosis and do not change SQLite timeouts.
Load average after the diagnostic:0.428/0.227/0.135; shared host with other agents,
not equivalent to the GitHub runner. No paid/model/App request, fixture edit or
full-suite rerun occurred.

Next actionable step is capture these boundaries in an environment that actually
reproduces the failure, including load/scheduler identity and the erroring writer.
Do not increase busy_timeout, remove the test, or declare success from nonreproduction.
Issue33 remains open for that evidence and a separate fix decision if warranted.
