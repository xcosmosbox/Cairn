package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func httpResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func guardAudit(t *testing.T) (*countedClient, context.Context) {
	t.Helper()
	f, e := os.OpenFile(filepath.Join(t.TempDir(), "calls.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &countedClient{file: f, cancel: cancel, used: map[string]bool{}}, ctx
}
func TestActualProductionClientRetryAndRedirectAreTransportFenced(t *testing.T) {
	t.Setenv("CAIRN_LLM_API_KEY", "offline-test-token")
	for _, mode := range []string{"429_then_success", "redirect_denied"} {
		t.Run(mode, func(t *testing.T) {
			audit, ctx := guardAudit(t)
			calls := 0
			next := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != dkconfig.DeepSeekEndpoint || r.Method != "POST" {
					t.Fatal("unexpected transport destination")
				}
				if mode == "redirect_denied" {
					response := httpResponse(r, 307, "")
					response.Header.Set("Location", "https://unapproved.invalid/credential-sink")
					return response, nil
				}
				if calls == 1 {
					return httpResponse(r, 429, ""), nil
				}
				return httpResponse(r, 200, `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`), nil
			})
			previous := http.DefaultTransport
			http.DefaultTransport = &endpointGuard{next: next, audit: audit}
			defer func() { http.DefaultTransport = previous }()
			client, e := llm.NewOpenAICompatClient(llm.Config{Model: dkconfig.DeepSeekFlashModel, Endpoint: dkconfig.DeepSeekEndpoint, MaxTokens: 64, Timeout: "5s", MaxRetries: 1})
			if e != nil {
				t.Fatal(e)
			}
			_, e = client.Complete(ctx, llm.CompleteRequest{System: "Return JSON.", User: "offline JSON", MaxTokens: 64})
			if mode == "429_then_success" {
				if e != nil || calls != 2 || audit.wire != 2 || audit.failed {
					t.Fatalf("actual retry not accounted: %v %d %+v", e, calls, audit)
				}
			} else {
				if e == nil || calls != 1 || audit.wire != 1 || !audit.failed || ctx.Err() == nil {
					t.Fatal("redirect dispatched outside fixed endpoint or failed open")
				}
			}
			raw, e := os.ReadFile(audit.file.Name())
			if e != nil {
				t.Fatal(e)
			}
			for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
				var event map[string]any
				if json.Unmarshal(line, &event) != nil {
					t.Fatal("not compact one-event JSONL")
				}
			}
		})
	}
}
func TestFourWireBudgetAndLedgerFailureStopBeforeNextSend(t *testing.T) {
	audit, ctx := guardAudit(t)
	calls := 0
	g := &endpointGuard{audit: audit, next: roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return httpResponse(r, 200, "{}"), nil })}
	for i := 0; i < 4; i++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", dkconfig.DeepSeekEndpoint, strings.NewReader("{}"))
		response, e := g.RoundTrip(req)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", dkconfig.DeepSeekEndpoint, strings.NewReader("{}"))
	if _, e := g.RoundTrip(req); e == nil || calls != 4 || audit.wire != 4 || ctx.Err() == nil {
		t.Fatal("fifth wire attempt admitted")
	}
	failed, otherCtx := guardAudit(t)
	failed.file.Close()
	sent := false
	guard := &endpointGuard{audit: failed, next: roundTripFunc(func(r *http.Request) (*http.Response, error) { sent = true; return httpResponse(r, 200, "{}"), nil })}
	other, _ := http.NewRequestWithContext(otherCtx, "POST", dkconfig.DeepSeekEndpoint, strings.NewReader("{}"))
	if _, e := guard.RoundTrip(other); e == nil || sent || failed.wire != 0 || otherCtx.Err() == nil {
		t.Fatal("failed durable admission allowed transport")
	}
}

func fixturePath() string {
	return filepath.Join(rootPath(), "docs/coordination/reports/issue16-realmodel-20261024-live16/attempt-02")
}
func originalGraph(t *testing.T) extract.Result {
	t.Helper()
	raw, e := pin(filepath.Join(fixturePath(), "dump/05_extract_result.json"), graphSHA)
	if e != nil {
		t.Fatal(e)
	}
	var g extract.Result
	if e = json.Unmarshal(raw, &g); e != nil {
		t.Fatal(e)
	}
	return g
}
func TestFrozenProjectionRestoresRealScopesAndOnlyTwoRows(t *testing.T) {
	in, e := loadInputs(fixturePath())
	if e != nil {
		t.Fatal(e)
	}
	if len(in.Scopes) != 3 || in.InventorySHA == "" {
		t.Fatal("missing original scope/inventory proof")
	}
	count := 0
	for si, s := range in.Graph.Domains[0].Subdomains {
		if s.Slug != extract.Slugify(subdomainNames[si]) || s.Slug == "" {
			t.Fatal("derived production scope lost")
		}
		count += len(s.Relations)
	}
	if count != 2 || len(in.Graph.Domains[0].Subdomains[0].Relations) != 0 || in.Graph.Domains[0].Subdomains[1].Relations[0] != wanted[0] || in.Graph.Domains[0].Subdomains[2].Relations[0] != wanted[1] {
		t.Fatal("projection changed frozen candidate denominator")
	}
	// Re-marshalling would lose the json:"-" slugs: prove that this dangerous
	// historical-dump boundary is real, and restoration is mandatory.
	var decoded extract.Result
	_ = json.Unmarshal(encoded(in.Graph), &decoded)
	if decoded.Domains[0].Slug != "" || decoded.Domains[0].Subdomains[1].Slug != "" {
		t.Fatal("test no longer covers omitted derived identities")
	}
}

