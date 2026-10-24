# Issue17: trustworthy process-memory follow-up

**Current-source RSS was observed reliably on this bounded synthetic workload.**
The run completed in7.368s:1791 measured queries,0business/protocol errors,
six warm MCP cells and four actual watcher A/B updates. The highest observed
service RSS/HWM was43716KiB. This is a resource observation, not an enforced
2GiB memory bound, capacity/SLO guarantee, long-run leak result or stable-release approval.

Session `cairn-runtime-rss-20261024`, product source
`18e7e7bfc989fc0216625b0851e105690edd3950`; Go1.22.12 linux/amd64,
`go build -trimpath -buildvcs=false -o .local/product-rss/bin/cairn-mcp ./service/cmd/cairn-mcp`.
Actual binary SHA256:`9991381fb803fe8acba932a1f1afafd9426ac10751d6d7ddeb99e4e2e15baae5`.
[Claim](https://github.com/xcosmosbox/Cairn/issues/17#issuecomment-6384286677)
and [ACK](https://github.com/xcosmosbox/Cairn/issues/17#issuecomment-6384294267)
bound this follow-up to180s and at most10updates. Actual updates:4.

## Why the new observations identify the service

This environment uses inner subprocess PIDs while `/proc` exposes outer namespace
PIDs. Direct `/proc/<Popen.pid>` can read an unrelated host process. The new small
resolver finds exactly one direct child matching the complete expected argv,
outer parent PID, innermost NSpid and stat start ticks. It rechecks argv/parent/
namespace/start identity before and after every memory read. Missing, ambiguous,
reused or inaccessible identity/fields produce null/unknown, never an invented zero.
It uses no monitoring service or privileged operation.

Before the product workload, a child acknowledged its32MiB allocation. The same
identity (inner7, outer111513, parent111508, start4416925) changed VmRSS6460→39240KiB,
a32780KiB increase. This validates the resolver's response to actual resident
memory, independently of the service result. Seven actual MCP process identities
then resolved successfully. The periodic trace retains578samples:576valid and
2null with reason `process identity changed`; those timestamps follow the final
query for the corresponding processes during shutdown. Nulls remain in the record.

## Warm actual-product memory

| Input / nodes | Transport / clients | Measured queries | RSS after warmup, KiB | Largest sampled RSS/HWM, KiB |
| --- | --- | ---: | ---: | ---: |
| Fixed fake /4 | stdio /1 |100|13232|19592|
| Fixed fake /4 | SSE /4 |100|19356|23076|
| Synthetic /1000 | stdio /1 |100|22284|26624|
| Synthetic /1000 | SSE /4 |100|27564|28808|
| Synthetic /10000 | stdio /1 |100|27860|28844|
| Synthetic /10000 | SSE /4 |100|28576|32140|

Each cell runs the real product, initializes MCP and validates nonempty expected
content;20warmup queries per client precede measurement. Warmup requests are not
included in the1791measured-query denominator. HWM is the kernel's process-lifetime
high-water mark read while identity is valid; it may include startup/prewarm memory,
not just the100query interval. There is one process per cell and no disk-cold or
repeatability claim. These memory cells are separate from the original older-source
timing baseline; no old source/timing measurement was rewritten or silently combined.

## Actual watcher memory under live queries

The existing real loopback Catalog/Release/asset watcher performed4updates at10k
nodes under4SSE clients, producing1191measured marker queries with0errors. Every
update required both completed-watcher log and observed correct generation. Source
DBs and immutable fixtures were isolated copies; all5inputDB hashes were unchanged.

RSS after the initial watcher pull and before A/B updates was30544KiB. Overall
largest valid process RSS/HWM:43716KiB. That is13172KiB above the single initial
postpull baseline; it includes query workload, caches and allocator behavior and is
not an isolated allocation cost or internal atomic-swap peak.

| Update | Target | Valid RSS samples in external update window | Largest current RSS, KiB | Above initial baseline, KiB |
| --- | --- | ---: | ---: | ---: |
|0|B|96|41172|10628|
|1|A|84|43716|13172|
|2|B|89|43716|13172|
|3|A|78|43268|12724|

Windows run from Catalog publication until new marker plus completion log become
externally visible. They include polling/HTTP/validation/install/observation overhead
and are not exact swap instants. Raw samples preserve timestamps for independent
recalculation. Post-update memory is not established as a steady-state plateau or leak.

## Limits and acceptance boundary

The watchdog checked identity-valid RSS/HWM approximately every10ms against2048MiB.
No sample approached that threshold, and its kill path was not triggered. This does
not prove an OS-enforced ceiling, memory reservation, complete coverage or absence
of subinterval spikes. The2GiB hard-limit/enforcement guarantee remains UNKNOWN.
Shared cgroup totals are not attributed to Cairn. Missing samples remain unknown.

This is a7.368s single-process, one-KB-at-a-time public synthetic observation on a
shared host. It does not establish many-KB/many-installation operation, scale beyond
10k, natural update cadence, adversarial inputs, long soak, production capacity or
an SLO. Load average before run:0.572/0.389/0.202; Linux6.18.55/Python3.12.14.
No model/embedding/App call, private data, production source change or optimization.
The original report's server RSS fields stay unknown; this follow-up supplies a new
source-bound measurement. Root must decide whether this bounded evidence satisfies
the intended release scope; #17 is not automatically closed by this report.

## Evidence and reproduction

[Summary](summary.json), [environment and input/binary identities](environment.json),
[actual updates](updates.json) and [archive manifest](manifest.json).
The62743-byte [archive](observations.tar.gz) has SHA256
`38cc27d25dda1937374fff0b8082a79fa7aab0177095a767deb2a0651505b45c`;
13members include exact raw query/RSS samples, watcher logs, configs and executed
`run.py`, `process_rss.py`, `memory_only.py`. The manifest hashes every member.
Extraction reads only this trusted fixed archive; verify SHA256/member hashes first.

Use the existing public fixture generator and fixed fake instructions in the
[harness README](../../../../benchmarks/product_baseline/README.md). This run copied
the prior public synthetic fixtures into its own `.local/product-rss/fixtures` and
the fixed fake into `.local/product-rss/fake`; it never wrote a prior task's inputs.
Exact invocation from this pinned checkout:

```sh
python3 benchmarks/product_baseline/memory_only.py --binary "$PWD/.local/product-rss/bin/cairn-mcp" --synthetic-root "$PWD/.local/product-rss/fixtures" --fake-bundle "$PWD/.local/product-rss/fake" --source-commit 18e7e7bfc989fc0216625b0851e105690edd3950 --output "$PWD/.local/product-rss/measured-1" --updates 4
```

Focused identity/accounting/transport tests and `make verify` passed. The former
cover namespace mismatch, unrelated direct PID, wrong parent/NSpid, ambiguity,
reuse/argv changes, missing memory and identity changing during a read. No full Go
suite pressure repeat was used as diagnosis. CI and separate-session review remain
separate evidence and must be checked before integration.
