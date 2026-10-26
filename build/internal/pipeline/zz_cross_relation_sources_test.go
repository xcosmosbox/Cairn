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
	"sync/atomic"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

type crossPipelineClient struct {
	root       string
	originals  map[string]string
	crossCalls atomic.Int64
}

func (c *crossPipelineClient) ProviderName() string { return "offline-cross-pipeline" }
func (c *crossPipelineClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	var text string
	switch {
	case strings.Contains(req.System, "知识结构化标注专家"):
		name, body := "Admission", "Admission requires an available quota before accepting work."
		if strings.Contains(req.User, "references/quota.md") {
			name, body = "Quota", "Quota is the prerequisite for admission in version 2."
		}
		text = fmt.Sprintf(`{"items":[{"tag":"concept","name":%q,"content":%q,"detail":%q,"confidence":0.95}]}`, name, body, body+" Generated annotation detail.")
	case strings.Contains(req.System, "语义核验专家"):
		text = `{"idempotent":true,"reason":"fixture retains each complete prerequisite sentence; generated-detail label adds no domain claim","unsupported_items":[],"omissions":[]}`
	case strings.Contains(req.System, "领域知识融合专家"):
		matches := regexp.MustCompile(`(?m)^- id=([^\s]+)`).FindAllStringSubmatch(req.User, -1)
		members := map[string]string{}
		for _, m := range matches {
			if strings.HasSuffix(m[1], "-admission") {
				members["a"] = m[1]
			}
			if strings.HasSuffix(m[1], "-quota") {
				members["q"] = m[1]
			}
		}
		if len(members) != 2 {
			return nil, fmt.Errorf("expected both original members: %v", members)
		}
		text = fmt.Sprintf(`{"domains":[{"name":"工程","summary":"工程知识","reusability_rationale":"可复用","subdomains":[{"name":"接纳","summary":"接纳规则","concepts":[{"id":"admission","label":"Concept","name":"Admission","summary":"Admission rule","confidence":0.95,"members":[%q]}],"relations":[{"source":"admission","target":"quota","kind":"depends_on","description":"Admission requires quota","confidence":0.9}]},{"name":"容量","summary":"容量规则","concepts":[{"id":"quota","label":"Concept","name":"Quota","summary":"Quota rule","confidence":0.95,"members":[%q]}],"relations":[]}]}]}`, members["a"], members["q"])
	case strings.Contains(req.System, "知识融合重写专家"):
		id, rel := "admission", "references/admission.md"
		if strings.Contains(req.User, "node_id=quota") {
			id, rel = "quota", "references/quota.md"
		}
		// 模拟标注之后磁盘内容变化；验证仍必须使用实际标注输入，而不是再次读取磁盘。
		if err := os.WriteFile(filepath.Join(c.root, rel), []byte("Later disk version is not the annotation source.\n"), 0o644); err != nil {
			return nil, err
		}
		text = fmt.Sprintf(`{"descriptions":[{"node_id":%q,"description":"Generated fused description cannot establish a cross relation."}]}`, id)
	case strings.Contains(req.System, "跨子域关系的原文验证专家"):
		c.crossCalls.Add(1)
		var input struct {
			Source, Target extract.CrossEndpoint
			Originals      []struct {
				ID, Skill, Path, SHA256, Content string
				Members                          []string
			} `json:"original_sources"`
		}
		if err := json.Unmarshal([]byte(req.User), &input); err != nil {
			return nil, err
		}
		if len(input.Originals) != 2 {
			return nil, fmt.Errorf("missing original neighborhood: %s", req.User)
		}
		quotes := []map[string]string{}
		for _, original := range input.Originals {
			want := c.originals[original.Path]
			if original.Content != want || original.SHA256 != extract.FreezeSource("system", original.Path, want).SHA256 {
				return nil, fmt.Errorf("source changed before validation: %s", original.Path)
			}
			role, member, quote := "source", input.Source.Members[0], "Admission requires an available quota before accepting work."
			if strings.Contains(original.Path, "quota.md") {
				role, member, quote = "target", input.Target.Members[0], "Quota is the prerequisite for admission in version 2."
			}
			quotes = append(quotes, map[string]string{"endpoint": role, "member_id": member, "source_id": original.ID, "quote": quote})
		}
		verdict := map[string]any{"disposition": "supported", "reason": "Original prerequisites support admission depending on quota.", "relation_supported": true, "direction_supported": true, "evidence": quotes}
		data, _ := json.Marshal(verdict)
		text = string(data)
	case strings.Contains(req.System, "文件命名助手"):
		text = `{"slug":"node"}`
	default:
		return nil, fmt.Errorf("unexpected model stage: %s", req.System)
	}
	return &llm.CompleteResponse{Text: text, FinishReason: "stop"}, nil
}

