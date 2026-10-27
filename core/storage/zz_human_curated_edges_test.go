package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

func humanEdgeStore(t *testing.T) (*DB, *EdgeRepo) {
	t.Helper()
	db, err := NewDB(DBOptions{Path: filepath.Join(t.TempDir(), "edges.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{"a", "b", "c"} {
		if err := NewNodeRepo(db).Insert(context.Background(), &dktypes.Node{ID: id, Label: dktypes.LabelConcept, Name: id, Summary: id, Domain: "d", Subdomain: "s", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return db, NewEdgeRepo(db)
}

func humanEdge(kind dktypes.RelationKind) *dktypes.Edge {
	return &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: kind, Description: "Human-authored condition", Properties: `{"owner":"human"}`, Provenance: dktypes.ProvenanceHumanCurated, Confidence: .93, SourceRefs: "original-a,original-b", Bidirectional: 1, Cardinality: "M:N", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
}

// 保留 SQL 类型与 NULL，避免 public Edge 把 NULL 映射为空字符串后掩盖重写。
// Compare raw storage values so NULL-to-empty changes cannot hide behind public models.
func edgeRowsSnapshot(t *testing.T, db *DB) [][]any {
	t.Helper()
	rows, err := db.Conn().Query(`SELECT id, source_id, target_id, kind, description, properties, provenance, confidence, source_refs, bidirectional, cardinality, created_at FROM edges ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result [][]any
	for rows.Next() {
		values := make([]any, 12)
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if b, ok := value.([]byte); ok {
				values[i] = append([]byte(nil), b...)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// 原故障：直接传入空集合删除人工边，同身份机器项覆盖描述/置信度但保留人工标志。
// The transaction boundary must defend R8 even when callers bypass I-7 protections.
func TestAutomaticSemanticReplacementPreservesCompleteHumanPayload(t *testing.T) {
	t.Parallel()
	for _, kind := range []dktypes.RelationKind{dktypes.KindTriggers, dktypes.KindDependsOn, dktypes.KindReferences, dktypes.KindGeneralizes, dktypes.KindComposes, dktypes.KindContradicts, dktypes.KindProvides} {
		for _, nulls := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/nulls=%t", kind, nulls), func(t *testing.T) {
				db, repo := humanEdgeStore(t)
				original := humanEdge(kind)
				if err := repo.Insert(context.Background(), original); err != nil {
					t.Fatal(err)
				}
				if nulls {
					if _, err := db.Conn().Exec(`UPDATE edges SET description=NULL, properties=NULL, source_refs=NULL, cardinality=NULL`); err != nil {
						t.Fatal(err)
					}
				}
				before := edgeRowsSnapshot(t, db)
				conflict := &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: kind, Description: "Machine replacement", Confidence: .2, Provenance: dktypes.ProvenanceLLMInferred, Properties: "machine", SourceRefs: "machine", Cardinality: "1:1", CreatedAt: time.Now()}
				for _, proposed := range [][]*dktypes.Edge{nil, {conflict}} {
					if err := repo.ReplaceSemanticBySubdomain(context.Background(), "d", "s", proposed); err != nil {
						t.Fatal(err)
					}
					if after := edgeRowsSnapshot(t, db); !reflect.DeepEqual(before, after) {
						t.Fatalf("human storage row changed: before=%#v after=%#v", before, after)
					}
				}
			})
		}
	}
}

// 旧库同身份可同时有机器/人工/多条人工行；保护不能取决于最早行的 provenance。
func TestAutomaticSemanticReplacementPreservesMixedHumanDuplicateIdentity(t *testing.T) {
	t.Parallel()
	for _, humanFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(humanFirst), func(t *testing.T) {
			db, repo := humanEdgeStore(t)
			human := humanEdge(dktypes.KindDependsOn)
			machine := *human
			machine.Provenance, machine.Description = dktypes.ProvenanceLLMInferred, "Older machine row"
			secondHuman := *human
			secondHuman.Description, secondHuman.Confidence = "Second distinct human row", .87
			order := []*dktypes.Edge{&machine, human, &secondHuman}
			if humanFirst {
				order = []*dktypes.Edge{human, &machine, &secondHuman}
			}
			if err := repo.InsertBatch(context.Background(), order); err != nil {
				t.Fatal(err)
			}
			before := edgeRowsSnapshot(t, db)
			conflict := machine
			conflict.Description, conflict.Confidence = "Generated overwrite", .1
			for _, proposed := range [][]*dktypes.Edge{nil, {&conflict}} {
				if err := repo.ReplaceSemanticBySubdomain(context.Background(), "d", "s", proposed); err != nil {
					t.Fatal(err)
				}
				if after := edgeRowsSnapshot(t, db); !reflect.DeepEqual(before, after) {
					t.Fatalf("mixed identity was collapsed or overwritten: before=%#v after=%#v", before, after)
				}
			}
		})
	}
}

// R8 保护只约束自动替换；机器身份仍可更新/删除，显式人工修改和删除仍可执行。
func TestHumanProtectionKeepsMachineAndExplicitMutationContracts(t *testing.T) {
	t.Parallel()
	db, repo := humanEdgeStore(t)
	ctx := context.Background()
	machine := humanEdge(dktypes.KindDependsOn)
	machine.Provenance = dktypes.ProvenanceLLMInferred
	duplicate := *machine
	duplicate.Description = "legacy duplicate"
	if err := repo.InsertBatch(ctx, []*dktypes.Edge{machine, &duplicate}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated := *machine
	updated.Description, updated.Confidence, updated.Properties = "Machine can revise", .4, "not authoritative on replacement"
	if err := repo.ReplaceSemanticBySubdomain(ctx, "d", "s", []*dktypes.Edge{&updated}); err != nil {
		t.Fatal(err)
	}
	after, err := repo.ListAll(ctx)
	// .5 不再丢弃完整旧机器重复行；只更新最早行的模型字段，后续行整行保留。
	// Lossless replacement retains siblings instead of collapsing their distinct payloads.
	if err != nil || len(after) != 2 || after[0].ID != before[0].ID || after[0].Description != updated.Description || after[0].Confidence != updated.Confidence || after[0].Properties != before[0].Properties || !reflect.DeepEqual(after[1], before[1]) {
		t.Fatalf("machine update/duplicate retention changed: %+v %v", after, err)
	}
	if err := repo.ReplaceSemanticBySubdomain(ctx, "d", "s", nil); err != nil {
		t.Fatal(err)
	}
	if rows := edgeRowsSnapshot(t, db); len(rows) != 0 {
		t.Fatalf("omitted machine identity became protected: %#v", rows)
	}
	human := humanEdge(dktypes.KindReferences)
	if err := repo.Insert(ctx, human); err != nil {
		t.Fatal(err)
	}
	manual := *human
	manual.Description, manual.Confidence, manual.Properties = "Deliberate manual edit", .99, `{"edited":true}`
	inserted, err := repo.InsertIfAbsent(ctx, &manual)
	if err != nil || inserted {
		t.Fatalf("explicit identity update failed: inserted=%t err=%v", inserted, err)
	}
	after, err = repo.ListAll(ctx)
	if err != nil || len(after) != 1 || after[0].Description != manual.Description || after[0].Confidence != manual.Confidence || after[0].Properties != manual.Properties {
		t.Fatalf("manual edit blocked: %+v %v", after, err)
	}
	deleted, err := repo.DeleteBetween(ctx, "a", "b", dktypes.KindReferences)
	if err != nil || deleted != 1 || len(edgeRowsSnapshot(t, db)) != 0 {
		t.Fatalf("explicit manual delete blocked: %d %v", deleted, err)
	}
}

// 新机器边落库失败时，整次自动替换回滚；保护分支不能改变事务原子性。
func TestHumanEdgeGuardRetainsReplacementRollback(t *testing.T) {
	t.Parallel()
	db, repo := humanEdgeStore(t)
	ctx := context.Background()
	machine := humanEdge(dktypes.KindReferences)
	machine.TargetID, machine.Provenance = "c", dktypes.ProvenanceLLMInferred
	if err := repo.InsertBatch(ctx, []*dktypes.Edge{humanEdge(dktypes.KindDependsOn), machine}); err != nil {
		t.Fatal(err)
	}
	before := edgeRowsSnapshot(t, db)
	if _, err := db.Conn().Exec(`CREATE TRIGGER reject_generated BEFORE INSERT ON edges WHEN NEW.description='reject' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	bad := &dktypes.Edge{SourceID: "b", TargetID: "c", Kind: dktypes.KindTriggers, Description: "reject", Confidence: .8, Provenance: dktypes.ProvenanceLLMInferred}
	if err := repo.ReplaceSemanticBySubdomain(ctx, "d", "s", []*dktypes.Edge{bad}); err == nil {
		t.Fatal("injected replacement failure was hidden")
	}
	if after := edgeRowsSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed replacement leaked mutations: before=%#v after=%#v", before, after)
	}
}
