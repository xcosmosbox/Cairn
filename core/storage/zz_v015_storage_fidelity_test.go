package storage

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

func v015StorageDB(t *testing.T) *DB {
	t.Helper()
	db, err := NewDB(DBOptions{Path: filepath.Join(t.TempDir(), "fidelity.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func v015StorageNode(id string) *dktypes.Node {
	return &dktypes.Node{ID: id, Label: dktypes.LabelEntity, Name: "Pump", Summary: "Old qualified summary", Description: "Old complete detail", Domain: "d", Subdomain: "sd", FileSlug: "stable-pump", Properties: `{"operator":"source"}`, SourceRefs: "a,b", Confidence: .71, Provenance: dktypes.ProvenanceLLMInferred, CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), UpdatedAt: time.Date(2025, 2, 2, 3, 4, 5, 0, time.UTC)}
}

// 原始位置不能被重复输入计数掩盖；中途 SQL 失败不得报告此前已执行行为提交。
func TestV015StorageSourceBatchCountsAndRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := v015StorageDB(t)
	repo := NewNodeSourceRepo(db)
	a := NodeSource{NodeUUID: "shared", MemberID: "m", Skill: "a", FilePath: "original/a.md", StartLine: 17, EndLine: 19}
	b := NodeSource{NodeUUID: "shared", MemberID: "m", Skill: "b", FilePath: "original/b.md", StartLine: 31, EndLine: 34}
	for repeat := 0; repeat < 2; repeat++ {
		got, err := repo.InsertBatchWithResult(ctx, []NodeSource{a, a, b})
		if err != nil || got != (NodeSourceBatchResult{Attempted: 3, DistinctSelected: 2, DuplicateRows: 1, Committed: 2}) {
			t.Fatalf("counts=%+v err=%v", got, err)
		}
	}
	before, _ := repo.ListAll(ctx)
	if _, err := db.Conn().Exec(`CREATE TRIGGER reject_late_source BEFORE INSERT ON node_sources WHEN NEW.file_path='original/fail.md' BEGIN SELECT RAISE(ABORT,'injected late failure'); END`); err != nil {
		t.Fatal(err)
	}
	a.StartLine = 21
	fail := NodeSource{NodeUUID: "shared", MemberID: "new", Skill: "a", FilePath: "original/fail.md"}
	got, err := repo.InsertBatchWithResult(ctx, []NodeSource{a, fail, fail})
	if err == nil || got.Committed != 0 || got.Attempted != 3 || got.DistinctSelected != 2 || got.DuplicateRows != 1 {
		t.Fatalf("failed transaction counts=%+v err=%v", got, err)
	}
	after, _ := repo.ListAll(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed batch changed origins: %v -> %v", before, after)
	}
	// A semantic member cannot silently switch its source skill within one incidence.
	conflict := b
	conflict.Skill = "different-skill"
	if got, err := repo.InsertBatchWithResult(ctx, []NodeSource{b, conflict}); err == nil || got.Committed != 0 {
		t.Fatalf("conflicting skills silently collapsed: %+v %v", got, err)
	}
}

// 旧冲突检查误拒绝合法重复位置；只存一个真实代表区间，计数仍按来源关联去重。
func TestV015StorageRepresentativeSourceSpan(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, locations := range [][][2]int{
		{{42, 43}, {7, 8}}, {{7, 8}, {42, 43}}, {{0, 0}, {7, 8}}, {{7, 8}, {0, 0}}, {{2, 0}, {7, 8}}, {{7, 12}, {7, 8}},
	} {
		db := v015StorageDB(t)
		repo := NewNodeSourceRepo(db)
		var rows []NodeSource
		for _, span := range locations {
			rows = append(rows, NodeSource{NodeUUID: "n", MemberID: "m", Skill: "ops", FilePath: "original.md", StartLine: span[0], EndLine: span[1]})
		}
		for repeat := 0; repeat < 2; repeat++ {
			counts, err := repo.InsertBatchWithResult(ctx, rows)
			if err != nil || counts != (NodeSourceBatchResult{Attempted: 2, DistinctSelected: 1, DuplicateRows: 1, Committed: 1}) {
				t.Fatalf("locations=%v counts=%+v err=%v", locations, counts, err)
			}
			stored, err := repo.ListAll(ctx)
			if err != nil || len(stored) != 1 || stored[0].StartLine != 7 || stored[0].EndLine != 8 {
				t.Fatalf("not a genuine canonical location: %+v %v", stored, err)
			}
			rows[0], rows[1] = rows[1], rows[0]
		}
	}
}

// 原有 reflow 只能保存正文；摘要/正文必须同语句更新，人工内容与旁支字段不得覆盖。
func TestV015StorageMachineContentPair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := v015StorageDB(t)
	repo := NewNodeRepo(db)
	n := v015StorageNode("pinned-uuid")
	if err := repo.Insert(ctx, n); err != nil {
		t.Fatal(err)
	}
	before, _ := repo.GetByID(ctx, n.ID)
	summary := "Operate only above 2 bar"
	detail := "Do not operate during maintenance. Calibration uncertain; source B disagrees. Literal 123e4567-e89b-12d3-a456-426614174000."
	if applied, err := repo.UpdateMachineContent(ctx, n.ID, summary, detail); err != nil || !applied {
		t.Fatalf("pair not applied: %v %v", applied, err)
	}
	after, _ := repo.GetByID(ctx, n.ID)
	want := *before
	want.Summary = summary
	want.Description = detail
	want.UpdatedAt = after.UpdatedAt
	if !reflect.DeepEqual(&want, after) {
		t.Fatalf("unrelated machine fields changed: want=%+v got=%+v", want, after)
	}
	if applied, err := repo.UpdateMachineContent(ctx, n.ID, summary, detail); err != nil || !applied {
		t.Fatal(applied, err)
	}
	repeated, _ := repo.GetByID(ctx, n.ID)
	if !reflect.DeepEqual(after, repeated) {
		t.Fatal("repeat changed exact stored node")
	}
	if _, err := db.Conn().Exec(`CREATE TRIGGER reject_pair BEFORE UPDATE OF description ON nodes WHEN NEW.description='fail' BEGIN SELECT RAISE(ABORT,'injected pair failure'); END`); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.UpdateMachineContent(ctx, n.ID, "wrong half", "fail"); err == nil || applied {
		t.Fatal("partial pair reported committed")
	}
	failed, _ := repo.GetByID(ctx, n.ID)
	if !reflect.DeepEqual(after, failed) {
		t.Fatal("pair failure left mismatched summary/detail")
	}
	human := v015StorageNode("human")
	human.Provenance = dktypes.ProvenanceHumanCurated
	if err := repo.Insert(ctx, human); err != nil {
		t.Fatal(err)
	}
	humanBefore, _ := repo.GetByID(ctx, human.ID)
	if applied, err := repo.UpdateMachineContent(ctx, human.ID, summary, detail); err != nil || applied {
		t.Fatalf("curated node overwritten: %v %v", applied, err)
	}
	humanAfter, _ := repo.GetByID(ctx, human.ID)
	if !reflect.DeepEqual(humanBefore, humanAfter) {
		t.Fatal("human fields changed")
	}
	if applied, err := repo.UpdateMachineContent(ctx, "absent", summary, detail); err != nil || applied {
		t.Fatal("missing UUID reported applied")
	}
}

