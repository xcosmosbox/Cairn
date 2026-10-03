package kbbundle

import (
	"context"
	"encoding/json"
	"github.com/xcosmosbox/cairn/core/storage"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 原先直接复制主库会遗漏尚未 checkpoint 的已提交 WAL 行。
func TestPackIncludesUncheckpointedWALAndActualSchema(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "live.db")
	db, err := storage.NewDB(storage.DBOptions{Path: src})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Conn().Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("INSERT INTO kg_manifest(key,value) VALUES('wal-probe','committed')"); err != nil {
		t.Fatal(err)
	}
	before, err := DigestFile(src)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Pack(PackRequest{KGDBPath: src, OutDir: filepath.Join(root, "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := DigestFile(src)
	if before != after {
		t.Fatal("pack modified source database")
	}
	if bundle.Manifest.SchemaVersion != storage.LatestSchemaVersion() {
		t.Fatalf("schema=%d", bundle.Manifest.SchemaVersion)
	}
	if _, err := Verify(bundle.Dir); err != nil {
		t.Fatal(err)
	}
	copy, err := storage.OpenReadOnly(filepath.Join(bundle.Dir, "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var value string
	if err := copy.Conn().QueryRowContext(context.Background(), "SELECT value FROM kg_manifest WHERE key='wal-probe'").Scan(&value); err != nil || value != "committed" {
		t.Fatalf("missing WAL commit: %q %v", value, err)
	}
	if _, err := Pack(PackRequest{KGDBPath: src, OutDir: filepath.Join(root, "bad"), Manifest: Manifest{SchemaVersion: 4}}); err == nil {
		t.Fatal("accepted incorrect advertised schema")
	}
}

func TestRelativeInstallResolvesGenerationAndInvalidContractPreservesCurrent(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db, err := storage.NewDB(storage.DBOptions{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	good, err := Pack(PackRequest{KGDBPath: source, OutDir: filepath.Join(root, "good")})
	if err != nil {
		t.Fatal(err)
	}
	// Relative path retains correct absolute symlink targets without changing process cwd.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, filepath.Join(root, "install"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := Install(good.Dir, relative)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(relative, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved != installed.Current || !filepath.IsAbs(installed.Current) {
		t.Fatalf("invalid generation target: %s %s", resolved, installed.Current)
	}
	db, err = storage.OpenExisting(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("DROP TABLE nodes_fts"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Pack(PackRequest{KGDBPath: source, OutDir: filepath.Join(root, "bad")}); err == nil {
		t.Fatal("accepted missing FTS with valid schema number")
	}
	after, err := filepath.EvalSymlinks(filepath.Join(relative, "current"))
	if err != nil || after != resolved {
		t.Fatalf("healthy current changed: %s %v", after, err)
	}
}

func TestInstallReusesImmutableGenerationWhileReaderPinsRollback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db, err := storage.NewDB(storage.DBOptions{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("INSERT INTO kg_manifest(key,value) VALUES('reader-probe','generation-A')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := Pack(PackRequest{KGDBPath: source, OutDir: filepath.Join(root, "a")})
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(root, "install")
	first, err := Install(a.Dir, install)
	if err != nil {
		t.Fatal(err)
	}
	pinned := filepath.Join(first.Current, "knowledge.db")
	info, _ := os.Stat(pinned)
	reader, err := storage.OpenReadOnly(pinned)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	db, err = storage.OpenExisting(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("UPDATE kg_manifest SET value='generation-B' WHERE key='reader-probe'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	b, err := Pack(PackRequest{KGDBPath: source, OutDir: filepath.Join(root, "b")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(b.Dir, install); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := Install(a.Dir, install); err != nil {
			t.Fatal(err)
		}
		var value string
		if err := reader.Conn().QueryRow("SELECT value FROM kg_manifest WHERE key='reader-probe'").Scan(&value); err != nil || value != "generation-A" {
			t.Fatalf("pinned reader corrupted: %s %v", value, err)
		}
	}
	after, _ := os.Stat(pinned)
	if !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("existing generation was rewritten")
	}
}

func rehashBundle(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "kb-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m.BundleDigest = ""
	canonical, _ := json.Marshal(m)
	digest, err := DigestFile(filepath.Join(dir, "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"knowledge.db": digest}
	aggregate, err := aggregateDigestWithManifest(files, canonical)
	if err != nil {
		t.Fatal(err)
	}
	m.BundleDigest = DigestPrefix(aggregate)
	data, _ = json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "kb-manifest.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	files["kb-manifest.json"] = DigestBytes(data)
	if err := writeChecksums(dir, files); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRejectsChecksumValidBrokenQueryContractWithoutChangingCurrent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db, err := storage.NewDB(storage.DBOptions{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	good, err := Pack(PackRequest{KGDBPath: source, OutDir: filepath.Join(root, "good")})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := Install(good.Dir, filepath.Join(root, "install"))
	if err != nil {
		t.Fatal(err)
	}
	bad, err := Pack(PackRequest{KGDBPath: source, OutDir: filepath.Join(root, "bad")})
	if err != nil {
		t.Fatal(err)
	}
	db, err = storage.OpenExisting(filepath.Join(bad.Dir, "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("DROP TABLE nodes_fts"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	rehashBundle(t, bad.Dir)
	if _, err := Install(bad.Dir, installed.InstallDir); err == nil {
		t.Fatal("correct checksums hid broken query contract")
	}
	actual, err := filepath.EvalSymlinks(filepath.Join(installed.InstallDir, "current"))
	if err != nil || actual != installed.Current {
		t.Fatal("healthy current changed")
	}
	// knowledge.db must participate in digest: omitting it cannot prove any artifact identity.
	data, err := os.ReadFile(filepath.Join(good.Dir, "checksums.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasSuffix(line, "  knowledge.db") {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(filepath.Join(good.Dir, "checksums.sha256"), []byte(strings.Join(kept, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(good.Dir); err == nil {
		t.Fatal("DB omitted from checksum coverage")
	}
}

func TestConcurrentInstallPublishesOneImmutableGeneration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db, err := storage.NewDB(storage.DBOptions{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	bundle, err := Pack(PackRequest{KGDBPath: source, OutDir: filepath.Join(root, "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(root, "install")
	start := make(chan struct{})
	failures := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := Install(bundle.Dir, install); failures <- err }()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, err := filepath.EvalSymlinks(filepath.Join(install, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := Verify(current); err != nil || *actual != bundle.Manifest {
		t.Fatalf("concurrent generation differs: %v", err)
	}
	entries, err := os.ReadDir(install)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("staging leftovers: %d", len(entries))
	}
}
