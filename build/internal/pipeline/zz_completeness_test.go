package pipeline

import (
	"context"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFullIngestRejectsSilentlySkippedNodesBeforeReplacingDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "candidate.db")
	original := []byte("previous database bytes")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	// Ingest silently skips a domain without its slug, including eligible leaves.
	res := &extract.Result{Domains: []extract.Domain{{Subdomains: []extract.Subdomain{{Concepts: []extract.Node{{ID: "node", Name: "node", Confidence: 0.9}}}}}}}
	o := &Orchestrator{minConf: 0.8}
	if _, err := o.fullRebuildIngest(context.Background(), path, res, nil, nil, "local:fixture"); err == nil || !strings.Contains(err.Error(), "incomplete materialization") {
		t.Fatalf("silently skipped eligible node accepted: %v", err)
	}
	if actual, err := os.ReadFile(path); err != nil || string(actual) != string(original) {
		t.Fatalf("previous database replaced: %q %v", actual, err)
	}
}
