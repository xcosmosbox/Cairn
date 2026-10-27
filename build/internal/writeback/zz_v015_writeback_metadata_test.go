package writeback

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"gopkg.in/yaml.v3"
)

// Declared metadata must not silently lose required fields through reading or
// crash recovery; legacy omission remains compatible and cannot claim knowledge.
func TestV015WritebackMetadataPresenceAndLegacy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.md")
	u := pairNode()
	u.Sources = []SourceReference{{MemberID: "member", Skill: "source", FilePath: "doc.md", StartLine: 99, EndLine: 103, CoordinateSpace: "source_document"}}
	u.Relations = []*dktypes.Edge{{ID: 7, SourceID: u.UUID, TargetID: "other", Kind: dktypes.KindDependsOn, Description: `{"classification":"legacy_unverified","description":"unless stopped"}`, Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0}}
	md := RenderDoc([]NodeUpdate{u})
	if err := WriteDocContent(root, "doc.md", md, []NodeUpdate{u}, time.Now()); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path + ".kg.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sources", "sources_known", "relations", "relations_known", "version"} {
		t.Run(key, func(t *testing.T) {
			var data map[string]any
			if err := yaml.Unmarshal(original, &data); err != nil {
				t.Fatal(err)
			}
			nodes := data["nodes"].([]any)
			node := nodes[0].(map[string]any)
			graph := node["graph_metadata"].(map[string]any)
			delete(graph, key)
			bad, err := yaml.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".kg.yaml", bad, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadSidecar(path); err == nil {
				t.Fatalf("missing %s silently accepted", key)
			}
			journal, _ := json.Marshal(pairJournal{MD: []byte(md), Sidecar: bad, OldMD: []byte(md), OldSidecar: bad, HadMD: true, HadSidecar: true})
			if err := os.WriteFile(path+".kg-writeback.json", journal, 0600); err != nil {
				t.Fatal(err)
			}
			if err := RecoverDocPair(root, "doc.md"); err == nil {
				t.Fatalf("missing %s silently recovered", key)
			}
			if err := os.Remove(path + ".kg-writeback.json"); err != nil {
				t.Fatal(err)
			}
		})
	}
	var data map[string]any
	if err := yaml.Unmarshal(original, &data); err != nil {
		t.Fatal(err)
	}
	delete(data["nodes"].([]any)[0].(map[string]any), "graph_metadata")
	legacy, _ := yaml.Marshal(data)
	if err := os.WriteFile(path+".kg.yaml", legacy, 0600); err != nil {
		t.Fatal(err)
	}
	sc, err := ReadSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Nodes[0].GraphMetadata != nil {
		t.Fatal("legacy received invented graph metadata")
	}
	if !SidecarMetadataMatches(*sc, "doc.md", []NodeUpdate{u}) {
		t.Fatal("legacy optional omission broke metadata compatibility")
	}
	if strings.Contains(md, "legacy_unverified") {
		t.Fatal("relation metadata leaked into editable Markdown")
	}
}

// Metadata-only refresh must keep a human edit visible to the next diff rather
// than advancing doc_hash/summary_hash to absorb the edited content.
func TestV015WritebackRefreshPreservesHumanBaseline(t *testing.T) {
	root := t.TempDir()
	u := pairNode()
	u.Sources = []SourceReference{{MemberID: "member", FilePath: "old.md", CoordinateSpace: "source_document"}}
	md := RenderDoc([]NodeUpdate{u})
	if err := WriteDocContent(root, "doc.md", md, []NodeUpdate{u}, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSidecar(filepath.Join(root, "doc.md"))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(md, "\nsummary\n", "\nhuman summary\n", 1)
	if err := os.WriteFile(filepath.Join(root, "doc.md"), []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	u.Sources[0].FilePath = "moved.md"
	if err := RefreshGraphMetadataContext(context.Background(), root, "doc.md", []NodeUpdate{u}); err != nil {
		t.Fatal(err)
	}
	after, err := ReadSidecar(filepath.Join(root, "doc.md"))
	if err != nil {
		t.Fatal(err)
	}
	if after.DocHash != before.DocHash || after.Nodes[0].SummaryHash != before.Nodes[0].SummaryHash {
		t.Fatal("human baseline advanced")
	}
	bytes, _ := os.ReadFile(filepath.Join(root, "doc.md"))
	if string(bytes) != edited {
		t.Fatal("human edit overwritten")
	}
	if after.Nodes[0].GraphMetadata.Sources[0].FilePath != "moved.md" {
		t.Fatal("metadata not refreshed")
	}
}

func TestV015WritebackKnownEmptyDistinctFromUnknown(t *testing.T) {
	for _, known := range []bool{false, true} {
		u := pairNode()
		if known {
			u.Sources = []SourceReference{}
			u.Relations = []*dktypes.Edge{}
		}
		sf := buildSidecar("doc.md", RenderDoc([]NodeUpdate{u}), []nodeView{u.toNodeView()}, time.Now())
		g := sf.Nodes[0].GraphMetadata
		if g.SourcesKnown != known || g.RelationsKnown != known || len(g.Sources) != 0 || len(g.Relations) != 0 {
			t.Fatalf("nil/empty semantics lost: known=%t metadata=%+v", known, g)
		}
	}
}
