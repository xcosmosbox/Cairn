package pipeline

import (
	"context"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"path/filepath"
	"testing"
	"time"
)

func TestStageContextRetainsLeaseGuardAndSynchronousCancellation(t *testing.T) {
	journal, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if won, err := journal.AcquireLease(context.Background(), "repo", "owner", time.Minute); err != nil || !won {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(journal.WithRepoLease(context.Background(), "repo", "owner"))
	stage, stop := stageCtx(parent, time.Minute)
	defer stop()
	if err := journal.ReleaseLease(context.Background(), "repo", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AcquireLease(context.Background(), "repo", "successor", time.Minute); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := writeback.WriteDocContentContext(stage, root, "doc.md", "", nil, time.Now()); err == nil {
		t.Fatal("stage discarded lease ownership")
	}
	cancel()
	if stage.Err() != context.Canceled {
		t.Fatal("parent cancellation was asynchronous")
	}
}
