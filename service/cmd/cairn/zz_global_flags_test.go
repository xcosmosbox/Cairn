package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/storage"
)

// 原实现先取 args[0] 为命令，既拒绝文档中的前置全局标志，也漏掉空路径。
// Global flags must be removed before routing, and invalid values must fail early.
func TestGlobalDBPathParsing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		path string
		want []string
		bad  bool
	}{
		{"before", []string{"--db-path", "a.db", "status"}, "a.db", []string{"status"}, false},
		{"after", []string{"status", "--db-path", "a.db"}, "a.db", []string{"status"}, false},
		{"equals", []string{"--db-path=a.db", "find", "term"}, "a.db", []string{"find", "term"}, false},
		{"middle", []string{"find", "--db-path", "a.db", "term"}, "a.db", []string{"find", "term"}, false},
		{"last value", []string{"--db-path=a.db", "status", "--db-path", "b.db"}, "b.db", []string{"status"}, false},
		{"literal", []string{"find", "--", "--db-path=a.db"}, defaultDBPath, []string{"find", "--", "--db-path=a.db"}, false},
		{"missing", []string{"status", "--db-path"}, "", nil, true},
		{"flag as value", []string{"--db-path", "--help"}, "", nil, true},
		{"empty equals", []string{"status", "--db-path="}, "", nil, true},
		{"empty value", []string{"--db-path", "", "status"}, "", nil, true},
		{"blank value", []string{"--db-path= \t", "status"}, "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := append([]string(nil), tt.args...)
			path, err := extractDBPath(&args)
			if (err != nil) != tt.bad {
				t.Fatalf("path=%q err=%v, want bad=%v", path, err, tt.bad)
			}
			if !tt.bad && (path != tt.path || !reflect.DeepEqual(args, tt.want)) {
				t.Fatalf("path=%q args=%v, want path=%q args=%v", path, args, tt.path, tt.want)
			}
		})
	}
}

// 原实现打开数据库后才处理未知命令/子命令帮助；缺库会掩盖真正的诊断。
// Help and routing errors must be independent of database availability.
func TestHelpAndUnknownCommandWithoutDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, args := range [][]string{
		{"--db-path", path, "help", "find"},
		{"--db-path=" + path, "--help"},
		{"status", "--help", "--db-path", path},
	} {
		if err := Run(args); err != nil {
			t.Fatalf("Run(%v): %v", args, err)
		}
	}
	err := Run([]string{"--db-path", path, "no-such-command"})
	if err == nil || !strings.Contains(err.Error(), "no-such-command") || strings.Contains(err.Error(), "database") {
		t.Fatalf("expected routing error, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("help/routing created DB: %v", err)
	}
}

func legacyCLIContext(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Conn().Exec(`INSERT INTO nodes (id,label,name,summary,domain,subdomain) VALUES ('kept','Concept','kept','summary','d','s')`); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewManifestRepo(db).Set(context.Background(), "protected", "original"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	return path
}

// 原 CLI 使用 NewDB，使只读 status 也迁移旧库。查询需保持字节、mtime 与模式版本。
// Querying a legacy DB must not migrate it, including --record=false.
func TestCLIQueriesDoNotMigrateLegacyDatabase(t *testing.T) {
	t.Parallel()
	path := legacyCLIContext(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--db-path", path, "status"},
		{"status", "--db-path=" + path},
		{"sentinel", "--record=false", "--db-path", path},
	} {
		if err := Run(args); err != nil {
			t.Fatalf("Run(%v): %v", args, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("read-only CLI changed database bytes or mtime")
	}
	db, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if version, err := db.SchemaVersion(); err != nil || version != 4 {
		t.Fatalf("legacy schema migrated: version=%d err=%v", version, err)
	}
}

// 显式观测留存必须继续可用，但不能借机执行 KG 迁移或修改已有知识。
// Explicit --record writes only observation metadata and never migrates the KG.
func TestCLIExplicitSentinelRecording(t *testing.T) {
	t.Parallel()
	path := legacyCLIContext(t)
	if err := Run([]string{"--db-path", path, "sentinel", "--record"}); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	version, err := db.SchemaVersion()
	if err != nil || version != 4 {
		t.Fatalf("record migrated DB: version=%d err=%v", version, err)
	}
	manifest := storage.NewManifestRepo(db)
	if original, err := manifest.Get(context.Background(), "protected"); err != nil || original != "original" {
		t.Fatalf("existing metadata changed: %q %v", original, err)
	}
	if history, err := manifest.Get(context.Background(), "sentinel.history"); err != nil || !strings.Contains(history, `"ts"`) {
		t.Fatalf("observation missing: %q %v", history, err)
	}
	var nodes, manifestEntries int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM nodes WHERE id='kept' AND name='kept'").Scan(&nodes); err != nil || nodes != 1 {
		t.Fatalf("knowledge modified: nodes=%d err=%v", nodes, err)
	}
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM kg_manifest").Scan(&manifestEntries); err != nil || manifestEntries != 2 {
		t.Fatalf("unexpected metadata writes: count=%d err=%v", manifestEntries, err)
	}
}

func TestSentinelRecordingFlagMustBeParsed(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		args []string
		want bool
		bad  bool
	}{
		{nil, false, false},
		{[]string{"--record"}, true, false},
		{[]string{"--record=true"}, true, false},
		{[]string{"--record=false"}, false, false},
		{[]string{"--record=wrong"}, false, true},
		{[]string{"--", "--record"}, false, true},
	} {
		got, err := parseSentinelRecord(tt.args)
		if (err != nil) != tt.bad || got != tt.want {
			t.Errorf("args=%v got=%v err=%v, want=%v bad=%v", tt.args, got, err, tt.want, tt.bad)
		}
	}
}
