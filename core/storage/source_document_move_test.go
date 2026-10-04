package storage

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSourceDocumentMoveRollsBackWholeBatch(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"file_state failure", "late destination conflict", "stale source snapshot"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			db, err := NewDB(DBOptions{Path: filepath.Join(t.TempDir(), "move.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			sources, files := NewNodeSourceRepo(db), NewFileStateRepo(db)
			a := NodeSource{NodeUUID: "a", MemberID: "m1", Skill: "skill", FilePath: "a.md", StartLine: 10, EndLine: 12}
			c := NodeSource{NodeUUID: "c", MemberID: "m2", Skill: "skill", FilePath: "c.md"}
			if err := sources.InsertBatch(ctx, []NodeSource{a, c}); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"a.md", "c.md"} {
				if err := files.Upsert(ctx, "repo", path, "hash"); err != nil {
					t.Fatal(err)
				}
			}
			version, err := NewKBVersionRepo(db).Bump(ctx)
			if err != nil {
				t.Fatal(err)
			}
			moves := []SourceDocumentMove{
				{OldPath: "a.md", NewPath: "b.md", Sources: []NodeSource{a}, PreviousHash: "hash", ContentHash: "hash"},
				{OldPath: "c.md", NewPath: "d.md", Sources: []NodeSource{c}, PreviousHash: "hash", ContentHash: "hash"},
			}
			switch failure {
			case "file_state failure":
				_, err = db.Conn().Exec(`CREATE TRIGGER reject_move BEFORE UPDATE ON file_states
					WHEN NEW.file_path='b.md' BEGIN SELECT RAISE(ABORT, 'injected file_state failure'); END`)
			case "late destination conflict":
				err = files.Upsert(ctx, "repo", "d.md", "occupied")
			case "stale source snapshot":
				moves[1].Sources[0].MemberID = "wrong"
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeSources, _ := sources.ListAll(ctx)
			beforeStates, _ := files.ListByRepo(ctx, "repo")
			if err := NewIncrementalMutationRepo(db).MoveSourceDocuments(ctx, "repo", moves); err == nil {
				t.Fatal("failing batch succeeded")
			}
			afterSources, _ := sources.ListAll(ctx)
			afterStates, _ := files.ListByRepo(ctx, "repo")
			afterVersion, _ := NewKBVersionRepo(db).Current(ctx)
			if !reflect.DeepEqual(beforeSources, afterSources) || !reflect.DeepEqual(beforeStates, afterStates) || afterVersion != version {
				t.Fatal("failed move partially committed sources, file_states or KB version")
			}
		})
	}
}
