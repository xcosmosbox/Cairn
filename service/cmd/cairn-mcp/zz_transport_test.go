package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the real entry point in a child, keeping handshake/transport regressions
// independent of local fixed binaries and preventing test framework stdout leaks.
func TestMCPTransportChild(t *testing.T) {
	if os.Getenv("CAIRN_TRANSPORT_TEST") != "1" {
		return
	}
	os.Args = []string{"cairn-mcp", "--config", os.Getenv("CAIRN_TRANSPORT_CONFIG")}
	if addr := os.Getenv("CAIRN_TRANSPORT_ADDR"); addr != "" {
		os.Args = append(os.Args, "--listen", addr)
	}
	flag.CommandLine = flag.NewFlagSet("cairn-mcp", flag.ExitOnError)
	main()
	os.Exit(0)
}

func transportChild(t *testing.T, addr string) (*exec.Cmd, *bytes.Buffer, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	dbpath := lifecycleDB(t, filepath.Join(dir, "knowledge.db"), 1)
	cfgpath := filepath.Join(dir, "mcp.yaml")
	if err := os.WriteFile(cfgpath, []byte(fmt.Sprintf("mcp:\n  knowledge_bases:\n    - name: smoke\n      path: %s\n", dbpath)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPTransportChild$")
	cmd.Env = append(os.Environ(), "CAIRN_TRANSPORT_TEST=1", "CAIRN_TRANSPORT_CONFIG="+cfgpath, "CAIRN_TRANSPORT_ADDR="+addr)
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	return cmd, stderr, cancel
}

func wireRequest(id int, method string, params interface{}) map[string]interface{} {
	return map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

func requireWireSuccess(t *testing.T, resp map[string]interface{}, id int) map[string]interface{} {
	t.Helper()
	if resp["jsonrpc"] != "2.0" || resp["id"] != float64(id) || resp["error"] != nil {
		t.Fatalf("bad response: %+v", resp)
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing result: %+v", resp)
	}
	if result["isError"] == true {
		t.Fatalf("tool failed: %+v", result)
	}
	return result
}

func TestRealStdioHandshakeAndQueries(t *testing.T) {
	t.Parallel()
	cmd, stderr, _ := transportChild(t, "")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
	roundtrip := func(req interface{}, id int) map[string]interface{} {
		t.Helper()
		if err := encoder.Encode(req); err != nil {
			t.Fatal(err)
		}
		var resp map[string]interface{}
		if err := decoder.Decode(&resp); err != nil {
			t.Fatalf("decode: %v; stderr=%s", err, stderr.String())
		}
		return requireWireSuccess(t, resp, id)
	}
	result := roundtrip(wireRequest(1, "initialize", map[string]interface{}{"protocolVersion": mcpProtocolVersion, "capabilities": map[string]interface{}{}, "clientInfo": map[string]interface{}{"name": "regression", "version": "1"}}), 1)
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Fatalf("wrong negotiated date: %+v", result)
	}
	if err := encoder.Encode(map[string]interface{}{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	listed := roundtrip(wireRequest(2, "tools/list", map[string]interface{}{}), 2)
	if len(listed["tools"].([]interface{})) != 6 {
		t.Fatal("tool definitions changed")
	}
	for i, name := range []string{"domain_status", "domain_search", "list_knowledge_bases", "describe_knowledge_layer"} {
		args := map[string]interface{}{"kg": "smoke", "keyword": "aggregate0", "verbose": true}
		res := roundtrip(wireRequest(i+3, "tools/call", map[string]interface{}{"name": name, "arguments": args}), i+3)
		content := res["content"].([]interface{})
		if len(content) == 0 || content[0].(map[string]interface{})["text"] == "" {
			t.Fatalf("empty %s response", name)
		}
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("server exit: %v stderr=%s", err, stderr.String())
	}
}

func TestRealHTTPRESTAndLegacySSEHandshake(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	cmd, stderr, cancel := transportChild(t, addr)
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	base := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get(base + "/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP server unavailable: %v; %s", err, stderr.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, path := range []string{"/api/tools", "/api/kbs", "/api/status?kg=smoke", "/api/search?kg=smoke&keyword=aggregate0", "/api/impact?kg=smoke&entity=aggregate0", "/api/describe?kg=smoke"} {
		res, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]interface{}
		err = json.NewDecoder(res.Body).Decode(&response)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("%s status=%d decode=%v", path, res.StatusCode, err)
		}
		if response["error"] != nil || response["isError"] == true {
			t.Fatalf("REST error: %+v", response)
		}
		switch {
		case path == "/api/tools":
			if len(response["tools"].([]interface{})) != 6 {
				t.Fatal(response)
			}
		case path == "/api/kbs":
			if len(response["knowledge_bases"].([]interface{})) != 1 {
				t.Fatal(response)
			}
		case strings.HasPrefix(path, "/api/status"):
			if !strings.Contains(response["markdown"].(string), "Total Nodes: 1") {
				t.Fatal(response)
			}
		case strings.HasPrefix(path, "/api/search"):
			if !strings.Contains(response["markdown"].(string), "- ID: 0") {
				t.Fatal(response)
			}
		case strings.HasPrefix(path, "/api/impact"):
			if !strings.Contains(response["markdown"].(string), "Reachable nodes: 0") {
				t.Fatal(response)
			}
		case strings.HasPrefix(path, "/api/describe"):
			if response["kg_name"] != "smoke" {
				t.Fatal(response)
			}
		}
	}
	// Exercise real REST query_syntax forwarding; an ignored option would make
	// the explicit boolean query behave like the empty literal-AND query.
	for _, tc := range []struct {
		syntax  string
		wantHit bool
	}{{"text", false}, {"fts5", true}} {
		path := "/api/search?kg=smoke&keyword=" + url.QueryEscape("aggregate0 OR absent") + "&query_syntax=" + tc.syntax
		res, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]interface{}
		err = json.NewDecoder(res.Body).Decode(&response)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || response["isError"] == true || response["error"] != nil {
			t.Fatalf("REST syntax request failed: %+v %v", response, err)
		}
		text, _ := response["markdown"].(string)
		if strings.Contains(text, "- ID: 0") != tc.wantHit {
			t.Fatalf("REST lost syntax %s: %+v", tc.syntax, response)
		}
	}
	stream, err := client.Get(base + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatal(stream.Status)
	}
	reader := bufio.NewReader(stream.Body)
	event := func() string {
		t.Helper()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("SSE read: %v", err)
			}
			if strings.HasPrefix(line, "data: ") {
				return strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
	}
	endpoint := event()
	if !strings.HasPrefix(endpoint, "/rpc?session=") {
		t.Fatalf("bad SSE endpoint: %s", endpoint)
	}
	post := func(request interface{}) {
		t.Helper()
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Post(base+endpoint, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusAccepted {
			t.Fatal(res.Status)
		}
	}
	post(wireRequest(1, "initialize", map[string]interface{}{"protocolVersion": mcpProtocolVersion, "capabilities": map[string]interface{}{}, "clientInfo": map[string]interface{}{"name": "regression", "version": "1"}}))
	var initialized map[string]interface{}
	if err := json.Unmarshal([]byte(event()), &initialized); err != nil {
		t.Fatal(err)
	}
	if result := requireWireSuccess(t, initialized, 1); result["protocolVersion"] != mcpProtocolVersion {
		t.Fatal(result)
	}
	post(map[string]interface{}{"jsonrpc": "2.0", "method": "notifications/initialized"})
	post(wireRequest(2, "tools/list", map[string]interface{}{}))
	var listed map[string]interface{}
	if err := json.Unmarshal([]byte(event()), &listed); err != nil {
		t.Fatal(err)
	}
	if result := requireWireSuccess(t, listed, 2); len(result["tools"].([]interface{})) != 6 {
		t.Fatal(result)
	}
}
