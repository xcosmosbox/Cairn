// 专用沙箱真实 Forge 验收；all credentials remain in memory and are never logged.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	forge "github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	auth "github.com/xcosmosbox/cairn/core/githubapp"
)

func emit(event string, fields map[string]any) {
	fields["event"] = event
	fields["time_utc"] = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(fields)
	fmt.Println(string(b))
}
func run() (resultErr error) {
	if os.Getenv("CAIRN_LIVE_GITHUB_APP") != "1" {
		return fmt.Errorf("requires explicit live acceptance opt-in")
	}
	const owner, name = "xcosmosbox", "cairn-app-sandbox"
	const installationID = int64(174795212)
	key, e := os.ReadFile(os.Getenv("CAIRN_APP_PRIVATE_KEY_FILE"))
	if e != nil {
		return fmt.Errorf("runtime key read failed (details suppressed)")
	}
	a, e := auth.NewAppAuth(5454525, key, "")
	if e != nil {
		return fmt.Errorf("native auth construction failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	f := forge.NewHTTPForge(a, installationID)
	tok, _, e := f.GetInstallToken(ctx)
	if e != nil {
		return fmt.Errorf("native Forge token exchange failed (details suppressed)")
	}
	client := &http.Client{Timeout: 45 * time.Second}
	defer func() {
		r, _ := http.NewRequest("DELETE", "https://api.github.com/installation/token", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		resp, e := client.Do(r)
		status := 0
		if resp != nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
		emit("cleanup_forge_token", map[string]any{"status": status, "transport_ok": e == nil})
		if e != nil || (status != 204 && status != 401) {
			resultErr = fmt.Errorf("created Forge token cleanup not confirmed, status=%d", status)
		}
	}()
	call := func(method, path string, body any) (int, map[string]any, error) {
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
		}
		r, e := http.NewRequestWithContext(ctx, method, "https://api.github.com"+path, reader)
		if e != nil {
			return 0, nil, fmt.Errorf("request construction failed")
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Accept", "application/vnd.github+json")
		r.Header.Set("User-Agent", "Cairn-Issue15-Forge-Acceptance")
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, e := client.Do(r)
		if e != nil {
			return 0, nil, fmt.Errorf("sandbox request failed (details suppressed)")
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		emit("supporting_http", map[string]any{"method": method, "path": path, "status": resp.StatusCode, "request_id": resp.Header.Get("X-GitHub-Request-Id")})
		return resp.StatusCode, out, nil
	}
	base, e := f.GetBranchSHA(ctx, owner, name, "main")
	if e != nil {
		return fmt.Errorf("native GetBranchSHA failed (details suppressed)")
	}
	runID := "app-acceptance-" + time.Now().UTC().Format("20060102T150405") + "-" + strconv.Itoa(os.Getpid())
	branch := "acceptance/" + runID
	if e = f.CreateBranch(ctx, owner, name, branch, base); e != nil {
		return fmt.Errorf("native CreateBranch failed (details suppressed)")
	}
	got, e := f.GetBranchSHA(ctx, owner, name, branch)
	if e != nil || got != base {
		return fmt.Errorf("native created branch readback mismatch")
	}
	emit("native_branch", map[string]any{"branch": branch, "base_sha": base, "readback_sha": got})
	path := "/repos/" + owner + "/" + name + "/contents/.cairn-acceptance/" + runID + ".txt"
	status, result, e := call("PUT", path, map[string]any{"message": "test(app): " + runID, "branch": branch, "content": base64.StdEncoding.EncodeToString([]byte("Cairn live AppAuth / HTTPForge acceptance. No model calls.\n"))})
	if e != nil || status != 201 {
		return fmt.Errorf("supporting fixture commit failed, status=%d", status)
	}
	commit, _ := result["commit"].(map[string]any)
	head, _ := commit["sha"].(string)
	if len(head) != 40 {
		return fmt.Errorf("fixture commit missing SHA")
	}
	spec := forge.PullRequestSpec{Owner: owner, Repo: name, HeadBranch: branch, BaseBranch: "main", Title: "Cairn App acceptance: " + runID, Body: "Dedicated test fixture for native AppAuth/HTTPForge acceptance. No product release or stable-readiness claim.", RunMarker: runID}
	pr, e := f.EnsurePullRequest(ctx, spec)
	if e != nil {
		return fmt.Errorf("native EnsurePullRequest failed (details suppressed)")
	}
	if pr.HeadSHA != head || pr.HeadBranch != branch || pr.BaseBranch != "main" || pr.State != "open" {
		return fmt.Errorf("native PR identity mismatch")
	}
	replayed, e := f.EnsurePullRequest(ctx, spec)
	if e != nil || replayed.Number != pr.Number {
		return fmt.Errorf("native PR replay mismatch")
	}
	readback, e := f.GetPullRequest(ctx, owner, name, pr.Number)
	if e != nil || readback.HeadSHA != head {
		return fmt.Errorf("native PR readback failed")
	}
	emit("native_pull_request", map[string]any{"number": pr.Number, "url": pr.HTMLURL, "head_sha": head, "branch": branch, "state": pr.State, "replay_same_number": true})
	releaseSpec := forge.ReleaseSpec{Owner: owner, Repo: name, Tag: runID, Target: head, Title: "App acceptance fixture " + runID, Body: "Dedicated sandbox prerelease for real GitHub App permission acceptance. No product release.", Prelease: true}
	release, e := f.EnsureRelease(ctx, releaseSpec)
	if e != nil {
		return fmt.Errorf("native EnsureRelease failed (details suppressed)")
	}
	replayedRelease, e := f.EnsureRelease(ctx, releaseSpec)
	if e != nil || replayedRelease.ID != release.ID || !release.Prelease {
		return fmt.Errorf("native release replay mismatch")
	}
	emit("native_release", map[string]any{"id": release.ID, "tag": release.Tag, "url": release.HTMLURL, "target_sha": head, "prerelease": release.Prelease, "replay_same_id": true, "assets_tested": false})
	emit("sandbox_objects_preserved", map[string]any{"reason": "reviewable dedicated acceptance fixtures", "pr_merged": false, "branch": branch, "pr": pr.HTMLURL, "release": release.HTMLURL})
	return nil
}
func main() {
	if e := run(); e != nil {
		emit("failure", map[string]any{"message": e.Error()})
		os.Exit(1)
	}
}