func TestActualProductionVerifierEnumeratesTwoCrossScopesOffline(t *testing.T) {
	in, err := loadInputs(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	c, ctx := guardAudit(t)
	c.next = &offlineClient{}
	report := extract.NewExtractor(c, 16384).ValidateCrossRelations(ctx, &in.Graph, in.Docs, in.Sources, extract.CrossRelationOptions{MaxEvidenceBytes: 65536})
	if report.Candidates != 2 || c.calls != 2 {
		t.Fatalf("lost actual cross-scope denominator: %+v / calls=%d", report, c.calls)
	}
	for _, decision := range report.Decisions {
		if decision.Disposition != "rejected_semantic" || decision.Source.Subdomain == "" || decision.Target.Subdomain == "" || decision.Source.Subdomain == decision.Target.Subdomain {
			t.Fatalf("frozen source/scopes never reached actual verifier: %+v", decision)
		}
	}
}
func TestProjectionRejectsChangedMovedDuplicateAndMissingOriginalRows(t *testing.T) {
	cases := map[string]func(*extract.Result){
		"changed": func(g *extract.Result) { g.Domains[0].Subdomains[1].Relations[2].Description = "modified candidate" },
		"moved": func(g *extract.Result) {
			g.Domains[0].Subdomains[0].Relations = append(g.Domains[0].Subdomains[0].Relations, wanted[0])
		},
		"duplicate": func(g *extract.Result) {
			g.Domains[0].Subdomains[1].Relations = append(g.Domains[0].Subdomains[1].Relations, wanted[0])
		},
		"missing":  func(g *extract.Result) { g.Domains[0].Subdomains[2].Relations = nil },
		"taxonomy": func(g *extract.Result) { g.Domains[0].Subdomains[1].Name = "regrouped" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			g := originalGraph(t)
			change(&g)
			if _, _, e := restoreAndProject(&g); e == nil {
				t.Fatal("unfrozen row/scope accepted")
			}
		})
	}
}
func TestInputByteMutationFailsBeforeAnyClientExists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "graph.json")
	raw, e := os.ReadFile(filepath.Join(fixturePath(), "dump/05_extract_result.json"))
	if e != nil {
		t.Fatal(e)
	}
	raw = append(raw, ' ')
	if e = os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = pin(p, graphSHA); e == nil {
		t.Fatal("changed frozen bytes accepted")
	}
}

type offlineClient struct {
	mu    sync.Mutex
	calls int
}

func (*offlineClient) ProviderName() string { return "offline" }
func (c *offlineClient) Complete(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return &llm.CompleteResponse{Text: `{"disposition":"rejected","reason":"offline only"}`, FinishReason: "stop", InputTokens: 1, OutputTokens: 1}, nil
}
func TestTwoUniqueLogicalCallsAreDurableAndNoThirdCallIsAdmitted(t *testing.T) {
	f, e := os.OpenFile(filepath.Join(t.TempDir(), "calls.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	next := &offlineClient{}
	c := &countedClient{next: next, file: f, cancel: cancel, used: map[string]bool{}}
	request := func(i int) llm.CompleteRequest {
		source := extract.CrossEndpoint{Domain: extract.Slugify(domainName), Subdomain: extract.Slugify(subdomainNames[[]int{1, 2}[i]]), Alias: wanted[i].Source}
		target := extract.CrossEndpoint{Domain: extract.Slugify(domainName), Subdomain: extract.Slugify(subdomainNames[[]int{0, 1}[i]]), Alias: wanted[i].Target}
		return llm.CompleteRequest{System: "offline", User: string(encoded(map[string]any{"source": source, "target": target, "relation_candidate": wanted[i]}))}
	}
	for i := 0; i < 2; i++ {
		if _, e = c.Complete(ctx, request(i)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = c.Complete(ctx, request(0)); e == nil {
		t.Fatal("third/duplicate logical call admitted")
	}
	if c.calls != 2 || next.calls != 2 || len(c.receipts) != 2 {
		t.Fatal("logical denominator changed")
	}
}
func TestInconclusiveOrFailureDoesNotCountAsSemanticNegativePass(t *testing.T) {
	makeReport := func(status, verdict string) *extract.CrossRelationReport {
		return &extract.CrossRelationReport{Candidates: 2, Accepted: 1, Decisions: []extract.CrossRelationDecision{{Relation: wanted[0], Disposition: "accepted", VerifierDisposition: "supported", Evidence: []extract.CrossEvidence{{Endpoint: "source"}, {Endpoint: "target"}}}, {Relation: wanted[1], Disposition: status, VerifierDisposition: verdict}}}
	}
	if !gate(makeReport("rejected_semantic", "rejected"), 2) {
		t.Fatal("explicit bounded positive/negative result not recognized")
	}
	for _, x := range [][2]string{{"insufficient_evidence", "insufficient_evidence"}, {"execution_error", ""}, {"invalid_verdict", ""}, {"rejected_semantic", "supported"}} {
		if gate(makeReport(x[0], x[1]), 2) {
			t.Fatal("inconclusive/error hidden as semantic pass")
		}
	}
	if gate(makeReport("rejected_semantic", "rejected"), 0) {
		t.Fatal("zero actual calls hidden as pass")
	}
}
