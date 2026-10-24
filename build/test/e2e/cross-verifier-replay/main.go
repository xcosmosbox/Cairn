// cross-verifier-replay reuses two exact retained attempt02 candidates. It does
// not annotate, discover, regroup, materialize or write a knowledge graph.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

const baseSHA = "75a77a0d5d1e1d5077c4758e56f2bc7fd3ac62a8"
const graphSHA = "2dddeeb9ef1070bc3b75f1a7fbc0801a67a9fbbbfcd258dfa0abf83627eaee54"
const docsSHA = "6be352ae656864c962930c2e547571de797b9e7254389f7dba324cec74aa2c1c"
const queueSHA = "a1a3905bcf204cb702a6fd1b48521505d3115b06240a4b3ff4fdbcf8c2b8e135"
const timeoutSHA = "962d59eed21fe457189a5cf252f6b95e86e85efa7903717ddbf7df892a0a2587"
const verifierSHA = "0dad47ace8c9fd8192c99ac271f9ecacbbc40752c06b3735c81be1dfd1fe8914"
const domainName = "并发队列与背压控制"

var subdomainNames = []string{"阻塞队列模型", "生产者重试与预算", "运行环境约束"}
var wanted = []extract.Relation{
	{Source: "retry-budget", Target: "blocking-queue", Kind: "depends_on", Description: "重试预算依赖阻塞队列的容量与等待行为。", Confidence: 0.9},
	{Source: "free-fixed-fake", Target: "retry-budget", Kind: "contradicts", Description: "假运行环境中不存在重试预算，与含预算场景相矛盾。", Confidence: 0.8},
}

type inputs struct {
	Graph        extract.Result
	Docs         []*dktypes.AnnotatedDocument
	Sources      []extract.FrozenSource
	Scopes       []map[string]string
	InventorySHA string
}

func digest(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func encoded(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}
func rootPath() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(f), "../../../.."))
}
func writeNew(path string, v any) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	raw := encoded(v)
	n, e := f.Write(raw)
	if e == nil && n != len(raw) {
		e = errors.New("short receipt write")
	}
	if e == nil {
		e = f.Sync()
	}
	return e
}
func pin(path, expected string) ([]byte, error) {
	b, e := os.ReadFile(path)
	if e != nil || digest(b) != expected {
		return nil, fmt.Errorf("frozen input identity mismatch: %s", filepath.Base(path))
	}
	return b, nil
}
func nodeInventory(g extract.Result) []byte {
	for di := range g.Domains {
		for si := range g.Domains[di].Subdomains {
			g.Domains[di].Subdomains[si].Relations = nil
		}
	}
	return encoded(g)
}

// json:"-" drops derived scopes in historical dumps. Restore only with the
// production Slugify function, then compare the original expected groups.
func restoreAndProject(g *extract.Result) ([]map[string]string, string, error) {
	if len(g.Domains) != 1 || g.Domains[0].Name != domainName || len(g.Domains[0].Subdomains) != 3 {
		return nil, "", errors.New("original taxonomy changed")
	}
	beforeRaw := encoded(g)
	var before extract.Result
	if json.Unmarshal(beforeRaw, &before) != nil {
		return nil, "", errors.New("graph decode")
	}
	beforeInv := nodeInventory(before)
	g.Domains[0].Slug = extract.Slugify(g.Domains[0].Name)
	if g.Domains[0].Slug == "" {
		return nil, "", errors.New("empty domain scope")
	}
	counts := [2]int{}
	scopes := []map[string]string{}
	for si := range g.Domains[0].Subdomains {
		sd := &g.Domains[0].Subdomains[si]
		if sd.Name != subdomainNames[si] {
			return nil, "", errors.New("original subdomain changed")
		}
		sd.Slug = extract.Slugify(sd.Name)
		if sd.Slug == "" {
			return nil, "", errors.New("empty subdomain scope")
		}
		scopes = append(scopes, map[string]string{"domain": g.Domains[0].Name, "domain_slug": g.Domains[0].Slug, "subdomain": sd.Name, "subdomain_slug": sd.Slug})
		kept := []extract.Relation{}
		for _, r := range sd.Relations {
			for wi, w := range wanted {
				if r.Source == w.Source && r.Target == w.Target && r.Kind == w.Kind {
					if r != w || si != []int{1, 2}[wi] {
						return nil, "", errors.New("selected original relation changed or moved")
					}
					counts[wi]++
					kept = append(kept, r)
				}
			}
		}
		sd.Relations = kept
	}
	if counts != [2]int{1, 1} {
		return nil, "", errors.New("exactly two unique retained candidate rows required")
	}
	if scopes[0]["subdomain_slug"] == scopes[1]["subdomain_slug"] || scopes[1]["subdomain_slug"] == scopes[2]["subdomain_slug"] || scopes[0]["subdomain_slug"] == scopes[2]["subdomain_slug"] {
		return nil, "", errors.New("ambiguous derived scopes")
	}
	// Marshal excludes derived slugs; stripping relations on a detached copy
	// verifies that nodes, taxonomy, members and every other retained field agree.
	afterRaw := encoded(g)
	var detached extract.Result
	_ = json.Unmarshal(afterRaw, &detached)
	afterInv := nodeInventory(detached)
	if string(beforeInv) != string(afterInv) {
		return nil, "", errors.New("non-relation graph content changed")
	}
	return scopes, digest(beforeInv), nil
}
func loadInputs(fixture string) (inputs, error) {
	var in inputs
	g, e := pin(filepath.Join(fixture, "dump/05_extract_result.json"), graphSHA)
	if e != nil {
		return in, e
	}
	d, e := pin(filepath.Join(fixture, "dump/04_extract_input.json"), docsSHA)
	if e != nil {
		return in, e
	}
	if json.Unmarshal(g, &in.Graph) != nil || json.Unmarshal(d, &in.Docs) != nil {
		return in, errors.New("frozen graph/annotations invalid")
	}
	if len(in.Docs) != 2 {
		return in, errors.New("two original annotation documents required")
	}
	for _, entry := range []struct{ path, hash string }{{"references/queue.md", queueSHA}, {"references/timeout.md", timeoutSHA}} {
		raw, e := pin(filepath.Join(fixture, "original", entry.path), entry.hash)
		if e != nil {
			return in, e
		}
		in.Sources = append(in.Sources, extract.FreezeSource("public-synthetic-queue", entry.path, string(raw)))
	}
	in.Scopes, in.InventorySHA, e = restoreAndProject(&in.Graph)
	return in, e
}

