// Command fixture 只创建合成兼容性输入；不提取文档、不调用模型。
// It creates synthetic local recovery fixtures, never a product build result.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/core/storage"
)

func main() {
	if len(os.Args) != 3 {
		panic("usage: fixture create OUT | verify BUNDLE | break-contract BUNDLE")
	}
	var err error
	switch os.Args[1] {
	case "create":
		err = create(os.Args[2])
	case "verify":
		_, err = kbbundle.Verify(os.Args[2])
	case "break-contract":
		err = breakContract(os.Args[2])
	default:
		err = fmt.Errorf("unknown mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func create(out string) error {
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	for _, spec := range []struct {
		name, word string
		schema     int
	}{{"a", "LegacyAnchor", 4}, {"b", "ModernAnchor", 5}} {
		path := filepath.Join(out, spec.name+".db")
		db, err := storage.NewDB(storage.DBOptions{Path: path, JournalMode: "DELETE"})
		if err != nil {
			return err
		}
		ctx := context.Background()
		err = storage.NewNodeRepo(db).Insert(ctx, &dktypes.Node{ID: "recovery-" + spec.name, Label: dktypes.LabelConcept, Name: spec.word, Summary: "synthetic " + spec.word + " generation", Domain: "engineering", Subdomain: "recovery", Confidence: 1, Provenance: dktypes.ProvenanceHumanCurated, SourceRefs: "synthetic/reference.md"})
		if err == nil {
			err = storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: "recovery-" + spec.name, MemberID: "member-" + spec.name, Skill: "synthetic", FilePath: "synthetic/reference.md", StartLine: 3, EndLine: 5}})
		}
		if err == nil {
			err = storage.NewFTSIndex(db).RebuildAll(ctx)
		}
		if err == nil && spec.schema == 4 {
			_, err = db.Conn().Exec("ALTER TABLE nodes DROP COLUMN file_slug; PRAGMA user_version=4")
		}
		closeErr := db.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = kbbundle.Pack(kbbundle.PackRequest{KGDBPath: path, OutDir: filepath.Join(out, spec.name), Manifest: kbbundle.Manifest{KG: "recovery", SourceRepo: "synthetic/local-recovery", SourceRef: "fixture", SourceCommit: strings.Repeat(spec.name, 40), BuilderVersion: "synthetic-test", BuilderCommit: strings.Repeat("c", 40), SchemaVersion: spec.schema, PromptSetVersion: "none", Model: "none", ConfigDigest: kbbundle.DigestPrefix(kbbundle.DigestBytes([]byte("synthetic-recovery"))), KBVersion: spec.name, CreatedAt: "2026-10-24T00:00:00Z"}})
		if err != nil {
			return err
		}
	}
	return nil
}

// 合成攻击样本重新签摘要，确认拒绝来自查询契约而非 checksum。
// Recompute the documented v1 digest so the bad FTS contract is the only defect.
func breakContract(dir string) error {
	db, err := storage.OpenExisting(filepath.Join(dir, "knowledge.db"))
	if err != nil {
		return err
	}
	_, err = db.Conn().Exec("DROP TABLE nodes_fts")
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	data, err := os.ReadFile(filepath.Join(dir, "kb-manifest.json"))
	if err != nil {
		return err
	}
	var m kbbundle.Manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return err
	}
	m.BundleDigest = ""
	canonical, err := json.Marshal(m)
	if err != nil {
		return err
	}
	digest, err := kbbundle.DigestFile(filepath.Join(dir, "knowledge.db"))
	if err != nil {
		return err
	}
	h := sha256.New()
	fmt.Fprintf(h, "knowledge.db\n%s\n", digest)
	h.Write(canonical)
	h.Write([]byte("\n"))
	m.BundleDigest = kbbundle.DigestPrefix(hex.EncodeToString(h.Sum(nil)))
	data, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "kb-manifest.json"), data, 0644); err != nil {
		return err
	}
	checksums := fmt.Sprintf("%s  kb-manifest.json\n%s  knowledge.db\n", kbbundle.DigestBytes(data), digest)
	return os.WriteFile(filepath.Join(dir, "checksums.sha256"), []byte(checksums), 0644)
}
