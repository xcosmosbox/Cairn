package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/service/internal/config"
)

func mutateLoadedDatabase(t *testing.T, path, statement string) {
	t.Helper()
	db, err := storage.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Conn().Exec(statement); err != nil {
		t.Fatal(err)
	}
}

// 原联邦路径吞掉所有或部分 SQL 错误，把不完整结果当作正常零命中返回。
// Healthy KB results survive; partial failures remain explicit through MCP and REST.
func TestKnowledgeBaseReadErrorsAreLocal(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "all-failed", true: "partial-failed"}[partial], func(t *testing.T) {
			dir := t.TempDir()
			path := lifecycleDB(t, filepath.Join(dir, "broken.db"), 1)
			state := lifecycleState(t, path)
			if partial {
				if err := state.hotSwapDB("healthy", lifecycleDB(t, filepath.Join(dir, "healthy.db"), 1), "healthy"); err != nil {
					t.Fatal(err)
				}
			}
			mutateLoadedDatabase(t, path, "DROP TABLE nodes")
			for _, name := range []string{"domain_search", "list_knowledge_bases", "describe_knowledge_layer"} {
				params, _ := json.Marshal(map[string]interface{}{"name": name, "arguments": map[string]interface{}{"keyword": "aggregate0"}})
				resp := handleToolsCall(state, &jsonRPCRequest{ID: json.RawMessage("1"), Params: params})
				result, ok := resp.Result.(*toolResult)
				if !ok || result.IsError == partial || (!partial && !strings.Contains(result.Content[0].Text, "smoke")) {
					t.Fatalf("%s masked SQL failure: %+v", name, resp)
				}
				recorder := httptest.NewRecorder()
				writeJSONResponse(recorder, resp)
				var body map[string]interface{}
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if !partial {
					if body["isError"] != true || body["error"] == nil {
						t.Fatalf("REST masked %s failure: %s", name, recorder.Body.String())
					}
				} else {
					failures, ok := body["failed_kbs"].([]interface{})
					if body["partial"] != true || !ok || len(failures) != 1 || failures[0].(map[string]interface{})["kg"] != "smoke" {
						t.Fatalf("missing partial status: %s", recorder.Body.String())
					}
					if name == "domain_search" {
						hits, ok := body["hits"].([]interface{})
						if !ok || len(hits) == 0 || hits[0].(map[string]interface{})["kg"] != "healthy" {
							t.Fatalf("lost healthy hits: %s", recorder.Body.String())
						}
					}
				}
			}
		})
	}
}

func TestDescribeDoesNotHideCrossKnowledgeLinkReadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := lifecycleDB(t, filepath.Join(dir, "knowledge.db"), 1)
	state := lifecycleState(t, path)
	mutateLoadedDatabase(t, path, "DROP TABLE cross_references")
	for _, args := range []map[string]interface{}{{}, {"kg": "smoke"}} {
		result, message := toolDescribeKnowledgeLayer(state, args)
		if result != nil || !strings.Contains(message, "cross-KG links") {
			t.Fatalf("link SQL error masquerades as valid empty links: %+v %s", result, message)
		}
	}
}

func TestWatcherShutdownCancelsAndWaitsForInflightFetch(t *testing.T) {
	// Public catalogs must reach the watcher without a configured token.
	t.Parallel()
	dir := t.TempDir()
	state := lifecycleState(t, lifecycleDB(t, filepath.Join(dir, "knowledge.db"), 1))
	entered, canceled := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("public watcher sent Authorization")
		}
		if requests.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			close(canceled)
		} else {
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()
	state.catalogAPIBase = server.URL
	install := filepath.Join(dir, "installed")
	state.watchConfigs = map[string]config.KBConfig{"smoke": {CatalogRepo: "owner/catalog", CatalogBranch: "main", ManifestDir: "knowledge-bases", InstallDir: install, PollInterval: "1ms"}}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	state.startCatalogWatcher(parent)
	state.startCatalogWatcher(parent) // Repeated startup must not orphan a worker's cancel function.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not fetch")
	}
	// close must cancel the actual request and join its worker, rather than leaving
	// a background pull capable of changing current after shutdown returned.
	closed := make(chan struct{})
	go func() { state.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited on unbounded request")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("HTTP request did not receive cancellation")
	}
	if _, err := os.Stat(install); !os.IsNotExist(err) {
		t.Fatalf("shutdown installed a bundle: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("duplicate watcher launched %d requests", requests.Load())
	}
}

func TestCanceledWatcherParentDoesNotStartFetch(t *testing.T) {
	state := &serverState{watchConfigs: map[string]config.KBConfig{"smoke": {CatalogRepo: "owner/catalog", TokenEnv: "unused"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state.startCatalogWatcher(ctx)
	done := make(chan struct{})
	go func() { state.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("already canceled watcher did not stop")
	}
}

// 错误的显式配置原来会静默选择 ./knowledge.db，可能返回别的语料。
func TestMalformedExplicitConfigDoesNotFallBack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	if cfg, err := loadServerConfig(missing, false); err != nil || cfg == nil {
		t.Fatalf("implicit no-config default: %v", err)
	}
	if cfg, err := loadServerConfig(missing, true); err == nil || cfg != nil {
		t.Fatal("explicit missing config accepted")
	}
	malformed := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(malformed, []byte("mcp: [invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{true, false} {
		if cfg, err := loadServerConfig(malformed, explicit); err == nil || cfg != nil {
			t.Fatal("malformed config fell back to unrelated default database")
		}
	}
}