type callReceipt struct {
	Call      int              `json:"logical_call"`
	Relation  extract.Relation `json:"relation"`
	SystemSHA string           `json:"system_sha256"`
	UserSHA   string           `json:"user_sha256"`
	Started   string           `json:"started_at"`
	Finished  string           `json:"finished_at,omitempty"`
	Response  bool             `json:"response_received"`
	Error     bool             `json:"complete_error"`
	Input     int              `json:"reported_input_tokens"`
	Output    int              `json:"reported_output_tokens"`
	Finish    string           `json:"finish_reason,omitempty"`
}
type countedClient struct {
	next     llm.Client
	mu       sync.Mutex
	calls    int
	used     map[string]bool
	file     *os.File
	cancel   context.CancelFunc
	failed   bool
	receipts []callReceipt
	wire     int
}

func (c *countedClient) ProviderName() string { return c.next.ProviderName() }
func (c *countedClient) persist(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		c.failed = true
		c.cancel()
		return err
	}
	raw = append(raw, '\n')
	n, e := c.file.Write(raw)
	if e == nil && n != len(raw) {
		e = errors.New("short call receipt")
	}
	if e == nil {
		e = c.file.Sync()
	}
	if e != nil {
		c.failed = true
		c.cancel()
	}
	return e
}

// 逻辑重试上限不能约束 net/http 的重定向；每次真实 transport 调用都必须
// 先落盘，再检查固定目的地与总预算。绝不记录 Authorization 或重定向 URL。
// Redirects are independently fenced before any transport/key egress.
type endpointGuard struct {
	next  http.RoundTripper
	audit *countedClient
}

