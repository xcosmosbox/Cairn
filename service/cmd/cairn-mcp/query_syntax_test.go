package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPQuerySyntaxSingleAndFederated(t *testing.T) {
	state := lifecycleState(t, lifecycleDB(t, filepath.Join(t.TempDir(), "query.db"), 1))
	for _, kg := range []string{"", "smoke"} {
		for _, tc := range []struct {
			syntax, query    string
			hasHit, hasError bool
		}{
			{"text", "aggregate0 OR absent", false, false},
			{"fts5", "aggregate0 OR absent", true, false},
			{"fts5", "aggregate*", true, false},
			{"text", `"`, false, false},
			{"fts5", `"`, false, true},
			{"invalid", "aggregate0", false, true},
		} {
			res, err := toolDomainSearch(state, map[string]interface{}{"keyword": tc.query, "query_syntax": tc.syntax, "kg": kg})
			if (err != "") != tc.hasError {
				t.Fatalf("kg=%s query=%q syntax=%s error=%s", kg, tc.query, tc.syntax, err)
			}
			if !tc.hasError {
				matched := strings.Contains(res.Content[0].Text, "- ID: 0")
				if matched != tc.hasHit {
					t.Fatalf("wrong search semantics: %s", res.Content[0].Text)
				}
			}
		}
	}
	if _, err := toolDomainSearch(state, map[string]interface{}{"keyword": "aggregate0", "query_syntax": 1}); err == "" {
		t.Fatal("non-string syntax accepted")
	}
}
