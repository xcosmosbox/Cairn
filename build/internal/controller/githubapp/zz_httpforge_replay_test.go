package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	coregh "github.com/xcosmosbox/cairn/core/githubapp"
)

func replayHTTPForge(t *testing.T, handler http.HandlerFunc) *HTTPForge {
	return replayHTTPForgeWithTokenHook(t, handler, nil)
}

func replayHTTPForgeWithTokenHook(t *testing.T, handler http.HandlerFunc, tokenHook func()) *HTTPForge {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/1/access_tokens" {
			if tokenHook != nil {
				tokenHook()
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"token": "fixture-token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	auth, err := coregh.NewAppAuth(1, data, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return NewHTTPForge(auth, 1)
}

func replaySpec() PullRequestSpec {
	return PullRequestSpec{Owner: "owner", Repo: "repo", HeadBranch: "bot/branch", BaseBranch: "main", RunMarker: "cairn-run-id:run-1:source:digest", Title: "proposal", Body: "human-readable description"}
}
func replayPR(spec PullRequestSpec, number int, body string) map[string]any {
	return map[string]any{
		"number": number, "state": "open", "body": body, "merged": false, "merged_at": nil, "head": map[string]any{"ref": spec.HeadBranch, "sha": strings.Repeat("a", 40), "label": spec.Owner + ":" + spec.HeadBranch, "user": map[string]string{"login": spec.Owner}, "repo": map[string]string{"full_name": spec.Owner + "/" + spec.Repo}}, "base": map[string]any{"ref": spec.BaseBranch, "repo": map[string]string{"full_name": spec.Owner + "/" + spec.Repo}}, "html_url": "https://fixture.invalid/pull/" + fmt.Sprint(number),
	}
}

func TestHTTPPullRequestReplayAfterLostReceiptAndRemoteClose(t *testing.T) {
	t.Parallel()
	for _, merged := range []bool{false, true} {
		t.Run(fmt.Sprintf("merged=%t", merged), func(t *testing.T) {
			spec := replaySpec()
			var mu sync.Mutex
			var saved map[string]any
			var creates atomic.Int32
			forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if r.URL.Query().Get("state") != "all" {
						t.Error("replay query excluded closed/merged proposals")
					}
					mu.Lock()
					defer mu.Unlock()
					if saved == nil {
						json.NewEncoder(w).Encode([]any{})
					} else {
						json.NewEncoder(w).Encode([]any{saved})
					}
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method %s", r.Method)
					http.Error(w, "unexpected", 400)
					return
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				creates.Add(1)
				mu.Lock()
				saved = replayPR(spec, 7, body["body"])
				mu.Unlock()
				// 远端已创建 PR，但网络在回执前中断；随后人已关闭/合并它。
				// The receipt may be lost even though the remote action committed.
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
			})
			if _, err := forge.EnsurePullRequest(context.Background(), spec); err == nil {
				t.Fatal("lost response did not error")
			}
			mu.Lock()
			saved["state"] = "closed"
			if merged {
				saved["merged"] = true
				saved["merged_at"] = time.Now().UTC().Format(time.RFC3339)
			}
			mu.Unlock()
			pr, err := forge.EnsurePullRequest(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if pr.Number != 7 || pr.State != "closed" || pr.Merged != merged || pr.HeadBranch != spec.HeadBranch || pr.HeadSHA == "" || pr.BaseBranch != spec.BaseBranch || creates.Load() != 1 {
				t.Fatalf("remote proposal duplicated/lost fields: %+v creates=%d", pr, creates.Load())
			}
		})
	}
}

