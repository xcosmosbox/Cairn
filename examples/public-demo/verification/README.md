# Public demo verification — fixed fake only

Task [#16](https://github.com/xcosmosbox/Cairn/issues/16), session
`cairn-16-20261024T0956Z-env`, 2026-10-24 10:01 UTC.
Base implementation: `9a07c54d681e52b14458645dc79ffb07aa35da3f`.
Tested public-example commit: `9995d9a867943bdfe398c1c8f1d8ba49c3a5f4ff`.

**PASS: the public fixed-fake command and negative examples.**
**NOT RUN: consuming the new multi-document Skill input or extracting its relationship.**
These are separate claims. The latter remains an acceptance gap if #16 requires actual
multi-document consumption; this report does not lower or silently satisfy that condition.

## Reproduction and environment

A new detached worktree was created at the tested commit with
`git worktree add --detach .local/issue16-clean 9995d9a867943bdfe398c1c8f1d8ba49c3a5f4ff`.
Its initial worktree was clean, with no `bin/` or `.local/` directory. From that checkout:

```bash
python3 examples/public-demo/demo.py
```

This is the same example command documented in the public README. It exited 0 from
10:01:30.900283 to 10:01:37.655371 UTC. The new checkout rebuilt all binaries; none were
copied in. The previously verified Go 1.22.12 installation and module/build cache were reused.
No model/App key, private archive, paid call or Viewer build was needed. This used an
empty clean checkout rather than claiming a second network clone was performed.

[Clean-checkout receipt](clean-checkout.log) records the exact command and exit.
[Full machine receipt](receipt.json) retains every child command's arguments, UTC timestamp,
exit and stdout/stderr, as well as the actual JSON-RPC requests/responses, generated
manifest and source metadata. [MCP stderr](mcp-stderr.log) preserves server search logs.
[Artifact manifest](artifact-manifest.json) records their bytes/SHA256.
The example script's own SHA256 is in the machine receipt, binding the executed code.

## Results and original acceptance conditions

| Original #16 condition | Evidence / result |
| --- | --- |
| Small explicitly synthetic Skill documents, cross-document relationship and expected query behavior | `SKILL.md` links two reference documents; they explicitly describe `重试预算 depends_on 阻塞队列`. `expected-queries.json` separates the fixed fake's observed query contract from the input relation whose extraction is not run. Materials are provided; actual consumption of that input remains unverified. |
| Distinguish fixed fake knowledge from real LLM extraction | README, each input document, expected JSON and runtime summary explicitly say the fixed fake does not read these documents. No extraction-quality or GraphRAG-benefit claim. |
| Run demo, queries and failures from an empty directory/clean checkout; record commands/results | Pass for the existing fixed fake: clean checkout, fresh build, Stable, Bundle checksums, 4 nodes/3 edges, known-content CLI and real MCP query, opt-in source path/span, missing DB exit 1 without creation, missing UUID unknown, and sample-only query zero hits. The complete receipt is linked above. This is not a multi-document ingestion run. |
| No new model dependency/lock changes/default private archive prerequisite | Pass: standard-library Python invokes existing binaries only. All three dependency/lock files are identical to the implementation baseline; final clean checkout remains clean. No private input is needed. |

The known fixed concept is `阻塞队列`, with the exact expected queue summary. Both CLI
and MCP responses contain its actual UUID; the source response identifies that same
UUID, `source_refs=[fake]`, path `SKILL.md` and stored span 6–8. All three stable Bundle
files have identical hashes before and after queries. Negative cases distinguish exit
errors from successful zero-result transport rather than declaring all exit-0 calls useful.

No fake/production/config/schema/lock/benchmark source was changed. Only the public
example files, a RESTORE entry and one root README entry were added. No full Go/Viewer
regression suite was rerun for this documentation/example-only task; focused execution,
Python syntax, JSON/document/link checks and scope/lock integrity were sufficient.

Original acceptance remains with independent review and the maintainer. Keep #16 partial
and open if runtime consumption of the linked documents is required. A plan, intended
relationship or clear documentation cannot be substituted for that missing execution.
