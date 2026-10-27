# v0.1.3 caller cancellation outcome repair

Task: [#111](https://github.com/xcosmosbox/Cairn/issues/111). Date: 2026-10-27 UTC.
Starting main: `a4a94baa034223345e1b696f6b8800b364fb7977`, tree
`634b8b94a01aa4cb907276ffff26ba8fc771023c`.

## Fresh failure and retained history

The [prior acceptance](https://github.com/xcosmosbox/Cairn/issues/111#issuecomment-6432033920)
and [node helper report](2026-10-27-v013-node-fidelity.md) remain dated historical evidence.
Fresh semantic and contract research independently reproduced one owned node-wrapper
defect and the coordinator [reopened #111](https://github.com/xcosmosbox/Cairn/issues/111#issuecomment-6432223324).
Identity/mapping and the ordinary semantic controls passed; those results did not cover
the existing-node read interrupted by caller cancellation.

Before repair, `reflower.fuseDescriptions` classified every failed existing-node
`GetByID` as `unavailable`. A caller-cancelled or expired context therefore lost its
factual `cancelled` outcome at this boundary. The real SQLite reproduction makes
zero model calls, authorizes no replacement and leaves stored Summary/Description
and UUID unchanged. This is incorrect outcome classification, not demonstrated
content corruption or a new semantic omission.

The independent acceptance test on the exact starting main fails for both pre-cancel
and elapsed-deadline cases: each of two existing nodes reports `unavailable`, while
the mixed new node correctly reports `cancelled`. The same baseline passes healthy
existing/new processing, closed-store and missing-node classification, and retention
of a completed sibling when a later generation cancels the caller.

## Narrow correction and acceptance boundary

The code owner repairs only the I-6 node wrapper. Caller cancellation/deadline is
reported with its actual reason before/during an existing-node read; every intended
node retains its identity and factual outcome. Cancelled work authorizes no empty or
stale replacement, and already completed siblings remain usable. Genuine missing-node
and store failures remain `unavailable`; finite model retry policy is unchanged.
The production diff adds eight lines after the real `GetByID`, checking the caller's
context before classifying an ordinary lookup failure. The owner's regression also
holds the sole SQLite connection and cancels after the real lookup begins, without
sleep-based synchronization or substituting the read result.

The new acceptance tests use temporary real SQLite stores and scripted local model
responses. They verify observable writes/outcomes and retained database content,
without substituting an implementation-shaped read mock. Scripted semantic success
is a control, not empirical model accuracy or whole-original semantic certification.
The repair owner and integration/acceptance author are separate sessions; fresh
research reviewers remain separate from both authors, sharing account/environment.

Existing-target repaired Summary application still belongs to #113/#114. Ordinary
production entry wiring, surviving semantic-outcome binding, six-original source-to-graph
and raw-source repeat acceptance remain #114. Parent #108 is unaccepted. This repair
starts no #112/v0.1.4 worker and expands none of those task boundaries.

## Actual verification

Environment: Go1.22.12 at `/workspace/scratch/17701a3d35cd/toolchain/go/bin/go`,
`GOTOOLCHAIN=local`, linked-worktree `-buildvcs=false`.

| Command / source | Actual result |
| --- | --- |
| `go test -buildvcs=false ./build/internal/incremental -run TestV013CancellationAcceptance -count=1` on starting main plus independent test | exit1; cancelled/deadline existing-node outcomes incorrectly unavailable |
| Same unchanged independent test on the repaired source | exit0; mixed intended cancellations, actual reasons, retained pairs, healthy/store-error controls and completed sibling preserved |
| `go test -buildvcs=false ./build/internal/extract ./build/internal/incremental ./build/internal/pipeline ./build/test/e2e/semantic-evidence -count=1` | exit0 |
| `go test -race -buildvcs=false ./build/internal/incremental -run 'TestNodeReflowExistingRead\|TestV013CancellationAcceptance' -count=1` | exit0 |
| `go build -buildvcs=false ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental` | exit0 |
| `go vet -buildvcs=false ./build/internal/extract ./build/internal/incremental ./build/internal/pipeline` | exit0 |
| `python3 -m unittest discover -s build/test/e2e/semantic-evidence -p '*_test.py'` | exit0;10 tests |
| `python3 -m unittest discover -s .github/scripts -p 'test_*.py'` | exit0;12 tests |
| `make check-invariants` | exit0 |
| Local Markdown target validation / `git diff --check` | exit0;16 local targets / clean diff |

The prior saved-response replay remains historical evidence and is not rerun for this
read-path correction. Exact combined tree, fresh research and normal CI receipts are
recorded in the task/PR before acceptance. No paid calls, full609 run, archive audit
or local fullverify campaign is required or performed. Originals, gold, retained
responses and prior reports remain unchanged.

The I-7 relation tail must retain SHA256
`4f043628cbe327a6906c40002656e14eee877c91c8048c7b48df4679f204429d`.
The [serialized ownership acknowledgement](https://github.com/xcosmosbox/Cairn/issues/111#issuecomment-6432232230)
returns whole-file `reflow.go` ownership to the narrow .3 repair owner and suspends
the previous next-writer handoff. Only a new explicit accepted release in #111/#112
permits the next acknowledged relation owner to write the file.