func TestHTTPPullRequestSearchPagesAndExactIdentity(t *testing.T) {
	t.Parallel()
	spec := replaySpec()
	var pages atomic.Int32
	var creates atomic.Int32
	forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			creates.Add(1)
			http.Error(w, "unexpected create", 500)
			return
		}
		pages.Add(1)
		q := r.URL.Query()
		if q.Get("head") != spec.Owner+":"+spec.HeadBranch || q.Get("base") != spec.BaseBranch || q.Get("state") != "all" {
			t.Errorf("incomplete filters: %s", r.URL.RawQuery)
		}
		if q.Get("page") == "1" {
			wrongMarker := replayPR(spec, 1, spec.RunMarker+"-suffix")
			wrongBranch := replayPR(spec, 2, spec.RunMarker)
			wrongBranch["head"].(map[string]any)["ref"] = "wrong"
			wrongBase := replayPR(spec, 3, spec.RunMarker)
			wrongBase["base"].(map[string]any)["ref"] = "other"
			wrongOwner := replayPR(spec, 4, spec.RunMarker)
			wrongOwner["head"].(map[string]any)["user"] = map[string]string{"login": "other"}
			w.Header().Set("Link", `<https://api.github.com/repos/owner/repo/pulls?page=2>; rel="next"`)
			json.NewEncoder(w).Encode([]any{wrongMarker, wrongBranch, wrongBase, wrongOwner})
			return
		}
		target := replayPR(spec, 42, "<!-- cairn-replay-marker:"+spec.RunMarker+" -->")
		target["state"] = "closed"
		target["merged_at"] = time.Now().UTC().Format(time.RFC3339)
		json.NewEncoder(w).Encode([]any{target})
	})
	pr, err := forge.EnsurePullRequest(context.Background(), spec)
	if err != nil || pr.Number != 42 || !pr.Merged || pages.Load() != 2 || creates.Load() != 0 {
		t.Fatalf("pagination identity failure: pr=%+v pages=%d creates=%d err=%v", pr, pages.Load(), creates.Load(), err)
	}
}

func TestHTTPNewRunGetsDistinctProposalAndNestedResponse(t *testing.T) {
	t.Parallel()
	old := replaySpec()
	spec := old
	spec.RunMarker = "cairn-run-id:run-10:source:digest"
	var creates atomic.Int32
	forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]any{replayPR(old, 1, old.RunMarker)})
			return
		}
		creates.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !exactReplayMarker(body["body"], spec.RunMarker) {
			t.Error("machine identity was not persisted")
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(replayPR(spec, 2, body["body"]))
	})
	pr, err := forge.EnsurePullRequest(context.Background(), spec)
	if err != nil || pr.Number != 2 || pr.HeadBranch != spec.HeadBranch || pr.BaseBranch != spec.BaseBranch || pr.HeadSHA != strings.Repeat("a", 40) || creates.Load() != 1 {
		t.Fatalf("created response lost nested fields: pr=%+v creates=%d err=%v", pr, creates.Load(), err)
	}
}

func TestHTTPMalformedPullRequestSuccessIsNotAReceipt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body string
	}{{"invalid-json", "{"}, {"zero-id", `{"number":0,"state":"open"}`}, {"missing-head", `{"number":1,"state":"open","body":"cairn-run-id:run-1:source:digest","head":{"ref":"bot/branch"},"base":{"ref":"main"}}`}} {
		t.Run(test.name, func(t *testing.T) {
			forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls") {
					json.NewEncoder(w).Encode([]any{})
					return
				}
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusCreated)
				}
				fmt.Fprint(w, test.body)
			})
			if pr, err := forge.EnsurePullRequest(context.Background(), replaySpec()); err == nil {
				t.Fatalf("malformed create acknowledged: %+v", pr)
			}
			if pr, err := forge.GetPullRequest(context.Background(), "owner", "repo", 1); err == nil {
				t.Fatalf("malformed observation acknowledged: %+v", pr)
			}
		})
	}
}

func TestHTTPPullRequestListFailureDoesNotCreate(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var creates atomic.Int32
			forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					creates.Add(1)
				}
				http.Error(w, "remote unavailable", status)
			})
			if _, err := forge.EnsurePullRequest(context.Background(), replaySpec()); err == nil || creates.Load() != 0 {
				t.Fatalf("read failure became create: creates=%d err=%v", creates.Load(), err)
			}
		})
	}
}

func TestHTTPReleaseReadFailuresAndMalformedReceipts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{{"read-outage", 503, `{}`}, {"bad-existing-json", 200, `{`}, {"existing-zero-id", 200, `{"id":0,"tag_name":"v1"}`}, {"wrong-existing-tag", 200, `{"id":3,"tag_name":"v2"}`}, {"bad-create-json", 404, `{`}, {"created-zero-id", 404, `{"id":0,"tag_name":"v1"}`}} {
		t.Run(test.name, func(t *testing.T) {
			var creates atomic.Int32
			forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(test.status)
					if test.status != 404 {
						fmt.Fprint(w, test.body)
					}
					return
				}
				creates.Add(1)
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, test.body)
			})
			if receipt, err := forge.EnsureRelease(context.Background(), ReleaseSpec{Owner: "owner", Repo: "repo", Tag: "v1"}); err == nil {
				t.Fatalf("malformed release receipt accepted: %+v", receipt)
			}
			if test.status != 404 && creates.Load() != 0 {
				t.Fatal("non-404 read triggered create")
			}
		})
	}
}

