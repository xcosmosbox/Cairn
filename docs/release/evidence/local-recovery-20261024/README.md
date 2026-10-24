# Local recovery evidence, 2026-10-24

Session `cairn-release-readiness-20261024`; [#40 CLAIM](https://github.com/xcosmosbox/Cairn/issues/40#issuecomment-6384945379)
and [ACK](https://github.com/xcosmosbox/Cairn/issues/40#issuecomment-6384949911).
No production code, schema, format, defaults, existing tests or dependencies changed.
This report is submitted for independent review/root acceptance, not stable acceptance.

## Identity and successful replay

[Completed receipt](completed-run-receipt.json): 2026-10-24T13:48:35–13:48:38Z,
Linux x86_64, Go1.22.12. Product source is base
`c6a2b16f21434147947f4ed30d65a06321571546`; new helper/harness were uncommitted
at execution and are identified by exact SHA256 in the receipt. The same helper
and harness bytes are committed in `19d684849a1de7eeb97f8fde5990650cdddb4f4e`.
No `GOFLAGS` override was used. Pinned Go was restored by coordination from the
official archive with independently checked SHA256; a prior archive connection
timeout belongs to environment restoration, not this replay's product result.

Actual command:

```sh
python3 scripts/release/local_recovery.py --out /workspace/scratch/c1bb49a45412/local-recovery-third
```

Build environment: `GOTOOLCHAIN=local`, Go executable
`/workspace/scratch/c1bb49a45412/cairn-toolchain/go/bin/go`, GOPATH
`/workspace/scratch/c1bb49a45412/cairn-toolchain/gopath`, GOCACHE
`/workspace/scratch/c1bb49a45412/cairn-toolchain/go-build`.

The receipt retains all31 subprocess commands with exit/output and four complete
real stdio MCP sessions. Actual `cairnctl` installation selected schema4 A,
schema5 B, then the preserved original A. CLI/MCP known-content and retired-content
negative queries passed on every selection and after both rejected candidates.
Stored source path/span remained owned by the expected live UUID. Each Bundle's
original manifest and digest remained unchanged. Inventories include every
regular file's SHA256, size, inode and nanosecond mtime; both retained installed
generations and original source Bundles remained identical to their own initial
inventories. Installed bytes also matched the original Bundle bytes.

The corrupt-checksum pull exited1 with the checksum error. The separate missing-FTS
candidate had recomputed valid file checksums and v1 aggregate digest; verification
reached the query-contract error after checksum/aggregate validation, and its
pull exited1 with that same error. Both preserved healthy `current` and all
observed candidate/healthy bytes. These expected nonzero commands are successful
rejection checks, not erased failures.

## Retained unsuccessful harness attempts

| Attempt | Exact retained receipt | Why incomplete / correction |
| --- | --- | --- |
| First, 13:45:57–13:46:49Z | [attempt1](attempt1-receipt.json) | Harness incorrectly expected full64-character install directory. Actual supported implementation uses16-character digest prefix and full manifest verification. Assertion corrected; product installation had succeeded. |
| Second, 13:47:22–13:47:24Z | [attempt2](attempt2-receipt.json) | Fixture supplied colon-bearing `source_refs` (`path:3-5`), which production metadata correctly omits under its safety contract. Fixture now uses the relative path and separate stored line-span fields. No production sanitization changed. |

Both receipts remain `incomplete`. Three product binary hashes are identical
across all attempts; only fixture/harness expectations changed. Neither failed
attempt is a schema incompatibility or a lost valid source citation.

## Required verification

| Actually executed check | Result / retained raw evidence |
| --- | --- |
| `python3 -m py_compile scripts/release/local_recovery.py` | exit0; focused syntax check |
| Existing8 compatibility/lifecycle/immutable tests, race/count1 | exit0; [exact command](focused-race-command.json), [log](focused-race.log); includes true v4 startup/hotswap byte equality, invalid swaps and complete in-flight reader lifetime |
| `go test -race -count=1 ./...` | exit0, 13:50:07–13:50:40Z; [log](repository-race.log), [identity/exits](required-checks.json) |
| `make verify` | exit0, 13:50:40–13:50:50Z; vet, all8 binaries and architecture invariants; [log](make-verify.log) |

The required source checks used commit `19d684849a1de7eeb97f8fde5990650cdddb4f4e`.
Subsequent changes only index/rename the receipt and add this evidence report.
Relative-link and exact script-hash verification passed. Whitespace validation
excludes raw `.log` files: the retained CLI `why` output has one trailing space
after its empty summary; the original command output is preserved.
No pressure iteration, timeout relaxation, fixture assertion relaxation, repeated
paid/free baseline, model call, App call, remote pointer, tag or release occurred.

## Scope and unresolved limits

The process replay tests Linux local Bundle selection and CLI/stdio MCP startup;
existing focused tests establish live in-process hotswap/read-only behavior.
It is synthetic data, not real extraction, a deployed daemon recovery, an OS matrix,
arbitrary old library/binary downgrade, Catalog rollback or quality evaluation.
Final candidate full verification remains separate. See [operator paths and #33 risk](../../LOCAL_RECOVERY.md).
The original #33 failure remains unknown/unfixed: the accepted disposition is a
conditional explicit missing-evolution-history warning risk, not proof of
concurrent-history completeness. Independent review/root acceptance and exact
remote PR/CI status remain task records, not claims made by this file.
