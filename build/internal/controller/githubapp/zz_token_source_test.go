package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
)

type contractTokenSource struct {
	base string
	get  func(context.Context, int64) (string, time.Time, error)
}

func (s contractTokenSource) HTTPBaseURL() string { return s.base }
func (s contractTokenSource) InstallToken(ctx context.Context, id int64) (string, time.Time, error) {
	return s.get(ctx, id)
}

// 抽象凭证来源后不能缓存旧 token；下一次 HTTP 操作仍必须重新询问来源。
// A replaceable token source must be consulted for every HTTP operation.
func TestTokenSourceRefreshesPerHTTPRequest(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		want := "token first"
		if n == 2 {
			want = "token second"
		}
		if r.Header.Get("Authorization") != want {
			t.Errorf("request %d did not use current token", n)
		}
		json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": strings.Repeat("a", 40)}})
	}))
	defer server.Close()
	var reads atomic.Int32
	forge := NewHTTPForge(contractTokenSource{base: server.URL, get: func(ctx context.Context, id int64) (string, time.Time, error) {
		if id != 42 {
			t.Errorf("installation id=%d", id)
		}
		if reads.Add(1) == 1 {
			return "first", time.Now().Add(time.Hour), nil
		}
		return "second", time.Now().Add(time.Hour), nil
	}}, 42)
	for i := 0; i < 2; i++ {
		if _, err := forge.GetBranchSHA(context.Background(), "owner", "repo", "main"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 || reads.Load() != 2 {
		t.Fatalf("requests=%d token reads=%d", calls.Load(), reads.Load())
	}
}

// 凭证获取前后的取消与失权都必须挡住真实远端请求。
// Cancellation or lease loss during credential retrieval must prevent the network effect.
func TestTokenSourceCancellationAndLeaseTakeoverPreventHTTPRequest(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel-before", "cancel-during", "lease-during", "token-error"} {
		t.Run(mode, func(t *testing.T) {
			var calls, reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": strings.Repeat("a", 40)}})
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var st *store.Store
			if mode == "lease-during" {
				var err error
				st, err = store.Open(filepath.Join(t.TempDir(), "controller.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				if err := st.UpsertRepo(ctx, model.ManagedRepo{ID: "repo", GitHubOwner: "owner", GitHubName: "repo", Branch: "main", KGGroup: "kg", Enabled: true}); err != nil {
					t.Fatal(err)
				}
				if won, err := st.AcquireLease(ctx, "repo", "owner", time.Minute); err != nil || !won {
					t.Fatalf("acquire=%t err=%v", won, err)
				}
				ctx = st.WithRepoLease(ctx, "repo", "owner")
			}
			if mode == "cancel-before" {
				cancel()
			}
			forge := NewHTTPForge(contractTokenSource{base: server.URL, get: func(context.Context, int64) (string, time.Time, error) {
				reads.Add(1)
				switch mode {
				case "cancel-during":
					cancel()
				case "lease-during":
					if err := st.ReleaseLease(context.Background(), "repo", "owner"); err != nil {
						t.Fatal(err)
					}
					if won, err := st.AcquireLease(context.Background(), "repo", "successor", time.Minute); err != nil || !won {
						t.Fatalf("takeover=%t err=%v", won, err)
					}
				case "token-error":
					return "", time.Time{}, errors.New("synthetic token source failure")
				}
				return "synthetic-token", time.Time{}, nil
			}}, 42)
			if _, err := forge.GetBranchSHA(ctx, "owner", "repo", "main"); err == nil {
				t.Fatal("invalid action succeeded")
			}
			if calls.Load() != 0 || (mode == "cancel-before" && reads.Load() != 0) {
				t.Fatalf("requests=%d token reads=%d", calls.Load(), reads.Load())
			}
		})
	}
}
