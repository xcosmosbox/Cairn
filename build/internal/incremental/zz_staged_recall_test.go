package incremental

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

func TestStagedRecallAncestorUnionCapsFailBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, domains := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate-domains-%v", domains), func(t *testing.T) {
			in := stagedTestInput(5)
			for i := range in.Documents[0].Annotation.Items {
				in.Documents[0].Annotation.Items[i].Name = fmt.Sprintf("marker%d", i)
				in.Documents[0].Annotation.Items[i].Content = fmt.Sprintf("marker%d", i)
			}
			stagedRepin(&in)
			canonical, units, _ := stagedCanonicalInput(in)
			s, e := openStagedSession(context.Background(), canonical, StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage")})
			if e != nil {
				t.Fatal(e)
			}
			defer s.close()
			c := stagedCatalogue{}
			parents := map[string]bool{}
			for u := 0; u < 5; u++ {
				for j := 0; j < 15; j++ {
					d, sub := "domain", fmt.Sprintf("bucket-%d-%d", u, j)
					if domains {
						d = sub
					}
					if !parents[d] {
						parents[d] = true
						c.Nodes = append(c.Nodes, dktypes.Node{ID: domainNodeID(d), Label: dktypes.LabelDomain, Name: "unmatched parent", Summary: "unmatched parent", Domain: d, Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred})
					}
					c.Nodes = append(c.Nodes, dktypes.Node{ID: subdomainNodeID(d, sub), Label: dktypes.LabelSubdomain, Name: "unmatched parent", Summary: "unmatched parent", Domain: d, Subdomain: sub, Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred})
					member := fmt.Sprintf("seed-%d-%d", u, j)
					id := extract.NodeUUID([]string{member})
					text := fmt.Sprintf("marker%d", u)
					c.Nodes = append(c.Nodes, dktypes.Node{ID: id, Label: dktypes.LabelEntity, Name: text, Summary: text, Domain: d, Subdomain: sub, Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred})
					c.Births = append(c.Births, StagedMappingBirth{id, domainNodeID(d), subdomainNodeID(d, sub), []string{member}})
					c.Sources = append(c.Sources, storage.NodeSource{NodeUUID: id, MemberID: member, Skill: "skill", FilePath: member + ".md"})
					c.Done = append(c.Done, member)
				}
			}
			sort.Slice(c.Nodes, func(i, j int) bool { return c.Nodes[i].ID < c.Nodes[j].ID })
			sort.Slice(c.Births, func(i, j int) bool { return c.Births[i].UUID < c.Births[j].UUID })
			sort.Slice(c.Sources, func(i, j int) bool { return c.Sources[i].NodeUUID < c.Sources[j].NodeUUID })
			sort.Strings(c.Done)
			s.state = c
			if e = s.project(context.Background()); e != nil {
				t.Fatal(e)
			}
			selected := units
			if domains {
				selected = units[:3]
			}
			if _, e = s.preparePlan(context.Background(), selected, 1, ""); !errors.Is(e, ErrStagedMappingOverflow) {
				t.Fatalf("ancestor union cap not enforced: %v", e)
			}
			// The next leftmost half is evaluated against this same parent catalogue.
			if _, e = s.preparePlan(context.Background(), units[:2], 1, ""); e != nil {
				t.Fatalf("bounded left half did not fit: %v", e)
			}
		})
	}
}

func TestStagedRecallLiteralUnicodeTokenOR(t *testing.T) {
	t.Parallel()
	u := stagedUnit{Sources: []stagedIncidence{{Item: dktypes.AnnotatedItem{Name: "alpha\u2003OR alpha a\"b (x):*", Content: strings.Repeat("界", 41) + " tail"}}, {Item: dktypes.AnnotatedItem{Name: "SECOND", Content: "SECOND"}}}}
	want := `"alpha" OR "OR" OR "a""b" OR "(x):*" OR "` + strings.Repeat("界", 40) + `"`
	if q := stagedRecallQuery(u); q != want || strings.Contains(q, "…") || strings.Contains(q, "SECOND") {
		t.Fatalf("literal query mismatch: %s", q)
	}
}

