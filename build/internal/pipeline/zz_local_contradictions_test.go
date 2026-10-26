package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/storage"
)

type localContradictionPipelineClient struct {
	base          crossPipelineClient
	compatible    bool
	verifications atomic.Int64
}

func (c *localContradictionPipelineClient) ProviderName() string {
	return "offline-local-contradiction-pipeline"
}
func (c *localContradictionPipelineClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	switch {
	case strings.Contains(req.System, "领域知识融合专家"):
		response, err := c.base.Complete(ctx, req)
		if err != nil {
			return nil, err
		}
		var result extract.Result
		if err := json.Unmarshal([]byte(response.Text), &result); err != nil {
			return nil, err
		}
		d := &result.Domains[0]
		d.Subdomains[0].Concepts = append(d.Subdomains[0].Concepts, d.Subdomains[1].Concepts...)
		d.Subdomains[0].Relations[0].Kind = "contradicts"
		d.Subdomains = d.Subdomains[:1]
		raw, err := json.Marshal(map[string]any{"domains": result.Domains})
		if err != nil {
			return nil, err
		}
		return &llm.CompleteResponse{Text: string(raw), FinishReason: "stop"}, nil
	case strings.Contains(req.System, "知识融合重写专家"):
		return &llm.CompleteResponse{Text: `{"descriptions":[{"node_id":"admission","description":"Admission source detail."},{"node_id":"quota","description":"Quota source detail."}]}`, FinishReason: "stop"}, nil
	case strings.Contains(req.System, "领域知识关系识别专家"):
		return &llm.CompleteResponse{Text: `{"relations":[]}`, FinishReason: "stop"}, nil
	case strings.Contains(req.System, "跨子域关系的原文验证专家"):
		c.verifications.Add(1)
		var input struct {
			Source, Target extract.CrossEndpoint
			Originals      []struct {
				ID, Path, SHA256, Content string
				Members                   []string
			} `json:"original_sources"`
		}
		if err := json.Unmarshal([]byte(req.User), &input); err != nil {
			return nil, err
		}
		if input.Source.Subdomain != input.Target.Subdomain || len(input.Originals) != 2 {
			return nil, fmt.Errorf("expected one local complete neighborhood")
		}
		var evidence []map[string]string
		for _, src := range input.Originals {
			original := c.base.originals[src.Path]
			if src.Content != original || src.SHA256 != extract.FreezeSource("system", src.Path, original).SHA256 {
				return nil, fmt.Errorf("local verifier used rewritten source")
			}
			role, member := "source", input.Source.Members[0]
			if strings.Contains(src.Path, "quota.md") {
				role, member = "target", input.Target.Members[0]
			}
			evidence = append(evidence, map[string]string{"endpoint": role, "member_id": member, "source_id": src.ID, "quote": strings.TrimSpace(original)})
		}
		verdict := map[string]any{"disposition": "supported", "reason": "Explicit source claims checked for the same subject, attribute, time and condition.", "relation_supported": true, "direction_supported": true, "contradiction": map[string]bool{"same_subject": true, "same_attribute": !c.compatible, "same_time": true, "same_conditions": true, "incompatible": !c.compatible}, "evidence": evidence}
		raw, err := json.Marshal(verdict)
		return &llm.CompleteResponse{Text: string(raw), FinishReason: "stop"}, err
	default:
		return c.base.Complete(ctx, req)
	}
}

// 原故障发生在正式调用链：本域候选直接入库；只测 helper 无法证明持久化安全。
// Exercise the real full pipeline, including validation, UUID mapping and SQLite ingest.
func TestFullPipelinePersistsOnlyVerifiedLocalContradictions(t *testing.T) {
	t.Parallel()
	for _, compatible := range []bool{false, true} {
		t.Run(fmt.Sprint(compatible), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: system\ndescription: engineering\n---\n# Skill\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "references"), 0o755); err != nil {
				t.Fatal(err)
			}
			originals := map[string]string{"references/admission.md": "In version 2 under condition X, admission is enabled.\n", "references/quota.md": "In version 2 under condition X, admission is disabled.\n"}
			if compatible {
				originals["references/admission.md"] = "Buying a refurbished Mac can save money.\n"
				originals["references/quota.md"] = "Avoid older Intel Macs when choosing a refurbished Mac.\n"
			}
			for path, source := range originals {
				if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			client := &localContradictionPipelineClient{base: crossPipelineClient{root: root, originals: originals}, compatible: compatible}
			dbPath := filepath.Join(t.TempDir(), "graph.db")
			o, err := NewOrchestrator(Options{Client: client, MaxRetries: 1, MaxRollbacks: 1, MinConfidence: .8, RepositoryIdentity: "local:local-contradictions", DumpDir: filepath.Join(t.TempDir(), "stages")})
			if err != nil {
				t.Fatal(err)
			}
			report, err := o.RunFullRebuild(context.Background(), root, dbPath)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if compatible {
				want = 0
			}
			if report.CrossRelations.ValidationPolicy != extract.CrossRelationPolicyV2 || report.CrossRelations.Candidates != 1 || report.CrossRelations.Accepted != want || client.verifications.Load() != 1 || report.CrossRelations.Decisions[0].Scope != extract.CrossRelationScopeLocal || report.CrossRelations.Decisions[0].Kind != "contradicts" {
				t.Fatalf("local validation not wired into full rebuild: %+v", report.CrossRelations)
			}
			db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM edges WHERE kind='contradicts'`).Scan(&count); err != nil || count != want {
				t.Fatalf("persisted contradiction count=%d want=%d: %v", count, want, err)
			}
			if compatible {
				db.Close()
				return
			}
			// 本变更不把既有人工边重新送入验证。此处只证明无变更增量兼容性，
			// 不代表新增/脏子域关系已接入原文验证。
			// No-op incremental preservation is compatibility evidence, not new-edge coverage.
			if _, err := db.Conn().Exec(`UPDATE edges SET provenance='human_curated', properties='{"keep":"human"}', source_refs='original-ref', bidirectional=1, cardinality='1:N' WHERE kind='contradicts'`); err != nil {
				t.Fatal(err)
			}
			before, err := storage.NewEdgeRepo(db).ListAll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			inc, err := incremental.NewIncrementalOrchestrator(incremental.Options{Client: client, RepositoryIdentity: "local:local-contradictions", MinConfidence: .8})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inc.Run(context.Background(), root, dbPath); err != nil {
				t.Fatal(err)
			}
			check, err := storage.OpenReadOnly(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer check.Close()
			after, err := storage.NewEdgeRepo(check).ListAll(context.Background())
			if err != nil || !reflect.DeepEqual(before, after) || client.verifications.Load() != 1 {
				t.Fatalf("existing curated edge changed or reverified: %v, before=%+v after=%+v", err, before, after)
			}
		})
	}
}
