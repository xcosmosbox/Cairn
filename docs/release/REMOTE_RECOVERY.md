# Private Catalog recovery

The first root-authorized [#44](https://github.com/xcosmosbox/Cairn/issues/44)
rehearsal stopped at a private asset download404. After the independently reviewed
asset-API fix, one existing-object continuation passed A→B→rejected badA→originalA
with the actual native-App MCP watcher. [Continuation evidence](evidence/remote-recovery-20261024/continuation/README.md)
is submitted for separate review/root acceptance. This is bounded synthetic
private-sandbox evidence, not stable-release acceptance or a capacity guarantee.

Native MCP GitHub App authentication initialized for App5454525/installation174795212.
The actual watcher read the isolated sandbox's stable pointer, detected A's digest,
then `kbbundle.PullRemote` returned `下载 bundle.tar.gz: HTTP 404`.
In that original attempt no healthy generation installed. B update, bad-candidate
retention and rollback were **NOT RUN** then. Its failure remains immutable; the
later continuation used reviewed code and the same published artifacts.

The source at base `2d87bbdf075960eade455fac2aa2f5148ee31530` chooses
`browser_download_url` from the matching Release asset and downloads it with a
token header. This identifies the failing product route; it does not yet prove
whether GitHub's private asset URL/authentication semantics or another condition
caused the404. A subsequent root-approved read-only comparison downloaded this
same existing A asset through the official asset API: metadata200, octet-stream302,
then200 from GitHub's asset host without an Authorization header. The4830-byte
SHA256 matched the original upload. This demonstrates that the official asset
route works for the preserved private artifact; the original product route still
failed. The separate fix is #47/PR51; the continuation uses its accepted merge.
Existing fake/loopback strict-pull tests
do not establish real private-asset download behavior.

## Original bounded experiment

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
A → B → reject badA while servingB → preserved originalA. The later continuation
exercised that sequence with reviewed code and retained artifacts; its acceptance
remains a root decision. Local/API comparisons do not replace actual watcher evidence.

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

After the original run, review corrected a UTF-8 log-offset calculation in its
then-unexecuted bad-candidate path from byte count to character count. That original
receipt retains the exact original script hash/source commit and is never rewritten
as a later pass.

## Admitted existing-object continuation

After the private asset fix was independently reviewed and merged at
`5d99a24b94b1465b59cc19f0de772a499451fe91`, root admitted one continuation using
the preserved branch/releases/assets and only the remaining B→badA→originalA
pointer commits. It requires separate quick guard review and a clean merged-source
MCP binary with attested source/hash; it does not recreate or overwrite fixtures.
Original failure/diagnostic files remain immutable.

Before the continuation, root clarified the three-postcondition-poll number as
an operational target, not a measured hard correctness/budget gate. The hard
limits are one attempt, ten minutes overall live time and ninety seconds per
phase. A ten-second watcher interval is configured. The watcher stops immediately
after required final state/query/inventory checks, before remote metadata
readbacks. The receipt records observed digest/swap/error log events, actual RPC
and helper HTTP counts. Successful unchanged-digest polls and product HTTP request
counts are uninstrumented and remain unknown; no ≤3 observed-poll claim is made.
This prereplay clarification does not relax healthy-generation, provenance,
checksum or exact-rollback criteria. Continuation results remain a separate report.
