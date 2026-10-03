package incremental

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/storage"
	"gopkg.in/yaml.v3"
)

// ReadSidecar previously replayed a pending pair journal during read-only legacy
// ownership attestation. A later UUID/member rejection had already changed MD.
func TestRejectedLegacyAttestationDoesNotReplayWritebackJournal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, dbPath := filepath.Join(dir, "repo"), filepath.Join(dir, "knowledge.db")
	identityFixture(t, repo, dbPath, "", true)
	path := filepath.Join(repo, identityTestDoc)
	oldMD, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldSC, err := os.ReadFile(path + ".kg.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sf, err := writeback.ReadSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	u := writeback.NodeUpdate{UUID: identityTestNode, Tag: "Concept", Name: "A-owned knowledge", Domain: "engineering", Subdomain: "queues", DomainSlug: "engineering", SubdomainSlug: "queues", Members: []string{"m1"}, Summary: "pending replacement", Provenance: "extraction"}
	newMD := []byte(writeback.RenderDoc([]writeback.NodeUpdate{u}))
	sf.DocHash = writeback.HashEditable(string(newMD))
	sf.Nodes[0].SummaryHash = writeback.HashEditable(u.Summary)
	newSC, err := yaml.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := json.Marshal(struct {
		MD         []byte `json:"md"`
		Sidecar    []byte `json:"sidecar"`
		OldMD      []byte `json:"old_md"`
		OldSidecar []byte `json:"old_sidecar"`
		HadMD      bool   `json:"had_md"`
		HadSidecar bool   `json:"had_sidecar"`
	}{newMD, newSC, oldMD, oldSC, true, true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".kg-writeback.json", journal, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("UPDATE node_sources SET member_id='not-attested'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "git:github.com/owner/repo", AdoptLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), repo, dbPath); err == nil {
		t.Fatal("unproven legacy ownership accepted")
	}
	afterMD, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterSC, err := os.ReadFile(path + ".kg.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oldMD, afterMD) || !bytes.Equal(oldSC, afterSC) {
		t.Fatal("read-only rejected ownership preflight replayed journal and modified source pair")
	}
	if actual, err := os.ReadFile(path + ".kg-writeback.json"); err != nil || !bytes.Equal(actual, journal) {
		t.Fatal("rejected preflight consumed recovery journal")
	}
}