func TestHTTPReleaseCreationAndAssetReceiptValidation(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(file, []byte("bundle"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/releases") {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["target_commitish"] != "catalog-main" {
						t.Errorf("target_commitish lost: %+v", body)
					}
					w.WriteHeader(http.StatusCreated)
					fmt.Fprint(w, `{"id":3,"tag_name":"v1"}`)
					return
				}
				if r.URL.Query().Get("name") != "bundle with space.tar.gz" {
					t.Errorf("asset name not encoded: %s", r.URL.RawQuery)
				}
				w.WriteHeader(http.StatusCreated)
				if valid {
					fmt.Fprint(w, `{"id":4,"name":"bundle with space.tar.gz"}`)
				} else {
					fmt.Fprint(w, `{"id":0,"name":"wrong"}`)
				}
			})
			receipt, err := forge.EnsureRelease(context.Background(), ReleaseSpec{Owner: "owner", Repo: "repo", Tag: "v1", Target: "catalog-main", AssetName: "bundle with space.tar.gz", AssetFilePath: file})
			if valid && (err != nil || receipt.ID != 3) {
				t.Fatalf("valid receipt rejected: %+v err=%v", receipt, err)
			}
			if !valid && err == nil {
				t.Fatal("malformed asset receipt accepted")
			}
		})
	}
}

func TestHTTPActionRechecksLeaseAfterCredentialLookup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if won, err := s.AcquireLease(ctx, "repo", "owner", time.Minute); err != nil || !won {
		t.Fatalf("acquire=%t err=%v", won, err)
	}
	var requests atomic.Int32
	forge := replayHTTPForgeWithTokenHook(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "must not issue repo request", 500)
	}, func() {
		if _, err := s.DB().Exec(`DELETE FROM repo_leases WHERE repo_id='repo'`); err != nil {
			t.Error(err)
		}
	})
	// token 可以阻塞或回调改动 ownership；成功返回不表示之后仍有写权限。
	// Recheck ownership after credentials and before starting any repository HTTP action.
	if _, err := forge.EnsurePullRequest(s.WithRepoLease(ctx, "repo", "owner"), replaySpec()); err == nil || requests.Load() != 0 {
		t.Fatalf("lost owner issued HTTP action: requests=%d err=%v", requests.Load(), err)
	}
}

func TestHTTPPullRequestPaginationWithoutLinkStillChecksFullPages(t *testing.T) {
	t.Parallel()
	spec := replaySpec()
	var pages atomic.Int32
	forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("unexpected creation")
			http.Error(w, "unexpected", 500)
			return
		}
		pages.Add(1)
		if r.URL.Query().Get("page") == "1" {
			items := make([]any, 100)
			for i := range items {
				items[i] = replayPR(spec, i+1, "different-marker")
			}
			json.NewEncoder(w).Encode(items)
			return
		}
		json.NewEncoder(w).Encode([]any{replayPR(spec, 101, spec.RunMarker)})
	})
	receipt, err := forge.EnsurePullRequest(context.Background(), spec)
	if err != nil || receipt.Number != 101 || pages.Load() != 2 {
		t.Fatalf("full page truncated replay search: receipt=%+v pages=%d err=%v", receipt, pages.Load(), err)
	}
}

func TestHTTPBranchSuccessRequiresCommitFact(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"commit":{}}`, `{"commit":{"sha":""}}`, `{"commit":{"sha":"  "}}`} {
		t.Run(body, func(t *testing.T) {
			forge := replayHTTPForge(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if sha, err := forge.GetBranchSHA(context.Background(), "owner", "repo", "main"); err == nil || sha != "" {
				t.Fatalf("missing branch fact accepted: sha=%q err=%v", sha, err)
			}
		})
	}
}
