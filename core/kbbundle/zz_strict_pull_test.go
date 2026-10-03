package kbbundle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullRequiresCompleteMatchingCatalogAndExactAsset(t *testing.T) {
	for _, scenario := range []string{"missing-digest", "invalid-digest", "incomplete-provenance", "changed-source", "changed-schema", "wrong-kg", "missing-asset", "raw-db", "exact-asset"} {
		t.Run(scenario, func(t *testing.T) {
			cm, asset := pullCancellationBundle(t)
			root := t.TempDir()
			prior := filepath.Join(root, "prior")
			tar := filepath.Join(root, "prior.tar.gz")
			if err := os.WriteFile(tar, asset, 0600); err != nil {
				t.Fatal(err)
			}
			if err := ExtractTarball(tar, prior); err != nil {
				t.Fatal(err)
			}
			installDir := filepath.Join(root, "install")
			old, err := Install(prior, installDir)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing-digest":
				cm.BundleDigest = ""
			case "invalid-digest":
				cm.BundleDigest = "sha256:xyz"
			case "incomplete-provenance":
				cm.PromptSetVersion = ""
			case "changed-source":
				cm.SourceRef = "human-changed"
			case "changed-schema":
				cm.SchemaVersion = 4
			case "wrong-kg":
				cm.KG = "other"
			case "missing-asset":
				cm.AssetName = "missing.tar.gz"
			case "raw-db":
				asset = []byte("SQLite format 3\x00bare DB is insufficient")
			case "exact-asset":
				cm.AssetName = "bundle-custom.tar.gz"
			}
			var base string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/contents/"):
					_ = json.NewEncoder(w).Encode(cm)
				case strings.Contains(r.URL.Path, "/releases/tags/"):
					wanted := "bundle.tar.gz"
					if scenario == "exact-asset" {
						wanted = cm.AssetName
					}
					_ = json.NewEncoder(w).Encode(ghRelease{TagName: cm.ReleaseTag, Assets: []ghAsset{{Name: "knowledge.db", BrowserDownloadURL: base + "/wrong"}, {Name: wanted, BrowserDownloadURL: base + "/asset"}}})
				case r.URL.Path == "/asset":
					_, _ = w.Write(asset)
				default:
					t.Errorf("unexpected substitute release/asset request: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			base = server.URL
			defer server.Close()
			res, err := PullRemoteContext(context.Background(), PullRemoteSpec{CatalogOwner: "owner", CatalogRepo: "catalog", CatalogBranch: "main", ManifestDir: "knowledge-bases", KG: "smoke", Token: "synthetic", APIBaseURL: base, InstallDir: installDir})
			if res != nil {
				defer os.RemoveAll(res.TempDir)
			}
			if scenario == "exact-asset" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("unproved catalog/asset accepted: %s", scenario)
			}
			current, err := filepath.EvalSymlinks(filepath.Join(installDir, "current"))
			if err != nil || current != old.Current {
				t.Fatalf("previous healthy generation changed: %s %v", current, err)
			}
		})
	}
}
