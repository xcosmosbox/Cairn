package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/storage"
)

func fixture(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frozen.db")
	db, err := storage.OpenSQLite(path, storage.SQLiteOptions{JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE TABLE nodes(id TEXT PRIMARY KEY,name TEXT,summary TEXT,synonyms TEXT,tags TEXT,description TEXT,domain TEXT,subdomain TEXT)",
		"CREATE VIRTUAL TABLE nodes_fts USING fts5(name,summary,synonyms,tags,description,domain,subdomain,prefix='2 3')",
		"CREATE TABLE node_sources(node TEXT,path TEXT,line INTEGER)",
		"CREATE TABLE edges(a TEXT,b TEXT,weight REAL,payload BLOB)",
		"CREATE TABLE nodes_fts_unrelated(k TEXT PRIMARY KEY,v BLOB) WITHOUT ROWID",
		"INSERT INTO nodes VALUES('a','训练服务数据层','alpha.beta',NULL,'[]','解释训练机制','域','子域')",
		"INSERT INTO nodes VALUES('b','kernel','alpha-beta','[\"甲\",\"乙\"]','','raw ASCII','domain','subdomain')",
		"INSERT INTO nodes_fts(rowid,name,summary,synonyms,tags,description,domain,subdomain) SELECT rowid,name,summary,synonyms,tags,description,domain,subdomain FROM nodes",
		"INSERT INTO node_sources VALUES('a','原文.md',7)",
		"INSERT INTO edges VALUES('a','b',0.25,x'00ff00')",
		"INSERT INTO nodes_fts_unrelated VALUES('must compare',x'0102')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	hash, _, err := shaFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, hash
}

func TestCopyProfilesPreserveSourceAndEveryNonFTSTable(t *testing.T) {
	for _, profile := range []string{"literal", "trigram", "han-v1"} {
		t.Run(profile, func(t *testing.T) {
			source, hash := fixture(t)
			original, _ := os.ReadFile(source)
			dir := t.TempDir()
			output, receipt := filepath.Join(dir, "copy.db"), filepath.Join(dir, "copy.json")
			m, err := copyIndex(source, output, receipt, profile, hash)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(source)
			if !bytes.Equal(original, after) || m.SourceDBBeforeSHA256 != m.SourceDBAfterSHA256 || !m.Unchanged || !reflect.DeepEqual(m.Before, m.After) {
				t.Fatal("source bytes or non-FTS logical identity changed")
			}
			if _, ok := m.Before["nodes_fts_unrelated"]; !ok {
				t.Fatal("prefix exclusion hid an unrelated non-FTS table")
			}
			if m.IndexRows != 2 || m.OutputDBSHA256 == hash || m.Format != "cairn-fts-index-copy/v1" {
				t.Fatalf("bad manifest: %+v", m)
			}
			db, err := openImmutable(output)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var mismatches int
			if err := db.QueryRow("SELECT count(*) FROM nodes_fts LEFT JOIN nodes ON nodes.rowid=nodes_fts.rowid WHERE nodes.id IS NULL").Scan(&mismatches); err != nil || mismatches != 0 {
				t.Fatalf("index rowids drifted: %d %v", mismatches, err)
			}
			query := `"训练"`
			want := 0
			if profile == "han-v1" {
				query, err = storage.CompileFTSPlainText("训练", storage.FTSTextHanV1)
				want = 1
			}
			var count int
			if err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH ?", query).Scan(&count); err != nil || count != want {
				t.Fatalf("profile %s query=%q hits=%d want=%d err=%v", profile, query, count, want, err)
			}
			if profile == "trigram" && !strings.Contains(m.IndexSchema, "tokenize='trigram'") {
				t.Fatal("wrong trigram schema")
			}
		})
	}
}

func TestCopyFailsClosedWithoutOverwriting(t *testing.T) {
	source, hash := fixture(t)
	dir := t.TempDir()
	output, receipt := filepath.Join(dir, "copy.db"), filepath.Join(dir, "copy.json")
	if _, err := copyIndex(source, output, receipt, "han-v1", strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted wrong source hash")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("wrong hash created output")
	}
	if err := os.WriteFile(receipt, []byte("existing receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyIndex(source, output, receipt, "han-v1", hash); err == nil {
		t.Fatal("overwrote existing receipt")
	}
	got, _ := os.ReadFile(receipt)
	if string(got) != "existing receipt" {
		t.Fatal("receipt bytes changed")
	}
	if _, err := copyIndex(source, source, filepath.Join(dir, "other.json"), "literal", hash); err == nil {
		t.Fatal("accepted source overwrite")
	}
	if err := os.WriteFile(source+"-wal", []byte("uncheckpointed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyIndex(source, output, filepath.Join(dir, "other.json"), "literal", hash); err == nil {
		t.Fatal("copied uncheckpointed DB")
	}
	alias := filepath.Join(t.TempDir(), "alias.db")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := copyIndex(alias, output, filepath.Join(dir, "alias.json"), "literal", hash); err == nil || !strings.Contains(err.Error(), "nonempty -wal") {
		t.Fatalf("symlink hid source WAL: %v", err)
	}
}

func TestTableIdentityDetectsValuesAndRowIDs(t *testing.T) {
	path, _ := fixture(t)
	db, err := storage.OpenSQLite(path, storage.SQLiteOptions{ExistingOnly: true, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, _, err := databaseIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"UPDATE edges SET payload=x'00ff01'", "UPDATE nodes SET rowid=99 WHERE id='a'"} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	after, _, err := databaseIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	if before["edges"].SHA256 == after["edges"].SHA256 || before["nodes"].SHA256 == after["nodes"].SHA256 {
		t.Fatal("logical hash ignored bytes or rowid")
	}
}
