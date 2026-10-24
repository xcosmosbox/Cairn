# Explicit live GitHub App acceptance

This bounded harness exercises actual `core/githubapp.AppAuth` and the daemon's
`HTTPForge` against the dedicated private `xcosmosbox/cairn-app-sandbox` repository.
The registered fixture identities are App `5454525`, installation `174795212`,
repository `1432125826`. It does not accept arbitrary repository write targets.

Ordinary Go tests exclude `zz_live_acceptance_test.go` through its `livegithubapp`
build tag. Even with that tag, the test skips without `CAIRN_LIVE_GITHUB_APP=1`.
Supply the authorized key only as the runtime file named by
`CAIRN_APP_PRIVATE_KEY_FILE`; never commit a key, credential configuration, token,
authorization header or private file reference. The runner inherits those variables
and records command arguments/output, not environment values.

```sh
# Activate an existing Go 1.22+ toolchain and dependency cache first.
# Set CAIRN_APP_PRIVATE_KEY_FILE locally to the authorized user-supplied runtime file.
export CAIRN_LIVE_GITHUB_APP=1
python3 build/test/e2e/github-app/run.py --output /ABSOLUTE/NEW/RECEIPT_DIRECTORY
```

The output directory must be new. The runner stops on a failing stage; to diagnose
the independent Forge stage after inspecting such a failure, its exact command is
`go run -buildvcs=false ./build/test/e2e/github-app` with the same opt-in/runtime
environment. Output contains a sanitized failure message, never API response bodies.

The native auth test verifies App/installation identities, accepted JWT, native
installation token, cache hit, sandbox access, and reduced-token Contents-read access
plus a denied write. Only an empty sandbox is initialized, with an acceptance README
on main. A permission failure that unexpectedly creates its probe file is cleaned
with the native token and fails the test. Other existing content is preserved.

The revocation probe deletes only the token created by that invocation, then polls
private sandbox access at most 12 times with two-second waits. It requires an observed
401 and records initial 200 responses. Native `InstallToken` still returns that
revoked cached credential. The test then sets only its cached expiry to +60 seconds,
causing the actual production refresh branch to exchange a new token with GitHub.
This accelerated cache-boundary test does not wait out a natural one-hour lifetime
or establish transparent 401 recovery. No production auth source is changed.

The Forge stage uses actual `HTTPForge` branch create/read, PR create/read/replay,
and prerelease create/replay. A direct Contents API request supplies the test commit.
Branches, unmerged PRs and prerelease fixtures remain for inspection; they are not
product releases. No release assets or merge operation are tested. Created credentials
receive cleanup revocation requests, with unsuccessful cleanup causing nonzero exit.
The intentional all-repository installation configuration is preserved; native tokens
use its default scope, while only the negative-test token is narrowed explicitly.

See the [recorded live acceptance](../../../../docs/coordination/reports/issue15-real-app-20261024/README.md)
for actual source identities, failures, commands, receipts and remaining boundaries.