// 共享节点重复/变更/移动来源后，每个原始来源继续独立贡献；只在机器节点最后引用归零时删。
func TestV015StorageOriginalSourceLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := v015StorageDB(t)
	nodes, sources, files, mutation := NewNodeRepo(db), NewNodeSourceRepo(db), NewFileStateRepo(db), NewIncrementalMutationRepo(db)
	n := v015StorageNode("shared")
	clean := v015StorageNode("clean")
	for _, node := range []*dktypes.Node{n, clean} {
		if err := nodes.Insert(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	cleanBefore, _ := nodes.GetByID(ctx, clean.ID)
	a := NodeSource{NodeUUID: n.ID, MemberID: "a", Skill: "a", FilePath: "original/a.md", StartLine: 17, EndLine: 19}
	b := NodeSource{NodeUUID: n.ID, MemberID: "b", Skill: "b", FilePath: "original/b.md", StartLine: 31, EndLine: 34}
	for i := 0; i < 2; i++ {
		if err := sources.InsertBatch(ctx, []NodeSource{a, a, b}); err != nil {
			t.Fatal(err)
		}
	}
	a.StartLine = 20
	a.EndLine = 22
	if err := sources.InsertBatch(ctx, []NodeSource{a}); err != nil {
		t.Fatal(err)
	}
	if got, err := sources.CountDistinctFilesByNode(ctx, n.ID); err != nil || got != 2 {
		t.Fatal(got, err)
	}
	if err := files.Upsert(ctx, "repo", a.FilePath, "raw-hash"); err != nil {
		t.Fatal(err)
	}
	if err := mutation.MoveSourceDocuments(ctx, "repo", []SourceDocumentMove{{OldPath: a.FilePath, NewPath: "original/moved.md", Sources: []NodeSource{a}, PreviousHash: "raw-hash", ContentHash: "raw-hash"}}); err != nil {
		t.Fatal(err)
	}
	a.FilePath = "original/moved.md"
	got, _ := sources.ListByNode(ctx, n.ID)
	if !reflect.DeepEqual(got, []NodeSource{a, b}) {
		t.Fatalf("move altered raw identity/span or sibling: %+v", got)
	}
	if old, _ := files.Get(ctx, "repo", "original/a.md"); old != nil {
		t.Fatal("old file baseline retained")
	}
	if moved, _ := files.Get(ctx, "repo", a.FilePath); moved == nil || moved.ContentHash != "raw-hash" {
		t.Fatal("moved baseline lost")
	}
	deleted, err := mutation.ApplySourceDeletion(ctx, n.ID, a.FilePath)
	if err != nil || deleted.Deleted || deleted.RemainingDocs != 1 {
		t.Fatalf("shared node lost on first delete: %+v %v", deleted, err)
	}
	survivor, _ := nodes.GetByID(ctx, n.ID)
	if survivor == nil || survivor.ID != n.ID || survivor.Summary != n.Summary || survivor.Description != n.Description || survivor.SourceRefs != "b" {
		t.Fatalf("surviving source/node changed: %+v", survivor)
	}
	if rows, _ := sources.ListByNode(ctx, n.ID); !reflect.DeepEqual(rows, []NodeSource{b}) {
		t.Fatal(rows)
	}
	replay, err := mutation.ApplySourceDeletion(ctx, n.ID, a.FilePath)
	if err != nil || !replay.NoContribution || replay.Deleted {
		t.Fatal(replay, err)
	}
	last, err := mutation.ApplySourceDeletion(ctx, n.ID, b.FilePath)
	if err != nil || !last.Deleted || last.RemainingDocs != 0 {
		t.Fatal(last, err)
	}
	if gone, _ := nodes.GetByID(ctx, n.ID); gone != nil {
		t.Fatal("unreferenced machine node retained")
	}
	cleanAfter, _ := nodes.GetByID(ctx, clean.ID)
	if !reflect.DeepEqual(cleanBefore, cleanAfter) {
		t.Fatal("clean node changed")
	}
}

// 原 C2 在最后原始来源删除时也删除 human_curated 节点，丢失独立人工知识。
func TestV015StorageSourceDeletionKeepsCuratedContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := v015StorageDB(t)
	n := v015StorageNode("human")
	n.Provenance = dktypes.ProvenanceHumanCurated
	nodes, sources := NewNodeRepo(db), NewNodeSourceRepo(db)
	if err := nodes.Insert(ctx, n); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"domain::d", "skill::a"} {
		layer := v015StorageNode(id)
		if id == "domain::d" {
			layer.Label = dktypes.LabelDomain
		} else {
			layer.Label = dktypes.LabelSkill
		}
		if err := nodes.Insert(ctx, layer); err != nil {
			t.Fatal(err)
		}
	}
	if err := sources.InsertBatch(ctx, []NodeSource{{NodeUUID: n.ID, MemberID: "m", Skill: "a", FilePath: "original/a.md"}}); err != nil {
		t.Fatal(err)
	}
	edges := NewEdgeRepo(db)
	for _, edge := range []*dktypes.Edge{
		{SourceID: n.ID, TargetID: "domain::d", Kind: dktypes.KindReferences, Description: "Operator's independent relationship", Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1},
		{SourceID: "skill::a", TargetID: "domain::d", Kind: dktypes.KindProvides, Description: "Operator's independent contribution", Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1},
	} {
		if err := edges.Insert(ctx, edge); err != nil {
			t.Fatal(err)
		}
	}
	beforeEdges, _ := edges.ListAll(ctx)
	got, err := NewIncrementalMutationRepo(db).ApplySourceDeletion(ctx, n.ID, "original/a.md")
	if err != nil || got.Deleted || got.RemainingDocs != 0 {
		t.Fatalf("last original deleted human content: %+v %v", got, err)
	}
	kept, _ := nodes.GetByID(ctx, n.ID)
	if kept == nil || kept.Summary != n.Summary || kept.Description != n.Description || kept.Provenance != n.Provenance || kept.Properties != n.Properties || kept.SourceRefs != "" {
		t.Fatalf("human content or surviving provenance wrong: %+v", kept)
	}
	if remaining, _ := sources.CountDistinctFilesByNode(ctx, n.ID); remaining != 0 {
		t.Fatal("stale original reference retained")
	}
	afterEdges, _ := edges.ListAll(ctx)
	if !reflect.DeepEqual(beforeEdges, afterEdges) {
		t.Fatal("human semantic/provides edge changed")
	}
}

