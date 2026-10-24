# Remote Catalog replay: original failure retained

Session `cairn-remote-recovery-20261024`; [CLAIM](https://github.com/xcosmosbox/Cairn/issues/44#issuecomment-6385085428)
and [ACK](https://github.com/xcosmosbox/Cairn/issues/44#issuecomment-6385089981).
Root permitted one bounded private sandbox experiment and required stopping at
the first unexpected failure, with no production changes or retry-until-green.

## Identity and outcome

Product baseline `2d87bbdf075960eade455fac2aa2f5148ee31530`; execution source
local commit `5a43d86035c2c422da834f9b5b8783486e2e160d` adds only the new harness/helper.
The [receipt](receipt.json) binds their exact SHA256 and three compiled binary
hashes. Go1.22.12; same restored toolchain/GOPATH/GOCACHE as the local rehearsal,
no GOFLAGS override. Run14:01:28–14:03:11Z; live part14:01:30–14:03:11Z,
approximately101seconds, below the ten-minute cap. Python exited1 after the
unexpected product download404; MCP termination exit-15 is deliberate cleanup.

Actual command used the authorized runtime key with its location suppressed:

```sh
python3 scripts/release/remote_recovery.py --live-sandbox \
  --key-file /AUTHORIZED/RUNTIME_KEY --out /workspace/scratch/c1bb49a45412/remote-recovery-first
```

Only the initial pointerA was committed. Native product logs show App ready,
new stableA detected, and `kbbundle.PullRemote: 下载 bundle.tar.gz: HTTP 404`.
The watcher was stopped on this first error, before its next two-second poll.
See [full product stderr](mcp-stderr.log). No RPC request was sent because no KG
installed. Therefore fail-candidate retention, live B transition, sources and
pointer rollback remain **NOT RUN**, not failures or passes of those scenarios.

## Preserved sandbox objects

Private repo `xcosmosbox/cairn-app-sandbox`, fixed ID1432125826, was checked before
mutation. New branch `acceptance/recovery-20261024T140151-2` starts at main
`12033cabbf506a59538c6cee56f44ded09509edd`; main/settings/older artifacts unchanged.
Initial pointer commit `7bcf2826e45acb91b03a564a118e627ee9eef818`, blob
`259d9f5d6a3460f11cd94ea567b2535c75969b09`, path
`knowledge-bases/recovery/stable.json`.

| Synthetic object | Prerelease ID | Asset ID | Size |
| --- | ---: | ---: | ---: |
| [A](https://github.com/xcosmosbox/cairn-app-sandbox/releases/tag/recovery-20261024T140151-2-a) |422765591|672412561|4830|
| [B](https://github.com/xcosmosbox/cairn-app-sandbox/releases/tag/recovery-20261024T140151-2-b) |422765709|672413017|4604|
| [badA](https://github.com/xcosmosbox/cairn-app-sandbox/releases/tag/recovery-20261024T140151-2-bad) |422765864|672413625|4858|

All assets are named `bundle.tar.gz`. Receipt upload responses retain server
SHA256, size, creation/update time and state; all three server digests independently
compare equal to locally computed archive hashes. These are private synthetic
fixtures, not public reproducibility or product stable releases. Missing sandbox
access for another reader remains unknown rather than pass/absent. No remote
object or token was deleted; only the helper's local0600 token file was unlinked.

## Evidence boundaries and next action

The matching Release asset URL comes from `core/kbbundle/pull.go`'s
`getReleaseAssetURLContext`; current download uses its `browser_download_url` and
token header. HTTP404 on that route is actual product evidence, while the exact
cause is not yet established. Native App readiness and successful fixture uploads
do not make this download pass. Existing strict-pull/in-flight/immutable unit tests
and the local rehearsal are distinct from real private Release distribution.

Under the separate [root-approved read-only diagnosis](https://github.com/xcosmosbox/Cairn/issues/44#issuecomment-6385165478),
one sandbox-scoped temporary token fetched existingA asset metadata200, then
requested the same asset API with `Accept: application/octet-stream`:302 to
`release-assets.githubusercontent.com`, followed without Authorization, then200.
The4830-byte download SHA256 matched original upload/server metadata. No browser
URL retry, new artifact/pointer or authentication/security-repeat experiment was
needed. [Sanitized diagnostic](asset-api-diagnostic.json) retains methods, hosts,
paths, status/Content-Type/request IDs and hashes; signed redirect query and all
credentials were deliberately excluded. Its private token file was removed
locally. This API comparison is not a product watcher success or completed recovery.

The original receipt/script hash is retained unchanged. A subsequent correction
to the never-executed bad-candidate UTF-8 log offset is documented in the
[operator report](../../REMOTE_RECOVERY.md); it has no bearing on the observed404.
No broad candidate/performance/security repeat or production fix is authorized
by this report. Root must approve bounded diagnosis/fix scope and any resumed
watcher experiment using the preserved objects. Independent review and exact-head
checks/root acceptance remain live task records, not inferred here.

Required checks for the new helper/automation/report, at local source
`97ffeb1204a3b1e6489ba0734ff5c6acf13e0565`: Python syntax exit0;
`go test -race -count=1 ./...` exit0 (14:07:48–14:08:13Z),
`make verify` exit0 (14:08:13–14:08:18Z). [Exact command receipts](required-checks.json),
[race log](repository-race.log), [verify log](make-verify.log). These engineering
passes do not override the actual private watcher404 or validate staged unexecuted
recovery code. Relative links, non-log whitespace and retained hash equality were
checked separately. No Viewer's deployment path was changed.
