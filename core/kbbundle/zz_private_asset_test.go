package kbbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// 浏览器 URL 在真实私有 Release 返回404；认证下载必须由 API assetID 决定目的地。
// A malicious metadata URL must never receive the authenticated request.
func TestPrivateAssetIDRouteAndPublicBrowserCompatibility(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"synthetic", ""} {
		t.Run(fmt.Sprint(token == ""), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if token == "" && r.Header.Get("Authorization") != "" {
					t.Error("public metadata request sent Authorization")
				}
				json.NewEncoder(w).Encode(ghRelease{Assets: []ghAsset{{ID: 27, Name: "bundle.tar.gz", BrowserDownloadURL: "https://untrusted.invalid/steal"}}})
			}))
			defer server.Close()
			url, name, err := getReleaseAssetURLContext(context.Background(), server.URL, "owner", "repo", "tag", token, "bundle.tar.gz")
			want := server.URL + "/repos/owner/repo/releases/assets/27"
			if token == "" {
				want = "https://untrusted.invalid/steal"
			}
			if err != nil || url != want || name != "bundle.tar.gz" {
				t.Fatalf("asset selection: %s %s %v", url, name, err)
			}
		})
	}
	for _, id := range []int64{0, -1} {
		if _, _, err := releaseAssetDownloadURL("https://api.github.com", "owner", "repo", ghAsset{ID: id, Name: "bundle.tar.gz", BrowserDownloadURL: "https://untrusted.invalid"}, "synthetic"); err == nil {
			t.Fatal("authenticated missing ID accepted")
		}
	}
}

type assetTransport func(*http.Request) (*http.Response, error)

func (f assetTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Go 默认会向子域转发 Authorization；模拟真实重定向链并检查每次实际发送的头。
// Exercise same-host, cross-host/subdomain/downgrade and return hops without global-client mutation.
func TestAssetRedirectCredentialsAndBound(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"https://cdn.example/asset", "https://child.api.example/asset", "http://api.example/plain"} {
		t.Run(target, func(t *testing.T) {
			client := assetDownloadClient()
			calls := 0
			client.Transport = assetTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				want := ""
				if calls <= 2 {
					want = "token synthetic"
				}
				if r.Header.Get("Authorization") != want {
					t.Errorf("hop%d host%s Authorization=%q", calls, r.URL.Host, r.Header.Get("Authorization"))
				}
				locations := []string{"https://api.example/same", target, "https://api.example/back"}
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("asset")), Request: r}
				if calls <= len(locations) {
					response.StatusCode = 302
					response.Header.Set("Location", locations[calls-1])
				}
				return response, nil
			})
			req, _ := http.NewRequest("GET", "https://api.example/start", nil)
			req.Header.Set("Authorization", "token synthetic")
			response, err := client.Do(req)
			if strings.HasPrefix(target, "http:") {
				if err == nil || !strings.Contains(err.Error(), "refusing HTTPS to HTTP") || calls != 2 {
					t.Fatalf("authenticated downgrade must fail before HTTP transport: calls=%d err=%v", calls, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if calls != 4 {
				t.Fatalf("redirect chain calls=%d", calls)
			}
		})
	}
	client := assetDownloadClient()
	calls := 0
	client.Transport = assetTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://api.example/loop"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if response, err := client.Get("https://api.example/loop"); err == nil {
		response.Body.Close()
		t.Fatal("unbounded asset redirects accepted")
	}
	if calls != 11 {
		t.Fatalf("expected initial+10 redirects, got%d", calls)
	}
}

func TestPrivateAssetOctetStreamAndCancelledContext(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token synthetic" || r.Header.Get("Accept") != "application/octet-stream" {
			t.Error("missing authenticated binary request headers")
		}
		io.WriteString(w, "binary")
	}))
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "asset")
	if err := downloadFileContext(context.Background(), server.URL, dest, "synthetic"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	missing := filepath.Join(t.TempDir(), "cancelled")
	if err := downloadFileContext(ctx, server.URL, missing, "synthetic"); !errors.Is(err, context.Canceled) {
		t.Fatalf("context lost: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("cancelled download created file")
	}
}

func TestPrivateAssetRouteStillRejectsCorruptBundle(t *testing.T) {
	t.Parallel()
	cm, good := pullCancellationBundle(t)
	root := t.TempDir()
	archive := filepath.Join(root, "good.tar.gz")
	if err := os.WriteFile(archive, good, 0600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "broken")
	if err := ExtractTarball(archive, broken); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(broken, "knowledge.db"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("checksum damage"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	badArchive := filepath.Join(root, "bad.tar.gz")
	if err := PackTarball(broken, badArchive); err != nil {
		t.Fatal(err)
	}
	bad, err := os.ReadFile(badArchive)
	if err != nil {
		t.Fatal(err)
	}
	var corrupt atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/contents/"):
			json.NewEncoder(w).Encode(cm)
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			json.NewEncoder(w).Encode(ghRelease{Assets: []ghAsset{{ID: 27, Name: cm.AssetName, BrowserDownloadURL: "https://untrusted.invalid/asset"}}})
		case r.URL.Path == "/repos/owner/repo/releases/assets/27":
			if r.Header.Get("Accept") != "application/octet-stream" || r.Header.Get("Authorization") != "token synthetic" {
				t.Error("private API request contract lost")
			}
			if corrupt.Load() {
				w.Write(bad)
			} else {
				w.Write(good)
			}
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	spec := PullRemoteSpec{CatalogOwner: "owner", CatalogRepo: "repo", CatalogBranch: "main", ManifestDir: "knowledge-bases", KG: "smoke", Token: "synthetic", APIBaseURL: server.URL, InstallDir: filepath.Join(root, "installed")}
	res, err := PullRemoteContext(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(res.TempDir)
	before, err := DigestFile(filepath.Join(res.Install.Current, "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	corrupt.Store(true)
	if _, err := PullRemoteContext(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "摘要不匹配") {
		t.Fatalf("corrupt bundle not rejected by checksums: %v", err)
	}
	current, err := filepath.EvalSymlinks(filepath.Join(spec.InstallDir, "current"))
	if err != nil || current != res.Install.Current {
		t.Fatal("bad download changed current")
	}
	after, _ := DigestFile(filepath.Join(current, "knowledge.db"))
	if before != after {
		t.Fatal("bad download changed healthy bytes")
	}
}
