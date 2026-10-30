# Small production baseline

Run this finite synthetic baseline before an end-to-end corpus campaign. It uses
the ordinary production Full/Incremental builders, actual SQLite persistence and
Markdown/sidecar writeback, then the real CLI and MCP read-only query paths.
The independent `oracle.json` is read only by the checker, outside builder input.

From the repository root, with Go 1.22+ and Python 3.9+:

```sh
python3 -m venv /tmp/cairn-mini-python
. /tmp/cairn-mini-python/bin/activate
python3 -m pip install -r build/test/e2e/mini-baseline/requirements.txt
make test-mini-baseline
# CAIRN_LLM_API_KEY is supplied by the configured runtime; never put it in argv.
make e2e-mini OUT=/absolute/path/to/new-mini-result
```

`--go /path/to/go` selects an existing toolchain. Each attempt needs a new output
directory. The driver builds the existing `corpus-capacity`, `cairn` and
`cairn-mcp` commands there and preserves production stage reports, source identity,
raw provider traces, errors and query results. Output is private by default;
review before publishing any raw trace. Credentials are never printed by this
wrapper. Provider mode and observed returned model names appear in `result.json`;
offline controls provide no live model credit. Missing usage remains unknown.

The sequence is full → unchanged → C4 changed addition → changed-unchanged →
independent full repeat of the pristine input. Each phase is isolated. Both
unchanged phases require zero model requests, no changed documents, equal semantic
graph rows and identical output files. Normal file-state/manifest bookkeeping may
change SQLite bytes. Queries have no API credential and must preserve database and
source/output bytes. CLI exit 0 with no expected UUID is a failure.

The full follow-up always needs an explicit source-only Markdown tree:

```sh
python3 build/test/e2e/mini-baseline/run.py \
  --out /absolute/path/to/new-campaign \
  --source-tree /absolute/path/to/full-source-only-tree
```

Only a mini PASS launches that separately selected full run. No previous/private
corpus location is a default. Full execution still needs its own independent
semantic acceptance; mini success does not alter any full-corpus denominator or
acceptance criteria. This task does not run that follow-up.

The three entirely original Chinese source bodies are 558 UTF-8 bytes in total.
`fixtures/change.md` is a separate source-only addition, never an initial input.
Print their fixed SHA-256 identities without a model call:

```sh
python3 build/test/e2e/mini-baseline/run.py --manifest
```

| Source | SHA-256 |
| --- | --- |
| `mini/SKILL.md` | `4be96fa2831738ed5fd5f195118b62a4e6364663ce28c1408dc454365385c767` |
| `mini/references/log.md` | `12134a5e43b8099d97fcd0c4078c6bb37393f4ee6740263356e1e528d4a12368` |
| `mini/references/pass.md` | `b629da1920566d7116a2ef84a0f47d26db7ba9025b07abe250c4a258bacfaadb` |
| Separate addition | `469241318c8d65c511c71d66a83ab4160fd241875871b30e756d1dc70ded943c` |

PASS covers three uniquely identified semantic nodes, their required source
incidences, ticket/count key values in the correct node and generated primary,
the actual directed `depends_on` edge with explicit exact-source byte/line evidence,
UUID persistence on incremental paths, source-document mirrors, writeback metadata
fidelity and query wiring. Independent repeat records actual UUID, source/member
and classification drift rather than equating identity drift with lost facts. Six
offline mutations of the persisted positive must be blocked: deleted/reversed required edge,
value moved to an unrelated node, invalid source path and incorrect relation source
hash, and missing source-document pairs. An UNKNOWN control result proves that the
wrapper blocks false success, not that it has established a semantic error.
No total node-count or generated-prose byte identity across independent full
runs is required. Unit controls use handmade local artifacts and are labelled as
such; the live campaign repeats controls against its actual production output.

These checks do not prove complete semantic understanding of condition, negation
or version scope, absence of all bugs, or whole-corpus quality. Review the final
three target bodies and relation evidence for the three semantic statements listed
in `oracle.json`; record that review separately. C4 here is one addition to an
existing source, not deletion, movement or arbitrary repeated change handoff.
Human-edit behavior remains covered by existing affected production tests, not by
a claimed live human-edit phase. A partial builder result stays PARTIAL even if
some facts survive; unavailable preparation/provider evidence is UNKNOWN, and
dependent phases remain UNRUN. Historical full360/194/19 standards are unchanged;
v0.1.6 remains unaccepted.

Actual campaign outcomes, execution/checker identities, observed request/usage
totals and remaining limits are recorded in [task #127](https://github.com/xcosmosbox/Cairn/issues/127).
Offline controls and normal CI never stand in for live or full-corpus acceptance.
