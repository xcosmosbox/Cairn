package storage

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// 原故障：合法的零置信度在保留旧边时被默认值 0.8 覆盖。
// Zero is a real confidence, including for new rows; only out-of-range inputs default.
func TestV015SemanticReplacementPreservesZeroConfidence(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "retained"}[existing], func(t *testing.T) {
			db, repo := humanEdgeStore(t)
			ctx := context.Background()
			edge := &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: "Legacy uncertain support", Properties: `{"uncertain":true}`, Confidence: 0, Provenance: dktypes.ProvenanceLLMInferred, SourceRefs: "source-zero", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
			var before [][]any
			if existing {
				if err := repo.Insert(ctx, edge); err != nil {
					t.Fatal(err)
				}
				// Public NULLs become empty strings; an exact retained row must avoid UPDATE.
				if _, err := db.Conn().Exec(`UPDATE edges SET cardinality=NULL`); err != nil {
					t.Fatal(err)
				}
				rows, err := repo.ListAll(ctx)
				if err != nil {
					t.Fatal(err)
				}
				edge = rows[0]
				before = edgeRowsSnapshot(t, db)
			}
			for repeat := 0; repeat < 2; repeat++ {
				if err := repo.ReplaceSemanticBySubdomain(ctx, "d", "s", []*dktypes.Edge{edge}); err != nil {
					t.Fatal(err)
				}
				rows, err := repo.ListAll(ctx)
				if err != nil || len(rows) != 1 || rows[0].Confidence != 0 {
					t.Fatalf("zero confidence was rewritten: rows=%+v err=%v", rows, err)
				}
				if existing && !reflect.DeepEqual(before, edgeRowsSnapshot(t, db)) {
					t.Fatal("retained zero-confidence payload changed")
				}
			}
		})
	}
	for _, invalid := range []float64{-1, 2} {
		_, repo := humanEdgeStore(t)
		err := repo.ReplaceSemanticBySubdomain(context.Background(), "d", "s", []*dktypes.Edge{{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Confidence: invalid}})
		rows, readErr := repo.ListAll(context.Background())
		if err != nil || readErr != nil || len(rows) != 1 || rows[0].Confidence != .8 {
			t.Fatalf("existing invalid-input default changed: invalid=%v rows=%+v err=%v read=%v", invalid, rows, err, readErr)
		}
	}
}

// 原故障：同三元组的第二条完整旧行被 first-wins/collapse 删除；丢失独立限定和元数据。
// A selected envelope revises the oldest description, retaining every sibling and full metadata.
func TestV015SemanticReplacementRetainsDuplicateRowsAndAssertions(t *testing.T) {
	t.Parallel()
	db, repo := humanEdgeStore(t)
	ctx := context.Background()
	first := humanEdge(dktypes.KindDependsOn)
	first.Provenance, first.Description, first.Confidence = dktypes.ProvenanceLLMInferred, "old first condition", 0
	second := *first
	second.Description, second.Properties, second.SourceRefs = "old second condition", `{"owner":"second"}`, "second-source"
	second.Confidence, second.Bidirectional, second.Cardinality = .62, 0, "1:N"
	second.CreatedAt = first.CreatedAt.Add(time.Hour)
	human := humanEdge(dktypes.KindReferences)
	if err := repo.InsertBatch(ctx, []*dktypes.Edge{first, &second, human}); err != nil {
		t.Fatal(err)
	}
	// Check storage-level NULL preservation on the untouched sibling as well.
	if _, err := db.Conn().Exec(`UPDATE edges SET cardinality=NULL WHERE description=?`, second.Description); err != nil {
		t.Fatal(err)
	}
	old, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := edgeRowsSnapshot(t, db)
	const envelope = `{"cairn_relation_assertions_v1":[{"classification":"legacy_unverified","description":"old first condition"},{"classification":"legacy_unverified","description":"old second condition"},{"classification":"explicit","description":"requires quota before 2025 except emergencies","sources":[{"id":"source-1","skill":"fixture","path":"original.md","sha256":"frozen-sha"}],"evidence":[{"endpoint":"source","member_id":"member-1","source_id":"source-1","quote":"requires quota before 2025 except emergencies","start_byte":7,"end_byte":55,"start_line":1,"end_line":1}]}]}`
	selected := &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: envelope, Confidence: 0, Provenance: dktypes.ProvenanceLLMInferred}
	for repeat := 0; repeat < 3; repeat++ {
		// Retained siblings preceding selection must not swallow the selected description.
		proposed := []*dktypes.Edge{old[1], selected, old[0], old[2], selected}
		if err := repo.ReplaceSemanticBySubdomain(ctx, "d", "s", proposed); err != nil {
			t.Fatal(err)
		}
		after := edgeRowsSnapshot(t, db)
		want := make([][]any, len(before))
		for i := range before {
			want[i] = append([]any(nil), before[i]...)
		}
		want[0][4] = envelope
		if !reflect.DeepEqual(want, after) {
			t.Fatalf("duplicate metadata/assertions lost or repeat grew rows: want=%#v after=%#v", want, after)
		}
		rows, err := repo.GetBetween(ctx, "a", "b")
		if err != nil || len(rows) != 3 || rows[0].Description != envelope || rows[1].Description != old[1].Description {
			t.Fatalf("readback lost selected/legacy assertions: rows=%+v err=%v", rows, err)
		}
	}
	// Omission is still authoritative for machine-only identities, while R8 survives.
	if err := repo.ReplaceSemanticBySubdomain(ctx, "d", "s", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListAll(ctx)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], old[2]) {
		t.Fatalf("machine omission/human retention changed: %+v %v", rows, err)
	}
}

