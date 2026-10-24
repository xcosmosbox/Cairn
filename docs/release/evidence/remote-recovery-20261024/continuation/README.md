# Existing-object native watcher continuation

One root-admitted continuation passed **A4 → B5 → rejected badA while servingB →
originalA4**. This report awaits independent evidence review/root acceptance; it
does not publish stable or turn the [original404](../README.md) into a pass.

Session `cairn-remote-recovery-resume-20261024`; [CLAIM](https://github.com/xcosmosbox/Cairn/issues/44#issuecomment-6385371351),
[ACK](https://github.com/xcosmosbox/Cairn/issues/44#issuecomment-6385375135),
[prereplay limit clarification](https://github.com/xcosmosbox/Cairn/issues/44#issuecomment-6385436134),
[independent guard PASS](https://github.com/xcosmosbox/Cairn/issues/44#issuecomment-6385465241).

## Frozen identity and actual command

The [receipt](receipt.json) binds harness source
`a025546ab84cccb8ebbf83252782fe30439d009d`, script SHA256
`03bc8b32bbfabb9ce9a67fd1b59bc085496a6e2d3150f30ded3d63d4fd554fb6` and product
source `5d99a24b94b1465b59cc19f0de772a499451fe91`, the accepted #47/PR51 merge.
The product built from a separate clean checkout; `go version -m` attests that
exact revision and `vcs.modified=false`. MCP SHA256:
`161f882245689198dbb034ff1ce5ca6a641128d350878429673113bc37fe90ad`.
It was independently inspected before live execution and rebuilt identically;
the runner rejects a different binary hash. Go1.22.12, no GOFLAGS override.

```sh
python3 scripts/release/remote_recovery.py --live-sandbox \
  --key-file /AUTHORIZED/RUNTIME_KEY \
  --out /workspace/scratch/c1bb49a45412/remote-recovery-continuation \
  --resume-receipt /workspace/scratch/c1bb49a45412/remote-recovery-first/receipt.json \
  --resume-work /tmp/cairn-remote-recovery-tp9141jh \
  --product-source /workspace/scratch/c1bb49a45412/cairn-remote-product \
  --expected-mcp-sha256 161f882245689198dbb034ff1ce5ca6a641128d350878429673113bc37fe90ad
```

The runtime key location is suppressed. Resume mode guarded fixed private
repoID1432125826, the preserved branch's exact initialA tip and catalog,
original3Release/assetIDs/metadata and complete retained fixture/archive hashes.
It skipped every branch/release/upload/initialA creation path. Native App
authentication inside the actual MCP independently fetched the published Bundles;
the helper never substituted downloaded bytes into its install directory.

## Observed result

| State | Actual product observation |
| --- | --- |
| Initial published A4 | Native watcher bootstrap from missing local DB, A digest/swap complete, LegacyAnchor positive/ModernAnchor negative, stored source UUID/path/span correct |
| B5 | Actual watcher completed B swap, ModernAnchor positive/LegacyAnchor negative, source metadata correct |
| Published checksum-invalid badA | One actual checksum mismatch; healthy B `current` and complete inventory unchanged, B query/source/negative checks still pass |
| Original published A4 rollback | Actual watcher completed A swap; original A content/source/negative checks pass; A and retained B SHA256/size/inode/nanosecond mtime inventories identical to their initial installed inventories |

Installed A/B bytes also match each original source Bundle inventory. Both retain
the original manifests/digests; rollback selected existing A rather than rewriting
it. [Raw product stderr](mcp-stderr.log) retains all successes and the expected
checksum rejection. No unexpected failure or retry-to-green occurred.

Only three remaining pointer commits were created in preserved branch
`acceptance/recovery-20261024T140151-2`:

| Pointer | Commit |
| --- | --- |
| B |`325d7b5a94dcd575b6337699b9540ab2655d7e45`|
| badA |`66548864c502dce3a60ddf05fa1a458e963e5be0`|
| originalA |`73ea1b31aa199ab0b969fcd0fbf70c86515ac34f`|

Original initialA commit `7bcf2826e45acb91b03a564a118e627ee9eef818`, all three
prereleases422765591/422765709/422765864 and assets672412561/672413017/672413625
remain preserved. Their asset IDs/names/digests/sizes/creation/update times/state
matched the original metadata before and after the replay. No reupload, new
branch, new initialA commit, object deletion, installation setting or model call.

## Limits and counts

Live execution14:35:21–14:37:20Z,119.582seconds, single attempt under600seconds.
State phases were approximately15.159s initialA,10.697s B,9.987s badA and9.950s
rollback; each was bounded by a90second deadline covering API/RPC/waits.
Required final state/query/inventory checks completed14:36:58Z; MCP was terminated
immediately before the final three metadata GETs. Exit-15 is this intentional
stop. The CLI runner exited0. No process/token file remains.

Observed native log events:4new-digest detections,3completed swaps,1intentional
pull/checksum failure.12MCP tool RPCs plus initialize, and12instrumented Python
API requests (including the three pointer PUTs and pre/post metadata reads).
The separate helper token exchange is recorded in its sanitized command response.
Unchanged-digest successful polls and native watcher HTTP request counts are
**unknown** because the accepted product binary is uninstrumented. The10second
poll interval and≤3postcondition-poll operational target are configuration/intent,
not a claimed observed bound. Root clarified that before the run; no correctness,
byte-preservation, rejection or rollback criterion was relaxed.

## Verification and remaining boundary

The explicit [four offline guard regressions](test_preflight_guards.py) reject
empty receipts, another branch, wrong asset IDs and wrong initial tip before output
creation/key access. Actual execution:4tests/0.260s/exit0 with
`CAIRN_RECOVERY_PRODUCT_SOURCE` set to the clean accepted5d99 checkout; Python
syntax and non-log whitespace checks exit0. Required source checks/remote CI and
independent final review are recorded separately before root acceptance.

Required local checks at final report source
`8168a98775127bc07affd3674636b0bb024a79fb` all exited0:
[four offline guards](offline-guards.log) at14:42:53–14:42:54Z,
[full Go race/count1](repository-race.log) at14:42:54–14:43:18Z and
[make verify](make-verify.log) at14:43:18–14:43:23Z.
[Exact command identities/exits](required-checks.json) and
[post-run original-source/inventory equality](post-run-local-verification.json)
are retained. Later edits only index these checks; the executed harness hash and
clean merged product binary remain unchanged. Remote exact-head CI and final
independent evidence review/root acceptance are not inferred from these local runs.

This is one private sandbox, one installation, synthetic two-generation data and
stdio MCP on Linux. It establishes actual private asset download/watcher update,
invalid-candidate retention and pointer rollback under this bounded scope. It does
not establish natural token expiry/401 recovery, full build-daemon lifecycle,
production throughput/long soak, arbitrary OS/binary/state downgrade or real-model
answer quality. Final stable candidate verification remains separate.
