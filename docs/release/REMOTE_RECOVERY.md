# Private Catalog recovery: blocked at the first asset download

The root-authorized [#44](https://github.com/xcosmosbox/Cairn/issues/44) rehearsal
stopped on its first unexpected product failure. Current private Release
distribution is **not accepted** by this evidence. The successful
[local recovery rehearsal](LOCAL_RECOVERY.md) does not replace this gate.

Native MCP GitHub App authentication initialized for App5454525/installation174795212.
The actual watcher read the isolated sandbox's stable pointer, detected A's digest,
then `kbbundle.PullRemote` returned `下载 bundle.tar.gz: HTTP 404`.
There was no installed healthy generation yet. B update, bad-candidate retention
and original-A rollback were **NOT RUN**. No retry/public-URL substitution was made.

The source at base `2d87bbdf075960eade455fac2aa2f5148ee31530` chooses
`browser_download_url` from the matching Release asset and downloads it with a
token header. This identifies the failing product route; it does not yet prove
whether GitHub's private asset URL/authentication semantics or another condition
caused the404. A subsequent root-approved read-only comparison downloaded this
same existing A asset through the official asset API: metadata200, octet-stream302,
then200 from GitHub's asset host without an Authorization header. The4830-byte
SHA256 matched the original upload. This demonstrates that the official asset
route works for the preserved private artifact; the original product route still
failed. A production fix requires a separate root-approved scope. Existing fake/loopback strict-pull tests
do not establish real private-asset download behavior.

## Exact bounded experiment

One isolated new branch and three unique prereleases/assets were created only in
`xcosmosbox/cairn-app-sandbox` (private repoID1432125826), preserving sandbox main,
older fixtures, App/installation settings and all created artifacts. A helper used
the native App JWT and a temporary sandbox-restricted token for fixture publication;
the actual MCP process independently used its native App installation-token path.
No secret/header/private credential location is retained in the report. The helper
token file was removed locally and MCP terminated after failure; no token
expiry/revocation/security-repeat experiment occurred.

| Surface | Actual result |
| --- | --- |
| Synthetic Bundle assets A(schema4), B(schema5), corrupt-checksum copy of A | Three uploads201; GitHub asset digests match the three original archive hashes |
| Initial stable pointerA | One Contents API commit201 on the isolated branch |
| Native MCP watcher bootstrap from missing local DB | App ready; selected A digest; private asset download404; stopped |
| PointerB → badA → originalA | Not executed |
| MCP business/source/negative queries and installed inventories | Not executed because initialA did not install |

The [evidence index](evidence/remote-recovery-20261024/README.md) contains immutable
remote identities and full sanitized receipts/logs. The intended sequence remains
A → B → reject badA while servingB → preserved originalA. Remote recovery stays
unaccepted until that sequence succeeds under reviewed product code and retained
published artifacts; local or extra API checks cannot stand in for the watcher.

## Explicit reproduction surface

The live harness creates new private sandbox fixtures and therefore requires
applicable authorization and the task claim/ACK; it is not a routine free smoke:

```sh
python3 scripts/release/remote_recovery.py --live-sandbox \
  --key-file /AUTHORIZED/RUNTIME_KEY --out /ABSOLUTE/NEW_RECEIPT_DIRECTORY
```

Go1.22+ and Python3.9+ are required. Source/helper/product binary identities and
actual command exits are recorded. Local preparation builds the accepted synthetic
fixture helper and packs three archives; it never calls a model. Live execution
has an overall ten-minute cap, per-condition sixty-second waits and a two-second
watch interval; it advances or stops immediately after each condition. It does
not keep polling an unexpected pull failure until green. Existing objects are
never overwritten/deleted by a reproduction; successful recovery would add only
the explicitly authorized isolated pointer commits.

The unexecuted remainder is staged code, not established evidence. After the run,
review corrected a UTF-8 log-offset calculation in that unexecuted bad-candidate
path from byte count to character count. No later scenario was run or claimed
passed, and the receipt retains the exact original script hash/source commit.
