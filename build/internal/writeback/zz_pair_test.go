package writeback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pairNode() NodeUpdate {
	return NodeUpdate{UUID: "node-test", Tag: "Concept", Name: "concept", Domain: "domain", Subdomain: "subdomain", DomainSlug: "domain", SubdomainSlug: "subdomain", Members: []string{"member"}, Summary: "summary", Description: "description", Provenance: "llm_inferred"}
}

// 原来 MD 成功而 sidecar 失败时原文已经被覆盖；必须先准备/验证整对文件。
func TestPairWriteFailurePreservesMarkdown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "references", "doc.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original prose"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".kg.yaml", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteDocContent(root, "references/doc.md", RenderDoc([]NodeUpdate{pairNode()}), []NodeUpdate{pairNode()}, time.Now()); err == nil {
		t.Fatal("expected sidecar target failure")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original prose" {
		t.Fatalf("original overwritten: %q", data)
	}
}

func TestPairJournalRecoversCrashAndPreservesLaterEdit(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish_pair", true: "preserve_new_edit"}[edited], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "doc.md")
			node := pairNode()
			if err := WriteDocContent(root, "doc.md", RenderDoc([]NodeUpdate{node}), []NodeUpdate{node}, time.Now()); err != nil {
				t.Fatal(err)
			}
			oldMD, _ := os.ReadFile(path)
			oldSC, _ := os.ReadFile(path + ".kg.yaml")
			node.Summary = "new summary"
			md := RenderDoc([]NodeUpdate{node})
			sf := buildSidecar("doc.md", md, []nodeView{node.toNodeView()}, time.Now())
			sc, err := encodeSidecar(sf)
			if err != nil {
				t.Fatal(err)
			}
			j := pairJournal{MD: []byte(md), Sidecar: sc, OldMD: oldMD, OldSidecar: oldSC, HadMD: true, HadSidecar: true}
			data, _ := json.Marshal(j)
			if err := atomicWriteFile(path+".kg-writeback.json", data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := atomicWriteFile(path, []byte(md), 0o644); err != nil {
				t.Fatal(err)
			} // crash between the two renames
			if edited {
				os.WriteFile(path, []byte("later human edit"), 0o644)
			}
			err = RecoverDocPair(root, "doc.md")
			var actual *sidecarFile
			if err == nil {
				actual, err = ReadSidecar(path)
			}
			if edited {
				if err == nil {
					t.Fatal("overwrote later edit")
				}
				current, _ := os.ReadFile(path)
				if string(current) != "later human edit" {
					t.Fatal("human edit lost")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if actual.DocHash != HashEditable(md) {
				t.Fatal("pair not recovered")
			}
			if _, err := os.Stat(path + ".kg-writeback.json"); !os.IsNotExist(err) {
				t.Fatal("journal not cleared")
			}
		})
	}
}

func TestWritesAndDeletesRejectSymlinkParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "references")); err != nil {
		t.Fatal(err)
	}
	original := []byte("outside human prose")
	path := filepath.Join(outside, "doc.md")
	os.WriteFile(path, original, 0o644)
	os.WriteFile(path+".kg.yaml", []byte("outside sidecar"), 0o644)
	if err := RewriteDoc(root, "references/doc.md", []NodeUpdate{pairNode()}, time.Now()); err == nil {
		t.Fatal("write traversed symlink")
	}
	if err := DeleteSidecar(root, "references/doc.md"); err == nil {
		t.Fatal("delete traversed symlink")
	}
	data, _ := os.ReadFile(path)
	sc, _ := os.ReadFile(path + ".kg.yaml")
	if string(data) != string(original) || string(sc) != "outside sidecar" {
		t.Fatal("outside files changed")
	}
}

func TestReadSidecarPendingJournalIsPure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.md")
	node := pairNode()
	md := RenderDoc([]NodeUpdate{node})
	if err := WriteDocContent(root, "doc.md", md, []NodeUpdate{node}, time.Now()); err != nil {
		t.Fatal(err)
	}
	beforeMD, _ := os.ReadFile(path)
	beforeSC, _ := os.ReadFile(path + ".kg.yaml")
	node.Summary = "pending next version"
	next := RenderDoc([]NodeUpdate{node})
	sc, err := encodeSidecar(buildSidecar("doc.md", next, []nodeView{node.toNodeView()}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(pairJournal{MD: []byte(next), Sidecar: sc, OldMD: beforeMD, OldSidecar: beforeSC, HadMD: true, HadSidecar: true})
	if err := os.WriteFile(path+".kg-writeback.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	beforeInfo, _ := os.Stat(path)
	if _, err := ReadSidecar(path); err == nil {
		t.Fatal("pending journal was accepted")
	}
	afterMD, _ := os.ReadFile(path)
	afterSC, _ := os.ReadFile(path + ".kg.yaml")
	afterInfo, _ := os.Stat(path)
	journal, err := os.ReadFile(path + ".kg-writeback.json")
	if err != nil || string(afterMD) != string(beforeMD) || string(afterSC) != string(beforeSC) || string(journal) != string(data) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("read recovered or modified pending pair")
	}
}

func TestSidecarOnlyFileSlugDriftIsStale(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	node := pairNode()
	node.FileSlug = "canonical-name"
	md := RenderDoc([]NodeUpdate{node})
	if err := WriteDocContent(root, "doc.md", md, []NodeUpdate{node}, time.Now()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "doc.md")
	sf, err := ReadSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	if sf.Nodes[0].FileSlug != node.FileSlug {
		t.Fatal("writer omitted authoritative file slug")
	}
	sf.Nodes[0].FileSlug = "wrong-name"
	if err := WriteSidecar(path, *sf); err != nil {
		t.Fatal(err)
	}
	stale := SidecarStale(root, "doc.md", md, []NodeUpdate{node})
	if !stale {
		t.Fatal("sidecar-only filename tampering suppressed repair")
	}
}
