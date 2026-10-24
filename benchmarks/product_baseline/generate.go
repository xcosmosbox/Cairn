// 固定合成图通过公开存储/Bundle API生成；no extraction, model, or production writes.
package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/core/storage"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	out := flag.String("out", "", "new output directory")
	count := flag.Int("nodes", 1000, "bounded synthetic node count")
	seed := flag.Int("seed", 20261024, "identity seed")
	commit := flag.String("commit", "", "pinned product commit")
	flag.Parse()
	if *count < 20 || *count > 10000 || len(*commit) != 40 || *out == "" {
		panic("require 20..10000 nodes, commit and new out")
	}
	if _, err := os.Stat(*out); !os.IsNotExist(err) {
		panic("output must not exist")
	}
	must(os.MkdirAll(*out, 0755))
	ctx := context.Background()
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := func(i int) string {
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("cairn-perf:%d:%d:%d", *seed, *count, i))).String()
	}
	for _, generation := range []string{"a", "b"} {
		dir := filepath.Join(*out, generation)
		must(os.Mkdir(dir, 0755))
		db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(dir, "source.db")})
		must(err)
		nodes := make([]*dktypes.Node, 0, *count)
		sources := make([]storage.NodeSource, 0, *count)
		for i := 0; i < *count; i++ {
			name := fmt.Sprintf("entity%06d", i)
			summary := fmt.Sprintf("bucket%02d deterministic synthetic queue knowledge", i%100)
			if i == 0 {
				name = "marker"
				summary = "generation" + generation
			}
			nodes = append(nodes, &dktypes.Node{ID: id(i), Label: dktypes.LabelEntity, Name: name, Summary: summary, Description: "Fixed bounded degree synthetic fixture; no model output.", Domain: "engineering", Subdomain: "queues", Confidence: 1, Provenance: dktypes.ProvenanceExtraction, SourceRefs: "synthetic", CreatedAt: stamp, UpdatedAt: stamp})
			sources = append(sources, storage.NodeSource{NodeUUID: id(i), MemberID: fmt.Sprint(i), Skill: "synthetic", FilePath: "synthetic.txt", StartLine: i + 1, EndLine: i + 1})
		}
		must(storage.NewNodeRepo(db).InsertBatch(ctx, nodes))
		must(storage.NewNodeSourceRepo(db).InsertBatch(ctx, sources))
		edges := make([]*dktypes.Edge, 0, *count*3)
		for i := 0; i < *count; i++ {
			for _, delta := range []int{1, 7, 17} {
				edges = append(edges, &dktypes.Edge{SourceID: id(i), TargetID: id((i + delta) % *count), Kind: dktypes.KindReferences, Description: "synthetic fixed degree", Provenance: dktypes.ProvenanceExtraction, Confidence: 1, SourceRefs: "synthetic", CreatedAt: stamp})
			}
		}
		must(storage.NewEdgeRepo(db).InsertBatch(ctx, edges))
		must(storage.NewFTSIndex(db).RebuildAll(ctx))
		version := stamp.Format(time.RFC3339)
		if generation == "b" {
			version = stamp.Add(time.Second).Format(time.RFC3339)
		}
		// 合成 fixture 使用固定版本时间；the public connection is used only for fixture metadata.
		_, err = db.Conn().Exec("INSERT INTO kb_version(version,updated_at) VALUES(?,?)", version, version)
		must(err)
		must(storage.ValidateQueryContract(ctx, db))
		must(db.Close())
		sum := sha1.Sum([]byte(fmt.Sprintf("synthetic:%d:%d:%s", *seed, *count, generation)))
		bundle, err := kbbundle.Pack(kbbundle.PackRequest{KGDBPath: filepath.Join(dir, "source.db"), OutDir: filepath.Join(dir, "bundle"), Manifest: kbbundle.Manifest{KG: "bench", SourceRepo: "synthetic/product-baseline", SourceRef: "generated", SourceCommit: hex.EncodeToString(sum[:]), BuilderVersion: "product-baseline/v1", BuilderCommit: *commit, SchemaVersion: 5, PromptSetVersion: "no-model", Model: "none", ConfigDigest: kbbundle.DigestPrefix(kbbundle.DigestBytes([]byte(fmt.Sprintf("seed=%d;nodes=%d;degree=3", *seed, *count)))), KBVersion: version, CreatedAt: version}})
		must(err)
		must(kbbundle.PackTarball(bundle.Dir, filepath.Join(dir, "bundle.tar.gz")))
		data, err := json.MarshalIndent(kbbundle.CatalogManifest{Manifest: bundle.Manifest, Channel: "stable", PublishedAt: version, ReleaseTag: "generation-" + generation, AssetName: "bundle.tar.gz"}, "", "  ")
		must(err)
		must(os.WriteFile(filepath.Join(dir, "catalog.json"), append(data, '\n'), 0644))
	}
	metadata, err := json.MarshalIndent(map[string]interface{}{"seed": *seed, "nodes": *count, "edges": *count * 3, "degree": 3, "generations": []string{"a", "b"}, "product_commit": *commit, "generation_method": "public storage batch APIs; fixed timestamps and deterministic UUIDs; no model"}, "", "  ")
	must(err)
	must(os.WriteFile(filepath.Join(*out, "fixture.json"), append(metadata, '\n'), 0644))
	fmt.Printf("generated seed=%d nodes=%d edges=%d generations=a,b\n", *seed, *count, *count*3)
}
