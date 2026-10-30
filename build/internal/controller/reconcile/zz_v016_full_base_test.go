package reconcile

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// 原故障：fingerprint触发Full时删除candidate，却不恢复仍含人工知识的stable。
// Exercise the caller before the real Full boundary, including a stale retry candidate.
func TestV016ControllerFullRestoresHumanStableBaseline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r, st, _, sha := publicationSetup(t, model.StateFullBuild)
	stable := filepath.Join(r.cfg.Service.BundleDir, "kg", "stable", "knowledge.db")
	if err := os.MkdirAll(filepath.Dir(stable), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: stable, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeRepo(db).Insert(ctx, &dktypes.Node{ID: "human-pump", Label: dktypes.LabelEntity, Name: "Pump", Summary: "Human", Description: "Operator qualification", Domain: "d", Subdomain: "s", Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(stable)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRepoStable(ctx, "repo", sha, "sha256:previous", "previous-fingerprint"); err != nil {
		t.Fatal(err)
	}
	candidate, err := storage.NewDB(storage.DBOptions{Path: r.candidateDBPath("run"), JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	candidate.Conn().Exec(`INSERT INTO kg_manifest(key,value) VALUES('failed-retry','discard')`)
	candidate.Close()
	called := false
	r.runner = &runner.FakeRunner{OnFull: func(ctx context.Context, req runner.FullRequest) (runner.BuildResult, error) {
		called = true
		if _, err := os.Stat(req.DBPath); err != nil {
			t.Fatalf("Full received no stable baseline: %v", err)
		}
		base, err := storage.OpenReadOnly(req.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		defer base.Close()
		node, err := storage.NewNodeRepo(base).GetByID(ctx, "human-pump")
		if err != nil || node == nil || node.Provenance != dktypes.ProvenanceHumanCurated || node.Description != "Operator qualification" {
			t.Fatalf("Full baseline lost curated stable payload: %+v %v", node, err)
		}
		var stale int
		if err := base.Conn().QueryRow(`SELECT count(*) FROM kg_manifest WHERE key='failed-retry'`).Scan(&stale); err != nil || stale != 0 {
			t.Fatalf("stale retry candidate reused: %d %v", stale, err)
		}
		return runner.BuildResult{DBPath: req.DBPath, Status: "complete"}, nil
	}}
	if _, err := r.Step(ctx, "run"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Full runner was not called")
	}
	after, err := os.ReadFile(stable)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("Full baseline restore modified immutable stable DB")
	}
}

func TestV016ControllerFullRefusesLiveCandidateWAL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r, _, _, _ := publicationSetup(t, model.StateFullBuild)
	path := r.candidateDBPath("run")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Conn().Exec(`PRAGMA wal_autocheckpoint=0; INSERT INTO kg_manifest(key,value) VALUES('live','retain')`); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	r.runner = &runner.FakeRunner{OnFull: func(context.Context, runner.FullRequest) (runner.BuildResult, error) {
		called = true
		return runner.BuildResult{}, nil
	}}
	result, err := r.Step(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if called || result.To != model.StateFailedRetryable {
		t.Fatalf("Full ignored live candidate ownership: called=%v result=%+v", called, result)
	}
	afterMain, _ := os.ReadFile(path)
	afterWAL, _ := os.ReadFile(path + "-wal")
	if !bytes.Equal(main, afterMain) || !bytes.Equal(wal, afterWAL) {
		t.Fatal("Full reset modified active candidate main/WAL")
	}
}

// A missing declared stable DB is unknown human state, not a first build.
func TestV016ControllerFullDoesNotLoseMissingDeclaredStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r, st, _, sha := publicationSetup(t, model.StateFullBuild)
	const digest = "sha256:declared-stable"
	if err := st.UpdateRepoStable(ctx, "repo", sha, digest, "previous-fingerprint"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.candidateDBPath("run"))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	r.runner = &runner.FakeRunner{OnFull: func(context.Context, runner.FullRequest) (runner.BuildResult, error) {
		called = true
		return runner.BuildResult{}, nil
	}}
	result, err := r.Step(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if called || result.To != model.StateFailedRetryable {
		t.Fatalf("missing stable baseline was treated as fresh: called=%v result=%+v", called, result)
	}
	after, err := os.ReadFile(r.candidateDBPath("run"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("missing stable preflight discarded previous candidate bytes")
	}
	saved, err := st.GetRun(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if saved.ErrorCode != "full_base" || !strings.Contains(saved.ErrorMessage, "declared stable baseline unavailable") {
		t.Fatalf("missing baseline cause lost: %+v", saved)
	}
	repo, err := st.GetRepo(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo.LastStableBundleDigest != digest || repo.LastStableSourceSHA != sha || repo.LastStableFingerprint != "previous-fingerprint" {
		t.Fatalf("missing baseline changed declared stable state: %+v", repo)
	}
}
