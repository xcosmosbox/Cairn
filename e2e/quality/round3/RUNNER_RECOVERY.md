# Runner admission and recovery boundary

The current source adds offline-tested authorization stop and judge durability
handling. This does not modify an archived implementation freeze or enable the
Source Backfill paid pilot. `run_source_backfill_pilot.py run` still returns
`execution_blocked_before_credential_access`; #13 must review the exact new
source and a separate pilot decision remains necessary.

## Stop and drain

The native solver and judge latch HTTP 401/403 at observation using the same
lock as new request admission. Neither retries those statuses. Previously
admitted calls may still send or finish: their response, known usage and final
result are retained. Native queued jobs remain explicit error results with
`network_attempted=false`; the judge status file lists cases not admitted.
Non-authorization HTTP errors retain the existing retry policies. A stopped run
exits unsuccessfully even when all its ledgers remain internally consistent.

Judge request admission verifies both ledgers and fsyncs the request event
before submitting HTTP work. An append failure stops new admission but does not
unwind result collection. Both the usage event and grade are independently
attempted for every returned response. A failed primary append produces a
separate fsynced `*-ledger-failure-*.json` receipt containing the original
record. The original file is not rewritten or presented as repaired. The run
fails after draining, and `GRADES_PATH.run-status.json` records collection,
not-admitted cases, failures and conservative accounting. This status file is a
summary; the primary ledgers and independent receipts are the audit evidence.

No software can guarantee a durable receipt on a filesystem that rejects both
primary and recovery writes. Such a recovery write failure is reported and the
run remains failed; it is never represented as verified persistence. This
implementation does not claim cross-process admission coordination or repair of
an arbitrary historic filesystem incident. Use one runner process per output.

## Crash recovery

Judge resume requires complete JSONL rows, matching input/config/source hashes,
unique ordered request attempts, matching response/result identities, and no
unresolved failure receipts. It rejects symlinks and live inode/size changes.
A durable request with no response keeps its full reserved token estimate,
explicit `usage unknown`, and is never automatically resent. New unrelated
cases may run within the remaining original budgets. A response missing its
grade, a partial append, a duplicate/orphan response, or a mismatched identity
requires an explicit audit; resume does not infer success or reconstruct data.
The native solver continues requiring a new empty output directory.

## Freeze migration

1. Preserve all historical protocols, inputs, gold, controls, execution README,
   freezes, outputs and raw failure bytes at their original identities.
2. After review, pin a new source commit and hashes for the solver, judge,
   durable ledger and their tests. The judge config now includes the
   `cairn-durable-judge/v1` instrumentation identity and exact source hashes;
   an older ledger fails identity validation rather than silently mixing runs.
3. Create a separate, explicitly reviewed freeze amendment/new pilot freeze.
   Record which old freeze it supersedes for future execution, and compare the
   unchanged model, questions, gold, search/model-turn counts, prompts, context
   limits and output/token budgets. Do not automatically replace a freeze or
   reuse an old output directory to make the hashes pass.
4. Keep previously paid controls and their denominators. Review offline mock
   receipts and all changed source bindings before deciding any future paid
   invocation. This repair itself grants no paid execution or credential scope.

Local regression command:

```sh
python3 -m unittest discover -s e2e/quality -p 'test_*.py'
python3 e2e/quality/export_receipts.py --self-test
```

The runner tests use synthetic inputs and fake keys, block real socket creation,
and exercise concurrent 401/403, non-auth HTTP errors, live ledger replacement,
partial request writes, response write failure/draining and crash recovery.
They establish these boundaries, not online answer accuracy or provider billing.
