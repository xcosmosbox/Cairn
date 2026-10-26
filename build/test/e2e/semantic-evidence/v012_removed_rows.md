# v0.1.2 historical removed-row disposition

Task [#110](https://github.com/xcosmosbox/Cairn/issues/110), session
`cairn-v012-integration-20261026`, 2026-10-26 UTC. This is a new source-semantic
review of the three historical removals, not a changed historical run or new model
judgment. Raw names, source bodies, candidates and provider feedback remain in the
[authorized packet](v011_known_losses.md#fixed-inputs-and-reproducibility).

The complete originals and the accepted inventory remain authoritative. Inventory
SHA256 is `f0bfc3a6b8d802f3b15fe7c7e2f6db2e8c09ea4feb7564b241b76d42f78e44d8`;
the historical archive SHA256 remains
`cdf5f96ae41bee23ff16eba3e690bf392606691e5f0acf22de3d05c501c86921`.
The reviewer compared each complete removed `content`/`detail` with its full original
paragraphs and attached inventory facets, rather than trusting the old verifier's
unsupported index or comparing names alone. The source spelling variation for a
person in S01 is retained as a source feature, not evidence of a second person.

| Source / initial zero-based row | Source-bound reference | Semantic finding and required disposition |
| --- | --- | --- |
| S01 / 2 | S01-P005 and S01-P011; notably U003, U007, U031–U032 | The row's founder involvement is unsupported: the original identifies a former vice-president. The rest carries supported privacy positioning and mission to counter larger competitors, the author's privacy tradeoff despite worse performance/readability, the missed query qualifier, former-officer affiliation, and an allegation with admitted possible misinterpretation. Revise the unsupported role; retain all these components, attribution, uncertainty and countervailing original context. Deleting the entire row does not discharge those obligations. |
| S06 / 17 | S06-P025 / U024–U027, R009 | The detail adds a monthly billing interval which the original does not state. The source supports an ad-free paid pilot for some users at the stated price with no other strings attached, attributed to one English-speaking market outside the named country for now, without imminent broad availability. Remove the invented interval; preserve the entire offer, pilot limitations, attribution and timing caveat. |
| S06 / 18 | S06-P025 / U024–U027, R009 | The summary independently adds the same monthly interval. Its detail preserves the same price, ad-free offer with no other strings attached, limited pilot and timing substance, so blanket deletion also risks erasing valid knowledge. Repair the interval and recheck the complete replacement with all that meaning. Overlapping rows may be represented faithfully together; duplication does not justify discarding their shared substantive topic. |

These are mixed-support rows: the old assessment identified a genuine unsupported
addition, but whole-row deletion was not a semantic preservation strategy. No claim
is made that an external fact is true, that a billing interval has been externally
verified, or that every surrounding historical accepted row already has full coverage.
The original, rather than outside knowledge, controls the finding. All six sources
and their 360 substantive inventory units remain in the denominator.

`TestV012AcceptanceMixedTruthRowsAreRevisedNotDeleted` uses small public invented
equivalents: former officer versus founder, and pilot price versus invented billing
interval. It checks that concrete critique revises the offending document, retains
all supported material, and triggers full replacement assessment. This proves the
helper's repair routing under deterministic responses. It does not replay or certify
new annotations for the private originals. Ordinary full-entry construction and final
source-to-graph acceptance remain [#114](https://github.com/xcosmosbox/Cairn/issues/114).
