package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent adopters must not overwrite each other's repository assignment.
func TestRepositoryIdentityImmutableAcrossConnections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity.db")
	one, err := NewDB(DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := NewDB(DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, db := range []*DB{one, two} {
		wg.Add(1)
		go func(db *DB, identity string) {
			defer wg.Done()
			<-start
			results <- NewRepositoryIdentityRepo(db).Set(ctx, identity)
		}(db, []string{"git:github.com/owner/a", "git:github.com/owner/b"}[i])
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected one owner, successful assignments=%d", successes)
	}
	identity, err := NewRepositoryIdentityRepo(one).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewRepositoryIdentityRepo(two).Set(ctx, identity); err != nil {
		t.Fatalf("idempotent identity assignment: %v", err)
	}
}
