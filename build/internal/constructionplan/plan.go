package constructionplan

import (
	"fmt"
	"sort"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

const (
	MaxBatchUnits     = 32
	CandidatesPerUnit = 15
	MaxCandidateUnion = MaxBatchUnits * CandidatesPerUnit
	Unknown           = "UNKNOWN"
	NotAdmissible     = "NOT_ADMISSIBLE"
)

// Incidence 保留每个(document,member)的完整标注，不因共享04 ID合并来源正文。
// A shared unit has multiple incidences, each retaining its own source and complete detail.
type Incidence struct {
	Document       FrozenDocument        `json:"document"`
	OriginalSHA256 string                `json:"original_sha256"`
	Item           dktypes.AnnotatedItem `json:"item"`
}

type Unit struct {
	ID      string      `json:"id"`
	Sources []Incidence `json:"sources"`
}
type Batch struct {
	Index                    int    `json:"index"`
	Units                    []Unit `json:"units"`
	CandidateUnionUpperBound int    `json:"candidate_union_upper_bound"`
}

// Plan 只给出结构计划；完整名册也不代表未来调用的输入/输出预算已获准。
// Phase1 never emits live admission, including when all documents have accepted checkpoints.
type Plan struct {
	Format               string           `json:"format"`
	Method               MethodIdentity   `json:"historical_annotation_method"`
	ArchiveCommit        string           `json:"archive_commit"`
	ManifestSHA256       string           `json:"manifest_sha256"`
	CorpusSHA256         string           `json:"corpus_sha256"`
	RosterSHA256         string           `json:"roster_sha256"`
	ExpectedDocuments    int              `json:"expected_documents"`
	AcceptedDocuments    int              `json:"accepted_documents"`
	UniqueUnits          int              `json:"unique_units"`
	SourceIncidences     int              `json:"source_incidences"`
	MissingDocuments     []FrozenDocument `json:"missing_documents"`
	AllDocumentsAccepted bool             `json:"all_documents_accepted"`
	Batches              []Batch          `json:"batches"`
	Status               string           `json:"status"`
	Admissible           bool             `json:"admissible"`
	WireFit              string           `json:"wire_fit"`
	OutputFit            string           `json:"output_fit"`
	RoutingFit           string           `json:"routing_fit"`
}

// BuildPlan 按冻结docID/memberID顺序首次出现分批；并发完成顺序不参与结果。
// Unique units are scheduled once, while every distinct document/member incidence survives.
func BuildPlan(checkpoint *VerifiedCheckpoint) (*Plan, error) {
	if checkpoint == nil || !checkpoint.verified || len(checkpoint.roster) == 0 || len(checkpoint.documents) == 0 {
		return nil, fmt.Errorf("constructionplan.BuildPlan: missing verified nonempty checkpoint")
	}
	plan := &Plan{Format: "cairn-bounded-construction-plan/v1", Method: checkpoint.identity, ArchiveCommit: checkpoint.archiveCommit, ManifestSHA256: checkpoint.manifestSHA, CorpusSHA256: checkpoint.corpusSHA, RosterSHA256: checkpoint.rosterSHA, ExpectedDocuments: len(checkpoint.roster), AcceptedDocuments: len(checkpoint.documents), Status: NotAdmissible, WireFit: Unknown, OutputFit: Unknown, RoutingFit: Unknown, MissingDocuments: []FrozenDocument{}, Batches: []Batch{}}
	docs := checkpoint.Documents()
	sort.Slice(docs, func(i, j int) bool { return docs[i].Source.DocID < docs[j].Source.DocID })
	accepted := map[string]bool{}
	unitIndex := map[string]int{}
	var units []Unit
	for _, d := range docs {
		accepted[d.Source.DocID] = true
		sort.Slice(d.Annotation.Items, func(i, j int) bool { return d.Annotation.Items[i].ID < d.Annotation.Items[j].ID })
		for _, it := range d.Annotation.Items {
			index, exists := unitIndex[it.ID]
			if !exists {
				index = len(units)
				unitIndex[it.ID] = index
				units = append(units, Unit{ID: it.ID, Sources: []Incidence{}})
			}
			units[index].Sources = append(units[index].Sources, Incidence{d.Source, d.OriginalSHA256, it})
			plan.SourceIncidences++
		}
	}
	for _, d := range checkpoint.roster {
		if !accepted[d.DocID] {
			plan.MissingDocuments = append(plan.MissingDocuments, d)
		}
	}
	sort.Slice(plan.MissingDocuments, func(i, j int) bool { return plan.MissingDocuments[i].DocID < plan.MissingDocuments[j].DocID })
	plan.UniqueUnits = len(units)
	plan.AllDocumentsAccepted = len(plan.MissingDocuments) == 0
	for start := 0; start < len(units); start += MaxBatchUnits {
		end := start + MaxBatchUnits
		if end > len(units) {
			end = len(units)
		}
		plan.Batches = append(plan.Batches, Batch{Index: len(plan.Batches), Units: append([]Unit(nil), units[start:end]...), CandidateUnionUpperBound: (end - start) * CandidatesPerUnit})
	}
	return plan, nil
}
