package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/service/internal/service"
)

func lifecycleDB(t *testing.T, path string, nodes int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nodes; i++ {
		if _, err := db.Conn().Exec(`INSERT INTO nodes(id,label,name,summary,domain,subdomain) VALUES(?,'Entity',?,'summary','engineering','queues')`, fmt.Sprint(i), fmt.Sprintf("aggregate%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.NewFTSIndex(db).RebuildAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func lifecycleState(t *testing.T, path string) *serverState {
	t.Helper()
	db, svc, resolved, err := openValidatedKB(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &serverState{svcs: map[string]service.KnowledgeService{"smoke": svc}, storages: map[string]*storage.DB{"smoke": db}, meta: map[string]kbMeta{"smoke": {Name: "smoke", Path: resolved}}, digests: map[string]string{"smoke": "old"}}
	t.Cleanup(state.close)
	return state
}

type pausedStatus struct {
	service.KnowledgeService
	db                *storage.DB
	firstRead, resume chan struct{}
	once              sync.Once
}

func (s *pausedStatus) Status(ctx context.Context) (*service.StatusResult, error) {
	// 原实现仅保护路由查找；两条 SQL 之间热换代就关闭了本请求仍持有的库。
	// Pause between real SQL operations to reproduce the old reader-lifetime bug.
	var count int
	if err := s.db.Conn().QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes").Scan(&count); err != nil {
		return nil, err
	}
	s.once.Do(func() { close(s.firstRead) })
	select {
	case <-s.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.KnowledgeService.Status(ctx)
}

func TestHotSwapKeepsCompleteRequestAlive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := lifecycleState(t, lifecycleDB(t, filepath.Join(dir, "old.db"), 1))
	old := state.storages["smoke"]
	paused := &pausedStatus{KnowledgeService: state.svcs["smoke"], db: old, firstRead: make(chan struct{}), resume: make(chan struct{})}
	state.svcs["smoke"] = paused
	result := make(chan string, 1)
	go func() {
		res, err := toolDomainStatus(state, map[string]interface{}{"kg": "smoke"})
		if err != "" {
			result <- err
			return
		}
		result <- res.Content[0].Text
	}()
	select {
	case <-paused.firstRead:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach first SQL")
	}
	if err := state.hotSwapDB("smoke", lifecycleDB(t, filepath.Join(dir, "new.db"), 2), "new"); err != nil {
		t.Fatal(err)
	}
	if err := old.Conn().Ping(); err != nil {
		t.Fatalf("old DB closed before request finished: %v", err)
	}
	current, _, release, err := state.getSvc("smoke")
	if err != nil {
		t.Fatal(err)
	}
	status, err := current.Status(context.Background())
	release()
	if err != nil || status.TotalNodes != 2 {
		t.Fatalf("new requests must see new generation: %+v %v", status, err)
	}
	close(paused.resume)
	select {
	case text := <-result:
		if !strings.Contains(text, "Total Nodes: 1") {
			t.Fatalf("inflight request lost old generation: %s", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request failed to finish")
	}
	if err := old.Conn().Ping(); err == nil {
		t.Fatal("retired DB leaked after last reader released")
	}
}

func TestGenerationLeasesReleaseOnceAndShutdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := lifecycleState(t, lifecycleDB(t, filepath.Join(dir, "old.db"), 1))
	old := state.storages["smoke"]
	_, _, first, err := state.getSvc("smoke")
	if err != nil {
		t.Fatal(err)
	}
	_, second := state.svcsSnapshot()
	if err := state.hotSwapDB("smoke", lifecycleDB(t, filepath.Join(dir, "new.db"), 2), "new"); err != nil {
		t.Fatal(err)
	}
	first()
	first()
	if err := old.Conn().Ping(); err != nil {
		t.Fatal("double release reclaimed a different active reader")
	}
	second()
	if err := old.Conn().Ping(); err == nil {
		t.Fatal("old generation was not reclaimed")
	}
	active := state.storages["smoke"]
	svc, _, release, err := state.getSvc("smoke")
	if err != nil {
		t.Fatal(err)
	}
	state.close()
	if _, _, _, err := state.getSvc("smoke"); err == nil {
		t.Fatal("closed state accepted new reader")
	}
	if _, err := svc.Status(context.Background()); err != nil {
		t.Fatalf("shutdown interrupted acquired reader: %v", err)
	}
	release()
	if err := active.Conn().Ping(); err == nil {
		t.Fatal("shutdown leaked last generation")
	}
}

func TestInvalidSwapPreservesHealthyGeneration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		prepare func(*testing.T, string) string
	}{
		{"text", func(t *testing.T, p string) string {
			if err := os.WriteFile(p, []byte("not sqlite"), 0600); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"missing", func(t *testing.T, p string) string { return p }},
		{"unsupported-old-schema", func(t *testing.T, p string) string { return mutateLifecycleDB(t, p, "PRAGMA user_version=3") }},
		{"future-schema", func(t *testing.T, p string) string { return mutateLifecycleDB(t, p, "PRAGMA user_version=999") }},
		{"missing-fts", func(t *testing.T, p string) string { return mutateLifecycleDB(t, p, "DROP TABLE nodes_fts") }},
		{"missing-node-column", func(t *testing.T, p string) string {
			return mutateLifecycleDB(t, p, "ALTER TABLE nodes DROP COLUMN file_slug")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			state := lifecycleState(t, lifecycleDB(t, filepath.Join(dir, "old.db"), 1))
			old, path := state.storages["smoke"], state.meta["smoke"].Path
			candidate := tc.prepare(t, filepath.Join(dir, "candidate.db"))
			if err := state.hotSwapDB("smoke", candidate, "invalid"); err == nil {
				t.Fatal("invalid candidate was accepted")
			}
			if state.storages["smoke"] != old || state.digests["smoke"] != "old" || state.meta["smoke"].Path != path {
				t.Fatal("failed validation changed current generation")
			}
			res, msg := toolDomainStatus(state, map[string]interface{}{"kg": "smoke"})
			if msg != "" || !strings.Contains(res.Content[0].Text, "Total Nodes: 1") {
				t.Fatalf("old healthy service damaged: %v %s", res, msg)
			}
		})
	}
}

func mutateLifecycleDB(t *testing.T, path, statement string) string {
	t.Helper()
	lifecycleDB(t, path, 1)
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(statement); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// 真正的 v4 没有 file_slug；仅修改 user_version 的 v5 库不能覆盖兼容性。
// Exercise the legacy shape through startup, all MCP tools and a live swap without writes.
func TestLegacyV4StartupAndHotSwapRemainReadOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	legacy := lifecycleDB(t, filepath.Join(dir, "legacy.db"), 1)
	writer, err := storage.OpenExisting(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Conn().Exec("ALTER TABLE nodes DROP COLUMN file_slug"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Conn().Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, startup := range []bool{true, false} {
		t.Run(map[bool]string{true: "startup", false: "hot-swap"}[startup], func(t *testing.T) {
			var state *serverState
			if startup {
				state = lifecycleState(t, legacy)
			} else {
				state = lifecycleState(t, lifecycleDB(t, filepath.Join(dir, "current.db"), 2))
				if err := state.hotSwapDB("smoke", legacy, "legacy"); err != nil {
					t.Fatalf("legacy generation rejected: %v", err)
				}
			}
			cases := []struct {
				name string
				args map[string]interface{}
				want string
			}{
				{"domain_search", map[string]interface{}{"keyword": "aggregate0"}, "aggregate0"},
				{"domain_status", map[string]interface{}{"kg": "smoke"}, "Schema Version: 4"},
				{"domain_impact", map[string]interface{}{"entity_name": "aggregate0"}, "aggregate0"},
				{"resolve_node", map[string]interface{}{"uuid": "0"}, "aggregate0"},
				{"list_knowledge_bases", map[string]interface{}{}, "smoke"},
				{"describe_knowledge_layer", map[string]interface{}{"kg": "smoke"}, "smoke"},
			}
			for _, tc := range cases {
				params, _ := json.Marshal(map[string]interface{}{"name": tc.name, "arguments": tc.args})
				resp := handleToolsCall(state, &jsonRPCRequest{ID: json.RawMessage("1"), Params: params})
				result, ok := resp.Result.(*toolResult)
				if !ok || result.IsError || !strings.Contains(result.Content[0].Text, tc.want) {
					t.Fatalf("legacy %s query failed: %+v", tc.name, resp)
				}
			}
			var columns int
			if err := state.storages["smoke"].Conn().QueryRow("SELECT count(*) FROM pragma_table_info('nodes') WHERE name='file_slug'").Scan(&columns); err != nil || columns != 0 {
				t.Fatalf("query introduced v5 column: count=%d err=%v", columns, err)
			}
			state.close()
		})
	}
	after, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("legacy MCP startup or query modified the database")
	}
}

func TestSymlinkDoesNotRetargetAcquiredConnections(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := lifecycleDB(t, filepath.Join(dir, "generation-a", "knowledge.db"), 1)
	second := lifecycleDB(t, filepath.Join(dir, "generation-b", "knowledge.db"), 2)
	current := filepath.Join(dir, "current")
	if err := os.Symlink(filepath.Dir(first), current); err != nil {
		t.Fatal(err)
	}
	state := lifecycleState(t, filepath.Join(current, "knowledge.db"))
	old := state.storages["smoke"]
	svc, _, release, err := state.getSvc("smoke")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// 原实现保存 current 路径：池中的连接重开后可悄悄指向另一代际。
	// Force a pooled connection reopen after switching the mutable symlink.
	old.Conn().SetMaxIdleConns(0)
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(second), current); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(context.Background())
	if err != nil || status.TotalNodes != 1 {
		t.Fatalf("acquired generation changed after symlink switch: %+v %v", status, err)
	}
	if err := state.hotSwapDB("smoke", filepath.Join(current, "knowledge.db"), "new"); err != nil {
		t.Fatal(err)
	}
	if state.meta["smoke"].Path != second {
		t.Fatalf("mutable path retained: %q", state.meta["smoke"].Path)
	}
}

func TestInitializeNegotiatesImplementedVersion(t *testing.T) {
	t.Parallel()
	for _, requested := range []string{mcpProtocolVersion, "2025-06-18", "0.1"} {
		data, _ := json.Marshal(map[string]interface{}{"protocolVersion": requested})
		resp := handleInitialize(&jsonRPCRequest{ID: json.RawMessage("1"), Params: data})
		if resp.Error != nil {
			t.Fatal(resp.Error)
		}
		if got := resp.Result.(map[string]interface{})["protocolVersion"]; got != mcpProtocolVersion {
			t.Fatalf("requested %s got %v", requested, got)
		}
	}
	for _, params := range []string{`{}`, `null`, `{"protocolVersion":42}`, `{"protocolVersion":""}`, `{`} {
		resp := handleInitialize(&jsonRPCRequest{ID: json.RawMessage("1"), Params: json.RawMessage(params)})
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("invalid initialize accepted: %s %+v", params, resp)
		}
	}
	if got := handleRequest(nil, &jsonRPCRequest{Method: "ping", ID: json.RawMessage("2")}); got.Error != nil {
		t.Fatal(got.Error)
	}
	if got := handleRequest(nil, &jsonRPCRequest{Method: "notifications/unknown"}); got != nil {
		t.Fatal("notification generated response")
	}
}

func TestToolErrorsAreMachineReadable(t *testing.T) {
	t.Parallel()
	resp := handleToolsCall(&serverState{}, &jsonRPCRequest{ID: json.RawMessage("1"), Params: json.RawMessage(`{"name":"domain_search","arguments":{}}`)})
	res, ok := resp.Result.(*toolResult)
	if !ok || !res.IsError || len(res.Content) != 1 {
		t.Fatalf("tool failure masquerades as success: %+v", resp)
	}
}
