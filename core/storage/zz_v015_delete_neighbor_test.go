package storage

import (
	"context"
	"reflect"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// 原失败模式：删节点后无法再从边找出独立存活邻居，回写漏掉其只读关系快照。
// Both directions, shared original docs and human neighbors must survive the atomic delete.
func TestV015DeleteNeighborSourceSnapshot(t *testing.T) {
	t.Parallel()
	for _, phantom := range []bool{false, true} {
		name := "delete"
		if phantom {
			name = "phantom"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := v015StorageDB(t)
			nodes, sources, edges := NewNodeRepo(db), NewNodeSourceRepo(db), NewEdgeRepo(db)
			for _, id := range []string{"deleted", "incoming", "outgoing", "clean"} {
				node := v015StorageNode(id)
				if id == "incoming" {
					node.Provenance = dktypes.ProvenanceHumanCurated
				}
				if err := nodes.Insert(ctx, node); err != nil {
					t.Fatal(err)
				}
			}
			rows := []NodeSource{
				{NodeUUID: "deleted", MemberID: "d", Skill: "one", FilePath: "original/deleted.md"},
				{NodeUUID: "outgoing", MemberID: "o1", Skill: "one", FilePath: "original/outgoing.md", StartLine: 7, EndLine: 8},
				{NodeUUID: "outgoing", MemberID: "o2", Skill: "two", FilePath: "original/mirror.md", StartLine: 21, EndLine: 22},
				{NodeUUID: "incoming", MemberID: "i1", Skill: "one", FilePath: "original/incoming.md"},
				{NodeUUID: "incoming", MemberID: "i2", Skill: "two", FilePath: "original/mirror.md"},
				{NodeUUID: "clean", MemberID: "c", Skill: "one", FilePath: "original/clean.md"},
			}
			if err := sources.InsertBatch(ctx, rows); err != nil {
				t.Fatal(err)
			}
			for _, pair := range [][2]string{{"deleted", "outgoing"}, {"incoming", "deleted"}, {"deleted", "outgoing"}, {"deleted", "deleted"}, {"outgoing", "incoming"}} {
				if err := edges.Insert(ctx, &dktypes.Edge{SourceID: pair[0], TargetID: pair[1], Kind: dktypes.KindReferences, Provenance: dktypes.ProvenanceExtraction, Confidence: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if phantom {
				// Reproduce the legacy half-committed state while retaining orphan edges.
				if _, err := db.Conn().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Conn().Exec(`DELETE FROM nodes WHERE id='deleted'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Conn().Exec(`PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
			}
			wantSources, err := sources.ListAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var keptSources []NodeSource
			for _, row := range wantSources {
				if row.NodeUUID != "deleted" {
					keptSources = append(keptSources, row)
				}
			}
			before, err := nodes.GetByID(ctx, "incoming")
			if err != nil {
				t.Fatal(err)
			}
			got, err := NewIncrementalMutationRepo(db).ApplySourceDeletion(ctx, "deleted", "original/deleted.md")
			if err != nil || got == nil || got.Phantom != phantom || got.Deleted == phantom {
				t.Fatalf("delete=%+v err=%v", got, err)
			}
			wantDocs := []string{"original/incoming.md", "original/mirror.md", "original/outgoing.md"}
			if !reflect.DeepEqual(got.AffectedSourceDocs, wantDocs) {
				t.Fatalf("lost/expanded original docs: %v != %v", got.AffectedSourceDocs, wantDocs)
			}
			after, err := nodes.GetByID(ctx, "incoming")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("human neighbor changed: %+v -> %+v (%v)", before, after, err)
			}
			stored, err := sources.ListAll(ctx)
			if err != nil || !reflect.DeepEqual(stored, keptSources) {
				t.Fatalf("neighbor original identity changed: %+v (%v)", stored, err)
			}
			remaining, err := edges.ListAll(ctx)
			if err != nil || len(remaining) != 1 || remaining[0].SourceID != "outgoing" || remaining[0].TargetID != "incoming" {
				t.Fatalf("unrelated relationship changed: %+v (%v)", remaining, err)
			}
			replay, err := NewIncrementalMutationRepo(db).ApplySourceDeletion(ctx, "deleted", "original/deleted.md")
			if err != nil || len(replay.AffectedSourceDocs) != 0 {
				t.Fatalf("settled replay dirtied neighbors: %+v (%v)", replay, err)
			}
		})
	}
}

// 已保留的节点不删关系；无贡献重放和人工边保护不能扩大回写或误删人工知识。
func TestV015DeleteNeighborRetainedEndpoint(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"shared", "human-node", "human-edge", "no-contribution"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db := v015StorageDB(t)
			nodes, sources, edges := NewNodeRepo(db), NewNodeSourceRepo(db), NewEdgeRepo(db)
			n := v015StorageNode("retained")
			if mode == "human-node" {
				n.Provenance = dktypes.ProvenanceHumanCurated
			}
			for _, node := range []*dktypes.Node{n, v015StorageNode("neighbor")} {
				if err := nodes.Insert(ctx, node); err != nil {
					t.Fatal(err)
				}
			}
			rows := []NodeSource{{NodeUUID: "retained", MemberID: "m", Skill: "a", FilePath: "a.md"}, {NodeUUID: "neighbor", MemberID: "n", Skill: "b", FilePath: "neighbor.md"}}
			if mode == "shared" {
				rows = append(rows, NodeSource{NodeUUID: "retained", MemberID: "m2", Skill: "b", FilePath: "b.md"})
			}
			if err := sources.InsertBatch(ctx, rows); err != nil {
				t.Fatal(err)
			}
			prov := dktypes.ProvenanceExtraction
			if mode == "human-edge" {
				prov = dktypes.ProvenanceHumanCurated
			}
			if err := edges.Insert(ctx, &dktypes.Edge{SourceID: "retained", TargetID: "neighbor", Kind: dktypes.KindReferences, Provenance: prov, Confidence: 1}); err != nil {
				t.Fatal(err)
			}
			path := "a.md"
			if mode == "no-contribution" {
				path = "stale.md"
			}
			got, err := NewIncrementalMutationRepo(db).ApplySourceDeletion(ctx, "retained", path)
			if err != nil || got.Deleted || got.Phantom || len(got.AffectedSourceDocs) != 0 || got.NoContribution != (mode == "no-contribution") {
				t.Fatalf("retained=%+v err=%v", got, err)
			}
			if node, err := nodes.GetByID(ctx, "retained"); err != nil || node == nil {
				t.Fatal("retained node lost", err)
			}
			if remaining, err := edges.ListAll(ctx); err != nil || len(remaining) != 1 {
				t.Fatal("retained relationship lost", remaining, err)
			}
		})
	}
}

