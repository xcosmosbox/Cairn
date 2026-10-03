package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func manifestCounter(current string) (string, error) {
	value := 0
	if current != "" {
		var err error
		value, err = strconv.Atoi(current)
		if err != nil {
			return "", err
		}
	}
	return strconv.Itoa(value + 1), nil
}

func manifestUpdateFixture(t *testing.T) (string, *DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observations.db")
	db, err := NewDB(DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return path, db
}

// Separate wrappers have distinct Go mutexes, so only the SQLite transaction
// can prevent both callbacks from transforming the same stale value.
func TestManifestUpdateAcrossConnections(t *testing.T) {
	t.Parallel()
	path, db := manifestUpdateFixture(t)
	const connections = 8
	const increments = 8
	var repos []*ManifestRepo
	for i := 0; i < connections; i++ {
		connection, err := OpenExisting(path)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		repos = append(repos, NewManifestRepo(connection))
	}
	start := make(chan struct{})
	failures := make(chan error, connections)
	var wg sync.WaitGroup
	for _, repo := range repos {
		wg.Add(1)
		go func(repo *ManifestRepo) {
			defer wg.Done()
			<-start
			for i := 0; i < increments; i++ {
				if err := repo.UpdateValue(context.Background(), "counter", manifestCounter); err != nil {
					failures <- err
					return
				}
			}
		}(repo)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	value, err := NewManifestRepo(db).Get(context.Background(), "counter")
	if err != nil || value != strconv.Itoa(connections*increments) {
		t.Fatalf("lost successful updates: value=%s err=%v", value, err)
	}
}

func TestManifestUpdateProcess(t *testing.T) {
	if os.Getenv("CAIRN_MANIFEST_UPDATE_CHILD") != "1" {
		return
	}
	db, err := OpenExisting(os.Getenv("CAIRN_MANIFEST_UPDATE_PATH"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for i := 0; i < 8; i++ {
		if err := NewManifestRepo(db).UpdateValue(context.Background(), "counter", manifestCounter); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if err := db.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestManifestUpdateAcrossProcesses(t *testing.T) {
	t.Parallel()
	path, db := manifestUpdateFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const processes = 4
	failures := make(chan error, processes)
	var wg sync.WaitGroup
	for i := 0; i < processes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManifestUpdateProcess$")
			cmd.Env = append(os.Environ(), "CAIRN_MANIFEST_UPDATE_CHILD=1", "CAIRN_MANIFEST_UPDATE_PATH="+path)
			output, err := cmd.CombinedOutput()
			if err != nil {
				failures <- fmt.Errorf("child: %w: %s", err, output)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	value, err := NewManifestRepo(db).Get(context.Background(), "counter")
	if err != nil || value != strconv.Itoa(processes*8) {
		t.Fatalf("lost successful process updates: value=%s err=%v", value, err)
	}
}

func TestManifestUpdateRollsBackAndReleasesConnection(t *testing.T) {
	t.Parallel()
	path, db := manifestUpdateFixture(t)
	manifest := NewManifestRepo(db)
	if err := manifest.Set(context.Background(), "counter", "4"); err != nil {
		t.Fatal(err)
	}
	if err := manifest.UpdateValue(context.Background(), "counter", func(string) (string, error) { return "5", errors.New("injected transform failure") }); err == nil {
		t.Fatal("transform failure swallowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := manifest.UpdateValue(ctx, "counter", func(string) (string, error) { cancel(); return "5", nil }); err == nil {
		t.Fatal("canceled update succeeded")
	}
	value, err := manifest.Get(context.Background(), "counter")
	if err != nil || value != "4" {
		t.Fatalf("failed transform changed value: %s %v", value, err)
	}
	if err := manifest.UpdateValue(context.Background(), "counter", manifestCounter); err != nil {
		t.Fatalf("transaction/connection leaked: %v", err)
	}
	readonly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if err := NewManifestRepo(readonly).UpdateValue(context.Background(), "counter", manifestCounter); err == nil {
		t.Fatal("read-only connection wrote observation")
	}
	value, err = manifest.Get(context.Background(), "counter")
	if err != nil || value != "5" {
		t.Fatalf("readonly failure changed value: %s %v", value, err)
	}
}
