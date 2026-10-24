# Issue #15: real GitHub App acceptance

As of 2026-10-24 12:05 UTC. Session `cairn-15-20261024T1157Z-app`.
[Task #15](https://github.com/xcosmosbox/Cairn/issues/15),
[claim](https://github.com/xcosmosbox/Cairn/issues/15#issuecomment-6383910235),
[scope amendment](https://github.com/xcosmosbox/Cairn/issues/15#issuecomment-6383917307),
[delegated coordinator ACK](https://github.com/xcosmosbox/Cairn/issues/15#issuecomment-6383922110).

**Real native App authentication and dedicated sandbox operations passed.** GitHub
accepted the native JWT, resolved the correct installation, issued an installation
token, allowed private sandbox access, denied a reduced-token write, and accepted a
real native refresh exchange after an explicitly injected cache-expiry boundary.
Actual `HTTPForge` created/read/replayed a branch, PR and prerelease.

Immediate revocation rejection did not hold: an initial verifier assumption failed.
The retained second attempt observed real rejection after about two seconds, before
refreshing. Transparent recovery from 401 and natural one-hour expiry are not claimed.
Maintainer acceptance and independent-session review remain separate from these runs.

## Source and registered scope

| Item | Recorded identity |
| --- | --- |
| Product baseline | `10f2f4d44b01b9e8657136af8094353a24eec4e2` |
| Initial tested harness | `5e765ddeb21cfc7aa032a4eb28ec10eac11b02f2` |
| Final live/ordinary tested harness | `c1c3c90d6626b974b9cb9a5c527ae2dbea890775` |
| App | `5454525`, `xcosmosbox-cairn-test` |
| Installation/account | `174795212` / `xcosmosbox` |
| Runtime repository | `xcosmosbox/cairn-app-sandbox`, private, ID `1432125826` |
| Key reference | user supplied runtime file |
| Installation selection | `all`, intentionally authorized by the maintainer |
| Required effective installation permissions | `contents: write`, `pull_requests: write`, `metadata: read` |
| Native token request | Installation default; production `InstallToken` POST has no narrowing body |
| Negative-test token | Repository ID `1432125826`; `contents: read`, `metadata: read` |

[Full registered permission matrix](registered-permissions.json) preserves the
complete nonsecret installation permission map returned by GitHub, not only the
required subset. This configuration is accepted as intentional and was not changed.
All repository operations targeted the sandbox. No denied-repository exclusion claim
or unrelated-repository probe is made. The negative test establishes reduced-token
permission enforcement inside the sandbox.

The product auth/Forge sources were unchanged from the baseline. Their Git blob
identities, OS and actual toolchain are in [environment.json](environment.json).
The Go executable was the verified #10 toolchain at
`/workspace/scratch/110c4593aeb1/cairn-env/.local/toolchain/go/bin/go` (Go 1.22.12).
The existing `.local/gopath` module cache and `.cache/go-build` cache beside that
toolchain were reused, with `GOTOOLCHAIN=local`. No secret values, authorization
headers, runtime key location or private artifact reference are saved here.

## Actual commands and outcomes

The [runner](../../../../build/test/e2e/github-app/run.py) saves exact command arrays,
UTC times, source SHA and exit status. It never records inherited environment values.

| Result | Command / scope | Exit | Receipt |
| --- | --- | ---: | --- |
| FAIL, preserved | `go test -tags=livegithubapp -count=1 -v -run '^TestLiveAppAcceptance$' ./core/githubapp`, initial immediate-401 assumption | 1 | [attempt 01](attempt-01/01-native-auth-live.log) |
| PASS | Same native live command with bounded real revocation observation | 0 | [attempt 02 auth](attempt-02/01-native-auth-live.log) |
| PASS | `go run -buildvcs=false ./build/test/e2e/github-app` | 0 | [attempt 02 Forge](attempt-02/02-native-forge-live.log) |
| PASS | `go test -race -count=1 ./...`, ordinary build excludes live test | 0 | [race](checks/01-go-race.log) |
| FAIL, environment/worktree VCS stamping | `make verify` | 2 | [original](checks/02-make-verify.log) |
| PASS | `GOFLAGS=-buildvcs=false make verify` | 0 | [VCS workaround](checks/03-make-verify-buildvcs-off.log) |
| PASS, SKIP as intended | Tagged test without live opt-in | 0 | [opt-out](checks/04-live-optout.log) |

The first `make verify` failed at Go's VCS status lookup (exit128), before builds;
the explicit `-buildvcs=false` workaround is the same worktree approach documented
by existing product performance measurements. No Makefile, source or lockfile was
modified to address that environment limitation. There were no paid/model runs.
The retry completed vet, the eight binary builds and architecture invariants.
The [artifact manifest](artifact-manifest.json) binds the report, matrix, environment
and all original command receipts to their exact byte counts and SHA256 values.

## Native authentication, permission and recovery observations

1. Actual `AppAuth.AppJWT` signed the runtime key. `GET /app` returned200 with the
   expected App ID; `GET /app/installations/174795212` matched App/account/permissions.
   Actual `ResolveInstallation` returned the same installation for the sandbox.
2. Actual `InstallToken` made a real POST201. A second call returned the same
   credential/expiry and made zero additional exchange requests. Private sandbox
   metadata and `main` branch were readable. The sandbox already had `main`; the
   empty-repository bootstrap path was not executed.
3. A separate official API request used the native JWT to mint the reduced read-only
   token. Its Contents permission was checked as `read`, branch/main read returned200,
   and a unique content-write probe returned403. No denied probe file was created.
4. Actual revocation returned204. The exact token passed to DELETE was the one
   retrieved again from the native cache; equality and zero exchange count were
   asserted without logging either value. Sandbox access initially remained200,
   then became401 on the second bounded poll, about two seconds later. Attempt01
   recorded the same immediate200 and failed its premature401 assertion; it was
   not replaced or relabeled as a pass.
5. Native cache reuse after revocation is observable; there is no transparent401
   invalidation/retry in the inspected product code. Only after confirmed401 did
   the test set that cache entry's expiry to `time.Now()+60s`. The real native
   refresh branch then made one live POST201, produced a different credential,
   and private sandbox access returned200. Clock, JWT, network and HTTP response
   were not fabricated. This is recovery via accelerated cache expiry, not automatic
   401 recovery or a natural one-hour passage test.

Both attempts requested revocation of all credentials they created (two and three,
respectively), and the Forge stage requested revocation of its own token; every
cleanup request returned204. These cleanup status receipts do not independently
wait for each token's propagation; only the dedicated revoked token was polled to401.

## Actual sandbox Forge fixtures

| Object | Identity / behavior |
| --- | --- |
| Base `main` | `12033cabbf506a59538c6cee56f44ded09509edd` |
| Branch | `acceptance/app-acceptance-20261024T120229-318`; actual create and readback matched base |
| Supporting fixture commit | `6821f7d768a91735bef9085ef4a10acdd7b8e8c3` via Contents API |
| PR | [sandbox PR #1](https://github.com/xcosmosbox/cairn-app-sandbox/pull/1), actual create/read; replay returned same number |
| Release | [sandbox acceptance prerelease](https://github.com/xcosmosbox/cairn-app-sandbox/releases/tag/app-acceptance-20261024T120229-318), ID `422706406`; replay returned same ID |

The source of branch/PR/release operations was the actual `HTTPForge` using actual
`AppAuth`; only creation of the one-line fixture commit used a direct Contents API
request. PR and release readback identities were validated by the helper. A separate
connector readback also saw PR #1 open, unmerged, bot author
`xcosmosbox-cairn-test[bot]`, and the same head/base SHA; that metadata readback is
additional evidence, not a substitute for the native authentication run.

The test PR remains open and unmerged; its branch/tag/prerelease remain reviewable
in the dedicated private sandbox. Release assets, merge operations, Git CLI push,
daemon/MCP full lifecycle and real Bundle publication were not exercised here.
The prerelease is a test fixture and makes no product stable-release claim.

## Existing upper-layer boundaries and handoff

The baseline `core/githubapp/README.md` already records these boundaries, confirmed
by source inspection rather than new integration scenarios: daemon selects the first
configured repository's installation (`build/cmd/cairnd/main.go`); MCP selects one
valid watched catalog installation (map iteration order can affect the first choice),
then shares it across watches (`service/cmd/cairn-mcp/main.go`). A per-installation
token cache therefore does not establish upper-layer multi-installation routing.

`InstallToken` holds its global mutex during network refresh. An already-canceled
waiter rechecks cancellation only after acquiring that mutex; immediate cancellation
while waiting is not guaranteed. These existing documented limitations and the
observed revoked-cache behavior are not silently fixed by this verification change.
Separate routing/cancellation/401-recovery implementation work requires its own scope.

To reproduce, check out the tested harness (or this report's final branch), activate
the existing Go environment, and follow the [explicit runner instructions](../../../../build/test/e2e/github-app/README.md)
with an authorized user-supplied runtime key and a new output directory. Reproduction
creates new sandbox fixtures and mints/revokes new credentials; it does not reuse old
tokens or overwrite old receipt directories. Review the exact final evidence commit
and required gates before maintainer acceptance. No process or paid run is left
running after the recorded command receipts complete.
