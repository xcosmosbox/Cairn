# Frozen public multi-hop retrieval baseline

This adapter evaluates free lexical retrieval over **all 609 original documents**
in MultiHop-RAG at revision `71ac0d0bd1f951d2d6b70311f7d2ae404e1ffa82`.
It measures source localization and visible original facts. It does not measure
answer correctness, citation correctness, refusal correctness, or GraphRAG benefit.
Task: [#21](https://github.com/xcosmosbox/Cairn/issues/21).

## Inputs and isolation

`manifest.json` pins exact SHA-256, bytes, record counts and dataset revision.
The downloader derives immutable URLs under
`https://huggingface.co/datasets/yixuantt/MultiHopRAG/resolve/<revision>/`.
It downloads only `corpus.json`, `MultiHopRAG.json`, and `README.md`; it never runs
upstream model, embedding, or dataset scripts. Existing files must match exactly;
outputs are never overwritten. No credentials or paid APIs are used.

The dataset metadata says ODC-BY. Individual news articles retain their separate
copyright. Do not commit downloaded JSON, article text, local databases, or tool
contexts. This directory publishes code, source hashes, frozen question IDs and
metrics only. See the pinned upstream README for its attribution and source links.

`build-index` reads only the complete corpus, verifies its identity and exact
allowed schema, and has no question/answer/gold input argument. Each original
URL identifies one document by SHA-256; body bytes have their own hash. It chunks
only the original body by Python `re.finditer(r'\S+')`: 256 whitespace tokens,
stride 224. Unicode code-point `[start,end)` offsets and body hash bind each chunk
ID. Original metadata stays in the documents table. Both retrieval arms search
the same canonical JSON `{title,source,published_at}` followed by newline and
the exact body chunk. Both display those three original metadata fields.

The evaluator separately loads labels and validates every gold URL and fact
against original body text. The retriever accepts only `{qid,query}` and rejects
extra fields. Gold contexts are never substituted for the full corpus. No labels
or generated facts enter the index. The index is hash-bound, closed/checkpointed,
opened read-only with SQLite immutable mode, and checked again after evaluation.

## Frozen questions

Query IDs are SHA-256 of NFKC, casefold, whitespace-normalized question text.
Non-null questions group by `docs:` plus sorted unique exact support URLs joined
with newline; null questions group by `null:` plus normalized question text.
Compute SHA-256 of `cairn-mhr-v1` + NUL + group key: first eight hex digits mod 5
equal zero assigns DEV; the rest assigns HOLDOUT. A support group cannot cross
splits. Within each question type, sort by qid and select the first 32 DEV and
64 HOLDOUT questions. Insufficient counts fail; no reseeding or splitting groups.
The eight smoke IDs are the first two selected DEV questions per type.

Before freezing, the adapter audits all cross-split question pairs for token
5-shingle Jaccard >= 0.85 (NFKC/casefold/whitespace normalization, `\w+` tokens).
Any match fails with pair hashes for quarantine and coordination; it does not
alter the seed. Exact normalized duplicate questions also fail. The frozen
manifest contains IDs/type/group hashes, never questions or gold facts.

This is **new-question holdout over a shared fixed corpus**. It makes no
new-document generalization claim. The CLI exposes only `run-dev`; no HOLDOUT
evaluation option exists. HOLDOUT remains frozen and unevaluated. Future use
needs a separately recorded protocol decision, frozen implementation and budget.
The independent MuSiQue-Full external validation protocol is maintained in
`docs/research/`; this adapter does not download or run MuSiQue this round.

## Retrieval and metrics

Each question makes one original-query call, top 10 chunks, no query expansion,
stopword tuning, solver rewrites, follow-up search, or gold-aware reranking.

| Arm | Fixed retrieval semantics |
| --- | --- |
| `raw_lexical_sanity` | Casefold literal OR of whitespace words / quoted phrases; matched-term count, then occurrence density, then chunk ID |
| `raw_source_fts_sqlite` | SQLite FTS5 `unicode61`, safely quoted original whitespace atoms joined with OR; BM25 ascending, then chunk ID |

Punctuation inside an FTS atom follows SQLite tokenization/phrase semantics.
The raw arm is a sanity comparison; it is not the historical paid raw control.
The FTS arm is an external original-source index, not Cairn production node FTS.
Real Cairn node FTS/graph/backfill are `not_run_no_real_graph`; deterministic
chunk fixtures are never presented as a real GraphRAG build.

**Ranking metrics, before context clipping:** DocHit, DocRecall and AllGoldDocs at 5/10 use unique
support document IDs. Repeated chunks cannot inflate document recall. After
**Visible evidence metrics, after context clipping:** FactRecall and AllGoldFacts require an entire exact gold fact occurrence
to lie within the union of visible original-body intervals from the same doc.
Repeated `(URL,fact)` pairs count once; metadata text cannot satisfy a fact.
The common 10,000 Unicode code-point budget counts canonical serialized context
including IDs, offsets, metadata and body. The last chunk can be prefix-clipped;
its end offset shrinks accordingly and the remainder is not retrieved again.
Additional `visible_doc_recall` and `visible_all_gold_docs` diagnose source
coverage after the display budget; they do not replace the frozen ranking metrics.
The two counts `unique_support_doc_count` and `evidence_fact_count` are separate:
one document may contribute several facts. Neither is silently called a hop count.

Null questions are excluded from positive recall denominators. Their retrieved
count and empty rate are separate diagnostics; an empty retrieval is not evidence
of correct answer abstention. Exceptions count in the fixed question denominator
and are recorded separately from successful zero hits. Macro metrics, per-type
and both unique-document-count and fact-count summaries, empty/error/clipping rates, p50/p95 wall-clock
retrieval-plus-packing latency and index bytes are reported. Timing includes
Python harness cost and is not a product latency or SLO benchmark (#17).

## Reproduce

Python 3.9+ with SQLite FTS5 is required. Run from the repository root:

```bash
python3 -m unittest discover -s e2e/benchmarks/multihop_public -p 'test_*.py' -v
python3 e2e/benchmarks/multihop_public/benchmark.py download --data-root /tmp/cairn-mhr-data
python3 e2e/benchmarks/multihop_public/benchmark.py freeze --data-root /tmp/cairn-mhr-data --output /tmp/cairn-mhr-freeze.json
python3 e2e/benchmarks/multihop_public/benchmark.py build-index --data-root /tmp/cairn-mhr-data --output /tmp/cairn-mhr.db --receipt /tmp/cairn-mhr-index.json
python3 e2e/benchmarks/multihop_public/benchmark.py run-dev --data-root /tmp/cairn-mhr-data --index /tmp/cairn-mhr.db --receipt /tmp/cairn-mhr-index.json --freeze /tmp/cairn-mhr-freeze.json --output /tmp/cairn-mhr-dev.json
```

Compare regenerated freeze/source/protocol identities to committed receipts.
SQLite database bytes can differ with SQLite versions; the receipt records the
local index identity and deterministic source inventory. Test fixtures are
synthetic, require no network, and cover label isolation, source hashes, query
identity, original offsets, split leakage, duplicate quarantine, deterministic
index/search behavior, read-only access, budget clipping and metric denominators.
The existing Correctness workflow also runs these focused tests. Production Go,
runner and Viewer gates remain unchanged.

## Recorded DEV run

`dev-summary.json` records the implementation commit/hash, environment, source,
protocol, split and index identities, exact command shape and exit code. It
contains aggregate metrics only; the CLI's generated result also has DEV-only
query audit IDs and visible intervals. All 609 source documents, 2,556 questions
and 6,084 gold facts validated with zero unmapped source URLs or non-exact body
facts. There are 1,275 support groups and zero cross-split near duplicates.
The frozen roster is DEV128 plus HOLDOUT256; only DEV ran.

| External lexical arm | Ranking DocRecall@10 | Ranking AllGoldDocs@10 | Visible doc recall | Visible AllGoldDocs | Visible FactRecall | Visible AllGoldFacts |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Raw sanity | 52.08% | 13.54% | 44.62% | 9.38% | 25.95% | 6.25% |
| SQLite FTS5 | 70.49% | 35.42% | 58.59% | 25.00% | 39.76% | 10.42% |

Recall means use the 96 answerable DEV questions; the 32 null questions are
excluded. Both arms had zero errors and zero empty results. Both returned ten
chunks on every question and exceeded the 10,000-character context budget on
every question: **100% display truncation, after top-10 retrieval**. The ranking
columns therefore cannot be read as fully visible evidence. Null questions also
returned ten chunks each; refusal/answer accuracy remains unmeasured.

The index contains 4,976 chunks and occupies 24,936,448 bytes in the recorded
SQLite environment. Frozen protocol parameters were unchanged between initial
run and the final reporting run, which only adds explicit fact/document strata
and visible-source diagnostics. No DEV-driven optimization or HOLDOUT scoring
was performed. The local acceptance is the focused ten-test synthetic suite,
source verification, freeze, index build and free DEV run. Full Go/Viewer and
existing-runner validation remains separate CI/#10 evidence.
