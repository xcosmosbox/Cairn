# Initial product performance baseline

This is a bounded external measurement of the real `cairn` and `cairn-mcp`
binaries. It is separate from `e2e/benchmarks/multihop_public` and its Python
retrieval-quality harness. No LLM, embedding, model key, production database or
production configuration is required. Nothing here changes production behavior.

Reuse:

- `e2e/real-consumer-smoke.py`: real CLI/MCP stdio and HTTP/SSE framing, handshake,
  business-error checks and response bounds. SSE workers attach to one actual
  server rather than starting one server per concurrent client.
- `core/storage` public node/edge/source/FTS APIs and `core/kbbundle` packing:
  deterministic synthetic 1,000/10,000-node graphs, three directed edges per node,
  fixed timestamps and UUIDs, two immutable A/B marker generations. Synthetic
  SourceRepo/SourceCommit fields are generated fixture identities, not an assertion
  that a real source repository was ingested or extraction quality was measured.
- Existing Catalog/Release/asset loopback pattern in `core/kbbundle` pull tests.
  The actual MCP watcher uses the configured local API endpoint and an isolated
  synthetic loopback token; the test never calls GitHub or mints credentials.
- The existing free `cairnd --once --fake` Bundle as a four-node control input.
  Its actual byte identity is recorded rather than inferred from its directory.

## Run

Use the verified Go environment from the #10 restore report. From a pinned
checkout, build product binaries once. The recorded run uses Go 1.22.12,
`-trimpath -buildvcs=false`; the explicit product commit and binary hashes provide
the source binding. The VCS flag is needed for Go's VCS stamping in this worktree
and is not a runtime optimization. Do not replace binaries during measurement.

```sh
mkdir -p .local/product-perf/bin
go build -trimpath -buildvcs=false -o .local/product-perf/bin/cairn ./service/cmd/cairn
go build -trimpath -buildvcs=false -o .local/product-perf/bin/cairn-mcp ./service/cmd/cairn-mcp
go build -trimpath -buildvcs=false -o .local/product-perf/generate ./benchmarks/product_baseline
.local/product-perf/generate --out .local/product-perf/fixtures/1000 --nodes 1000 --seed 20261024 --commit PRODUCT_SHA
.local/product-perf/generate --out .local/product-perf/fixtures/10000 --nodes 10000 --seed 20261024 --commit PRODUCT_SHA
python3 -m unittest discover -s benchmarks/product_baseline -p 'test_*.py'
python3 benchmarks/product_baseline/run.py \
  --bin-dir "$PWD/.local/product-perf/bin" \
  --synthetic-root "$PWD/.local/product-perf/fixtures" \
  --fake-bundle /ABSOLUTE/ACTUAL/FAKE/STABLE/BUNDLE \
  --source-commit PRODUCT_SHA --output "$PWD/.local/product-perf/new-run"
```

To create the fake input on a new host, build `cairnd`, run the documented
`./bin/cairnd run --once --fake --config configs/cairnd-fake.example.yaml`, and
inspect that invocation's actual session/Bundle. Existing #10 binary artifacts
are local runtime inputs; source code plus the generation commands are the
portable reproduction path. New fake sessions can have different historical
source/Bundle identities; record the actual DB hash instead of substituting a
prior receipt. Synthetic fixture outputs are exclusive-create and never rewrite
an old run.

## Scope and interpretation

Default plan: 500 requests per cell, three independent process repetitions,
20 warmup requests per client, 30 startup samples per transport and scale,
SSE concurrency 1/4, and at most ten real watcher A/B updates at 10k nodes. The
fixed mixed workload rotates exact hit, broader hit and no-hit queries; warm
results also retain per-query-class quantiles. Actual n and errors take priority
over the plan. The run is capped at 600 measured wall seconds and a monitored
2,048 MiB RSS ceiling per child; the 10 ms watchdog kills registered children at
those limits. Polling is not an OS memory reservation and can miss brief RSS
bursts; CLI additionally records the kernel's per-child `wait4` peak RSS.
Fixture generation and binary compilation are setup, outside the measured run.

CLI latency includes a new process, SQLite open/validation, query, rendering and
the bounded Python client's file/poll overhead (up to its 0.5 ms wait poll).
MCP warm latency is a complete stdio or SSE tool round trip. Startup latency
includes process launch, initialization and the first successful query, with
shutdown outside that latency. Throughput uses each measured cell's wall time,
including driver scheduling/recording overhead; it is not a storage-only rate.

No cache drop or privileged operation is attempted. "New process" has an
uncontrolled OS page cache; even input hashing warms pages. "Warm" means the
same process after explicit warmup. No disk-cold claim is made. RSS comes from
Linux `/proc` current/high-water sampling and per-child `wait4`; server warm
idle and peak values include the real product process, not the Python driver.

Real watcher metrics include stable publication to observed new-generation
visibility under four live SSE clients, first observed Catalog poll, and asset
write completion to visibility. They include local HTTP, checksum/contract
validation, installation, service replacement and observation delay, not a
measurement of the internal swap lock alone. A watcher-completed log and marker
response are both required. Raw query spans expose old/new generations and
requests overlapping the externally observed update window. Exact reader lease
correctness remains covered separately by the existing lifecycle regressions;
a timing sample is not proof of all possible in-flight schedules.

Quantiles use linear interpolation at `(n - 1) * p`, including failed request
latencies. Failed/no-response cases remain in the denominator. Small-n startup
and update p99 values are descriptive, not stable tail estimates. Preserve
partial results, environment CPU/memory limits, workload/data/binary hashes and
shared-host concurrency noise. This initial synthetic single-host baseline is
not a production SLO, capacity proof, or evidence of GraphRAG answer quality.