// 人工边也是人工知识，不能因其机器端点最后失去原始来源而被级联清除。
func TestV015StorageSourceDeletionKeepsCuratedRelationship(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := v015StorageDB(t)
	nodes, sources, edges := NewNodeRepo(db), NewNodeSourceRepo(db), NewEdgeRepo(db)
	for _, id := range []string{"machine", "target"} {
		if err := nodes.Insert(ctx, v015StorageNode(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sources.InsertBatch(ctx, []NodeSource{{NodeUUID: "machine", MemberID: "m", Skill: "a", FilePath: "original/a.md"}}); err != nil {
		t.Fatal(err)
	}
	if err := edges.Insert(ctx, &dktypes.Edge{SourceID: "machine", TargetID: "target", Kind: dktypes.KindReferences, Description: "Operator confirmed relation", Confidence: 1, Provenance: dktypes.ProvenanceHumanCurated}); err != nil {
		t.Fatal(err)
	}
	before, _ := edges.ListAll(ctx)
	got, err := NewIncrementalMutationRepo(db).ApplySourceDeletion(ctx, "machine", "original/a.md")
	if err != nil || got.Deleted || got.RemainingDocs != 0 {
		t.Fatalf("curated relationship endpoint deleted: %+v %v", got, err)
	}
	if kept, _ := nodes.GetByID(ctx, "machine"); kept == nil || kept.SourceRefs != "" {
		t.Fatal("curated graph endpoint lost or stale source retained")
	}
	after, _ := edges.ListAll(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("human relationship changed")
	}
}