// 跨边从实际冻结输入验证，随后 UUID/筛选/入库/回写全部走正式路径，不能靠生成 description 验真。
func TestFullPipelineFreezesOriginalSourceForCrossRelations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: system\ndescription: engineering\n---\n# Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	originals := map[string]string{"references/admission.md": "# Admission\nAdmission requires an available quota before accepting work.\n", "references/quota.md": "# Quota\nQuota is the prerequisite for admission in version 2.\n"}
	for path, content := range originals {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	client := &crossPipelineClient{root: root, originals: originals}
	dump := filepath.Join(t.TempDir(), "dump")
	o, err := NewOrchestrator(Options{Client: client, MaxRetries: 1, MaxRollbacks: 1, MinConfidence: .8, RepositoryIdentity: "local:cross-pipeline", DumpDir: dump, CrossRelationEvidenceBytes: 128 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	rpt, err := o.RunFullRebuild(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.DocsRewritten != 2 || rpt.Repair.DanglingFound != 0 || rpt.CrossRelations.Candidates != 1 || rpt.CrossRelations.Accepted != 1 || client.crossCalls.Load() != 1 || rpt.Ingest.SemanticEdges != 1 || rpt.Ingest.EdgesSkipped != 0 {
		t.Fatalf("incomplete production path: %+v cross=%+v ingest=%+v", rpt, rpt.CrossRelations, rpt.Ingest)
	}
	data, err := os.ReadFile(filepath.Join(dump, "00_frozen_sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []extract.FrozenSource
	if err := json.Unmarshal(data, &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("wrong original denominator: %+v", snapshots)
	}
	for _, snapshot := range snapshots {
		if snapshot.Content != originals[snapshot.Path] || snapshot.SHA256 != extract.FreezeSource(snapshot.Skill, snapshot.Path, snapshot.Content).SHA256 {
			t.Fatalf("not frozen original: %+v", snapshot)
		}
		written, err := os.ReadFile(filepath.Join(root, snapshot.Path))
		if err != nil {
			t.Fatal(err)
		}
		if string(written) == snapshot.Content || !strings.Contains(string(written), "Generated fused description") {
			t.Fatalf("writeback not exercised: %s", written)
		}
	}
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM edges WHERE source_id=? AND target_id=? AND kind='depends_on'`, extract.NodeUUID([]string{"system-concept-admission"}), extract.NodeUUID([]string{"system-concept-quota"})).Scan(&count); err != nil || count != 1 {
		t.Fatalf("UUID edge was not persisted: %d %v", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// 新建跨边随后经历正式增量 C1 人工编辑；既有边所有非 LLM 字段必须原样保存，UUID 钉死。
	// Exercise existing incremental protections on the newly materialized cross edge.
	mutable, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutable.Conn().Exec(`UPDATE edges SET properties='{"audit":"original"}', provenance='human_curated', source_refs='original-ref', bidirectional=1, cardinality='1:N' WHERE kind='depends_on'`); err != nil {
		t.Fatal(err)
	}
	before, err := storage.NewEdgeRepo(mutable).ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mutable.Close()
	inc, err := incremental.NewIncrementalOrchestrator(incremental.Options{Client: client, RepositoryIdentity: "local:cross-pipeline", MinConfidence: .8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inc.Run(context.Background(), root, dbPath); err != nil {
		t.Fatal(err)
	}
	admissionPath := filepath.Join(root, "references/admission.md")
	md, err := os.ReadFile(admissionPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(md), "Generated fused description cannot establish a cross relation.", "Human curated admission description.", 1)
	if changed == string(md) {
		t.Fatal("missing editable generated block")
	}
	if err := os.WriteFile(admissionPath, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	incrementalReport, err := inc.Run(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if incrementalReport.C1Edits != 1 {
		t.Fatalf("C1 edit did not exercise incremental: %+v", incrementalReport)
	}
	check, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	after, err := storage.NewEdgeRepo(check).ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var beforeCross, afterCross *dktypes.Edge
	for _, edge := range before {
		if edge.Kind == dktypes.KindDependsOn {
			beforeCross = edge
		}
	}
	for _, edge := range after {
		if edge.Kind == dktypes.KindDependsOn {
			afterCross = edge
		}
	}
	if beforeCross == nil || !reflect.DeepEqual(beforeCross, afterCross) {
		t.Fatalf("incremental changed protected cross edge metadata: before=%+v after=%+v", beforeCross, afterCross)
	}
	curated, err := storage.NewNodeRepo(check).GetByID(context.Background(), extract.NodeUUID([]string{"system-concept-admission"}))
	if err != nil || curated == nil || curated.Provenance != dktypes.ProvenanceHumanCurated || curated.Description != "Human curated admission description." {
		t.Fatalf("human content/identity not preserved: %+v %v", curated, err)
	}

}