// 旧机器重复行完全保留，不能因 ID 不同而把后来的旧描述写回最早行。
// Passing exact retained rows in reverse order must be a storage-level no-op.
func TestV015SemanticReplacementRetainedDuplicateOrderIsIrrelevant(t *testing.T) {
	t.Parallel()
	db, repo := humanEdgeStore(t)
	ctx := context.Background()
	first := humanEdge(dktypes.KindDependsOn)
	first.Provenance, first.Confidence = dktypes.ProvenanceLLMInferred, 0
	second := *first
	second.Description = "second original condition"
	if err := repo.InsertBatch(ctx, []*dktypes.Edge{first, &second}); err != nil {
		t.Fatal(err)
	}
	old, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := edgeRowsSnapshot(t, db)
	if err := repo.ReplaceSemanticBySubdomain(ctx, "d", "s", []*dktypes.Edge{old[1], old[0]}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, edgeRowsSnapshot(t, db)) {
		t.Fatal("exact retained duplicate rows changed with input order")
	}
}

// 聚合描述更新后新边插入失败，必须把所有旧重复行及人工边恢复到原状态。
// A later insert failure rolls back the selected update and preserves every old row.
func TestV015SemanticReplacementDuplicateUpdateRollback(t *testing.T) {
	t.Parallel()
	db, repo := humanEdgeStore(t)
	ctx := context.Background()
	first := humanEdge(dktypes.KindDependsOn)
	first.Provenance, first.Confidence = dktypes.ProvenanceLLMInferred, 0
	second := *first
	second.Description, second.Properties = "distinct old condition", `{"distinct":true}`
	if err := repo.InsertBatch(ctx, []*dktypes.Edge{first, &second, humanEdge(dktypes.KindReferences)}); err != nil {
		t.Fatal(err)
	}
	before := edgeRowsSnapshot(t, db)
	if _, err := db.Conn().Exec(`CREATE TRIGGER reject_v015 BEFORE INSERT ON edges WHEN NEW.description='reject' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	selected := *first
	selected.Description = "selected aggregate including both old conditions"
	bad := &dktypes.Edge{SourceID: "b", TargetID: "c", Kind: dktypes.KindTriggers, Description: "reject", Confidence: .8, Provenance: dktypes.ProvenanceLLMInferred}
	if err := repo.ReplaceSemanticBySubdomain(ctx, "d", "s", []*dktypes.Edge{&selected, bad}); err == nil {
		t.Fatal("injected failure was hidden")
	}
	if !reflect.DeepEqual(before, edgeRowsSnapshot(t, db)) {
		t.Fatal("failed replacement leaked a selected update or lost old rows")
	}
}
