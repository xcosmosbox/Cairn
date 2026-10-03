package publisher

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/fakeforge"
	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/core/storage"
)

type countedReleaseForge struct {
	*fakeforge.FakeForge
	calls int
}

func (f *countedReleaseForge) EnsureRelease(ctx context.Context, spec githubapp.ReleaseSpec) (githubapp.Release, error) {
	f.calls++
	return f.FakeForge.EnsureRelease(ctx, spec)
}

type faultJournal struct {
	*store.Store
	once bool
}

func (j *faultJournal) MarkEffectApplied(ctx context.Context, key, id, response string) error {
	if j.once {
		j.once = false
		return errors.New("crash after remote success")
	}
	return j.Store.MarkEffectApplied(ctx, key, id, response)
}

func replaySpec(t *testing.T) (CandidateSpec, *countedReleaseForge, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "kg.db")
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewKBVersionRepo(db).Bump(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return CandidateSpec{Repo: dkconfig.RepoConfig{Owner: "owner", Name: "source", Branch: "main", KGGroup: "kg"}, Catalog: dkconfig.CatalogConfig{Branch: "main", ReleaseTagTemplate: "kb-{group}-{source_sha}"}, SourceSHA: strings.Repeat("a", 40), Fingerprint: Fingerprint{BuilderVersion: "test", DBSchemaVersion: storage.LatestSchemaVersion()}, ConfigDigest: "sha256:" + strings.Repeat("1", 64), KGDBPath: dbPath, BundleDir: filepath.Join(root, "candidate"), BuildReport: []byte(`{"ok":true}`)}, &countedReleaseForge{FakeForge: fakeforge.New()}, root
}

// 原先每次重试重新取 CreatedAt，跨秒生成新 tag/Release；现在冻结同一输入产物。
func TestCandidateReplayFreezesTimestampAndRejectsChangedInputs(t *testing.T) {
	t.Parallel()
	spec, forge, _ := replaySpec(t)
	p := New(forge, nil, githubapp.RepoRef{Owner: "owner", Name: "catalog"})
	first, err := p.EnsureCandidate(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.CreatedAt = time.Now().Add(time.Hour)
	second, err := p.EnsureCandidate(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.BundleDigest != second.Manifest.BundleDigest || first.Manifest.CreatedAt != second.Manifest.CreatedAt || first.Release.ID != second.Release.ID {
		t.Fatal("candidate identity changed on replay")
	}
	spec.SourceSHA = strings.Repeat("b", 40)
	if _, err := p.EnsureCandidate(context.Background(), spec); err == nil {
		t.Fatal("reused artifact for different source")
	}
}

func TestReleaseRemoteSuccessBeforeJournalPersistsReplaysSameIdentity(t *testing.T) {
	t.Parallel()
	spec, forge, root := replaySpec(t)
	path := filepath.Join(root, "state.db")
	journal, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := New(forge, nil, githubapp.RepoRef{Owner: "owner", Name: "catalog"})
	p.SetEffectJournal(&faultJournal{Store: journal, once: true})
	if _, err := p.EnsureCandidate(context.Background(), spec); err == nil {
		t.Fatal("expected persistence failure")
	}
	journal.Close()
	journal, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	p = New(forge, nil, githubapp.RepoRef{Owner: "owner", Name: "catalog"})
	p.SetEffectJournal(journal)
	published, err := p.EnsureCandidate(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if published.Release.ID != 1 || forge.calls != 2 {
		t.Fatalf("remote duplication: release=%d calls=%d", published.Release.ID, forge.calls)
	}
	if _, err := p.EnsureCandidate(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if forge.calls != 2 {
		t.Fatalf("applied effect called remote again: %d", forge.calls)
	}
}

func TestReplayRejectsDifferentArtifactEvenWithValidChecksums(t *testing.T) {
	t.Parallel()
	spec, forge, root := replaySpec(t)
	p := New(forge, nil, githubapp.RepoRef{Owner: "owner", Name: "catalog"})
	original, err := p.EnsureCandidate(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "different.db")
	db, err := storage.NewDB(storage.DBOptions{Path: other})
	if err != nil {
		t.Fatal(err)
	}
	db.Conn().Exec("INSERT INTO kg_manifest(key,value) VALUES('different','content')")
	db.Close()
	if _, err := kbbundle.Pack(kbbundle.PackRequest{KGDBPath: other, Manifest: original.Manifest, OutDir: spec.BundleDir, BuildReport: spec.BuildReport}); err != nil {
		t.Fatal(err)
	}
	if _, err := kbbundle.Verify(spec.BundleDir); err != nil {
		t.Fatal("fixture must have valid checksums:", err)
	}
	if _, err := p.EnsureCandidate(context.Background(), spec); err == nil {
		t.Fatal("trusted a different valid artifact despite frozen inputs")
	}
	if forge.calls != 1 {
		t.Fatal("invalid replay reached remote")
	}
}

type takeoverJournal struct{ *store.Store }

func (j takeoverJournal) ClaimEffect(ctx context.Context, effect model.ExternalEffect) (string, bool, error) {
	key, applied, err := j.Store.ClaimEffect(ctx, effect)
	if err != nil {
		return key, applied, err
	}
	if err := j.ReleaseLease(context.Background(), "repo", "owner"); err != nil {
		return "", false, err
	}
	if _, err := j.AcquireLease(context.Background(), "repo", "successor", time.Minute); err != nil {
		return "", false, err
	}
	return key, applied, nil
}
func TestEffectChecksLeaseImmediatelyBeforeRemoteMutation(t *testing.T) {
	t.Parallel()
	journal, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if won, err := journal.AcquireLease(context.Background(), "repo", "owner", time.Minute); err != nil || !won {
		t.Fatal(err)
	}
	ctx := journal.WithRepoLease(context.Background(), "repo", "owner")
	called := false
	_, err = Effect(ctx, takeoverJournal{journal}, "push", "remote", "immutable-head", func() (string, error) { called = true; return "ok", nil })
	if err == nil || called {
		t.Fatal("remote call issued after lease takeover before heartbeat")
	}
	_, err = Effect(ctx, nil, "push", "remote", "immutable-head", func() (string, error) { called = true; return "ok", nil })
	if err == nil || called {
		t.Fatal("standalone effect bypassed bound lease")
	}
}
