# v0.1.4 partial revision selection repair

Task: [#112](https://github.com/xcosmosbox/Cairn/issues/112). Date: 2026-10-27 UTC.
Research and repair baseline: main `f86ad506fb1b9638e19496b50b6b514f9b99c29b`,
tree `586c3ceea4b14a2ead7bb0f5231660a3ba78937f` (merged PR122).
The [fresh research decision](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6434312462)
reopens the owned relation-selection defect after PR122's dated acceptance.
The [new execution claims and exact scope](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6434331479)
authorize two new execution sessions, source repair and independent integration.
This report supplements the [original .4 report](2026-10-27-v014-relation-fidelity.md);
historical acceptance, original evidence and private packets remain unchanged.
No v0.1.5 executor or paid/model experiment was started by this repair.

## Failure and repair boundary

A first candidate set contains an overbroad assertion A, successful siblings and
another assertion X. A concrete rejection of X requests whole-set semantic revision,
including correction of A. The revised set passes complete+supported whole-set coverage;
corrected A verifies, but revised X receives unknown feedback and exhausts the shared
recovery allowance. Before repair, `Selected` and `Result` still contain obsolete A
with explicit credit because reconciliation happens only after every revised candidate
verifies. The changed-document adapter consumes `Selected` despite the unavailable
scope, so the stale assertion can reach SQLite as a newly explicit facet.

The replacement boundary is a parsed current whole set with positive complete+supported
coverage. At that boundary, reconcile exact scope-local selection identities before
candidate verification can stop. Remove superseded identities from current selection
and its verified cache; retain their audit history. Exact unchanged identities keep
their successful cache and distinct valid same-triple qualifiers remain independent.
Selections from other sources remain untouched. Reintroduction after removal requires
a fresh verdict. Unknown/malformed coverage does not accept replacement omissions.
No freeform critique parsing, new retry allowance, production DTO/schema change,
incremental production edit or storage edit belongs to this repair.

Persistent old edges are a different obligation: the caller still preserves old text
without certifying it as a new original assertion. Removing obsolete newly selected A
does not authorize deleting a persistent old A. The composed test below requires old A
to survive only as `legacy_unverified` alongside currently verified corrected facets.

## Independent composed regression

`TestV014PartialRevisionChangedSQLiteAcceptance` in
`build/internal/incremental/zz_v014_partial_revision_acceptance_test.go` uses a new
public synthetic original and the actual `recomputeRelationsWithSources` →
`applyIncrementalIngest` path with a temporary SQLite database. It does not duplicate
the source owner's terminal-state table. Both variants use the same frozen source:

| Obligation | Expected selection/application |
| --- | --- |
| Overbroad new A → qualified corrected A | Only corrected A has current explicit credit |
| Same A/B triple, distinct valid earlier-time facet | Both legitimate qualifiers survive |
| Exact successful C/D sibling | Survives and is assessed once |
| Revised C/A stays unknown after the one allowed revision | Unavailable, exhausted scope; no C/A edge applied |
| No persistent old A | Obsolete A absent from final/SQLite facets |
| Persistent old A with confidence .73 and extended fields | Old A separately `legacy_unverified`; tested metadata exact |

The controlled assessor deliberately accepts first-round overbroad A before the later
whole-set critique corrects it. This tests recovery from inconsistent semantic feedback;
it is not a claim that scripted judgments establish real-model semantic correctness.
Actual finite activity is two generations (initial plus one revision), two whole-set
coverage checks and six candidate checks: ten recorded attempts. Exact good siblings
are assessed once; revised unknown X receives no extra retry after the revision consumes
the one shared allowance. Scope outcome remains unavailable with reason and exhaustion,
while the three verified current assertions can apply as two graph triples.

Before the fix, both variants fail on baseline main: obsolete A remains `Selected`
and appears as `explicit` in final facets and actual SQLite readback. When old A exists,
it also has the separately retained legacy facet; that legacy obligation does not excuse
the extra explicit facet. The original source review probe independently fails on the
same baseline; its immutable SHA256 is
`6eb1f620ec8a119d6a6868e82ea7c1172703f97356625c695a6d90515dc2e10e`.
The new composed regression identity at the failing run is SHA256
`9b89cac191a510bf8d7e69d59d7400dea61d72a56207542ce16bfeaf5a7ecd8b`.

## Actual commands and current receipt

Runtime: Go1.22.12 at `/workspace/scratch/17701a3d35cd/toolchain/go/bin/go`,
`GOTOOLCHAIN=local`; all Go commands use `-buildvcs=false` for linked worktrees.
The independent integration session is `cairn-v014-r2-integration-20261027`;
the source owner is `cairn-v014-r2-repair-20261027`.

| Source | Actual command | Exit/result |
| --- | --- | --- |
| Baseline f86ad506 | `go test -buildvcs=false ./build/internal/incremental -run '^TestV014PartialRevisionChangedSQLiteAcceptance$' -count=1 -v` | 1; both variants reproduce false explicit SQLite facet |
| Repaired combined 51453551 | Same unchanged command and test SHA256 above | 0; both variants pass including SQLite readback |
| Repaired combined 51453551 | `go test -buildvcs=false ./build/internal/extract ./build/internal/incremental ./build/internal/pipeline ./build/test/e2e/semantic-evidence -count=1` | 0; four packages |
| Repaired combined 51453551 | `go build -buildvcs=false ./build/cmd/cairn-ingest ./build/cmd/cairn-incremental` | 0 |
| Repaired combined 51453551 | `go vet -buildvcs=false ./build/internal/extract ./build/internal/incremental ./build/internal/pipeline` | 0 |
| Repaired combined 51453551 | `make check-invariants` | 0 |
| Integration docs | `python3 -m unittest discover -s build/test/e2e/semantic-evidence -p '*_test.py'` | 0; ten tests |
| Integration docs | `python3 -m unittest discover -s .github/scripts -p 'test_*.py'` | 0; twelve tests |
| Integration docs | Local Markdown target existence check; `git diff --check` | 0; twenty local targets |

The initial integration fixture compile used an unavailable `EdgeRepo.Update` method;
it was corrected to ordinary insert/readback before the behavioral failure above.
No existing regression assertion was weakened. The source owner's explicitly released
commit is `e745068b7a716048fb02241a2ef8b4d00a670423`, tree
`40423435735f0ab944af1ab54c9ab6fdd4379185`; the integrator cherry-picked it without
manual production edits as `51453551beef36d01aef86bc8d6b236b4e2b9214`, combined tree
`2f40d3e67e645661c822922f508a2c5ae687f8c8`. This is the tested code checkpoint before
the final documentation receipt. The source author's tests additionally cover terminal
unknown/transport/permanent/cancelled outcomes, exact old history/other-source siblings,
source UUID/evidence identity, distinct source evidence, cache retirement/reintroduction
and the control that unknown coverage cannot accept omissions. The final patch modifies
one production file and adds two regression files plus these three documentation paths.

The [passing integration checkpoint](https://github.com/xcosmosbox/Cairn/issues/112#issuecomment-6434383406)
records the actual commands. Two independent exact-published-head reviews and normal CI
remain separate live PR events; this local receipt does not predeclare acceptance.
Sessions share the account/environment; deterministic controls are independent execution
and review, not independent empirical model replication. No blanket local full verify
or all-repository race run was added.

## Separate persistence failure and remaining owners

Fresh incremental and contract researchers independently reproduced a different,
pre-existing SQLite failure: omitted old machine edge confidence `0` becomes `0.8`
during I8. The source-aware adapter first returns the entire old `Edge` unchanged,
with no selected addition. Mutation occurs in `core/storage/edge_repo.go` normalization
inside `ReplaceSemanticBySubdomain`, before unchanged-row comparison. Schema and
extraction permit zero confidence. `edge_repo.go` and `incremental/ingest.go` are
unchanged between the pre-.4 and PR122 main baselines.

The unchanged isolated failing control is
`TestV014R2OmittedZeroConfidenceOldEdgeSQLite`, in researcher-only
`zz_v014_r2_research_probe_test.go` SHA256
`7ca29514220edaeab5951e725eb8fa65f378a3bd2b2b5cd6f7f9a913b909eed8`.
Its actual command, `go test -buildvcs=false ./build/internal/incremental -run
TestV014R2OmittedZeroConfidenceOldEdgeSQLite -count=1 -v`, exits 1 on baseline main.
This is a failing #113/.5 persistence acceptance control, not a passing .4 result;
the [coordinator's reproducible .5 handoff](https://github.com/xcosmosbox/Cairn/issues/113#issuecomment-6434350672)
records both independent failures and the exact storage owner.
The prior .4 report's full-field SQLite preservation statements cover its tested
nonzero-confidence rows only. Complete persistence preservation remains unaccepted;
the new .4 composition also uses .73 deliberately and does not conceal the zero control.
The probe stays isolated and is not published as production or rewritten to pass.

[#113](https://github.com/xcosmosbox/Cairn/issues/113) owns that storage fix, complete
SQLite/Markdown/sidecar readback and the previously deferred legacy duplicate-row shape.
[#114](https://github.com/xcosmosbox/Cairn/issues/114) owns ordinary source plumbing,
aggregate status and the fixed six-original plus supplement actual-model construction,
repeat and changed-document campaign. Deterministic helper evidence and a narrow repair
prove none of that campaign or parent #108 semantic completeness. No .5 execution,
paid/full609/history rerun, frozen source/gold modification or release/tag occurs here.
Refs #112.
