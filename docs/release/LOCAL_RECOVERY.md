# Local compatibility and recovery rehearsal

This is the bounded synthetic operator rehearsal for [#40](https://github.com/xcosmosbox/Cairn/issues/40),
following the [release contract](STABLE_CONTRACT.md). It does not accept a stable
release, choose a version or establish real document extraction/answer quality.

## Reproduce

From a clean pinned checkout, with Go1.22+ and Python3.9+, run:

```sh
python3 scripts/release/local_recovery.py --out /ABSOLUTE/NEW_RECEIPT_DIRECTORY
```

The output directory must not exist. The script builds `cairn`, `cairn-mcp`,
`cairnctl` and a synthetic fixture helper. It writes a receipt even after a failed
assertion; temporary binaries/fixtures/installed generations remain at the recorded
work directory for diagnosis. Dependency downloads may need network access; the
replay itself uses no network, model, credentials, daemon or Catalog.

The helper uses current storage APIs to create a schema5 graph, then removes
`nodes.file_slug` and declares schema4 for generation A. This exercises the legacy
table shape; it is not a claim to have restored arbitrary historical user libraries.
Generation B has the current schema5 shape and different known content. Each has
one synthetic human-curated node and stored source reference/span. `Model: none`
and the synthetic manifest identities must never be presented as an actual build.

| Rehearsal | Required observation |
| --- | --- |
| Local `cairnctl bundle pull` A → B → preserved A | Same original manifest/digest on rollback; existing A inode, modification time and SHA256 inventory retained |
| CLI and fresh stdio MCP on each selected generation | Current known content, retired-content negative result, stored source metadata, schema4/5 declaration |
| Query each selected library | Original source and installed Bundle bytes/inodes/times preserved; true v4 still lacks `file_slug` |
| Corrupted-checksum candidate | Nonzero exit with checksum mismatch, healthy `current` and candidate bytes unchanged |
| Missing-FTS candidate with recomputed valid checksums/aggregate digest | Verification reaches the query-contract error; install rejects it and retains healthy `current` |

`receipt.json` includes the source SHA, dirty-path record, helper/harness hashes,
Go/environment identity, binary hashes, actual command exits/output, RPC payloads,
manifests and per-file inventories. Retained [evidence](evidence/local-recovery-20261024/README.md)
records the exact run and separates initial harness failures from product behavior.

Live in-process hot swap and in-flight request lifetime use the existing
`TestLegacyV4StartupAndHotSwapRemainReadOnly`,
`TestHotSwapKeepsCompleteRequestAlive`,
`TestSymlinkDoesNotRetargetAcquiredConnections`,
`TestInvalidSwapPreservesHealthyGeneration` and
`TestInstallReusesImmutableGenerationWhileReaderPinsRollback` regressions.
Their evidence is separate from restarting a process after a local pointer change.
The local command does not tell an already running MCP process to reload: pause
the Catalog watcher/daemon before rollback and restart consumers on the selected,
validated configuration. Remote stable-pointer rollback needs a separately
authorized reviewed Catalog update. Minimum reader semver enforcement, arbitrary
binary/controller-state downgrade and other operating systems remain unverified.

## Operator build/update/recovery paths

`cairn-ingest --repo WORKSPACE --db WRITABLE_COPY` is the full-build entry;
`cairn-incremental --repo WORKSPACE --db WRITABLE_COPY` applies incremental edits.
Both require their model configuration and modify the writable workspace/database.
They are documented entry points, not executed by this free fixture rehearsal;
fresh real build/update/source-preservation evidence is a separate release gate.
Never point either command at an installed immutable generation. Preserve source
workspace, matching binary/configuration, original verified Bundle and Catalog
commit before an upgrade. Test builder migrations only on an explicit copy.

For local recovery, stop the selecting watcher/daemon, list preserved generations,
reinstall the verified previous Bundle with `cairnctl bundle pull --src-dir`, then
check `cairn --db-path .../current/knowledge.db status` and known/negative content
before restarting readers. The release contract supplies concrete command flags.
Retain controller state/config snapshots taken after stopping the daemon; restore
matching snapshots/binaries for downgrade. `retry`/`unblock` change controller run
state and do not repair a KG or downgrade its schema.

## Evolution-history limitation: #33 remains open

The retained [original failure and bounded diagnosis](../coordination/reports/issue33-lock-20261024-runtime/README.md)
show `evolve.Record` failing at `evolution.db` `BeginTx` with `SQLITE_BUSY`.
Source inspection places that return before selecting the parent, creating the
snapshot or inserting changeset/metrics rows. It does not demonstrate a corrupt
KG or partially published changeset. The original lock owner, cause/fairness and
broader production risk remain **UNKNOWN**; passing retries do not establish a fix
or a test-only/flaky classification.

In `cairn-ingest` and `cairn-incremental`, the evolution sidecar runs after the
main build/update and a Record error logs `[evolve]` as a nonfatal warning under
R-ev-1. A successful build can therefore lack an expected history entry. Root's
proposed first-stable local CLI/MCP/Bundle scope accepts this explicit residual
warning risk without promising complete concurrent evolution audit/history,
conditional on separate review of this failure boundary. [#33](https://github.com/xcosmosbox/Cairn/issues/33)
stays open and unfixed; other callers/configurations need their own evidence.

On this warning, retain the error/time, source Bundle/DB digest, build result,
toolchain and number of concurrent recorders. Check the evolution manifest,
changeset/metric counts and referenced snapshots before using history as an audit.
Keep the successful KG/Bundle; do not rebuild or edit an installed generation to
repair history. Stop competing writers before taking a consistent diagnostic
copy. A later successful observation records a later observation and does not
reconstruct the lost transition. Use #33's bounded diagnostic method in an
environment that reproduces the failure; do not mask it by increasing timeouts,
deleting concurrency tests or repeatedly rerunning until green.
