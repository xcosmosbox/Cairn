# Fixed R3 qualification and revision validation

Task [#73](https://github.com/xcosmosbox/Cairn/issues/73), under #14/#63. This is a new, bounded experiment. Historical #63 remains FAIL (11 accepted of 12); this is neither another historical round nor a control repeat.

## Frozen preparation

The offline driver was accepted and merged in [PR76](https://github.com/xcosmosbox/Cairn/pull/76). The exact integrated source `adb45e1f8cabaa240fc11725a6c7cdd0aa3f6b0c`, tree `757a8f205e4d2de681dc432da0d90d46a8689d6e`, was compiled with pinned official Go1.27.3 from a clean detached checkout. The final binary and zero-model-call preflight are identified in [freeze.json](freeze.json). [seed-preflight.json](seed-preflight.json) is metadata only; [plan.json](plan.json) fixes inputs and budgets. [required-gates.json](required-gates.json) records the recovered passing full race/verify gates and exact-head CI; the original full-gate receipts and logs are preserved.

The actual current qualification wire contains the complete 71,332-byte authoritative original and the fixed LAST R3 57-item candidate. It counts 23,869 input tokens with 65,536 reserved output tokens against the official 1,000,000 context limit. The free revision-builder probe counts 23,806 input tokens but uses a synthetic critique; it is not provider feedback or the eventual live revision request. Future actual revision and final-gate request sizes remain unknown and must pass the same exact-wire guard. Runtime files match their pinned hashes; the retained native wheel was not rebuilt.

## Method and stopping rule

At most three actual provider wires total, one per call, concurrency one, no wire or outer retries. Current GateB first qualifies fixed R3 against the same full original. Only a NEW typed explicit-false verdict with a nonblank reason permits production Revise using that NEW reason, nonempty FormatGate, and final current GateB. Initial qualification pass stops as a stochastic recheck with no revision-effect claim. Untyped, malformed, transport or execution error stops as failure. The archived rejection prose is provenance only. No alternative candidate, historical union, truncation, gate relaxation or full-pipeline fallback is allowed.

Private observed-content hashes bind saved responses and captures. Complete offline seedAudit must reconcile phase, source, wire, counter, usage and parser replay before any accepted checkpoint is usable. An accepted checkpoint is provisional until that audit passes. No AssignIDs/04 adapter is included.

## Execution status and limits

**No live request has started. Separate root technical GO is pending.** The ordinary user DeepSeek authorization persists; this preparation does not itself admit execution. Independent engineering/scientific agent reviews share the repository account and do not constitute independent human quality judgment.

Originals, candidate contents, archived/new reasons, responses and private logs remain restricted. This public packet contains method, identities, counts and limits only. Original spans remain unknown. A later result cannot establish causal improvement, factual recall, all12/full609 capacity, graph or answer quality, QA/gold/HOLDOUT results, or stable-release readiness.
