package storage

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

func TestCompileFTSPlainText(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"net.ops-worker", `"net.ops-worker"`},
		{"  alpha\tOR\nNOT NEAR  ", `"alpha" AND "OR" AND "NOT" AND "NEAR"`},
		{`name:"value" (heap) foo* C++ path/to/file`, `"name:""value""" AND "(heap)" AND "foo*" AND "C++" AND "path/to/file"`},
		{`"`, `""""`},
	} {
		got, err := CompileFTSPlainText(tc.query, FTSTextLiteral)
		if err != nil || got != tc.want {
			t.Errorf("compile %q: got %q, %v; want %q", tc.query, got, err, tc.want)
		}
	}
	for _, profile := range []FTSTextProfile{"", "unknown"} {
		if _, err := CompileFTSPlainText("alpha", profile); err == nil {
			t.Fatalf("invalid profile %q accepted", profile)
		}
	}
	if _, err := CompileFTSPlainText(" \t\n", FTSTextLiteral); err == nil {
		t.Fatal("blank query accepted")
	}
	for _, query := range []string{"alpha\x00OR beta", "alpha\xffbeta"} {
		for _, profile := range []FTSTextProfile{FTSTextLiteral, FTSTextHanV1} {
			if _, err := CompileFTSPlainText(query, profile); err == nil {
				t.Fatalf("unsafe query bytes accepted for %s: %q", profile, query)
			}
		}
	}
	got, err := CompileFTSPlainText("权限 worker", FTSTextHanV1)
	if err != nil || got != `" 权  限 " AND "worker"` {
		t.Fatalf("Han phrase split into unordered character terms: %q, %v", got, err)
	}
	for _, text := range []string{"ASCII_mixed-123", "𠀀汉〇々", "中\ufe00文"} {
		normalized, err := NormalizeFTSText(FTSTextHanV1, text)
		if err != nil || strings.ReplaceAll(normalized, " ", "") != text {
			t.Fatalf("normalization lost or reordered runes: %q -> %q, %v", text, normalized, err)
		}
		if !strings.ContainsAny(text, "𠀀汉〇々中\ufe00文") && normalized != text {
			t.Fatal("ASCII bytes changed")
		}
	}
}

func TestLiteralFTSSafetyAndAdvancedCompatibility(t *testing.T) {
	ctx := context.Background()
	db, err := NewDB(DBOptions{Path: filepath.Join(t.TempDir(), "search.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	nodes := []*dktypes.Node{
		{ID: "a", Name: "net.ops-worker", Summary: "alpha AND OR NOT NEAR", Description: `quota:"strict" C++ (heap) path/to/file`, Domain: "a", Label: dktypes.LabelEntity},
		{ID: "b", Name: "alpha-beta", Summary: "alphabet", Domain: "b", Label: dktypes.LabelConcept},
		{ID: "c", Name: "alpha", Summary: "beta", Domain: "a", Label: dktypes.LabelConcept},
	}
	for _, n := range nodes {
		n.Provenance, n.Confidence = dktypes.ProvenanceExtraction, 1
		if err := NewNodeRepo(db).Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	idx := NewFTSIndex(db)
	if err := idx.RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"net.ops-worker", "alpha AND", "OR", "NOT", "NEAR", `quota:"strict"`, "C++", "(heap)", "path/to/file"} {
		match, err := CompileFTSPlainText(query, FTSTextLiteral)
		if err != nil {
			t.Fatal(err)
		}
		hits, err := idx.Search(ctx, match, nil, "", 10)
		if err != nil || len(hits) != 1 || hits[0].Node.ID != "a" {
			t.Errorf("literal %q: got %+v, %v", query, hits, err)
		}
	}
	for _, query := range []string{`"`, "-", "absent OR alpha"} {
		match, _ := CompileFTSPlainText(query, FTSTextLiteral)
		hits, err := idx.Search(ctx, match, nil, "", 10)
		if err != nil || len(hits) != 0 {
			t.Errorf("literal %q should be empty: %+v %v", query, hits, err)
		}
	}
	for _, query := range []string{"alpha OR beta", "alph*"} {
		hits, err := idx.Search(ctx, query, nil, "", 10)
		if err != nil || len(hits) != 3 {
			t.Errorf("advanced %q changed: %d %v", query, len(hits), err)
		}
	}
	if _, err := idx.Search(ctx, `"`, nil, "", 10); err == nil {
		t.Fatal("invalid advanced MATCH succeeded")
	}
	hits, err := idx.Search(ctx, "alpha", []string{"a"}, dktypes.LabelConcept, 10)
	if err != nil || len(hits) != 1 || hits[0].Node.ID != "c" {
		t.Fatalf("scope/label changed: %+v %v", hits, err)
	}
	hits, err = idx.Search(ctx, "alpha", nil, "", 1)
	if err != nil || len(hits) != 1 {
		t.Fatalf("limit changed: %+v %v", hits, err)
	}
}

func TestHanPhraseSemanticsIncludingFalseAdjacency(t *testing.T) {
	db, err := NewDB(DBOptions{Path: filepath.Join(t.TempDir(), "han.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Conn().Exec(`CREATE VIRTUAL TABLE probe USING fts5(body, other)`)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"权限控制", "控制权限", "限权", "权-限", `["并发","控制"]`, "A中文B", "ab中文cd", "数据数据", "𠀀汉字"}
	for i, text := range texts {
		normalized, _ := NormalizeFTSText(FTSTextHanV1, text)
		if _, err := db.Conn().Exec(`INSERT INTO probe(rowid,body,other) VALUES(?,?,?)`, i+1, normalized, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Positions cannot span FTS columns, even though they can span punctuation
	// or JSON element boundaries within one column. Those are explicit limits.
	if _, err := db.Conn().Exec(`INSERT INTO probe(rowid,body,other) VALUES(10,'权','限')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{"权限", []int{1, 2, 4}}, {"限权", []int{3}},
		{"权限 控制", []int{1, 2}}, {"控制权限", []int{2}},
		{"发控", []int{5}}, // Known false adjacency across serialized synonyms.
		{"A中", []int{6}}, {"b中", nil}, {"文B", []int{6}},
		{"数据数据", []int{8}}, {"𠀀汉", []int{9}},
	} {
		match, err := CompileFTSPlainText(tc.query, FTSTextHanV1)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.Conn().Query(`SELECT rowid FROM probe WHERE probe MATCH ? ORDER BY rowid`, match)
		if err != nil {
			t.Fatal(err)
		}
		var got []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got = append(got, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("query %q got %v want %v", tc.query, got, tc.want)
		}
	}
}