func (g *endpointGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	c := g.audit
	c.mu.Lock()
	class := ""
	if req.URL.String() != dkconfig.DeepSeekEndpoint || req.Method != http.MethodPost {
		class = "endpoint_or_method"
	} else if c.failed || c.wire >= 4 {
		class = "wire_budget_or_sticky_failure"
	} else if req.Context().Err() != nil {
		class = "cancelled_before_admission"
	}
	if class != "" {
		_ = c.persist(map[string]any{"event": "wire_rejected", "reason": class})
		c.failed = true
		c.cancel()
		c.mu.Unlock()
		return nil, errors.New("frozen replay transport admission rejected")
	}
	if err := c.persist(map[string]any{"event": "wire_admitted", "transport_attempt": c.wire + 1,
		"method": "POST", "endpoint": dkconfig.DeepSeekEndpoint, "time_utc": time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	c.wire++
	ordinal := c.wire
	c.mu.Unlock()
	resp, err := g.next.RoundTrip(req)
	row := map[string]any{"event": "wire_result", "transport_attempt": ordinal, "transport_error": err != nil}
	if resp != nil {
		row["http_status"] = resp.StatusCode
	}
	c.mu.Lock()
	persistErr := c.persist(row)
	c.mu.Unlock()
	if persistErr != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, persistErr
	}
	return resp, err
}
func (c *countedClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	var view struct {
		Relation extract.Relation      `json:"relation_candidate"`
		Source   extract.CrossEndpoint `json:"source"`
		Target   extract.CrossEndpoint `json:"target"`
	}
	if json.Unmarshal([]byte(req.User), &view) != nil {
		return nil, errors.New("unexpected verifier input")
	}
	wi := -1
	for i, w := range wanted {
		if view.Relation == w {
			wi = i
		}
	}
	if wi < 0 {
		return nil, errors.New("unfrozen candidate request")
	}
	sourceScope := subdomainNames[[]int{1, 2}[wi]]
	targetScope := subdomainNames[[]int{0, 1}[wi]]
	if view.Source.Domain != extract.Slugify(domainName) || view.Target.Domain != extract.Slugify(domainName) || view.Source.Subdomain != extract.Slugify(sourceScope) || view.Target.Subdomain != extract.Slugify(targetScope) || view.Source.Alias != wanted[wi].Source || view.Target.Alias != wanted[wi].Target {
		return nil, errors.New("wrong resolved candidate scope")
	}
	c.mu.Lock()
	if c.failed || c.calls >= 2 || c.used[wanted[wi].Kind] {
		c.mu.Unlock()
		return nil, errors.New("two unique logical calls maximum")
	}
	c.calls++
	c.used[wanted[wi].Kind] = true
	row := callReceipt{Call: c.calls, Relation: view.Relation, SystemSHA: digest([]byte(req.System)), UserSHA: digest([]byte(req.User)), Started: time.Now().UTC().Format(time.RFC3339Nano)}
	e := c.persist(map[string]any{"event": "start", "call": row})
	c.mu.Unlock()
	if e != nil {
		return nil, e
	}
	response, err := c.next.Complete(ctx, req)
	row.Finished = time.Now().UTC().Format(time.RFC3339Nano)
	row.Error = err != nil
	row.Response = response != nil
	if response != nil {
		row.Input = response.InputTokens
		row.Output = response.OutputTokens
		row.Finish = response.FinishReason
	}
	c.mu.Lock()
	c.receipts = append(c.receipts, row)
	e = c.persist(map[string]any{"event": "finish", "call": row})
	c.mu.Unlock()
	if e != nil {
		return response, e
	}
	return response, err
}
func gate(r *extract.CrossRelationReport, calls int) bool {
	if r == nil || r.Candidates != 2 || r.Accepted != 1 || len(r.Decisions) != 2 || calls != 2 {
		return false
	}
	positive, negative := false, false
	for _, d := range r.Decisions {
		if d.Relation == wanted[0] {
			positive = d.Disposition == "accepted" && d.VerifierDisposition == "supported" && len(d.Evidence) >= 2
		}
		if d.Relation == wanted[1] {
			negative = d.Disposition == "rejected_semantic" && d.VerifierDisposition == "rejected"
		}
	}
	return positive && negative
}

func run() error {
	mode := flag.String("mode", "preflight", "preflight or run")
	fixture := flag.String("fixture", filepath.Join(rootPath(), "docs/coordination/reports/issue16-realmodel-20261024-live16/attempt-02"), "unchanged retained attempt02")
	out := flag.String("out", "", "new isolated output")
	allow := flag.Bool("allow-paid", false, "root admitted one frozen replay after guard review")
	freezePin := flag.String("freeze-sha256", "", "reviewed preflight receipt hash")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		return errors.New("output required")
	}
	in, e := loadInputs(*fixture)
	if e != nil {
		return e
	}
	if _, e = pin(filepath.Join(rootPath(), "build/internal/extract/cross_relations.go"), verifierSHA); e != nil {
		return e
	}
	cmd := exec.Command("git", "diff", "--exit-code", baseSHA, "--", "build/internal", "core", "service", "go.mod", "go.sum")
	cmd.Dir = rootPath()
	if e = cmd.Run(); e != nil {
		return errors.New("accepted production source differs")
	}
	exe, _ := os.Executable()
	binary, e := os.ReadFile(exe)
	if e != nil {
		return e
	}
	headCmd := exec.Command("git", "rev-parse", "HEAD")
	headCmd.Dir = rootPath()
	head, e := headCmd.Output()
	if e != nil {
		return e
	}
	freeze := map[string]any{"base_source": baseSHA, "driver_source_commit": string(head), "executable_sha256": digest(binary), "production_verifier_sha256": verifierSHA, "input_hashes": map[string]string{"05": graphSHA, "04": docsSHA, "queue": queueSHA, "timeout": timeoutSHA}, "derived_scopes": in.Scopes, "unchanged_non_relation_inventory_sha256": in.InventorySHA, "projection_sha256": digest(encoded(in.Graph)), "selected_exact_rows": wanted, "logical_call_cap": 2, "wire_attempt_cap": 4, "http_max_retries": 1, "max_tokens": 16384, "http_timeout_seconds": 180, "overall_timeout_seconds": 300, "evidence_bytes": 65536, "endpoint": dkconfig.DeepSeekEndpoint, "requested_model": dkconfig.DeepSeekFlashModel, "new_annotation_or_build": false, "whole_run_retries": 0, "provider_response_IDs_failed_usage": "unknown_standard_client"}
	if *mode == "preflight" {
		if *allow {
			return errors.New("preflight is offline")
		}
		if e = os.Mkdir(*out, 0700); e != nil {
			return e
		}
		if e = writeNew(filepath.Join(*out, "projection.json"), in.Graph); e != nil {
			return e
		}
		return writeNew(filepath.Join(*out, "freeze.json"), freeze)
	}
	if *mode != "run" || !*allow {
		return errors.New("explicit admitted run required")
	}
	prior, e := pin(filepath.Join(*out, "freeze.json"), *freezePin)
	if e != nil {
		return e
	}
	if string(prior) != string(encoded(freeze)) {
		return errors.New("reviewed freeze no longer matches executable/source/input/options")
	}
	projection, e := os.ReadFile(filepath.Join(*out, "projection.json"))
	if e != nil || digest(projection) != digest(encoded(in.Graph)) {
		return errors.New("projection changed")
	}
	if _, e = os.Stat(filepath.Join(*out, "result.json")); !os.IsNotExist(e) {
		return errors.New("attempt already exists")
	}
	if os.Getenv("CAIRN_LLM_API_KEY") == "" {
		return errors.New("runtime model key unavailable")
	}
	file, e := os.OpenFile(filepath.Join(*out, "calls.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer file.Close()
	private, e := os.OpenFile(filepath.Join(*out, "private-provider.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer private.Close()
	// Persist the new ledger's directory entry before any request admission.
	// A failed directory sync leaves this attempt closed, with no dispatch.
	directory, e := os.Open(*out)
	if e != nil {
		return e
	}
	e = directory.Sync()
	closeErr := directory.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	previous := log.Writer()
	log.SetOutput(private)
	defer log.SetOutput(previous)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	client, e := llm.NewOpenAICompatClient(llm.Config{Provider: "openai_compatible", APIKeyEnv: "CAIRN_LLM_API_KEY", Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: 16384, Timeout: "180s", MaxRetries: 1})
	if e != nil {
		return e
	}
	counted := &countedClient{next: client, used: map[string]bool{}, file: file, cancel: cancel}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = &endpointGuard{next: previousTransport, audit: counted}
	defer func() { http.DefaultTransport = previousTransport }()
	started := time.Now()
	r := extract.NewExtractor(counted, 16384).ValidateCrossRelations(ctx, &in.Graph, in.Docs, in.Sources, extract.CrossRelationOptions{MaxEvidenceBytes: 65536})
	if e = writeNew(filepath.Join(*out, "cross-report.json"), r); e != nil {
		return e
	}
	if e = writeNew(filepath.Join(*out, "filtered-projection.json"), in.Graph); e != nil {
		return e
	}
	ok := gate(r, counted.calls) && !counted.failed && ctx.Err() == nil
	result := map[string]any{"status": "fail", "started_at": started.UTC().Format(time.RFC3339Nano), "finished_at": time.Now().UTC().Format(time.RFC3339Nano), "seconds": time.Since(started).Seconds(), "logical_calls": counted.calls, "calls": counted.receipts, "max_wire_attempts": 4, "admitted_transport_attempts": counted.wire, "failed_usage_responseIDs": "unknown_standard_client", "context_error": ctx.Err() != nil, "ledger_or_admission_error": counted.failed, "no_reannotation_full_build": true, "positive_accepted_negative_explicit_rejected": ok}
	if ok {
		result["status"] = "pass"
	}
	if e = writeNew(filepath.Join(*out, "result.json"), result); e != nil {
		return e
	}
	if !ok {
		return errors.New("frozen replay gate failed; retain outcome without rerun")
	}
	return nil
}
func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "Frozen verifier replay failed; retained receipts identify the boundary.")
		os.Exit(1)
	}
	fmt.Println("Frozen verifier replay completed; inspect retained evidence before acceptance.")
}
