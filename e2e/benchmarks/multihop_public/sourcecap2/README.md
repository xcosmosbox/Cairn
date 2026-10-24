# DEV source-cap experiment: negative fact-recall result

The maintainer rejected adopting source cap2 as a default or production strategy:
it increases source coverage while lowering visible FactRecall. The experiment
and its losses are retained as evidence. No further cap tuning on this DEV set,
production change, or HOLDOUT evaluation is part of this task.

Task [#27](https://github.com/xcosmosbox/Cairn/issues/27) builds on the accepted
baseline [#21 / PR28](https://github.com/xcosmosbox/Cairn/pull/28), merge
`bf5d7ec0828b85695a0ad5e196f11f32f02462cb`. Only this new subdirectory changes;
baseline source, frozen data identities, parameters and reports remain intact.

## Registered single factor

Before running, commit `537a447ddd1894431c9cd8895fb9e5e78a617a84` published
`manifest.json` (SHA-256
`c262870250977940b0c0825b3d1697550173cf6c48029c463457270fa8d38663`)
and aggregate motivation `baseline-diagnostic.json` (SHA-256
`e26d395a6e844fa82d7acdc7d3b749de7cf69d8cc074964d9eacb1f1c8749394`).
The original researcher diagnostic hash is also retained. Registration is linked
in the [pre-run checkpoint](https://github.com/xcosmosbox/Cairn/issues/27#issuecomment-6382749422).

Every one of the same 128 DEV questions executes the original SQLite FTS query
once, safely quoted whitespace atoms joined with OR, `unicode61`, ordered by
BM25 ascending then chunk ID. Both arms share the exact same top-50 candidate
sequence and its hash. The control takes the first ten. The treatment scans
that same rank sequence, takes at most two chunks per document ID, and stops at
ten. If fewer than ten survive, it retains the actual count without another
search, relaxed cap, reordered chunks, or extra budget.

The complete609-document index, source metadata, 256-token/stride224 chunks,
original query, frozen DEV IDs and 10,000-code-point serialized context packing
all reuse the unchanged baseline code. Only `{qid,query}` reaches the retriever.
Gold labels are used only by the evaluator. No solver, LLM, graph, paid API,
raw-arm rerun, production behavior change, or HOLDOUT scoring is involved.
The adapter verifies every pinned baseline file and index hash, opens the exact
closed index read-only, and checks the hash again afterward. The control's
original metrics must reproduce the frozen baseline overall and every type,
fact-count and unique-document-count stratum or the experiment fails.

## Recorded result

The real run exited0 in6.10s. It uses96 answerable DEV questions for positive
recall and32 null questions for separate diagnostics. All128 questions returned
ten final chunks in both arms; errors and empty results were zero, and all
top-ten contexts exceeded the common display budget. Ranking metrics are
calculated before clipping; visible document/fact metrics use only the final
displayed body intervals. Metadata never satisfies a gold fact.

| Metric | Rank10 control | Source-cap2 | Change |
| --- | ---: | ---: | ---: |
| Ranking DocRecall@10 |70.49%|76.48%|+5.99pp|
| Ranking AllGoldDocs@10 |35.42%|46.88%|+11.46pp|
| Visible document recall |58.59%|62.93%|+4.34pp|
| Visible AllGoldDocs |25.00%|29.17%|+4.17pp|
| Visible FactRecall |39.76%|37.33%|−2.43pp|
| Visible AllGoldFacts |10/96|11/96|+1 question|
| Visible distinct gold facts |99/266|93/266|−6 facts|

The paired FactRecall result is **4 gains, 10 losses, 82 ties**. Five facts become
visible and eleven formerly visible facts are lost; all eleven lost facts are
absent from the treatment's selected chunks, preserving the same-source-fact
failure case. Improvement in source coverage therefore does not establish an
improvement in visible fact coverage. This is DEV retrieval evidence only, not
answer, refusal, generalization, production latency or GraphRAG benefit.

| Mutually exclusive gold-fact outcome | Control | Source-cap2 |
| --- | ---: | ---: |
| Source absent from common pool50 |23|23|
| Source in pool50 but absent from final selected chunks |64|47|
| Source selected but fact absent from those chunks |60|77|
| Fact in selected chunks but lost only to display budget |20|26|
| Fact fully visible in final original-body intervals |99|93|

Both columns sum to the same266 distinct `(URL,fact)` gold facts. The source cap
reduces source exclusion but increases missing same-source facts and budget
loss. Null questions return ten chunks in both arms; no abstention accuracy is
inferred from retrieval counts.

`dev-summary.json` retains all original metrics and explicit type/fact-count/
unique-support-document-count strata. `paired-qids.json` retains every DEV qid,
shared pool hash, paired deltas, losses, fixed denominators and non-text audit
rows; it contains no question text, answer, gold fact or news text. The mean
unique-visible-doc diagnostic over **all128** questions is3.586→4.195. When
comparing to a positive-only diagnostic, use the corresponding positive strata
instead of mixing denominators.

The shared pool50 retrieval cost is recorded separately from each arm's
selection/packing cost. Shared-cache timings and fixed execution order do not
establish performance improvement, and no latency-benefit claim is made.

## Reproduce and review

Use the original verified downloads and the exact baseline index receipt. The
baseline README describes downloading/rebuilding without committing article
text. From the repository root:

```bash
python3 -m unittest discover -s e2e/benchmarks/multihop_public -p 'test_*.py' -v
python3 e2e/benchmarks/multihop_public/sourcecap2/experiment.py --data-root /tmp/cairn-mhr-data --index /tmp/cairn-mhr.db --output /tmp/sourcecap2-summary.json --paired-output /tmp/sourcecap2-paired.json
```

The focused suite discovers the six new tests and ten unchanged baseline tests
through `__init__.py`; no CI workflow or baseline test file changed. The new
tests cover rank preservation/shared-pool immutability, per-source cap2,
insufficient-source behavior without fallback, identical original packing and
offsets, explicit same-source-fact loss, complete loss partitions, paired
gain/loss/tie counts and registered identities. The result records source commit
and implementation hash; `validation.json` records actual commands and exit
codes. Local shell push has no credentials, so connector publication maps the
tested local tree to an identical remote tree as recorded in the PR/checkpoint.

Independent review and current CI apply to this report implementation. Acceptance
of this negative-result artifact does not adopt source cap2 or open HOLDOUT.
