# Compact evidence schema: v011-evidence-1

All public JSON evidence uses `schema_version: "v011-evidence-1"`. IDs are stable within
this frozen evidence corpus. Neither public summaries nor fixtures contain private
original names, bodies, original content hashes or raw requests/responses.

## supplement.json

- `empty_skill_markers`: unique normalized SKILL.md paths, materialized with zero bytes
  before discovery. These are two legitimate empty supported sources in addition to the
  eight substantive originals; they contain no expected knowledge.
- `sources`: unique `{source_id, path, content}` synthetic original rows. Paths are
  normalized relative paths with no traversal; identity does not derive from content
  that happens to resemble a UUID. Multiple sources can deliberately state the same fact.
- `cases`: unique `{case_id, source_ids, obligation, expected, owner, stage, evidence_kind}`.
  `source_ids` references the input rows; `expected` is a concrete observation, not builder
  input. Owners are `#110` through `#114`; stages are `annotation`, `node`, `relation`,
  `persistence` or `orchestration`. `evidence_kind` describes the assertion's nature.
- Failure cases may also include `expected_outcome`, `expected_attempts` and
  `expected_reason`. Outcomes use the four helper statuses in the handoff. A forced
  partial-case pass cannot certify semantic completeness on the normal success corpus.

## inventory_summary.json

- `sources`: exactly S01–S06, each with `source_id`, `original_bytes`, `original_lines`,
  `substantive_units`, `excluded_units`, `covered_units`, `classifications`,
  `exclusion_reasons` and `expected_inventory_ref`.
- `original_bytes`/`original_lines` measure the exact reconstructed source Markdown
  representation (51,183 bytes/288 lines), not JSON-encoded raw-body strings or generated
  writeback Markdown. Private reconstruction proof binds each representation to raw DB
  and frozen source inputs.
- `totals`: sums of corresponding source-level integer counts. `covered_units` equals
  `substantive_units`; the private partition separately covers excluded text with reasons.
- Private evidence digest and receipt bind the complete inventory and original manifest
  to this sanitized summary. The authorized private artifact specifies exact byte/line
  spans, original identity, complete expected content and review evidence. Public counts
  are not a substitute for access to that artifact.

Unit boundaries need not match historical annotation rows. The six originals define
semantic scope; the old QA selection, model item count and later accepted subsets do not.
