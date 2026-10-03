package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/storage"
)

// 原来的分离 Get/Set 让所有调用成功却只留下一个快照；独立连接也必须可靠追加。
// Concurrent successful recordings must all survive across independent connections.
func TestSentinelHistoryConcurrentAppends(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	manifest := storage.NewManifestRepo(db)
	if err := manifest.Set(ctx, "protected", "unchanged"); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	const perWriter = 4
	start := make(chan struct{})
	failures := make(chan error, writers)
	var wg sync.WaitGroup
	for id := 0; id < writers; id++ {
		connection, err := storage.OpenExisting(path)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		repo := storage.NewManifestRepo(connection)
		wg.Add(1)
		go func(id int, repo *storage.ManifestRepo) {
			defer wg.Done()
			<-start
			for i := 0; i < perWriter; i++ {
				point := &SentinelSnapshot{Timestamp: time.Unix(int64(id*perWriter+i), 0).UTC()}
				if err := appendSentinelHistory(ctx, repo, point, 0); err != nil {
					failures <- err
					return
				}
			}
		}(id, repo)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	raw, err := manifest.Get(ctx, sentinelHistoryKey)
	if err != nil {
		t.Fatal(err)
	}
	var history []SentinelSnapshot
	if err := json.Unmarshal([]byte(raw), &history); err != nil {
		t.Fatal(err)
	}
	seen := make(map[int64]bool)
	for _, point := range history {
		seen[point.Timestamp.Unix()] = true
	}
	if len(history) != writers*perWriter || len(seen) != writers*perWriter {
		t.Fatalf("lost successful snapshots: count=%d unique=%d", len(history), len(seen))
	}
	if raw, err := manifest.Get(ctx, "protected"); err != nil || raw != "unchanged" {
		t.Fatalf("unrelated metadata changed: %q %v", raw, err)
	}
}

func TestSentinelHistoryCapacityAndCorruptValue(t *testing.T) {
	t.Parallel()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "knowledge.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := storage.NewManifestRepo(db)
	for i := 0; i < 5; i++ {
		if err := appendSentinelHistory(ctx, repo, &SentinelSnapshot{Timestamp: time.Unix(int64(i), 0).UTC()}, 3); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := repo.Get(ctx, sentinelHistoryKey)
	if err != nil {
		t.Fatal(err)
	}
	var history []SentinelSnapshot
	if err := json.Unmarshal([]byte(raw), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || history[0].Timestamp.Unix() != 2 || history[2].Timestamp.Unix() != 4 {
		t.Fatalf("ring retention changed: %+v", history)
	}
	if err := repo.Set(ctx, sentinelHistoryKey, "broken existing history"); err != nil {
		t.Fatal(err)
	}
	if err := appendSentinelHistory(ctx, repo, &SentinelSnapshot{}, 3); err == nil {
		t.Fatal("corrupt history silently discarded")
	}
	raw, err = repo.Get(ctx, sentinelHistoryKey)
	if err != nil || raw != "broken existing history" {
		t.Fatalf("failed append damaged original: %q %v", raw, err)
	}
}