// 收集路径后发生删边失败时，来源/节点/边和结果必须整体回滚，不能发布虚假提交。
func TestV015DeleteNeighborRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := v015StorageDB(t)
	nodes, sources, edges := NewNodeRepo(db), NewNodeSourceRepo(db), NewEdgeRepo(db)
	for _, id := range []string{"deleted", "neighbor"} {
		if err := nodes.Insert(ctx, v015StorageNode(id)); err != nil {
			t.Fatal(err)
		}
	}
	rows := []NodeSource{{NodeUUID: "deleted", MemberID: "d", Skill: "a", FilePath: "a.md"}, {NodeUUID: "neighbor", MemberID: "n", Skill: "b", FilePath: "b.md"}}
	if err := sources.InsertBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := edges.Insert(ctx, &dktypes.Edge{SourceID: "neighbor", TargetID: "deleted", Kind: dktypes.KindReferences, Provenance: dktypes.ProvenanceExtraction, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	beforeEdges, _ := edges.ListAll(ctx)
	beforeNode, _ := nodes.GetByID(ctx, "deleted")
	if _, err := db.Conn().Exec(`CREATE TRIGGER reject_neighbor_delete BEFORE DELETE ON edges BEGIN SELECT RAISE(ABORT,'injected delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	got, err := NewIncrementalMutationRepo(db).ApplySourceDeletion(ctx, "deleted", "a.md")
	if err == nil || got != nil {
		t.Fatalf("failed mutation published a result: %+v (%v)", got, err)
	}
	afterEdges, _ := edges.ListAll(ctx)
	afterNode, _ := nodes.GetByID(ctx, "deleted")
	afterSources, _ := sources.ListAll(ctx)
	if !reflect.DeepEqual(beforeEdges, afterEdges) || !reflect.DeepEqual(beforeNode, afterNode) || !reflect.DeepEqual(rows, afterSources) {
		t.Fatal("failure partially committed graph/source changes")
	}
}
