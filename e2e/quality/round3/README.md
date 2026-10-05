# DEV source backfill experiment

This is an evaluation adapter, not production source hydration. It keeps the Go
adapter's node selection and ranking, then may substitute one verified original
text chunk from an exact source path attached to each returned node. Empty node
results stay empty. Unknown paths, unmatched text and repeated best chunks retain
the original node and an explicit diagnostic.

The complete frozen protocol, input identities, execution snapshot and audit
records are distributed through the authorized private review bundle in
`xcosmosbox/kd_manifeat`, under `evaluations/cairn-source-backfill-20261005/`.
They are not included in this public code directory. Restore that bundle and its
matching frozen inputs before running the experiment. No provider key is stored
in either code or reports.

The historical run is bound to the source files and README in its implementation
freeze. This public README is a later, general usage guide; the private archive
preserves the original execution README byte for byte. To reproduce the exact
snapshot, use the archived files. Do not silently rewrite historical paths,
protocols, databases or gold answers to make a different environment appear to
be the same baseline.

Synthetic checks can run without those private inputs:

```bash
python3 -m unittest discover -s e2e/quality -p 'test_*.py'
```

In a restored experiment workspace, `source_backfill_round3.py freeze` accepts
`--protocol` and `--output`; `run-offline` accepts `--freeze` and a new `--output`
directory. The freeze binds the exact protocol, source modules, wrappers, native
harness, Python runtime and inputs. Existing freezes and outputs are not
automatically overwritten. The fixed comparison has 960 standalone query cells,
320 native budget traces and 48 opaque evidence packets from six DEV questions.
Only actual native-visible text belongs in semantic review; standalone cells,
titles and source paths cannot substitute for factual evidence.

`semantic_gate_round3.py` validates all packet, mapping and independent-review
entries, requires exact visible body-text quotations, and reports supported-fact
additions and losses. Offline factual support is not solver answer accuracy.
`future-paid-preflight` only lists the bounded future pilot parameters; it makes
no provider call. Paid execution still requires a positive reviewed gate, fresh
identity checks, a dedicated runtime key and a separately reviewed runner using
the unchanged native protocol and durable ledger.

`run_source_backfill_pilot.py` separately pins the offline report, independent
gate, implementation freeze and existing paid controls, then binds its own
preflight source and tests in an additional freeze. It provides a concrete
twelve-task plan without rerunning controls. Its `run` command remains disabled
and exits before credential access: the frozen solver and judge still need a
reviewed global stop on authorization failure, and the judge needs durable
admission and complete collection of in-flight responses after a write failure.
A valid preflight is not a provider run or an online quality result.