func stagedSeedCatalogue(t *testing.T, s *stagedSession, count int) {
	t.Helper()
	c := stagedCatalogue{Nodes: []dktypes.Node{{ID: domainNodeID("d"), Label: dktypes.LabelDomain, Name: "alpha", Summary: "alpha", Domain: "d", Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred}, {ID: subdomainNodeID("d", "s"), Label: dktypes.LabelSubdomain, Name: "alpha", Summary: "alpha", Domain: "d", Subdomain: "s", Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred}}}
	for i := 0; i < count; i++ {
		member := fmt.Sprintf("seed-%03d", i)
		id := extract.NodeUUID([]string{member})
		c.Nodes = append(c.Nodes, dktypes.Node{ID: id, Label: dktypes.LabelEntity, Name: "alpha", Summary: "alpha", Domain: "d", Subdomain: "s", Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred})
		c.Births = append(c.Births, StagedMappingBirth{id, domainNodeID("d"), subdomainNodeID("d", "s"), []string{member}})
		c.Sources = append(c.Sources, storage.NodeSource{NodeUUID: id, MemberID: member, Skill: "skill", FilePath: member + ".md"})
		c.Done = append(c.Done, member)
	}
	sort.Slice(c.Nodes, func(i, j int) bool { return c.Nodes[i].ID < c.Nodes[j].ID })
	sort.Slice(c.Births, func(i, j int) bool { return c.Births[i].UUID < c.Births[j].UUID })
	sort.Slice(c.Sources, func(i, j int) bool { return c.Sources[i].NodeUUID < c.Sources[j].NodeUUID })
	sort.Strings(c.Done)
	s.state = c
	if e := s.project(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestStagedRecallLabelBeforeLimitAndBoundaryUUIDTie(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(1)
	canonical, units, e := stagedCanonicalInput(in)
	if e != nil {
		t.Fatal(e)
	}
	s, e := openStagedSession(context.Background(), canonical, StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage")})
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	stagedSeedCatalogue(t, s, 25)
	nodes, subs, doms, e := s.recall(context.Background(), units)
	if e != nil {
		t.Fatal(e)
	}
	if len(nodes) != 15 || len(subs) != 1 || len(doms) != 1 {
		t.Fatalf("label cutoff/ancestor closure failed %d/%d/%d", len(nodes), len(subs), len(doms))
	}
	var ids []string
	for _, n := range s.state.Nodes {
		if n.Label == dktypes.LabelEntity {
			ids = append(ids, n.ID)
		}
	}
	sort.Strings(ids)
	for i, n := range nodes {
		if n.ID != ids[i] {
			t.Fatal("BM25 boundary tie was not resolved before LIMIT")
		}
	}
	// 打乱物理 rowid 后，命中边界必须不变；排除按插入顺序偶然通过。
	// Delete/reinsert reverse row order while preserving the canonical catalogue.
	if _, e = s.db.Conn().ExecContext(context.Background(), `DELETE FROM nodes_fts`); e != nil {
		t.Fatal(e)
	}
	for i := len(s.state.Nodes) - 1; i >= 0; i-- {
		n := s.state.Nodes[i]
		if _, e = s.db.Conn().ExecContext(context.Background(), `INSERT INTO nodes_fts(rowid,name,summary,domain,subdomain) SELECT rowid,name,summary,domain,subdomain FROM nodes WHERE id=?`, n.ID); e != nil {
			t.Fatal(e)
		}
	}
	nodes2, _, _, e := s.recall(context.Background(), units)
	if e != nil {
		t.Fatal(e)
	}
	if stagedHash(nodes) != stagedHash(nodes2) {
		t.Fatal("physical insertion order changed cutoff")
	}
}

func TestStagedRecallMissingOrAmbiguousAncestorFails(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			in := stagedTestInput(1)
			canonical, units, _ := stagedCanonicalInput(in)
			s, e := openStagedSession(context.Background(), canonical, StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage")})
			if e != nil {
				t.Fatal(e)
			}
			defer s.close()
			stagedSeedCatalogue(t, s, 1)
			if mode == "missing" {
				var kept []dktypes.Node
				for _, n := range s.state.Nodes {
					if n.Label != dktypes.LabelSubdomain {
						kept = append(kept, n)
					}
				}
				s.state.Nodes = kept
				_, e = s.db.Conn().Exec(`DELETE FROM nodes_fts WHERE rowid IN (SELECT rowid FROM nodes WHERE label='Subdomain')`)
				if e != nil {
					t.Fatal(e)
				}
			} else {
				s.state.Nodes = append(s.state.Nodes, dktypes.Node{ID: "ambiguous-parent", Label: dktypes.LabelSubdomain, Domain: "d", Subdomain: "s"})
			}
			if _, _, _, e = s.recall(context.Background(), units); e == nil {
				t.Fatal("ancestor ambiguity silently dropped/fell back")
			}
		})
	}
}
