package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const v016Admission = "Admission requires quota only when bypass is off; emergency admission is exempt."
const v016Quota = "Quota limits scheduled admission; emergency admission is exempt."

// 这些来源固定且只含构建输入；脚本核验是接线控制，不代表真实模型语义验收。
// Fixed source-only fixtures exercise ordinary entry wiring, not model quality.
type v016EntryClient struct {
	mu       sync.Mutex
	mode     string
	calls    map[string]int
	requests []llm.CompleteRequest
	cancel   context.CancelFunc
}

func (*v016EntryClient) ProviderName() string { return "offline-v016-entry-control" }
func v016Reply(v any) (*llm.CompleteResponse, error) {
	b, e := json.Marshal(v)
	return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, e
}
func (c *v016EntryClient) Complete(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, r)
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	switch {
	case strings.Contains(r.System, "知识结构化标注专家"):
		path := regexp.MustCompile(`path: ([^\n]+)`).FindStringSubmatch(r.User)
		if len(path) != 2 {
			return nil, fmt.Errorf("fixture annotation path missing")
		}
		key := "annotate:" + path[1]
		c.calls[key]++
		if c.mode == "metadata" {
			return nil, fmt.Errorf("openai_compatible: status 503: fixed metadata source failure")
		}
		if path[1] == "references/quota.md" {
			if c.mode == "exhaust" || c.mode == "recover" && c.calls[key] == 1 {
				return nil, fmt.Errorf("openai_compatible: status 503: fixed temporary failure")
			}
			if c.mode == "cancel" {
				c.cancel()
				return nil, ctx.Err()
			}
		}
		name, body, line := "Admission", v016Admission, 6
		if path[1] == "references/quota.md" {
			name, body, line = "Quota", v016Quota, 2
			if c.mode == "revise" && c.calls[key] == 1 {
				body = "Quota limits scheduled admission."
			}
			if c.mode == "revise" && c.calls[key] == 2 && (!strings.Contains(r.User, "emergency admission is exempt") || !strings.Contains(r.User, "complete candidate")) {
				return nil, fmt.Errorf("revision omitted original, whole candidate or concrete critique")
			}
		}
		return v016Reply(map[string]any{"items": []any{map[string]any{"tag": "concept", "name": name, "content": body, "detail": body, "confidence": .95, "source_span": map[string]any{"start_line": line, "end_line": line, "quote": body}}}})
	case strings.Contains(r.System, "语义核验专家"):
		if strings.Contains(r.User, "## 原始文档 / original document\n# Quota\n") {
			c.calls["assess:quota"]++
			if c.mode == "assess" && c.calls["assess:quota"] == 1 {
				return v016Reply(map[string]any{"status": "unknown", "reason": "fixed first assessment is unavailable"})
			}
			if c.mode == "revise" && c.calls["assess:quota"] == 1 {
				return v016Reply(map[string]any{"supported": true, "reason": "exception omitted", "unsupported_items": []int{}, "omissions": []string{"emergency admission is exempt"}})
			}
		}
		return v016Reply(map[string]any{"supported": true, "reason": "fixed control retains every source qualification", "unsupported_items": []int{}, "omissions": []string{}})
	case strings.Contains(r.System, "领域知识融合专家"):
		members := regexp.MustCompile(`(?m)^- id=([^\s]+)`).FindAllStringSubmatch(r.User, -1)
		nodes := []map[string]any{}
		for _, m := range members {
			name, body, id := "Admission", v016Admission, "admission"
			if strings.Contains(m[1], "quota-") {
				name, body, id = "Quota", v016Quota, "quota"
			}
			nodes = append(nodes, map[string]any{"id": id, "label": "Concept", "name": name, "summary": body, "confidence": .95, "members": []string{m[1]}})
		}
		if len(nodes) == 0 {
			return nil, fmt.Errorf("fixture refuses empty accepted member set")
		}
		return v016Reply(map[string]any{"domains": []any{map[string]any{"name": "Operations", "summary": "Admission rules", "reusability_rationale": "Reusable source conditions", "subdomains": []any{map[string]any{"name": "Admission", "summary": "Qualified rules", "concepts": nodes, "relations": []any{}}}}}})
	case strings.Contains(r.System, "知识融合重写专家"):
		alias, ok := pipelineFixtureNodeAlias(r.User)
		if !ok {
			return nil, fmt.Errorf("fixture node alias missing")
		}
		body := v016Admission
		if strings.Contains(r.User, `"name":"Quota"`) {
			body = v016Quota
		}
		return v016Reply(map[string]any{"descriptions": []any{map[string]string{"node_id": alias, "summary": "Qualified " + strings.Split(body, " ")[0], "description": body}}})
	case strings.Contains(r.System, "SOURCE-RELATION GENERATE"):
		var in struct {
			Nodes   []struct{ Alias, Name string }
			Sources []struct{ Content string } `json:"original_sources"`
		}
		if err := json.Unmarshal([]byte(r.User), &in); err != nil {
			return nil, err
		}
		a, b := "", ""
		for _, n := range in.Nodes {
			if n.Name == "Admission" {
				a = n.Alias
			}
			if n.Name == "Quota" {
				b = n.Alias
			}
		}
		rows := []any{}
		if a != "" && b != "" {
			rows = append(rows, map[string]any{"source": a, "target": b, "kind": "depends_on", "description": v016Admission, "confidence": .9})
		}
		return v016Reply(map[string]any{"relations": rows})
	case strings.Contains(r.System, "SOURCE-RELATION COVERAGE"):
		return v016Reply(map[string]any{"complete": true, "supported": true, "reason": "fixed source relation retained"})
	case strings.Contains(r.System, "原文验证专家"):
		var in struct {
			Source, Target struct{ Members []string }
			Sources        []struct {
				ID, Content string
				Members     []string
			} `json:"original_sources"`
		}
		if err := json.Unmarshal([]byte(r.User), &in); err != nil {
			return nil, err
		}
		evidence := []map[string]string{}
		for role, members := range map[string][]string{"source": in.Source.Members, "target": in.Target.Members} {
			for _, s := range in.Sources {
				found := false
				for _, m := range members {
					for _, sm := range s.Members {
						if m == sm {
							evidence = append(evidence, map[string]string{"endpoint": role, "member_id": m, "source_id": s.ID, "quote": s.Content})
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if found {
					break
				}
			}
		}
		return v016Reply(map[string]any{"disposition": "supported", "reason": "exact fixed originals establish the conditional dependency", "relation_supported": true, "direction_supported": true, "explicit_source_assertion": true, "evidence": evidence})
	case strings.Contains(r.System, "文件命名助手"):
		return v016Reply(map[string]string{"slug": "fixture-node"})
	default:
		return nil, fmt.Errorf("unexpected fixture stage: %s", r.System)
	}
}
func v016Root(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if e := os.MkdirAll(filepath.Join(root, "references"), 0755); e != nil {
		t.Fatal(e)
	}
	for p, b := range map[string]string{"SKILL.md": "---\nname: fixture\ndescription: Operations\n---\n# Admission\n" + v016Admission + "\n", "references/quota.md": "# Quota\n" + v016Quota + "\n"} {
		if e := os.WriteFile(filepath.Join(root, p), []byte(b), 0644); e != nil {
			t.Fatal(e)
		}
	}
	return root
}

// JSON is also the external report contract; absent status must fail on the old entry.
func v016Report(t *testing.T, r any) struct {
	Status    string
	Reasons   []string
	Documents []annotation.DocumentResult
	Relations *extract.RelationProcessReport
} {
	t.Helper()
	var v struct {
		Status    string
		Reasons   []string
		Documents []annotation.DocumentResult
		Relations *extract.RelationProcessReport
	}
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}

// 原故障：SKILL正文被漏扫，旧单次入口不恢复，关系/文件只带生成描述而没有来源断言。
func TestV016OrdinaryFullSourceGraphFileAndUnchangedRepeat(t *testing.T) {
	t.Parallel()
	root := v016Root(t)
	rawByPath := map[string]string{}
	for _, path := range []string{"SKILL.md", "references/quota.md"} {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		rawByPath[path] = string(raw)
	}
	client := &v016EntryClient{}
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	o, e := NewOrchestrator(Options{Client: client, MaxRetries: 1, RepositoryIdentity: "local:v016"})
	if e != nil {
		t.Fatal(e)
	}
	rpt, e := o.RunFullRebuild(context.Background(), root, dbPath)
	if e != nil {
		t.Fatal(e)
	}
	view := v016Report(t, rpt)
	if rpt.DocsTotal != 2 || rpt.DocsRewritten != 2 || view.Status != "complete" || len(view.Documents) != 2 {
		t.Fatalf("ordinary source denominator/status: report=%+v view=%+v", rpt, view)
	}
	for _, p := range []string{"SKILL.md", "references/quota.md"} {
		if client.calls["annotate:"+p] != 1 {
			t.Fatalf("source not consumed exactly once: %s calls=%v", p, client.calls)
		}
	}
	if view.Relations == nil || len(view.Relations.Selected) == 0 {
		t.Fatal("source-aware selected relation report missing")
	}
	if rpt.Ingest.NodeSourcesAttempted != 2 || rpt.Ingest.NodeSourcesSelectedDistinct != 2 || rpt.Ingest.NodeSourcesDuplicates != 0 || rpt.Ingest.NodeSourcesInserted != 2 {
		t.Fatalf("factual source counts: %+v", rpt.Ingest)
	}
	db, e := storage.OpenReadOnly(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	nodes, e := storage.NewNodeRepo(db).ListAll(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	edges, e := storage.NewEdgeRepo(db).ListAll(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	semantic := 0
	for _, edge := range edges {
		if edge.Kind != dktypes.KindDependsOn {
			continue
		}
		semantic++
		var envelope struct {
			Assertions []struct {
				Classification string
				Sources        []extract.CrossSource
				Evidence       []extract.CrossEvidence
			} `json:"cairn_relation_assertions_v1"`
		}
		if e := json.Unmarshal([]byte(edge.Description), &envelope); e != nil {
			t.Fatalf("selected assertion envelope absent: %s %v", edge.Description, e)
		}
		if len(envelope.Assertions) == 0 {
			t.Fatal("no assertions")
		}
		for _, a := range envelope.Assertions {
			if a.Classification != "explicit" || len(a.Sources) == 0 || len(a.Evidence) != 2 {
				t.Fatalf("source assertion credit lost: %+v", a)
			}
			originalsByID := map[string]string{}
			for _, source := range a.Sources {
				raw, ok := rawByPath[source.Path]
				if !ok || source.Skill != "fixture" || source.SHA256 != extract.FreezeSource(source.Skill, source.Path, raw).SHA256 {
					t.Fatalf("assertion source is not the actual original: %+v", source)
				}
				originalsByID[source.ID] = raw
			}
			for _, ev := range a.Evidence {
				raw, ok := originalsByID[ev.SourceID]
				if !ok || ev.Quote == "" || ev.StartByte < 0 || ev.EndByte <= ev.StartByte || ev.EndByte > len(raw) || raw[ev.StartByte:ev.EndByte] != ev.Quote {
					t.Fatalf("assertion evidence is not exact original bytes: %+v", ev)
				}
			}
		}
	}
	if semantic != 1 {
		t.Fatalf("actual semantic edge count=%d", semantic)
	}
	for _, n := range nodes {
		if n.Label != dktypes.LabelConcept {
			continue
		}
		body, path := v016Admission, "SKILL.md"
		if n.Name == "Quota" {
			body, path = v016Quota, "references/quota.md"
		}
		if n.Summary != "Qualified "+n.Name || n.Description != body {
			t.Fatalf("verified full pair not applied: %+v", n)
		}
		md, e := os.ReadFile(filepath.Join(root, path))
		if e != nil || !strings.Contains(string(md), n.Summary) || !strings.Contains(string(md), body) {
			t.Fatalf("Markdown pair differs from SQLite: %s %v", md, e)
		}
		sc, e := writeback.ReadSidecar(filepath.Join(root, path))
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, sn := range sc.Nodes {
			if sn.UUID != n.ID {
				continue
			}
			found = true
			g := sn.GraphMetadata
			if g == nil || !g.SourcesKnown || !g.RelationsKnown || len(g.Sources) != 1 {
				t.Fatalf("unknown/incomplete actual graph metadata: %+v", g)
			}
			var actual []dktypes.Edge
			for _, edge := range edges {
				if edge.SourceID == n.ID || edge.TargetID == n.ID {
					actual = append(actual, *edge)
				}
			}
			var snapshots []dktypes.Edge
			for _, snapshot := range g.Relations {
				snapshots = append(snapshots, snapshot.Edge())
			}
			if !reflect.DeepEqual(snapshots, actual) {
				t.Fatalf("file relation snapshot differs from actual persisted edges: got=%+v want=%+v", g.Relations, actual)
			}
		}
		if !found {
			t.Fatal("persisted source node absent from sidecar")
		}
	}
	db.Close()
	before := map[string]string{}
	for _, p := range []string{"SKILL.md", "SKILL.md.kg.yaml", "references/quota.md", "references/quota.md.kg.yaml"} {
		b, e := os.ReadFile(filepath.Join(root, p))
		if e != nil {
			t.Fatal(e)
		}
		before[p] = string(b)
	}
	count := len(client.requests)
	inc, e := incremental.NewIncrementalOrchestrator(incremental.Options{Client: client, RepositoryIdentity: "local:v016", MaxRetries: 1})
	if e != nil {
		t.Fatal(e)
	}
	repeat, e := inc.Run(context.Background(), root, dbPath)
	if e != nil {
		t.Fatal(e)
	}
	if v016Report(t, repeat).Status != "complete" || repeat.DocsChanged != 0 || len(client.requests) != count {
		t.Fatalf("unchanged ordinary repeat did work: %+v", repeat)
	}
	for p, b := range before {
		got, e := os.ReadFile(filepath.Join(root, p))
		if e != nil || string(got) != b {
			t.Fatalf("unchanged repeat rewrote %s: %v", p, e)
		}
	}
	// Full repeat reuses the same frozen source bytes and declared repository identity.
	// It is distinct from the unchanged zero-model incremental check above.
	repeatedRoot := v016Root(t)
	second, err := o.RunFullRebuild(context.Background(), repeatedRoot, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if v016Report(t, second).Status != "complete" || second.Ingest.NodeSourcesSelectedDistinct != 2 || second.Ingest.NodeSourcesInserted != 2 || second.Ingest.SemanticEdges != 1 {
		t.Fatalf("full repeated original changed source/edge counts: %+v", second)
	}
	replayDB, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer replayDB.Close()
	replayNodes, err := storage.NewNodeRepo(replayDB).ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identities := map[string]string{}
	for _, node := range nodes {
		if node.Label == dktypes.LabelConcept {
			identities[node.Name] = node.ID
		}
	}
	replayCount := 0
	for _, node := range replayNodes {
		if node.Label != dktypes.LabelConcept {
			continue
		}
		replayCount++
		if identities[node.Name] != node.ID {
			t.Fatalf("same original/member input changed UUID: %+v", node)
		}
	}
	if replayCount != 2 {
		t.Fatalf("full repeat duplicated/dropped source nodes: %d", replayCount)
	}
}

func TestV016OrdinaryFiniteRecoveryAndFailedSibling(t *testing.T) {
	for _, mode := range []string{"recover", "assess", "revise", "exhaust", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := v016Root(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &v016EntryClient{mode: mode, cancel: cancel}
			o, e := NewOrchestrator(Options{Client: c, MaxRetries: 1, RepositoryIdentity: "local:v016-failure"})
			if e != nil {
				t.Fatal(e)
			}
			rpt, runErr := o.RunFullRebuild(ctx, root, filepath.Join(t.TempDir(), "graph.db"))
			if rpt == nil {
				t.Fatalf("failed run lost factual report: %v", runErr)
			}
			v := v016Report(t, rpt)
			if mode == "recover" || mode == "assess" || mode == "revise" {
				if runErr != nil || v.Status != "complete" || rpt.DocsRewritten != 2 {
					t.Fatalf("first failure did not recover: report=%+v err=%v", rpt, runErr)
				}
			} else if v.Status == "complete" || v.Status == "" || len(v.Reasons) == 0 {
				t.Fatalf("failed scope was hidden: %+v err=%v", v, runErr)
			}
			if len(v.Documents) != 2 {
				t.Fatalf("denominator shrank: %+v", v.Documents)
			}
			found := false
			for _, d := range v.Documents {
				if d.FilePath != "references/quota.md" {
					continue
				}
				found = true
				if mode == "recover" || mode == "assess" || mode == "revise" {
					if d.Outcome != annotation.DocumentComplete || len(d.Attempts) != map[string]int{"recover": 3, "assess": 3, "revise": 4}[mode] || d.Exhausted {
						t.Fatalf("recovery evidence wrong: %+v", d)
					}
					if mode == "assess" && (d.Attempts[1].Step != "assess" || d.Attempts[2].Step != "assess" || d.Attempts[1].CandidateID != d.Attempts[2].CandidateID || c.calls["annotate:references/quota.md"] != 1) {
						t.Fatalf("unknown assessment regenerated/revised the successful candidate: %+v", d)
					}
					if mode == "revise" && (d.Attempts[2].Step != "revise" || d.Attempts[0].CandidateID == d.Attempts[2].CandidateID) {
						t.Fatalf("semantic omission never revised the whole candidate: %+v", d)
					}
				}
				if mode == "exhaust" {
					if d.Outcome != annotation.DocumentUnavailable || len(d.Attempts) != 2 || !d.Exhausted || d.Reason == "" {
						t.Fatalf("exhaustion evidence wrong: %+v", d)
					}
				}
				if mode == "cancel" {
					if d.Outcome != annotation.DocumentCancelled || d.Exhausted {
						t.Fatalf("cancellation fabricated exhaustion: %+v", d)
					}
				}
			}
			if !found {
				t.Fatal("failed document disappeared")
			}
			if mode != "cancel" && c.calls["annotate:SKILL.md"] != 1 {
				t.Fatal("successful sibling replayed")
			}
			if mode == "exhaust" {
				if v.Status != "partial" || rpt.DocsRewritten != 1 || rpt.Ingest == nil || rpt.Ingest.NodeSourcesInserted != 1 {
					t.Fatalf("independent successful source was lost: %+v", rpt)
				}
				if v.Relations != nil && len(v.Relations.Selected) != 0 {
					t.Fatal("failed endpoint acquired dependent edge")
				}
			}
		})
	}
}

// 非空元数据也属于完整支持来源，不能为满足旧零LLM fixture而静默过滤。
func TestV016MetadataOnlySkillIsAnOrdinarySource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	raw := "---\nname: fixture\ndescription: Metadata is retained source content\n---\n"
	if e := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(raw), 0644); e != nil {
		t.Fatal(e)
	}
	c := &v016EntryClient{mode: "metadata"}
	o, e := NewOrchestrator(Options{Client: c, RepositoryIdentity: "local:v016-metadata", MaxRetries: 1})
	if e != nil {
		t.Fatal(e)
	}
	rpt, runErr := o.RunFullRebuild(context.Background(), root, filepath.Join(t.TempDir(), "graph.db"))
	if runErr == nil || rpt == nil {
		t.Fatalf("supported metadata source disappeared: %+v %v", rpt, runErr)
	}
	v := v016Report(t, rpt)
	if rpt.DocsTotal != 1 || len(v.Documents) != 1 || v.Status != "failed" || v.Documents[0].FilePath != "SKILL.md" || v.Documents[0].Outcome != annotation.DocumentUnavailable || len(v.Documents[0].Attempts) != 2 || !v.Documents[0].Exhausted {
		t.Fatalf("metadata source denominator/failure was hidden: %+v / %+v", rpt, v)
	}
	if len(c.requests) != 2 {
		t.Fatalf("metadata was not sent exactly within finite allowance: %d", len(c.requests))
	}
	for _, r := range c.requests {
		if !strings.Contains(r.User, raw) {
			t.Fatal("metadata bytes were truncated before annotation")
		}
	}
}
