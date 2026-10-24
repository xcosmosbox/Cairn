# Issue #17 — initial product performance baseline

Product source: `bf5d7ec0828b85695a0ad5e196f11f32f02462cb`. Session `cairn-product-perf-20261024-correctness-audit`.
[Task](https://github.com/xcosmosbox/Cairn/issues/17) / [ACK](https://github.com/xcosmosbox/Cairn/issues/17#issuecomment-6382732820) / [measurement checkpoint](https://github.com/xcosmosbox/Cairn/issues/17#issuecomment-6382868764).

**Timing/throughput/watcher measurements completed; server memory metrics remain partial.** The measured run finished in **100.233 seconds**, below its 600-second wall budget: **21,271 measured requests, 0 business/protocol errors**, all planned static cells and ten actual local watcher A/B updates. The three input DB hashes were unchanged. No LLM, paid request, real credential, production change or optimization was used. This is an initial synthetic shared-host baseline, not a production capacity or SLO claim.

## Repetition results

The workload rotates exact hit, broader hit and no-hit queries. Each table row has three independent process repetitions × 500 requests = 1,500 requests. Latencies below are **the median of the three repetition quantiles**, not a pooled quantile; throughput shows the repetition range. All repetition/per-query-class values and error denominators are in [summary.json](summary.json). CLI includes process startup; warm MCP uses 20 warmup requests per client.

| Nodes | Transport / concurrency | n | median p50 ms | median p95 ms | median p99 ms | requests/s range |
| ---: | --- | ---: | ---: | ---: | ---: | ---: |
| 4 | cli / 1 | 1,500 | 3.96 | 5.32 | 7.73 | 234.1–250.6 |
| 4 | stdio / 1 | 1,500 | 0.39 | 0.83 | 1.38 | 2052.2–2274.7 |
| 4 | sse / 1 | 1,500 | 0.92 | 1.55 | 2.65 | 940.9–1026.9 |
| 4 | sse / 4 | 1,500 | 2.31 | 5.24 | 7.55 | 1357.4–1490.6 |
| 1,000 | cli / 1 | 1,500 | 5.76 | 15.49 | 18.99 | 122.5–126.9 |
| 1,000 | stdio / 1 | 1,500 | 1.42 | 8.36 | 10.60 | 304.8–340.4 |
| 1,000 | sse / 1 | 1,500 | 1.96 | 8.77 | 10.07 | 269.1–279.4 |
| 1,000 | sse / 4 | 1,500 | 7.64 | 43.07 | 49.29 | 247.4–262.0 |
| 10,000 | cli / 1 | 1,500 | 5.88 | 16.39 | 20.16 | 119.5–120.2 |
| 10,000 | stdio / 1 | 1,500 | 1.46 | 8.66 | 10.19 | 308.8–314.3 |
| 10,000 | sse / 1 | 1,500 | 1.99 | 8.89 | 10.00 | 263.2–273.2 |
| 10,000 | sse / 4 | 1,500 | 7.63 | 42.81 | 47.04 | 240.8–262.3 |

Three-repetition variation is visible: the 10k SSE/4 throughput ranges from 240.8 to 262.3 requests/s. This mixed synthetic workload does not show a throughput gain from four SSE clients at 1k/10k. That observation does not isolate a SQLite, Go, driver or transport bottleneck; no optimization was selected. A bounded follow-up could profile broad-hit graph expansion and client/transport costs separately before changing anything.

## New-process startup and actual watcher updates

OS page caches were not dropped. The startup samples include launch, initialization and first query, with shutdown outside latency; they are not disk-cold measurements. Input hashing itself warms OS pages. Each startup row has only n=30, so p99 is descriptive.

| Nodes | Startup transport | n | p50 ms | p95 ms | p99 ms |
| ---: | --- | ---: | ---: | ---: | ---: |
| 4 | stdio | 30 | 5.38 | 8.15 | 9.77 |
| 4 | sse | 30 | 55.17 | 62.05 | 65.52 |
| 1,000 | stdio | 30 | 27.28 | 29.14 | 29.24 |
| 1,000 | sse | 30 | 56.90 | 58.65 | 59.31 |
| 10,000 | stdio | 30 | 189.01 | 219.34 | 234.21 |
| 10,000 | sse | 30 | 209.23 | 266.17 | 469.43 |

The **actual `cairn-mcp` watcher**, configured with a loopback Catalog/Release/asset API and a synthetic loopback-only token, completed ten A↔B transitions at 10k nodes while four SSE clients made 3,091 measured marker queries. Every transition required both a watcher-completed log and an observed new marker. This exercises real download, verification, install and service replacement; it does not call a test-only hot-swap function.

| Update metric | n | p50 ms | p95 ms | p99 ms | min–max ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| Local stable publication → new marker visible | 10 | 851.74 | 1042.64 | 1076.13 | 755.78–1084.51 |
| First observed Catalog poll | 10 | 58.33 | 92.86 | 96.93 | 6.59–97.95 |
| Asset server write completion → new marker visible | 10 | 822.94 | 941.17 | 977.02 | 733.10–985.99 |

3,091 request spans overlap at least one externally observed update window; all 3,091 update-phase queries returned one valid old/new marker and no business error. These windows include polling, local HTTP, validation, install and observation delay; they do not identify the internal atomic-swap instant or exact reader retirement time. Existing lifecycle tests separately establish old-reader correctness, and are not substituted for the measured update costs. Raw event times, marker hashes and watcher logs are retained in [updates.json](updates.json) and the archive.

## Memory limitation and environment

**Server idle/peak RSS and update RSS increment are unknown. The intended 2 GiB server RSS ceiling was not verifiably enforced.** The runtime exposes virtual subprocess PIDs that do not reliably identify the same process under `/proc/<pid>/status`; the sampler produced missing/zero placeholders. The public derived JSON uses null/unknown, while exact original bytes are preserved in the archive. Zero is not a memory result. One bounded `/proc`/ps diagnostic was unsuccessful and stopped; no full rerun or monitoring framework was added. #17 must not be marked as satisfying its complete memory criterion on this evidence.

CLI `wait4` reports a kernel child-lifecycle peak (including its launch/exec chain), not a persistent server RSS or allocation count:

| Nodes | CLI child-lifecycle maximum KiB |
| ---: | ---: |
| 4 | 53,588 |
| 1000 | 57,680 |
| 10000 | 61,776 |

Environment: Linux-6.18.55-x86_64-with-glibc2.39; Python 3.12.14; Go 1.22.12; visible CPUs 9, cgroup CPU quota `800000 100000` (8 cores), memory limit 8 GiB. Host loadavg before [0.13525390625, 0.400390625, 0.5546875], after [1.0869140625, 0.7265625, 0.66259765625]. Other agents shared this host and could compile/test concurrently; there was no exclusive CPU reservation. No cache drop, affinity tuning, new load-testing dependency or production SLO target was introduced. Fixture setup and compilation are outside the 100.233-second measured run.

Product binary hashes (`-trimpath -buildvcs=false`, fixed source above):

- `cairn`: `539cde89f50bf4a315e79a759200c4bf6aa106165cbee5655464fd54e12949eb`.
- `cairn-mcp`: `3637d0b70ba58d8956448011e533f60a3c793579df3fb27539cc040d2f6a3896`.

## Reproduce, verify, and remaining work

Generation and bounded commands: [harness README](../../../../benchmarks/product_baseline/README.md). The run used `--seconds 600 --rss-mib 2048 --samples 500 --repeats 3 --warmup 20 --cold-samples 30 --updates 10`. Its fake Bundle is the existing #10 four-node fixture; the actual DB hash is in [environment.json](environment.json). Synthetic inputs use seed 20261024, fixed timestamps/UUIDs, and public storage/Bundle APIs; [fixture-manifest.json](fixture-manifest.json) records DB, tarball and Catalog identities. Generation self-checks verified node/edge counts and A/B marker contents before measurement. Query samples validate protocol/business success and expected hit/empty contents, not only elapsed time.

Actual validation: four lightweight harness tests passed; real smoke completed 696 samples/0 errors with two watcher updates; formal run exited 0 with complete timing matrix, 21,271 samples/0 errors and unchanged inputs. The later resource limitation is explicitly retained as partial acceptance despite execution exit 0. All production source/dependencies are unchanged from the fixed product base. Separate-session review and maintainer acceptance remain required.

The 473,360-byte immutable archive is split into eight ordered parts (65,536 bytes maximum), each with size/SHA256. It includes exact raw JSONL, original summaries/environment/update events (including unsupported RSS placeholders), watcher logs, cell receipts and the exact measured runner/generator/test sources. Current source copies and derived summaries must not silently replace the archived measured version.

- Archive SHA256: `02815e119464b073c698afae6538847ce5eee66494568965a6069168d37e4bb1`.
- Raw JSONL SHA256: `a1f3da44a40e919a009e9cb90bf423734ad3d56d6a77289a3f566b8d5e875e3e`.
- [Transport manifest](transport/manifest.json) / [verification and reconstruction](transport/assemble.py).

```sh
python3 docs/coordination/reports/issue17-product-baseline-20261024/transport/assemble.py --output /NEW/EMPTY/OUTPUT
```

Local reconstruction verified all eight parts, archive identity and all nine members. Remote reconstruction must be independently verified after publication. Remaining bounded work is a trustworthy process memory measurement in an environment with valid process visibility; the accepted timing baseline need not be rerun merely to fill that gap. No background benchmark or provider task remains running.
