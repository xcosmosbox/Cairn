package constructionplan

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strconv"
	"testing"
)

// 分批上限约束的是unique04ID，不是来源行数；K15只是候选并集的上界。
func TestPlan32And33UniqueUnitBoundaries(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 32, 33, 64, 65} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()
			f := fixture(t, 1, 1, n, false)
			c, e := ImportCheckpoint(f.input, f.policy)
			if e != nil {
				t.Fatal(e)
			}
			p, e := BuildPlan(c)
			if e != nil {
				t.Fatal(e)
			}
			if p.UniqueUnits != n || p.SourceIncidences != n || len(p.Batches) != (n+31)/32 || !p.AllDocumentsAccepted || p.Admissible || p.Status != NotAdmissible || p.WireFit != Unknown || p.OutputFit != Unknown || p.RoutingFit != Unknown {
				t.Fatalf("invalid structural plan: %+v", p)
			}
			seen := map[string]bool{}
			for index, b := range p.Batches {
				if b.Index != index || len(b.Units) == 0 || len(b.Units) > 32 || b.CandidateUnionUpperBound != len(b.Units)*15 || b.CandidateUnionUpperBound > 480 {
					t.Fatalf("batch budget: %+v", b)
				}
				for _, u := range b.Units {
					if seen[u.ID] {
						t.Fatal("unit scheduled twice")
					}
					seen[u.ID] = true
				}
			}
		})
	}
}

func TestPlanSharedUnitRetainsEverySourceIncidence(t *testing.T) {
	t.Parallel()
	f := fixture(t, 3, 3, 20, true)
	c, e := ImportCheckpoint(f.input, f.policy)
	if e != nil {
		t.Fatal(e)
	}
	before := fixtureJSON(t, c.Documents())
	p, e := BuildPlan(c)
	if e != nil {
		t.Fatal(e)
	}
	if p.UniqueUnits != 20 || p.SourceIncidences != 60 || len(p.Batches) != 1 || p.Batches[0].CandidateUnionUpperBound != 300 {
		t.Fatalf("wrong unique/provenance denominator: %+v", p)
	}
	for _, u := range p.Batches[0].Units {
		if len(u.Sources) != 3 {
			t.Fatal("collapsed source incidences")
		}
		seen := map[string]bool{}
		var last string
		for _, s := range u.Sources {
			if seen[s.Document.DocID] || s.Item.ID != u.ID || s.Document.DocID < last || s.OriginalSHA256 == "" || s.Item.Detail == "" {
				t.Fatal("lost frozen source/member content")
			}
			seen[s.Document.DocID] = true
			last = s.Document.DocID
		}
	}
	p.Batches[0].Units[0].Sources[0].Item.SourceSpan.Quote = "mutated output"
	if string(before) != string(fixtureJSON(t, c.Documents())) {
		t.Fatal("planner output aliased checkpoint memory")
	}
}

// 任务完成/输入map顺序不能改变分批；有意保留manifest身份，不能把另一个归档伪装成同一输入。
func TestPlanCanonicalAcrossRosterDescriptorsAndCompletionOrder(t *testing.T) {
	t.Parallel()
	f := fixture(t, 4, 6, 12, false)
	c, e := ImportCheckpoint(f.input, f.policy)
	if e != nil {
		t.Fatal(e)
	}
	want, e := BuildPlan(c)
	if e != nil {
		t.Fatal(e)
	}
	r := rand.New(rand.NewSource(75))
	for n := 0; n < 20; n++ {
		r.Shuffle(len(f.policy.Documents), func(i, j int) {
			f.policy.Documents[i], f.policy.Documents[j] = f.policy.Documents[j], f.policy.Documents[i]
		})
		r.Shuffle(len(f.input.Descriptor.Documents), func(i, j int) {
			f.input.Descriptor.Documents[i], f.input.Descriptor.Documents[j] = f.input.Descriptor.Documents[j], f.input.Descriptor.Documents[i]
		})
		c, e := ImportCheckpoint(f.input, f.policy)
		if e != nil {
			t.Fatal(e)
		}
		// Simulate different completion arrival orders after independent import validation.
		r.Shuffle(len(c.documents), func(i, j int) { c.documents[i], c.documents[j] = c.documents[j], c.documents[i] })
		got, e := BuildPlan(c)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatal("completion/roster order affected canonical plan")
		}
	}
	// Checkpoint item order is checked against02, while planning uses frozen member order.
	c, e = ImportCheckpoint(f.input, f.policy)
	if e != nil {
		t.Fatal(e)
	}
	for i := range c.documents {
		r.Shuffle(len(c.documents[i].Annotation.Items), func(a, b int) {
			c.documents[i].Annotation.Items[a], c.documents[i].Annotation.Items[b] = c.documents[i].Annotation.Items[b], c.documents[i].Annotation.Items[a]
		})
	}
	got, e := BuildPlan(c)
	if e != nil || !reflect.DeepEqual(want, got) {
		t.Fatal("member arrival order affected plan")
	}
}

func TestPlanIncomplete609FailsAdmissionAndListsExactMissingIdentities(t *testing.T) {
	t.Parallel()
	f := fixture(t, 11, 609, 1, false)
	c, e := ImportCheckpoint(f.input, f.policy)
	if e != nil {
		t.Fatal(e)
	}
	p, e := BuildPlan(c)
	if e != nil {
		t.Fatal(e)
	}
	if p.ExpectedDocuments != 609 || p.AcceptedDocuments != 11 || len(p.MissingDocuments) != 598 || p.AllDocumentsAccepted || p.Admissible || p.Status != NotAdmissible {
		t.Fatalf("partial cache admitted: %+v", p)
	}
	accepted := map[string]bool{}
	for _, d := range c.Documents() {
		accepted[d.Source.DocID] = true
	}
	missing := map[string]bool{}
	var last string
	for _, d := range p.MissingDocuments {
		if accepted[d.DocID] || missing[d.DocID] || d.DocID < last {
			t.Fatal("missing identities duplicated/accepted/unsorted")
		}
		missing[d.DocID] = true
		last = d.DocID
	}
	for _, d := range f.policy.Documents {
		if !accepted[d.DocID] && !missing[d.DocID] {
			t.Fatal("missing identity omitted")
		}
	}
	raw, _ := json.Marshal(p)
	if len(raw) == 0 || p.WireFit != Unknown || p.OutputFit != Unknown {
		t.Fatal("missing truthful future capacity status")
	}
}

func TestPlanRejectsUnverifiedAndEmptyInput(t *testing.T) {
	t.Parallel()
	for _, c := range []*VerifiedCheckpoint{nil, {}, {verified: true}} {
		if _, e := BuildPlan(c); e == nil {
			t.Fatal("unverified/empty checkpoint accepted")
		}
	}
	f := fixture(t, 0, 1, 1, false)
	if _, e := ImportCheckpoint(f.input, f.policy); e == nil {
		t.Fatal("empty accepted checkpoint imported")
	}
}
