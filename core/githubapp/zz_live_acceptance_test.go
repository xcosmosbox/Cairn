//go:build livegithubapp

package githubapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 显式启用的真实 App 验收只操作专用沙箱；ordinary tests never compile this file.
// Cache expiry is injected locally; every credential exchange still goes to GitHub.
func TestLiveAppAcceptance(t *testing.T) {
	if os.Getenv("CAIRN_LIVE_GITHUB_APP") != "1" {
		t.Skip("requires explicit live acceptance opt-in")
	}
	const appID, installationID, repoID = int64(5454525), int64(174795212), int64(1432125826)
	const repo = "/repos/xcosmosbox/cairn-app-sandbox"
	key, err := os.ReadFile(os.Getenv("CAIRN_APP_PRIVATE_KEY_FILE"))
	if err != nil {
		t.Fatal("runtime key read failed (details suppressed)")
	}
	a, err := NewAppAuth(appID, key, "https://api.github.com")
	if err != nil {
		t.Fatal("NewAppAuth failed (details suppressed)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	emit := func(event string, fields map[string]any) {
		fields["event"] = event
		fields["time_utc"] = time.Now().UTC().Format(time.RFC3339)
		b, _ := json.Marshal(fields)
		t.Log(string(b))
	}
	exchanges := 0
	a.http.Transport = liveRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/access_tokens") {
			exchanges++
		}
		resp, e := http.DefaultTransport.RoundTrip(r)
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		emit("native_http", map[string]any{"method": r.Method, "host": r.URL.Host, "path": r.URL.Path, "status": status})
		return resp, e
	})
	client := &http.Client{Timeout: 30 * time.Second}
	call := func(method, path, credential string, body any) (int, map[string]any) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, e := json.Marshal(body)
			if e != nil {
				t.Fatal("request encoding failed")
			}
			reader = bytes.NewReader(b)
		}
		r, e := http.NewRequestWithContext(ctx, method, "https://api.github.com"+path, reader)
		if e != nil {
			t.Fatal("request construction failed")
		}
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Accept", "application/vnd.github+json")
		r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		r.Header.Set("User-Agent", "Cairn-Issue15-Live-Acceptance")
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, e := client.Do(r)
		if e != nil {
			t.Fatal("GitHub request failed (details suppressed)")
		}
		defer resp.Body.Close()
		var out map[string]any
		data, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if e != nil {
			t.Fatal("response read failed")
		}
		if len(data) > 0 && json.Unmarshal(data, &out) != nil {
			t.Fatal("unexpected non-JSON response")
		}
		emit("http", map[string]any{"method": method, "host": "api.github.com", "path": path, "status": resp.StatusCode, "request_id": resp.Header.Get("X-GitHub-Request-Id")})
		return resp.StatusCode, out
	}
	assertStatus := func(got, want int) {
		t.Helper()
		if got != want {
			t.Fatalf("HTTP status=%d want=%d; response body suppressed", got, want)
		}
	}
	jwt, e := a.AppJWT()
	if e != nil {
		t.Fatal("native AppJWT failed")
	}
	status, app := call("GET", "/app", jwt, nil)
	assertStatus(status, 200)
	if app["id"] != float64(appID) {
		t.Fatal("App identity mismatch")
	}
	status, inst := call("GET", fmt.Sprintf("/app/installations/%d", installationID), jwt, nil)
	assertStatus(status, 200)
	account, _ := inst["account"].(map[string]any)
	permissions, _ := inst["permissions"].(map[string]any)
	if inst["id"] != float64(installationID) || inst["app_id"] != float64(appID) || account["login"] != "xcosmosbox" || permissions["contents"] != "write" || permissions["pull_requests"] != "write" || inst["suspended_at"] != nil {
		t.Fatal("installation identity/permissions mismatch")
	}
	emit("identity", map[string]any{"app_id": appID, "installation_id": installationID, "account": "xcosmosbox", "repo": "xcosmosbox/cairn-app-sandbox", "repo_id": repoID, "key_reference": "user supplied runtime file", "repository_selection": inst["repository_selection"], "contents": permissions["contents"], "pull_requests": permissions["pull_requests"], "native_token_request_scope": "installation default (no body)"})
	resolved, e := a.ResolveInstallation(ctx, "xcosmosbox", "cairn-app-sandbox")
	if e != nil || resolved != installationID {
		t.Fatal("native ResolveInstallation failed")
	}
	tok, expiry, e := a.InstallToken(ctx, installationID)
	if e != nil {
		t.Fatal("native InstallToken failed (details suppressed)")
	}
	tokens := []string{tok}
	defer func() {
		// Cleanup only the credentials minted by this invocation; never touch App/key/installation.
		cleanupClient := &http.Client{Timeout: 30 * time.Second}
		for i, token := range tokens {
			r, _ := http.NewRequest("DELETE", "https://api.github.com/installation/token", nil)
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Accept", "application/vnd.github+json")
			resp, e := cleanupClient.Do(r)
			status := 0
			if resp != nil {
				status = resp.StatusCode
				resp.Body.Close()
			}
			emit("cleanup_created_token", map[string]any{"ordinal": i + 1, "status": status, "transport_ok": e == nil})
			if e != nil || (status != 204 && status != 401) {
				t.Error("created-token cleanup not confirmed")
			}
		}
	}()
	if !expiry.After(time.Now()) {
		t.Fatal("native token expiry invalid")
	}
	before := exchanges
	cached, cachedExp, e := a.InstallToken(ctx, installationID)
	if e != nil || cached != tok || cachedExp != expiry || exchanges != before {
		t.Fatal("native cache hit failed")
	}
	emit("cache_hit", map[string]any{"extra_exchange_count": exchanges - before, "expiry": expiry.UTC().Format(time.RFC3339)})
	status, metadata := call("GET", repo, tok, nil)
	assertStatus(status, 200)
	if metadata["id"] != float64(repoID) || metadata["private"] != true {
		t.Fatal("sandbox identity/private check failed")
	}
	// Only initialize a genuinely empty dedicated sandbox; preserve existing main content.
	status, _ = call("GET", repo+"/branches/main", tok, nil)
	if status == 404 {
		status, _ = call("PUT", repo+"/contents/.cairn-acceptance/README.md", tok, map[string]any{"message": "test(app): initialize dedicated acceptance sandbox", "branch": "main", "content": base64.StdEncoding.EncodeToString([]byte("Dedicated Cairn GitHub App acceptance sandbox.\n"))})
		assertStatus(status, 201)
		emit("sandbox_initialized", map[string]any{"branch": "main", "path": ".cairn-acceptance/README.md"})
	} else {
		assertStatus(status, 200)
	}
	// A reduced temporary token proves effective denial inside the accepted all-repo installation.
	status, limited := call("POST", fmt.Sprintf("/app/installations/%d/access_tokens", installationID), jwt, map[string]any{"repository_ids": []int64{repoID}, "permissions": map[string]string{"contents": "read", "metadata": "read"}})
	assertStatus(status, 201)
	readToken, _ := limited["token"].(string)
	if readToken == "" {
		t.Fatal("limited token missing")
	}
	tokens = append(tokens, readToken)
	limitedPerms, _ := limited["permissions"].(map[string]any)
	if limitedPerms["contents"] != "read" {
		t.Fatal("limited scope mismatch")
	}
	status, _ = call("GET", repo+"/contents/.cairn-acceptance/README.md?ref=main", readToken, nil)
	if status != 200 && status != 404 {
		t.Fatalf("read-only token read unexpected status %d", status)
	}
	denyPath := repo + "/contents/.cairn-acceptance/denied-" + time.Now().UTC().Format("20060102T150405.000000000") + ".txt"
	status, unexpected := call("PUT", denyPath, readToken, map[string]any{"message": "test(app): denied read-only permission probe", "branch": "main", "content": base64.StdEncoding.EncodeToString([]byte("This write must be denied.\n"))})
	if status == 201 {
		content, _ := unexpected["content"].(map[string]any)
		cleanupStatus, _ := call("DELETE", denyPath, tok, map[string]any{"message": "test(app): clean unexpected permission probe", "branch": "main", "sha": content["sha"]})
		emit("unexpected_write_cleanup", map[string]any{"status": cleanupStatus})
	}
	assertStatus(status, 403)
	emit("permission_negative", map[string]any{"requested_repository_ids": []int64{repoID}, "requested_contents": "read", "write_status": status, "boundary": "reduced temporary token within sandbox; not installation repository exclusion"})
	// Revocation is real. Native cached-token behavior is observed before accelerated recovery.
	status, _ = call("DELETE", "/installation/token", tok, nil)
	assertStatus(status, 204)
	before = exchanges
	revokedCached, _, e := a.InstallToken(ctx, installationID)
	if e != nil || revokedCached != tok || exchanges != before {
		t.Fatal("revoked-cache observation changed unexpectedly")
	}
	status, _ = call("GET", repo, revokedCached, nil)
	assertStatus(status, 401)
	emit("revoked_cache", map[string]any{"cached_token_reused": true, "extra_exchange_count": exchanges - before, "repository_status": status, "transparent_401_recovery": false})
	// Change only cached expiry, not clock/JWT/HTTP response, to enter the real refresh branch.
	a.mu.Lock()
	entry := a.instTokens[installationID]
	entry.expiry = time.Now().Add(time.Minute)
	a.instTokens[installationID] = entry
	a.mu.Unlock()
	before = exchanges
	renewed, renewedExp, e := a.InstallToken(ctx, installationID)
	if e != nil {
		t.Fatal("native live refresh after cache-boundary injection failed (details suppressed)")
	}
	tokens = append(tokens, renewed)
	if exchanges-before != 1 || renewed == tok || !renewedExp.After(time.Now()) {
		t.Fatal("native refresh exchange/result invalid")
	}
	status, _ = call("GET", repo, renewed, nil)
	assertStatus(status, 200)
	emit("refresh_recovery", map[string]any{"trigger": "injected cached expiry at +60 seconds", "real_github_exchange_count": exchanges - before, "repository_status": status, "expiry": renewedExp.UTC().Format(time.RFC3339), "natural_one_hour_expiration_tested": false, "production_source_modified": false})
}

type liveRoundTripper func(*http.Request) (*http.Response, error)

func (f liveRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
