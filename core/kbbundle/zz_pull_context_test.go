package kbbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/storage"
)

func TestCatalogFetchDeadlineCancelsResponseBody(t *testing.T) {
	t.Parallel()
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	// Allow scheduler headroom under race-enabled CI before the body blocks.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := FetchCatalogManifestContext(ctx, server.URL, "owner", "catalog", "main", "stable.json", "synthetic-token")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not propagated: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("request body did not receive deadline")
	}
}

func pullCancellationBundle(t *testing.T) (CatalogManifest, []byte) {
	t.Helper()
	dir := t.TempDir()
	dbpath := filepath.Join(dir, "kg.db")
	db, err := storage.NewDB(storage.DBOptions{Path: dbpath})
	if err != nil {
		t.Fatal(err)
	}
	kbVersion, err := storage.NewKBVersionRepo(db).Bump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundle, err := Pack(PackRequest{Manifest: Manifest{KG: "smoke", SourceRepo: "owner/source", SourceRef: "main", SourceCommit: strings.Repeat("a", 40), BuilderVersion: "test", BuilderCommit: strings.Repeat("b", 40), SchemaVersion: 5, PromptSetVersion: "test", Model: "test", ConfigDigest: DigestPrefix(DigestBytes([]byte("config"))), KBVersion: kbVersion, CreatedAt: "2026-10-03T00:00:00Z"}, KGDBPath: dbpath, OutDir: filepath.Join(dir, "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	tarpath := filepath.Join(dir, "bundle.tar.gz")
	if err := PackTarball(bundle.Dir, tarpath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tarpath)
	if err != nil {
		t.Fatal(err)
	}
	return CatalogManifest{Manifest: bundle.Manifest, Channel: "stable", PublishedAt: bundle.Manifest.CreatedAt, ReleaseTag: "test-release", AssetName: "bundle.tar.gz"}, data
}

func TestRemotePullCancellationCannotInstall(t *testing.T) {
	t.Parallel()
	for _, atEOF := range []bool{false, true} {
		t.Run(fmt.Sprint("after-complete-body-", atEOF), func(t *testing.T) {
			cm, asset := pullCancellationBundle(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reached := make(chan struct{})
			var base string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/contents/"):
					if err := json.NewEncoder(w).Encode(cm); err != nil {
						t.Error(err)
					}
				case strings.Contains(r.URL.Path, "/releases/tags/"):
					if err := json.NewEncoder(w).Encode(ghRelease{TagName: cm.ReleaseTag, Assets: []ghAsset{{Name: cm.AssetName, BrowserDownloadURL: base + "/asset"}}}); err != nil {
						t.Error(err)
					}
				case r.URL.Path == "/asset":
					w.WriteHeader(http.StatusOK)
					if atEOF {
						_, _ = w.Write(asset)
					} else {
						_, _ = w.Write(asset[:10])
					}
					w.(http.Flusher).Flush()
					close(reached)
					if atEOF {
						cancel()
						return
					}
					<-r.Context().Done()
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			base = server.URL
			defer server.Close()
			defer server.CloseClientConnections()
			installed := filepath.Join(t.TempDir(), "installed")
			done := make(chan error, 1)
			go func() {
				res, err := PullRemoteContext(ctx, PullRemoteSpec{CatalogOwner: "owner", CatalogRepo: "catalog", CatalogBranch: "main", ManifestDir: "knowledge-bases", KG: "smoke", Token: "synthetic-token", APIBaseURL: base, InstallDir: installed})
				if res != nil {
					os.RemoveAll(res.TempDir)
				}
				done <- err
			}()
			select {
			case <-reached:
			case <-time.After(5 * time.Second):
				t.Fatal("asset request never arrived")
			}
			if !atEOF {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled pull not rejected: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pull ignored cancellation")
			}
			if _, err := os.Stat(installed); !os.IsNotExist(err) {
				t.Fatalf("canceled pull began install: %v", err)
			}
		})
	}
}

func TestRequestedCatalogAssetSelection(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ghRelease{TagName: "release", Assets: []ghAsset{{Name: "knowledge.db", BrowserDownloadURL: "wrong"}, {Name: "bundle-custom.tar.gz", BrowserDownloadURL: "correct"}}})
	}))
	defer server.Close()
	url, name, err := getReleaseAssetURLContext(context.Background(), server.URL, "owner", "catalog", "release", "synthetic-token", "bundle-custom.tar.gz")
	if err != nil || url != "correct" || name != "bundle-custom.tar.gz" {
		t.Fatalf("wrong asset selected: %s %s %v", url, name, err)
	}
	if _, _, err := getReleaseAssetURLContext(context.Background(), server.URL, "owner", "catalog", "release", "synthetic-token", "missing.tar.gz"); err == nil {
		t.Fatal("missing requested asset silently substituted")
	}
}
