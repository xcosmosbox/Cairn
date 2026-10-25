//go:build linux

package main

// Opt-in, callable construction orchestration. There is deliberately no main
// wiring/default change. These files must join the EXISTING driverSources embed
// before any future live admission; this source-only implementation grants none.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xcosmosbox/cairn/build/internal/constructionplan"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const boundedConstructionMethod = "cairn-bounded-construction/v1;confidence=.7;incidence>=.95;documents=609;cross=other-scoped-SD-k15-fts5;verifier=1MiB;slug=fullUUID-collision;publish=linux-NOREPLACE"
const boundedGenerationFormat = "cairn-bounded-generation/v1"

type boundedCheckpointPin struct {
	Method                                                    constructionplan.MethodIdentity
	ArchiveCommit, ManifestSHA256, CorpusSHA256, RosterSHA256 string
}
type boundedPipelinePins struct {
	CodeSourceCommit, CorpusSHA256, RosterSHA256, AcceptedSetSHA256, MethodSHA256 string
	Checkpoints                                                                   []boundedCheckpointPin
	Admission                                                                     incremental.StagedMappingAdmissionIdentity
}
type boundedPipelineInput struct {
	// Private importer-verified values only. Neither raw04 nor the failed #85
	// negative checkpoint can be converted into this type by this controller.
	Checkpoints []*constructionplan.VerifiedCheckpoint
	Roster      []constructionplan.FrozenDocument
	Originals   []extract.FrozenSource
	Expected    boundedPipelinePins
}
type boundedPipelineBridge interface {
	incremental.StagedMappingAdmitter
	ConstructionIdentity() incremental.StagedMappingIdentity
	PrepareRequest(context.Context, llm.CompleteRequest, string, string, string, int, []string, string) (incremental.StagedMappingPreparedCall, error)
	AuthenticateObserved(context.Context, stagedBridgeObservedExpectation) (llm.CompleteResponse, error)
	WholeInventory(context.Context) (stagedBridgeInventory, error)
}
type boundedPipelineOptions struct {
	WorkDir, PublishPath, RepositoryIdentity string
	Resume                                   bool
	// The factory binds the independently pinned bridge source/member tables to
	// this private resolver. It cannot register arbitrary caller metadata.
	NewBridge func(func(context.Context, string, string, string, int, string) ([]string, error)) (boundedPipelineBridge, error)
	// Required only by the existing production materializer constructor. That
	// method makes no model calls; all construction dispatch uses NewBridge.
	MaterializationClient llm.Client
	fault                 func(string) error
}
type boundedDocumentReceipt struct {
	Document   constructionplan.VerifiedDocument
	Checkpoint boundedCheckpointPin
}
type boundedPipelineFrozen struct {
	Input     incremental.StagedMappingInput
	Documents []*dktypes.AnnotatedDocument
	Sources   []extract.BoundedStageSource
	Originals []extract.FrozenSource
	Receipts  []boundedDocumentReceipt
	Members   map[string][]string
	DocRoots  map[string]incremental.StagedMappingDocumentRoot
	Root      string
}
type boundedIncidenceForecast struct {
	Accepted, Selected int
	PerDocument        map[string]int
	NodeIDs            []string
	Triples            []string
}
type boundedPipelineResult struct {
	RunSHA256, GenerationPath, ManifestSHA256, CatalogueSHA256, DescriptionSHA256 string
	Forecast                                                                      boundedIncidenceForecast
	Cross                                                                         boundedCrossRecall
	Verification                                                                  *extract.CrossRelationReport
	InvalidUnverified                                                             int
}

func boundedPin(plan *constructionplan.Plan) boundedCheckpointPin {
	return boundedCheckpointPin{plan.Method, plan.ArchiveCommit, plan.ManifestSHA256, plan.CorpusSHA256, plan.RosterSHA256}
}

func freezeBoundedPipeline(input boundedPipelineInput) (*boundedPipelineFrozen, error) {
	p := input.Expected
	if !boundedUTF8Value(reflect.ValueOf(input.Expected)) || !boundedUTF8Value(reflect.ValueOf(input.Roster)) || !boundedUTF8Value(reflect.ValueOf(input.Originals)) {
		return nil, fmt.Errorf("bounded: invalid UTF8 before identity encoding")
	}
	if len(input.Checkpoints) == 0 || len(input.Checkpoints) != len(p.Checkpoints) || len(input.Roster) != 609 || len(input.Originals) != 609 || len(p.CodeSourceCommit) != 40 || p.MethodSHA256 != boundedDigestBytes([]byte(boundedConstructionMethod)) || constructionplan.RosterDigest(input.Roster) != p.RosterSHA256 {
		return nil, fmt.Errorf("bounded: missing/full609 input, independent pins or adopted method identity")
	}
	f := &boundedPipelineFrozen{Members: map[string][]string{}, DocRoots: map[string]incremental.StagedMappingDocumentRoot{}}
	roster := map[string]constructionplan.FrozenDocument{}
	paths := map[string]bool{}
	for _, d := range input.Roster {
		if roster[d.DocID].DocID != "" || paths[d.Path] || !boundedRelativePath(d.Path) {
			return nil, fmt.Errorf("bounded: duplicate/unsafe frozen document path")
		}
		roster[d.DocID] = d
		paths[d.Path] = true
	}
	originals := map[string]extract.FrozenSource{}
	for _, o := range input.Originals {
		key := o.Skill + "\x00" + o.Path
		if _, ok := originals[key]; ok || !utf8.ValidString(o.Content) || boundedDigestBytes([]byte(o.Content)) != o.SHA256 {
			return nil, fmt.Errorf("bounded: duplicate/invalid complete frozen original")
		}
		originals[key] = o
	}
	seenPins := map[string]bool{}
	for _, pin := range p.Checkpoints {
		key := boundedDigest(pin)
		if key == "" || seenPins[key] {
			return nil, fmt.Errorf("bounded: duplicate checkpoint pin")
		}
		seenPins[key] = true
	}
	for _, checkpoint := range input.Checkpoints {
		plan, e := constructionplan.BuildPlan(checkpoint)
		if e != nil {
			return nil, e
		}
		pin := boundedPin(plan)
		key := boundedDigest(pin)
		if !seenPins[key] || pin.CorpusSHA256 != p.CorpusSHA256 || pin.RosterSHA256 != p.RosterSHA256 || plan.ExpectedDocuments != 609 {
			return nil, fmt.Errorf("bounded: checkpoint provenance not independently allowed")
		}
		delete(seenPins, key)
		for _, d := range checkpoint.Documents() {
			if !boundedUTF8Value(reflect.ValueOf(d)) {
				return nil, fmt.Errorf("bounded: invalid accepted UTF8")
			}
			if expected, ok := roster[d.Source.DocID]; !ok || expected != d.Source {
				return nil, fmt.Errorf("bounded: checkpoint document outside frozen roster")
			}
			if _, ok := f.DocRoots[d.Source.DocID]; ok {
				return nil, fmt.Errorf("bounded: duplicate/conflicting accepted document ownership")
			}
			o, ok := originals[d.Source.Skill+"\x00"+d.Source.Path]
			if !ok || o.SHA256 != d.OriginalSHA256 {
				return nil, fmt.Errorf("bounded: original wrapper does not match accepted source")
			}
			if d.Annotation.Skill != d.Source.Skill || d.Annotation.FilePath != d.Source.Path || len(d.Annotation.Items) == 0 {
				return nil, fmt.Errorf("bounded: invalid accepted annotation binding")
			}
			if e := boundedRequireDetails(&d.Annotation); e != nil {
				return nil, e
			}
			for _, item := range d.Annotation.Items {
				f.Members[item.ID] = append(f.Members[item.ID], d.Source.DocID)
			}
			r := boundedDocumentReceipt{d, pin}
			checkpointSHA := boundedDigest(r)
			if checkpointSHA == "" {
				return nil, fmt.Errorf("bounded: unencodable accepted material")
			}
			root := incremental.StagedMappingDocumentRoot{DocID: d.Source.DocID, Skill: d.Source.Skill, FilePath: d.Source.Path, OriginalSHA256: d.OriginalSHA256, AcceptedCheckpointSHA256: checkpointSHA}
			f.DocRoots[root.DocID] = root
			f.Receipts = append(f.Receipts, r)
			f.Input.Roster = append(f.Input.Roster, root)
			f.Input.Documents = append(f.Input.Documents, incremental.StagedMappingDocument{Root: root, Annotation: d.Annotation})
			doc := d.Annotation
			f.Documents = append(f.Documents, &doc)
			f.Sources = append(f.Sources, extract.BoundedStageSource{DocID: root.DocID, Skill: root.Skill, Path: root.FilePath, OriginalSHA256: root.OriginalSHA256, AcceptedCheckpointSHA256: checkpointSHA})
			f.Originals = append(f.Originals, o)
		}
	}
	if len(seenPins) != 0 || len(f.Receipts) != 609 {
		return nil, fmt.Errorf("bounded: accepted checkpoint union does not cover every609 document")
	}
	sort.Slice(f.Receipts, func(i, j int) bool { return f.Receipts[i].Document.Source.DocID < f.Receipts[j].Document.Source.DocID })
	if boundedDigest(f.Receipts) != p.AcceptedSetSHA256 {
		return nil, fmt.Errorf("bounded: accepted-set root mismatch (per-document methods preserved)")
	}
	for id, docs := range f.Members {
		sort.Strings(docs)
		for i := 1; i < len(docs); i++ {
			if docs[i] == docs[i-1] {
				return nil, fmt.Errorf("bounded: duplicate accepted incidence")
			}
		}
		f.Members[id] = docs
	}
	f.Input.Identity = incremental.StagedMappingIdentity{CodeSourceCommit: p.CodeSourceCommit, CorpusSHA256: p.CorpusSHA256, MethodSHA256: p.MethodSHA256, ExpectedDocuments: 609, Admission: p.Admission}
	r, a, m, e := incremental.StagedMappingInputDigests(f.Input)
	if e != nil {
		return nil, e
	}
	f.Input.Identity.RosterSHA256 = r
	f.Input.Identity.AcceptedSetSHA256 = a
	f.Input.Identity.InputSHA256 = m
	// Mapping has its own explicitly documented roster/accepted material recipes;
	// preserve both those roots AND the imported corpus/checkpoint recipes.
	f.Root = boundedDigest(struct {
		Format  string
		Pins    boundedPipelinePins
		Mapping incremental.StagedMappingIdentity
	}{"cairn-bounded-pipeline-input/v1", p, f.Input.Identity})
	return f, nil
}

func boundedUTF8Value(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return boundedUTF8Value(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !boundedUTF8Value(v.Field(i)) {
				return false
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if !boundedUTF8Value(v.Index(i)) {
				return false
			}
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if !boundedUTF8Value(it.Key()) || !boundedUTF8Value(it.Value()) {
				return false
			}
		}
	}
	return true
}

func boundedRelativePath(p string) bool {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, v := range strings.Split(p, "/") {
		if v == "" || v == "." || v == ".." {
			return false
		}
	}
	return utf8.ValidString(p)
}

type boundedNodeLocation struct {
	Domain, Subdomain string
	Node              *extract.Node
}

func boundedNodes(graph *extract.Result) []boundedNodeLocation {
	var out []boundedNodeLocation
	for di := range graph.Domains {
		d := &graph.Domains[di]
		for si := range d.Subdomains {
			s := &d.Subdomains[si]
			for _, group := range []*[]extract.Node{&s.Entities, &s.Concepts} {
				for i := range *group {
					out = append(out, boundedNodeLocation{d.Slug, s.Slug, &(*group)[i]})
				}
			}
		}
	}
	return out
}
func boundedCloneGraph(g *extract.Result) (*extract.Result, error) {
	b, e := boundedEncode(g)
	if e != nil {
		return nil, e
	}
	var copy extract.Result
	if e = json.Unmarshal(b, &copy); e != nil {
		return nil, e
	}
	for di := range g.Domains {
		copy.Domains[di].Slug = g.Domains[di].Slug
		copy.Domains[di].Provenance = g.Domains[di].Provenance
		for si := range g.Domains[di].Subdomains {
			copy.Domains[di].Subdomains[si].Slug = g.Domains[di].Subdomains[si].Slug
		}
	}
	return &copy, nil
}

func boundedGraphRoot(graph *extract.Result) string {
	var hidden [][3]string
	for _, d := range graph.Domains {
		hidden = append(hidden, [3]string{d.Slug, d.Provenance, ""})
		for _, s := range d.Subdomains {
			hidden = append(hidden, [3]string{d.Slug, s.Slug, "subdomain"})
		}
	}
	return boundedDigest(struct {
		Graph  *extract.Result
		Hidden [][3]string
	}{graph, hidden})
}
func boundedForecast(graph *extract.Result, f *boundedPipelineFrozen) (boundedIncidenceForecast, error) {
	r := boundedIncidenceForecast{PerDocument: map[string]int{}}
	selected, _, e := extract.SelectMaterializationCandidates(graph, capacityMinConfidence)
	if e != nil {
		return r, e
	}
	incidences := map[string]bool{}
	for _, docs := range f.Members {
		r.Accepted += len(docs)
	}
	for _, loc := range boundedNodes(selected) {
		n := loc.Node
		r.NodeIDs = append(r.NodeIDs, n.ID)
		for _, member := range n.Members {
			docs := f.Members[member]
			if len(docs) == 0 {
				return r, fmt.Errorf("bounded: selected unknown member")
			}
			for _, docID := range docs {
				key := docID + "\x00" + member
				if incidences[key] {
					return r, fmt.Errorf("bounded: selected member ownership repeated")
				}
				incidences[key] = true
				r.PerDocument[docID]++
				r.Triples = append(r.Triples, n.ID+"\x00"+member+"\x00"+f.DocRoots[docID].FilePath)
			}
		}
	}
	r.Selected = len(incidences)
	sort.Strings(r.NodeIDs)
	sort.Strings(r.Triples)
	if r.Accepted == 0 || 100*r.Selected < 95*r.Accepted {
		return r, fmt.Errorf("bounded: exact postmapping incidence forecast below .95")
	}
	for id := range f.DocRoots {
		if r.PerDocument[id] < 1 {
			return r, fmt.Errorf("bounded: selected forecast leaves document %s empty", id)
		}
	}
	return r, nil
}

// The resolver is private, populated from immutable actual leaf descriptors,
// endpoint memberships and exact logical requests, never from caller claims.
type boundedResolvedWork struct {
	RequestSHA string
	Members    []string
}
type boundedResolver struct {
	works  map[string]boundedResolvedWork
	frozen *boundedPipelineFrozen
}

func (r *boundedResolver) resolve(ctx context.Context, stage, work, parent string, attempt int, logical string) ([]string, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	v, ok := r.works[boundedCallKey(stagedBridgeMetadata{Stage: stage, WorkID: work, ParentCatalogueRoot: parent, Attempt: attempt})]
	if !ok || v.RequestSHA != logical {
		return nil, fmt.Errorf("bounded: work/request not authenticated by current leaf descriptor")
	}
	return append([]string(nil), v.Members...), nil
}
func (r *boundedResolver) metadata(stage, work, parent string, attempt int, request llm.CompleteRequest, members []string) (stagedBridgeMetadata, error) {
	mset := map[string]bool{}
	dset := map[string]bool{}
	for _, m := range members {
		if len(r.frozen.Members[m]) == 0 {
			return stagedBridgeMetadata{}, fmt.Errorf("bounded: descriptor contains unknown member")
		}
		mset[m] = true
		for _, doc := range r.frozen.Members[m] {
			dset[doc] = true
		}
	}
	ms := sortedSet(mset)
	ids := sortedSet(dset)
	rows := []stagedBridgeDocument{}
	for _, id := range ids {
		rows = append(rows, stagedBridgeDocument{DocumentID: id, OriginalSHA256: r.frozen.DocRoots[id].OriginalSHA256})
	}
	m := stagedBridgeMetadata{Stage: stage, WorkID: work, ParentCatalogueRoot: parent, Attempt: attempt, UnitSourceDocIDs: ids, SourceBindingsSHA256: boundedDigest(rows)}
	v := boundedResolvedWork{boundedDigest(request), ms}
	key := boundedCallKey(m)
	if old, ok := r.works[key]; ok && !reflect.DeepEqual(old, v) {
		return m, fmt.Errorf("bounded: conflicting historical descriptor identity")
	}
	r.works[key] = v
	return m, nil
}
func boundedNodeMembers(graph *extract.Result, ids []string) ([]string, error) {
	byID := map[string]*extract.Node{}
	for _, loc := range boundedNodes(graph) {
		byID[loc.Node.ID] = loc.Node
	}
	set := map[string]bool{}
	for _, id := range ids {
		n := byID[id]
		if n == nil {
			return nil, fmt.Errorf("bounded: leaf endpoint absent")
		}
		for _, member := range n.Members {
			set[member] = true
		}
	}
	return sortedSet(set), nil
}
func boundedSavePrepared(request llm.CompleteRequest, m stagedBridgeMetadata, p incremental.StagedMappingPreparedCall) boundedSavedCall {
	return boundedSavedCall{Request: request, Metadata: m, Binding: p.Binding(), WireSHA256: boundedDigestBytes(p.Wire())}
}
func boundedMappingNextInventory(saved boundedSavedCall) boundedPhaseInventory {
	m := saved.Metadata
	return boundedPhaseInventory{Key: "mapping-next/" + boundedCallKey(m), Stage: "mapping", ParentRoot: m.ParentCatalogueRoot, MaximumCalls: 1, Works: []boundedInventoryWork{{ID: m.WorkID, Members: append([]string(nil), saved.MappingUnits...), Attempts: []boundedInventoryAttempt{boundedInventoryCall(saved)}}}}
}

type boundedMappingBridge struct {
	boundedPipelineBridge
	controller  *boundedCheckpoint
	resolver    *boundedResolver
	journalPath string
}
type boundedMappingHandle struct {
	incremental.StagedMappingPreparedCall
	owner *boundedMappingBridge
	call  boundedSavedCall
}

func (b *boundedMappingBridge) Prepare(ctx context.Context, call incremental.StagedMappingCall) (incremental.StagedMappingPreparedCall, error) {
	// RunStagedMapping holds its existing owner lock at this callback. Recheck
	// actual current history here, not just in an unlocked pre-Run file read.
	if e := boundedAuthenticateMappingJournal(ctx, b.journalPath, b.controller, b.boundedPipelineBridge, b.resolver); e != nil {
		return nil, e
	}
	p, e := b.boundedPipelineBridge.Prepare(ctx, call)
	if e != nil {
		return nil, e
	}
	m, e := b.resolver.metadata("mapping", call.BatchID, call.CatalogueSHA256, call.Attempt, call.Request, call.Units)
	if e != nil {
		return nil, e
	}
	saved := boundedSavePrepared(call.Request, m, p)
	saved.MappingUnits = append([]string(nil), call.Units...)
	inv := boundedMappingNextInventory(saved)
	if e = b.controller.freezeInventory(inv); e != nil {
		return nil, e
	}
	return &boundedMappingHandle{p, b, saved}, nil
}
func (h *boundedMappingHandle) Dispatch(ctx context.Context, binding incremental.StagedMappingWireBinding) (incremental.StagedMappingDispatchResult, error) {
	if h.Binding() != binding {
		return incremental.StagedMappingDispatchResult{}, fmt.Errorf("bounded: mapping capability mismatch")
	}
	response, e := h.owner.controller.execute(ctx, h.owner.boundedPipelineBridge, h.call, h.StagedMappingPreparedCall)
	if e != nil {
		return incremental.StagedMappingDispatchResult{}, e
	}
	saved := h.owner.controller.calls[boundedCallKey(h.call.Metadata)]
	// Mapping always sees the actual dispatched binding, including replay. A
	// mapping INTENT gap cannot be repaired through this wrapper: #80 stops first.
	if saved.Binding != binding {
		return incremental.StagedMappingDispatchResult{}, fmt.Errorf("bounded: mapping local checkpoint gap/recreated handle; reconciliation required")
	}
	return incremental.StagedMappingDispatchResult{Binding: binding, ReceiptID: saved.Result.ReceiptID, Response: response}, nil
}

func boundedStageInput(graph *extract.Result, f *boundedPipelineFrozen, p boundedPipelinePins, parent string) (extract.BoundedStageInput, error) {
	in := extract.BoundedStageInput{Graph: graph, Documents: f.Documents, Sources: f.Sources, Identity: extract.BoundedStageIdentity{CodeSourceCommit: p.CodeSourceCommit, CorpusSHA256: p.CorpusSHA256, RosterSHA256: p.RosterSHA256, AcceptedSetSHA256: p.AcceptedSetSHA256, MethodSHA256: p.MethodSHA256, ParentCatalogueSHA256: parent}}
	id, e := extract.ComputeBoundedStageIdentity(in)
	if e != nil {
		return in, e
	}
	in.Identity = id
	in.Expected = id
	return in, nil
}

type boundedPreparedLeaf struct {
	work     extract.BoundedStageWork
	requests [2]llm.CompleteRequest
	calls    [2]boundedSavedCall
	handles  [2]incremental.StagedMappingPreparedCall
}

func boundedPreflightStage(ctx context.Context, bridge boundedPipelineBridge, r *boundedResolver, graph *extract.Result, parent string, works []extract.BoundedStageWork) ([]boundedPreparedLeaf, error) {
	var leaves []boundedPreparedLeaf
	var visit func(extract.BoundedStageWork) error
	visit = func(w extract.BoundedStageWork) error {
		d := w.Descriptor()
		members, e := boundedNodeMembers(graph, d.NodeIDs)
		if e != nil {
			return e
		}
		leaf := boundedPreparedLeaf{work: w}
		for a := 1; a <= 2; a++ {
			req, e := w.Request(a)
			if e == nil {
				leaf.requests[a-1] = req
				var meta stagedBridgeMetadata
				meta, e = r.metadata(d.Stage, d.ID, parent, a, req, members)
				if e == nil {
					leaf.handles[a-1], e = bridge.PrepareRequest(ctx, req, meta.Stage, meta.WorkID, meta.ParentCatalogueRoot, meta.Attempt, meta.UnitSourceDocIDs, meta.SourceBindingsSHA256)
					if e == nil {
						leaf.calls[a-1] = boundedSavePrepared(req, meta, leaf.handles[a-1])
					}
				}
			}
			if e != nil {
				// Request rendering can exceed the structural body bound; UUID or
				// invalid material also fails closed after finite pure splitting.
				if !errors.Is(e, incremental.ErrStagedMappingOverflow) && leaf.requests[a-1].User != "" {
					return e
				}
				children, se := w.Split()
				if se != nil {
					return fmt.Errorf("bounded: singleton preflight failed without dropping material: %w", e)
				}
				for _, child := range children {
					if e = visit(child); e != nil {
						return e
					}
				}
				return nil
			}
		}
		leaves = append(leaves, leaf)
		return nil
	}
	for _, work := range works {
		if e := visit(work); e != nil {
			return nil, e
		}
	}
	return leaves, nil
}
func boundedRunStage(ctx context.Context, bridge boundedPipelineBridge, c *boundedCheckpoint, plan *extract.BoundedStagePlan, graph *extract.Result, leaves []boundedPreparedLeaf) (*extract.Result, error) {
	stage := "relation"
	if len(leaves) > 0 {
		stage = leaves[0].work.Descriptor().Stage
	}
	parent := boundedGraphRoot(graph)
	inv := boundedPhaseInventory{Key: stage + "/" + parent, Stage: stage, ParentRoot: parent, MaximumCalls: 2 * len(leaves), Ceiling: true}
	for _, leaf := range leaves {
		d := leaf.work.Descriptor()
		members, e := boundedNodeMembers(graph, d.NodeIDs)
		if e != nil {
			return nil, e
		}
		inv.Works = append(inv.Works, boundedInventoryWork{ID: d.ID, NodeIDs: d.NodeIDs, Members: members, Pairs: d.Pairs, Attempts: []boundedInventoryAttempt{boundedInventoryCall(leaf.calls[0]), boundedInventoryCall(leaf.calls[1])}})
	}
	if e := c.freezeInventory(inv); e != nil {
		return nil, e
	}
	var deltas []extract.BoundedStageDelta
	for _, leaf := range leaves {
		var delta extract.BoundedStageDelta
		var validation error
		for attempt := 0; attempt < 2; attempt++ {
			response, e := c.execute(ctx, bridge, leaf.calls[attempt], leaf.handles[attempt])
			if e != nil {
				return nil, e
			}
			// Only after exact authenticated complete stop/usage may this pure
			// schema/value/coverage validator authorize the fixed repair request.
			delta, validation = leaf.work.Validate(&response)
			if validation == nil {
				break
			}
		}
		if validation != nil {
			return nil, fmt.Errorf("bounded: schema attempts exhausted: %w", validation)
		}
		deltas = append(deltas, delta)
	}
	return plan.Apply(graph, deltas)
}

type boundedCrossHit struct {
	UUID string
	Rank float64
}
type boundedCrossQuery struct {
	UUID, Query string
	Hits        []boundedCrossHit
}
type boundedCrossRecall struct {
	Recipe, SQLiteVersion, SchemaSHA256, RowsSHA256, CatalogueSHA256, DescriptionSHA256, SHA256 string
	Queries                                                                                     []boundedCrossQuery
	Pairs                                                                                       []extract.BoundedPair
}

func boundedLiteralQuery(name, summary string) string {
	r := []rune(summary)
	if len(r) > 40 {
		summary = string(r[:40])
	}
	seen := map[string]bool{}
	var out []string
	for _, token := range strings.Fields(name + " " + summary) {
		if !seen[token] {
			seen[token] = true
			out = append(out, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
		}
	}
	return strings.Join(out, " OR ")
}
func boundedRecallCross(ctx context.Context, graph *extract.Result, catalogue, description string) (boundedCrossRecall, error) {
	r := boundedCrossRecall{Recipe: "E/C-only/full-fields/synonyms-space-joined-as-ingest/fts5-default-unicode61-prefix2,3/literal-token-OR/fullName+first40UnicodeSummary/OTHER(domain,subdomain)-beforeK15/bm25-then-UUID-BINARY/v1", CatalogueSHA256: catalogue, DescriptionSHA256: description}
	db, e := storage.NewDB(storage.DBOptions{Path: ":memory:"})
	if e != nil {
		return r, e
	}
	defer db.Close()
	if e = db.Conn().QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&r.SQLiteVersion); e != nil {
		return r, e
	}
	rows, e := db.Conn().QueryContext(ctx, "SELECT name,sql FROM sqlite_master WHERE name IN ('nodes','nodes_fts') ORDER BY name")
	if e != nil {
		return r, e
	}
	var schema [][2]string
	for rows.Next() {
		var v [2]string
		if e = rows.Scan(&v[0], &v[1]); e != nil {
			rows.Close()
			return r, e
		}
		schema = append(schema, v)
	}
	e = errors.Join(rows.Err(), rows.Close())
	if e != nil {
		return r, e
	}
	r.SchemaSHA256 = boundedDigest(schema)
	locations := boundedNodes(graph)
	sort.Slice(locations, func(i, j int) bool { return locations[i].Node.ID < locations[j].Node.ID })
	var content []any
	for _, loc := range locations {
		n := loc.Node
		if n.Label != "Entity" && n.Label != "Concept" {
			return r, fmt.Errorf("bounded cross: non-E/C corpus")
		}
		_, e = db.Conn().ExecContext(ctx, `INSERT INTO nodes(id,label,name,summary,synonyms,domain,subdomain,description,confidence) VALUES(?,?,?,?,?,?,?,?,?)`, n.ID, n.Label, n.Name, n.Summary, strings.Join(n.Synonyms, " "), loc.Domain, loc.Subdomain, n.Description, n.Confidence)
		if e != nil {
			return r, e
		}
		content = append(content, struct {
			Domain, Subdomain string
			Node              extract.Node
		}{loc.Domain, loc.Subdomain, *n})
	}
	r.RowsSHA256 = boundedDigest(content)
	_, e = db.Conn().ExecContext(ctx, `INSERT INTO nodes_fts(rowid,name,summary,synonyms,tags,description,domain,subdomain) SELECT rowid,name,summary,synonyms,tags,description,domain,subdomain FROM nodes ORDER BY id COLLATE BINARY`)
	if e != nil {
		return r, e
	}
	pairs := map[extract.BoundedPair]bool{}
	for _, loc := range locations {
		q := boundedCrossQuery{UUID: loc.Node.ID, Query: boundedLiteralQuery(loc.Node.Name, loc.Node.Summary), Hits: []boundedCrossHit{}}
		if q.Query != "" {
			hits, e := db.Conn().QueryContext(ctx, `SELECT nodes.id,bm25(nodes_fts) FROM nodes_fts JOIN nodes ON nodes.rowid=nodes_fts.rowid WHERE nodes_fts MATCH ? AND nodes.label IN ('Entity','Concept') AND NOT(nodes.domain=? AND nodes.subdomain=?) ORDER BY bm25(nodes_fts),nodes.id COLLATE BINARY LIMIT 15`, q.Query, loc.Domain, loc.Subdomain)
			if e != nil {
				return r, e
			}
			for hits.Next() {
				var hit boundedCrossHit
				if e = hits.Scan(&hit.UUID, &hit.Rank); e != nil {
					hits.Close()
					return r, e
				}
				q.Hits = append(q.Hits, hit)
				a, b := q.UUID, hit.UUID
				if b < a {
					a, b = b, a
				}
				pairs[extract.BoundedPair{First: a, Second: b}] = true
			}
			e = errors.Join(hits.Err(), hits.Close())
			if e != nil {
				return r, e
			}
		}
		r.Queries = append(r.Queries, q)
	}
	for pair := range pairs {
		r.Pairs = append(r.Pairs, pair)
	}
	sort.Slice(r.Pairs, func(i, j int) bool {
		a, b := r.Pairs[i], r.Pairs[j]
		if a.First != b.First {
			return a.First < b.First
		}
		return a.Second < b.Second
	})
	r.SHA256 = boundedDigest(r)
	return r, nil
}

func boundedVerifyRelations(ctx context.Context, bridge boundedPipelineBridge, c *boundedCheckpoint, r *boundedResolver, graph *extract.Result, f *boundedPipelineFrozen) (*extract.Result, *extract.CrossRelationReport, error) {
	plan := extract.BuildCrossRelationPlan(graph, f.Documents, f.Originals, extract.CrossRelationOptions{MaxEvidenceBytes: 1048576}, 65536)
	works := plan.Works()
	parent := boundedGraphRoot(graph)
	type ready struct {
		work   *extract.CrossRelationWork
		call   boundedSavedCall
		handle incremental.StagedMappingPreparedCall
	}
	var all []ready
	for _, w := range works {
		d := w.Descriptor()
		req, ok := w.Request()
		if !ok || d.PreflightDisposition != "" || d.EvidenceBytes > 1048576 {
			return nil, nil, fmt.Errorf("bounded verifier: nonrequestable preflight %s: %s", d.PreflightDisposition, d.PreflightReason)
		}
		members, e := boundedNodeMembers(graph, []string{d.Relation.Source, d.Relation.Target})
		if e != nil {
			return nil, nil, e
		}
		meta, e := r.metadata("cross_relation_verify", d.ID, parent, 1, req, members)
		if e != nil {
			return nil, nil, e
		}
		handle, e := bridge.PrepareRequest(ctx, req, meta.Stage, meta.WorkID, meta.ParentCatalogueRoot, 1, meta.UnitSourceDocIDs, meta.SourceBindingsSHA256)
		if e != nil {
			return nil, nil, e
		}
		all = append(all, ready{w, boundedSavePrepared(req, meta, handle), handle})
	}
	// No phase wire is sent until every complete source view and actual wire fits.
	inv := boundedPhaseInventory{Key: "cross_relation_verify/" + parent, Stage: "cross_relation_verify", ParentRoot: parent, MaximumCalls: len(all)}
	for _, ready := range all {
		d := ready.work.Descriptor()
		ids := []string{d.Relation.Source, d.Relation.Target}
		members, e := boundedNodeMembers(graph, ids)
		if e != nil {
			return nil, nil, e
		}
		inv.Works = append(inv.Works, boundedInventoryWork{ID: d.ID, NodeIDs: ids, Members: members, Attempts: []boundedInventoryAttempt{boundedInventoryCall(ready.call)}})
	}
	if e := c.freezeInventory(inv); e != nil {
		return nil, nil, e
	}
	var evaluations []*extract.CrossRelationEvaluation
	for _, w := range all {
		response, e := c.execute(ctx, bridge, w.call, w.handle)
		if e != nil {
			return nil, nil, e
		}
		evaluation := w.work.Evaluate(&response, nil)
		if evaluation.Decision().Disposition == "execution_error" {
			return nil, nil, fmt.Errorf("bounded verifier: execution failure")
		}
		evaluations = append(evaluations, evaluation)
	}
	return plan.Apply(graph, evaluations)
}

func boundedFreshSlugs(ctx context.Context, original *extract.Result) (*extract.Result, error) {
	graph, err := boundedCloneGraph(original)
	if err != nil {
		return nil, err
	}
	groups := map[string][]*extract.Node{}
	for _, loc := range boundedNodes(graph) {
		if loc.Node.FileSlug != "" {
			return nil, fmt.Errorf("bounded: unexpected immutable historical FileSlug in fresh mapping")
		}
	}
	extract.GenerateFileSlugs(ctx, graph.Domains, nil)
	for _, loc := range boundedNodes(graph) {
		key := loc.Domain + "\x00" + loc.Node.FileSlug
		groups[key] = append(groups[key], loc.Node)
	}
	for _, nodes := range groups {
		if len(nodes) > 1 {
			for _, node := range nodes {
				node.FileSlug += "-" + node.ID
			}
		}
	}
	seen := map[string]bool{}
	for _, loc := range boundedNodes(graph) {
		p := writeback.PrimaryRelPath(loc.Domain, loc.Node.FileSlug, loc.Node.ID)
		if seen[p] {
			return nil, fmt.Errorf("bounded: residual slug collision")
		}
		seen[p] = true
		if !boundedRelativePath(p) {
			return nil, fmt.Errorf("bounded: unsafe generated slug path")
		}
	}
	return graph, ctx.Err()
}
func boundedCheckManagedPath(root, rel string) error {
	if !boundedRelativePath(rel) {
		return fmt.Errorf("bounded: unsafe managed relative path")
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	if len(full) > 4096 {
		return fmt.Errorf("bounded: managed absolute path exceeds4096 bytes")
	}
	for _, component := range strings.Split(filepath.ToSlash(full), "/") {
		if len(component) > 255 {
			return fmt.Errorf("bounded: managed component exceeds255 UTF8 bytes")
		}
	}
	return nil
}
func boundedPathInventory(private, target string, graph *extract.Result, f *boundedPipelineFrozen) error {
	paths := map[string]string{}
	add := func(rel, owner string) error {
		if old, ok := paths[rel]; ok && old != owner {
			return fmt.Errorf("bounded: managed file collision between %s and %s", owner, old)
		}
		paths[rel] = owner
		for _, root := range []string{private, target} {
			if e := boundedCheckManagedPath(root, rel); e != nil {
				return e
			}
		}
		return nil
	}
	addDoc := func(rel, owner string) error {
		for _, suffix := range []string{"", ".kg.yaml", ".kg-writeback.json"} {
			if e := add("repository/"+rel+suffix, owner); e != nil {
				return e
			}
		}
		return nil
	}
	for id, doc := range f.DocRoots {
		if e := addDoc(doc.FilePath, id); e != nil {
			return e
		}
		if e := add("originals/"+doc.FilePath, id); e != nil {
			return e
		}
	}
	for _, loc := range boundedNodes(graph) {
		if e := addDoc(writeback.PrimaryRelPath(loc.Domain, loc.Node.FileSlug, loc.Node.ID), loc.Node.ID); e != nil {
			return e
		}
	}
	for _, p := range []string{"knowledge.db", "knowledge.db-wal", "knowledge.db-shm", "manifest.json", "receipts.json", "forecast.json", "cross-recall.json", "verification.json"} {
		if e := add(p, "generation"); e != nil {
			return e
		}
	}
	for _, p := range []string{"delivery-checkpoint.json", "mapping-receipt.json", "graph.json", "repository/.cairn-repository-id"} {
		if e := add(p, "generation"); e != nil {
			return e
		}
	}
	return nil
}

type boundedGenerationFile struct {
	Path, SHA256 string
	Bytes        int64
}
type boundedGenerationManifest struct {
	Format, RunSHA256, GraphSHA256, CheckpointHeadSHA256 string
	Files                                                []boundedGenerationFile
}

func boundedGenerationFiles(root string) ([]boundedGenerationFile, error) {
	var files []boundedGenerationFile
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bounded: generation symlink")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("bounded: generation nonregular file")
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		if rel == "manifest.json" {
			return nil
		}
		if e = boundedCheckManagedPath(root, filepath.ToSlash(rel)); e != nil {
			return e
		}
		if strings.HasSuffix(rel, ".kg-writeback.json") || strings.HasSuffix(rel, ".db-wal") || strings.HasSuffix(rel, ".db-shm") {
			return fmt.Errorf("bounded: unfinished writeback/DB sidecar in immutable generation")
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		files = append(files, boundedGenerationFile{filepath.ToSlash(rel), boundedDigestBytes(b), int64(len(b))})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, e
}
func boundedWriteJSON(path string, v any) error {
	b, e := boundedEncode(v)
	if e != nil {
		return e
	}
	return os.WriteFile(path, b, 0600)
}

func boundedAuditGeneration(ctx context.Context, root string, graph *extract.Result, f *boundedPipelineFrozen, forecast boundedIncidenceForecast) error {
	selected, _, e := extract.SelectMaterializationCandidates(graph, capacityMinConfidence)
	if e != nil {
		return e
	}
	db, e := storage.OpenReadOnly(filepath.Join(root, "knowledge.db"))
	if e != nil {
		return e
	}
	defer db.Close()
	var integrity string
	if e = db.Conn().QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); e != nil || integrity != "ok" {
		return fmt.Errorf("bounded audit: DB integrity check failed: %v", e)
	}
	foreign, e := db.Conn().QueryContext(ctx, "PRAGMA foreign_key_check")
	if e != nil {
		return e
	}
	badForeign := foreign.Next()
	e = errors.Join(foreign.Err(), foreign.Close())
	if e != nil || badForeign {
		return fmt.Errorf("bounded audit: DB foreign key check failed")
	}
	expectedNodes := map[string]bool{}
	expectedFields := map[string][]any{}
	for _, loc := range boundedNodes(selected) {
		expectedNodes[loc.Node.ID] = true
		n := loc.Node
		expectedFields[n.ID] = []any{n.Label, n.Name, n.Summary, n.Description, loc.Domain, loc.Subdomain, strings.Join(n.Synonyms, " "), strings.Join(n.SourceSkills, ","), n.FileSlug, n.Confidence}
	}
	fieldRows, e := db.Conn().QueryContext(ctx, "SELECT id,label,name,summary,COALESCE(description,''),domain,subdomain,COALESCE(synonyms,''),COALESCE(source_refs,''),COALESCE(file_slug,''),confidence FROM nodes WHERE label IN ('Entity','Concept')")
	if e != nil {
		return e
	}
	for fieldRows.Next() {
		var id, label, name, summary, description, domain, subdomain, synonyms, sources, slug string
		var confidence float64
		if e = fieldRows.Scan(&id, &label, &name, &summary, &description, &domain, &subdomain, &synonyms, &sources, &slug, &confidence); e != nil {
			fieldRows.Close()
			return e
		}
		actual := []any{label, name, summary, description, domain, subdomain, synonyms, sources, slug, confidence}
		if !reflect.DeepEqual(actual, expectedFields[id]) {
			fieldRows.Close()
			return fmt.Errorf("bounded audit: persisted node full fields or frozen UUID changed")
		}
	}
	if e = errors.Join(fieldRows.Err(), fieldRows.Close()); e != nil {
		return e
	}
	rows, e := db.Conn().QueryContext(ctx, "SELECT id FROM nodes WHERE label IN ('Entity','Concept') ORDER BY id")
	if e != nil {
		return e
	}
	var actualNodes []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		actualNodes = append(actualNodes, id)
	}
	e = errors.Join(rows.Err(), rows.Close())
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(actualNodes, forecast.NodeIDs) {
		return fmt.Errorf("bounded audit: exact selected E/C DB nodes differ from forecast")
	}
	allNodes := map[string]string{}
	allEdges := map[string]bool{}
	for _, doc := range f.Documents {
		allNodes["skill::"+doc.Skill] = "Skill"
	}
	for _, d := range selected.Domains {
		domainID := "domain::" + d.Slug
		allNodes[domainID] = "Domain"
		for _, skill := range d.SourceSkills {
			allEdges[edgeIdentity("skill::"+skill, domainID, "provides")] = true
		}
		for _, s := range d.Subdomains {
			subID := "subdomain::" + d.Slug + "::" + s.Slug
			allNodes[subID] = "Subdomain"
			allEdges[edgeIdentity(domainID, subID, "composes")] = true
			for _, group := range [][]extract.Node{s.Entities, s.Concepts} {
				for _, n := range group {
					allNodes[n.ID] = n.Label
					allEdges[edgeIdentity(subID, n.ID, "composes")] = true
				}
			}
			for _, rel := range s.Relations {
				allEdges[edgeIdentity(rel.Source, rel.Target, rel.Kind)] = true
			}
		}
	}
	allRows, e := db.Conn().QueryContext(ctx, "SELECT id,label FROM nodes")
	if e != nil {
		return e
	}
	gotNodes := map[string]string{}
	for allRows.Next() {
		var id, label string
		if e = allRows.Scan(&id, &label); e != nil {
			allRows.Close()
			return e
		}
		gotNodes[id] = label
	}
	if e = errors.Join(allRows.Err(), allRows.Close()); e != nil {
		return e
	}
	if !reflect.DeepEqual(allNodes, gotNodes) {
		return fmt.Errorf("bounded audit: complete taxonomy/skill/leaf inventory differs")
	}
	allRows, e = db.Conn().QueryContext(ctx, "SELECT source_id,target_id,kind FROM edges")
	if e != nil {
		return e
	}
	gotEdges := map[string]bool{}
	for allRows.Next() {
		var source, target, kind string
		if e = allRows.Scan(&source, &target, &kind); e != nil {
			allRows.Close()
			return e
		}
		key := edgeIdentity(source, target, kind)
		if gotEdges[key] {
			allRows.Close()
			return fmt.Errorf("bounded audit: duplicate graph edge")
		}
		gotEdges[key] = true
	}
	if e = errors.Join(allRows.Err(), allRows.Close()); e != nil {
		return e
	}
	if !reflect.DeepEqual(allEdges, gotEdges) {
		return fmt.Errorf("bounded audit: complete hierarchy/semantic graph edges differ")
	}
	expectedEdges := map[string]bool{}
	type semanticValue struct {
		Description string
		Confidence  float64
	}
	expectedSemantic := map[string]semanticValue{}
	for _, d := range selected.Domains {
		for _, s := range d.Subdomains {
			for _, rel := range s.Relations {
				expectedEdges[edgeIdentity(rel.Source, rel.Target, rel.Kind)] = true
				expectedSemantic[edgeIdentity(rel.Source, rel.Target, rel.Kind)] = semanticValue{rel.Description, rel.Confidence}
			}
		}
	}
	edges, e := db.Conn().QueryContext(ctx, "SELECT e.source_id,e.target_id,e.kind,COALESCE(e.description,''),e.confidence FROM edges e JOIN nodes n ON n.id=e.source_id WHERE n.label IN ('Entity','Concept')")
	if e != nil {
		return e
	}
	actualEdges := map[string]bool{}
	for edges.Next() {
		var source, target, kind, description string
		var confidence float64
		if e = edges.Scan(&source, &target, &kind, &description, &confidence); e != nil {
			edges.Close()
			return e
		}
		key := edgeIdentity(source, target, kind)
		if expectedSemantic[key] != (semanticValue{description, confidence}) {
			edges.Close()
			return fmt.Errorf("bounded audit: semantic edge description/confidence changed")
		}
		if actualEdges[key] {
			edges.Close()
			return fmt.Errorf("bounded audit: duplicate semantic DB edge")
		}
		actualEdges[key] = true
	}
	if e = errors.Join(edges.Err(), edges.Close()); e != nil {
		return e
	}
	if !reflect.DeepEqual(actualEdges, expectedEdges) {
		return fmt.Errorf("bounded audit: complete semantic relation projection differs")
	}
	rows, e = db.Conn().QueryContext(ctx, "SELECT node_uuid,member_id,file_path,skill,COALESCE(start_line,0),COALESCE(end_line,0) FROM node_sources ORDER BY node_uuid,member_id,file_path")
	if e != nil {
		return e
	}
	var triples []string
	byPath := map[string]incremental.StagedMappingDocumentRoot{}
	for _, doc := range f.DocRoots {
		byPath[doc.FilePath] = doc
	}
	expectedSpans := map[string][2]int{}
	for _, doc := range f.Documents {
		for _, item := range doc.Items {
			span := [2]int{}
			if item.SourceSpan != nil {
				span = [2]int{item.SourceSpan.StartLine, item.SourceSpan.EndLine}
			}
			expectedSpans[doc.FilePath+"\x00"+item.ID] = span
		}
	}
	for rows.Next() {
		var node, member, p, skill string
		var start, end int
		if e = rows.Scan(&node, &member, &p, &skill, &start, &end); e != nil {
			rows.Close()
			return e
		}
		if byPath[p].Skill != skill || !expectedNodes[node] || expectedSpans[p+"\x00"+member] != [2]int{start, end} {
			rows.Close()
			return fmt.Errorf("bounded audit: unbound DB source row")
		}
		triples = append(triples, node+"\x00"+member+"\x00"+p)
	}
	e = errors.Join(rows.Err(), rows.Close())
	if e != nil {
		return e
	}
	sort.Strings(triples)
	if !reflect.DeepEqual(triples, forecast.Triples) {
		return fmt.Errorf("bounded audit: source incidence set differs from forecast")
	}
	// Sidecar shared nodes list their full members. Intersect with each actual
	// document's accepted ownership; never invent a Cartesian member×source set.
	memberNode := map[string]string{}
	byNode := map[string]boundedNodeLocation{}
	for _, loc := range boundedNodes(selected) {
		byNode[loc.Node.ID] = loc
		for _, m := range loc.Node.Members {
			memberNode[m] = loc.Node.ID
		}
	}
	for id, doc := range f.DocRoots {
		md := filepath.Join(root, "repository", filepath.FromSlash(doc.FilePath))
		sc, e := writeback.ReadSidecar(md)
		if e != nil {
			return e
		}
		b, e := os.ReadFile(md)
		if e != nil {
			return e
		}
		if sc.DocHash != "sha256:"+boundedDigestBytes(b) {
			return fmt.Errorf("bounded audit: sidecar document hash mismatch")
		}
		got := map[string]bool{}
		for _, node := range sc.Nodes {
			if !expectedNodes[node.UUID] {
				return fmt.Errorf("bounded audit: unexpected source sidecar node")
			}
			loc := byNode[node.UUID]
			n := loc.Node
			if node.Name != n.Name || node.FileSlug != n.FileSlug || node.DomainSlug != loc.Domain || node.SubdomainSlug != loc.Subdomain || node.SummaryHash != "sha256:"+boundedDigestBytes([]byte(n.Summary)) || node.DescriptionHash != "sha256:"+boundedDigestBytes([]byte(n.Description)) || !reflect.DeepEqual(memberSet(node.Members), memberSet(n.Members)) {
				return fmt.Errorf("bounded audit: source sidecar full node binding changed")
			}
			for _, m := range node.Members {
				for _, d := range f.Members[m] {
					if d == id {
						if memberNode[m] != node.UUID || got[m] {
							return fmt.Errorf("bounded audit: sidecar ownership repeated/changed")
						}
						got[m] = true
					}
				}
			}
		}
		want := map[string]bool{}
		for member, node := range memberNode {
			if node != "" {
				for _, d := range f.Members[member] {
					if d == id {
						want[member] = true
					}
				}
			}
		}
		if !reflect.DeepEqual(got, want) || len(got) == 0 {
			return fmt.Errorf("bounded audit: document sidecar incidence coverage mismatch")
		}
		original, e := os.ReadFile(filepath.Join(root, "originals", filepath.FromSlash(doc.FilePath)))
		if e != nil || boundedDigestBytes(original) != doc.OriginalSHA256 {
			return fmt.Errorf("bounded audit: frozen original changed")
		}
	}
	for _, loc := range boundedNodes(selected) {
		docs := map[string]bool{}
		for _, m := range loc.Node.Members {
			for _, id := range f.Members[m] {
				docs[id] = true
			}
		}
		if len(docs) < 2 {
			continue
		}
		md := filepath.Join(root, "repository", filepath.FromSlash(writeback.PrimaryRelPath(loc.Domain, loc.Node.FileSlug, loc.Node.ID)))
		sc, e := writeback.ReadSidecar(md)
		if e != nil {
			return e
		}
		b, e := os.ReadFile(md)
		if e != nil || sc.DocHash != "sha256:"+boundedDigestBytes(b) || len(sc.Nodes) != 1 {
			return fmt.Errorf("bounded audit: invalid shared primary document")
		}
		n := sc.Nodes[0]
		if n.UUID != loc.Node.ID || !reflect.DeepEqual(memberSet(n.Members), memberSet(loc.Node.Members)) || n.SummaryHash != "sha256:"+boundedDigestBytes([]byte(loc.Node.Summary)) || n.DescriptionHash != "sha256:"+boundedDigestBytes([]byte(loc.Node.Description)) {
			return fmt.Errorf("bounded audit: shared primary source/node baseline changed")
		}
	}
	return nil
}

func runBoundedPipeline(ctx context.Context, input boundedPipelineInput, opts boundedPipelineOptions) (*boundedPipelineResult, error) {
	f, e := freezeBoundedPipeline(input)
	if e != nil {
		return nil, e
	}
	if opts.NewBridge == nil || opts.MaterializationClient == nil || opts.RepositoryIdentity == "" || !filepath.IsAbs(opts.PublishPath) || filepath.Clean(opts.PublishPath) != opts.PublishPath {
		return nil, fmt.Errorf("bounded: missing required bridge/materializer/private publication identity")
	}
	if e = boundedSafeExistingDirectory(opts.WorkDir); e != nil {
		return nil, e
	}
	if e = boundedSafeExistingDirectory(filepath.Dir(opts.PublishPath)); e != nil {
		return nil, e
	}
	root := boundedDigest(struct{ Input, Work, Publication, Repository string }{f.Root, opts.WorkDir, opts.PublishPath, opts.RepositoryIdentity})
	c, e := openBoundedCheckpoint(opts.WorkDir, root, opts.Resume, opts.fault)
	if e != nil {
		return nil, e
	}
	defer c.file.Close()
	if c.publication != nil {
		if _, _, _, e = c.pinnedPublication(opts.PublishPath); e != nil {
			return nil, e
		}
	} else if _, statErr := os.Lstat(opts.PublishPath); !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("bounded recovery STOP: existing publication has no independent publication-ready pin")
	}
	r := &boundedResolver{works: map[string]boundedResolvedWork{}, frozen: f}
	// Register all historical calls from authenticated mapping units or later
	// re-created leaf descriptors. Generic descriptor calls are registered when
	// their phase is rebuilt, BEFORE their saved responses are evaluated.
	for _, key := range c.order {
		call := c.calls[key]
		if call.Metadata.Stage == "mapping" {
			meta, e := r.metadata("mapping", call.Metadata.WorkID, call.Metadata.ParentCatalogueRoot, call.Metadata.Attempt, call.Request, call.MappingUnits)
			if e != nil || !reflect.DeepEqual(meta, call.Metadata) {
				return nil, fmt.Errorf("bounded: historical mapping source binding changed")
			}
		}
	}
	bridge, e := opts.NewBridge(r.resolve)
	if e != nil {
		return nil, e
	}
	if bridge == nil || bridge.Identity() != input.Expected.Admission || bridge.ConstructionIdentity() != f.Input.Identity {
		return nil, fmt.Errorf("bounded: bridge profile differs from independent expected identity")
	}
	// WholeInventory performs complete ledger validation itself. Authentication
	// of later-stage historical resolver keys occurs after their plans rebuild.
	if e = boundedReconcileInventoryOnly(ctx, c, bridge); e != nil {
		return nil, e
	}
	acceptedIncidences := 0
	for _, docs := range f.Members {
		acceptedIncidences += len(docs)
	}
	if e = c.freezeInventory(boundedPhaseInventory{Key: "mapping-budget/" + f.Root, Stage: "mapping-budget", ParentRoot: f.Root, MappingUniqueUnits: len(f.Members), AcceptedIncidences: acceptedIncidences, MaximumCalls: 2 * len(f.Members), Ceiling: true}); e != nil {
		return nil, e
	}
	if opts.Resume {
		if e = boundedAuthenticateMappingJournal(ctx, filepath.Join(opts.WorkDir, "mapping", "journal.jsonl"), c, bridge, r); e != nil {
			return nil, e
		}
	}
	mappingJournal := filepath.Join(opts.WorkDir, "mapping", "journal.jsonl")
	mapped, e := incremental.RunStagedMapping(ctx, f.Input, incremental.StagedMappingOptions{StageDir: filepath.Join(opts.WorkDir, "mapping"), Resume: opts.Resume, Admitter: &boundedMappingBridge{bridge, c, r, mappingJournal}})
	if e != nil {
		return nil, e
	}
	if e = boundedAuthenticateMappingJournal(ctx, mappingJournal, c, bridge, r); e != nil {
		return nil, e
	}
	journalBytes, e := os.ReadFile(mappingJournal)
	if e != nil {
		return nil, e
	}
	lines := bytes.Split(bytes.TrimSuffix(journalBytes, []byte{'\n'}), []byte{'\n'})
	var last struct{ Hash string }
	if len(lines) == 0 || json.Unmarshal(lines[len(lines)-1], &last) != nil || last.Hash != mapped.Receipt.JournalHeadSHA256 {
		return nil, fmt.Errorf("bounded: mapping journal changed across completed snapshot")
	}
	forecast, e := boundedForecast(mapped.Extraction, f)
	if e != nil {
		return nil, e
	}
	graph := mapped.Extraction
	descriptionInput, e := boundedStageInput(graph, f, input.Expected, mapped.Receipt.CatalogueSHA256)
	if e != nil {
		return nil, e
	}
	descriptionPlan, e := extract.BuildBoundedDescriptionPlan(descriptionInput)
	if e != nil {
		return nil, e
	}
	leaves, e := boundedPreflightStage(ctx, bridge, r, graph, boundedGraphRoot(graph), descriptionPlan.Works())
	if e != nil {
		return nil, e
	}
	graph, e = boundedRunStage(ctx, bridge, c, descriptionPlan, graph, leaves)
	if e != nil {
		return nil, e
	}
	relationInput, e := boundedStageInput(graph, f, input.Expected, mapped.Receipt.CatalogueSHA256)
	if e != nil {
		return nil, e
	}
	cross, e := boundedRecallCross(ctx, graph, relationInput.Identity.CatalogueSHA256, relationInput.Identity.DescriptionSHA256)
	if e != nil {
		return nil, e
	}
	if e = c.freezeInventory(boundedPhaseInventory{Key: "cross-recall/" + boundedGraphRoot(graph), Stage: "cross-recall", ParentRoot: boundedGraphRoot(graph), Cross: &cross}); e != nil {
		return nil, e
	}
	pairs := extract.BoundedCrossPairs{CatalogueSHA256: cross.CatalogueSHA256, DescriptionSHA256: cross.DescriptionSHA256, Pairs: cross.Pairs}
	pairs.ExpectedSHA256, e = extract.BoundedCrossPairDigest(pairs)
	if e != nil {
		return nil, e
	}
	relationPlan, e := extract.BuildBoundedRelationPlan(relationInput, pairs)
	if e != nil {
		return nil, e
	}
	leaves, e = boundedPreflightStage(ctx, bridge, r, graph, boundedGraphRoot(graph), relationPlan.Works())
	if e != nil {
		return nil, e
	}
	graph, e = boundedRunStage(ctx, bridge, c, relationPlan, graph, leaves)
	if e != nil {
		return nil, e
	}
	graph, verification, e := boundedVerifyRelations(ctx, bridge, c, r, graph, f)
	if e != nil {
		return nil, e
	}
	if e = c.reconcile(ctx, bridge); e != nil {
		return nil, e
	}
	if graph, e = boundedFreshSlugs(ctx, graph); e != nil {
		return nil, e
	}
	finalForecast, e := boundedForecast(graph, f)
	if e != nil || !reflect.DeepEqual(forecast, finalForecast) {
		return nil, fmt.Errorf("bounded: confidence/source forecast changed before materialization")
	}
	result := &boundedPipelineResult{RunSHA256: root, GenerationPath: opts.PublishPath, CatalogueSHA256: cross.CatalogueSHA256, DescriptionSHA256: cross.DescriptionSHA256, Forecast: forecast, Cross: cross, Verification: verification}
	for _, decision := range verification.Decisions {
		if decision.Disposition == "invalid_verdict" {
			result.InvalidUnverified++
		}
	}
	// A ready checkpoint can only replay/authenticate existing work. It then
	// verifies the exact independently pinned private/target generation, audits
	// semantics again, and either completes THAT rename or returns the same pin.
	if c.publication != nil {
		source, manifest, raw, e := c.pinnedPublication(opts.PublishPath)
		if e != nil {
			return nil, e
		}
		if manifest.GraphSHA256 != boundedGraphRoot(graph) {
			return nil, fmt.Errorf("bounded: pinned graph differs from authenticated phase replay")
		}
		if e = boundedAuditGeneration(ctx, source, graph, f, forecast); e != nil {
			return nil, e
		}
		if source != opts.PublishPath {
			if e = boundedPublishGeneration(source, opts.PublishPath); e != nil {
				return nil, e
			}
		}
		result.ManifestSHA256 = boundedDigestBytes(raw)
		return result, nil
	}
	private, e := os.MkdirTemp(filepath.Dir(opts.PublishPath), ".bounded-generation-")
	if e != nil {
		return nil, e
	}
	// On failure retain the private directory as explicit diagnostic evidence;
	// it is never mistaken for a published generation and never mutates live data.
	if e = boundedPathInventory(private, opts.PublishPath, graph, f); e != nil {
		return nil, e
	}
	for _, o := range f.Originals {
		for _, prefix := range []string{"originals", "repository"} {
			p := filepath.Join(private, prefix, filepath.FromSlash(o.Path))
			if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
				return nil, e
			}
			if e = os.WriteFile(p, []byte(o.Content), 0600); e != nil {
				return nil, e
			}
		}
	}
	skills := map[string]bool{}
	for _, doc := range f.Documents {
		skills[doc.Skill] = true
	}
	var metadata []ingest.SkillMeta
	for _, skill := range sortedSet(skills) {
		metadata = append(metadata, ingest.SkillMeta{Name: skill})
	}
	orchestrator, e := pipeline.NewOrchestrator(pipeline.Options{RepositoryIdentity: opts.RepositoryIdentity, Client: opts.MaterializationClient, MinConfidence: capacityMinConfidence, StageTimeout: 30 * time.Minute})
	if e != nil {
		return nil, e
	}
	materialized, materializeErr := orchestrator.MaterializeCompletedExtraction(ctx, filepath.Join(private, "repository"), filepath.Join(private, "knowledge.db"), graph, f.Documents, metadata)
	if materializeErr != nil {
		return nil, materializeErr
	}
	if materialized.Ingest == nil || materialized.Ingest.EdgesSkipped != 0 {
		return nil, fmt.Errorf("bounded: materializer skipped graph edges")
	}
	if e = boundedCheckpointDatabase(ctx, filepath.Join(private, "knowledge.db")); e != nil {
		return nil, e
	}
	if e = boundedAuditGeneration(ctx, private, graph, f, forecast); e != nil {
		return nil, e
	}
	for _, artifact := range []struct {
		name  string
		value any
	}{{"receipts.json", f.Receipts}, {"forecast.json", forecast}, {"cross-recall.json", cross}, {"verification.json", struct {
		*extract.CrossRelationReport
		InvalidUnverified int `json:"invalid_unverified"`
	}{verification, result.InvalidUnverified}}} {
		if e = boundedWriteJSON(filepath.Join(private, artifact.name), artifact.value); e != nil {
			return nil, e
		}
	}
	inv, e := bridge.WholeInventory(ctx)
	if e != nil {
		return nil, e
	}
	var calls []*boundedSavedCall
	for _, key := range c.order {
		calls = append(calls, boundedCopyCall(c.calls[key]))
	}
	if e = boundedWriteJSON(filepath.Join(private, "delivery-checkpoint.json"), struct {
		Root, Head  string
		Calls       []*boundedSavedCall
		Ledger      stagedBridgeInventory
		Inventories map[string]boundedPhaseInventory
	}{c.root, c.workHead(), calls, inv, c.inventories}); e != nil {
		return nil, e
	}
	if e = boundedWriteJSON(filepath.Join(private, "mapping-receipt.json"), struct {
		Receipt incremental.StagedMappingReceipt
		Births  []incremental.StagedMappingBirth
	}{mapped.Receipt, mapped.Births}); e != nil {
		return nil, e
	}
	var hidden [][3]string
	for _, d := range graph.Domains {
		hidden = append(hidden, [3]string{d.Slug, d.Provenance, ""})
		for _, s := range d.Subdomains {
			hidden = append(hidden, [3]string{d.Slug, s.Slug, "subdomain"})
		}
	}
	if e = boundedWriteJSON(filepath.Join(private, "graph.json"), struct {
		Graph  *extract.Result
		Hidden [][3]string
	}{graph, hidden}); e != nil {
		return nil, e
	}
	files, e := boundedGenerationFiles(private)
	if e != nil {
		return nil, e
	}
	manifest := boundedGenerationManifest{boundedGenerationFormat, root, boundedGraphRoot(graph), c.workHead(), files}
	manifestBytes, e := boundedEncode(manifest)
	if e != nil {
		return nil, e
	}
	if e = os.WriteFile(filepath.Join(private, "manifest.json"), manifestBytes, 0600); e != nil {
		return nil, e
	}
	if e = boundedSyncTree(private); e != nil {
		return nil, e
	}
	ready := boundedPublicationReady{RunSHA256: root, ManifestSHA256: boundedDigestBytes(manifestBytes), CheckpointHeadSHA256: c.workHead(), PrivatePath: private, TargetPath: opts.PublishPath}
	if e = c.append(boundedCheckpointEvent{Kind: "publication-ready", Publication: &ready}); e != nil {
		return nil, e
	}
	if opts.fault != nil {
		if e = opts.fault("before_publish"); e != nil {
			return nil, e
		}
	}
	if e = boundedPublishGeneration(private, opts.PublishPath); e != nil {
		return nil, e
	}
	if opts.fault != nil {
		if e = opts.fault("after_publish"); e != nil {
			return nil, e
		}
	}
	result.ManifestSHA256 = boundedDigestBytes(manifestBytes)
	return result, nil
}

func jsonUnmarshalCanonical(b []byte, v any) error {
	// Own checkpoint/manifest encodings are canonical, so aliases, duplicate
	// keys, changed casing and trailing fields cannot be silently normalized.
	if e := json.Unmarshal(b, v); e != nil {
		return e
	}
	canonical, e := boundedEncode(v)
	if e != nil || !bytes.Equal(b, canonical) {
		return fmt.Errorf("bounded: noncanonical generation manifest")
	}
	return nil
}

// Pre-replay whole-inventory bijection does not need semantic leaf seals, but
// does authenticate all saved actual receipts. ResolveMembers is then checked
// by AuthenticateObserved as their immutable phase descriptors are rebuilt.
func boundedReconcileInventoryOnly(ctx context.Context, c *boundedCheckpoint, bridge boundedPipelineBridge) error {
	inv, e := bridge.WholeInventory(ctx)
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, record := range inv.Records {
		k := boundedCallKey(record.Metadata)
		saved := c.calls[k]
		if seen[k] || saved == nil || saved.Result == nil || !record.Complete || record.Rejection != "" || saved.Binding != record.Binding || saved.Result.ReceiptID != record.ReceiptID || !reflect.DeepEqual(saved.Metadata, record.Metadata) || boundedDigest(saved.Request) != record.LogicalRequestSHA256 || record.ResponseContentSHA256 != boundedDigestBytes([]byte(saved.Result.Response.Text)) || record.InputTokens != saved.Result.Response.InputTokens || record.OutputTokens != saved.Result.Response.OutputTokens || record.FinishReason != saved.Result.Response.FinishReason || boundedCompleteResponse(*saved.Result, saved.Binding) != nil {
			return fmt.Errorf("bounded recovery STOP: whole-ledger orphan/unknown/local checkpoint gap")
		}
		seen[k] = true
	}
	if len(seen) != len(c.calls) {
		return fmt.Errorf("bounded recovery STOP: unobserved local intent or missing ledger segment")
	}
	return nil
}

// Read-only compatibility boundary with #80's versioned journal. #80 remains
// authoritative for its hash chain, canonical pending prefixes, response parsing,
// accepted states and DB projection. This reader only binds EVERY historical
// mapping call/response to the independently authenticated delivery checkpoint
// BEFORE #80 may accept it or dispatch more work. It never edits/supplements #80.
func boundedAuthenticateMappingJournal(ctx context.Context, p string, c *boundedCheckpoint, bridge boundedPipelineBridge, r *boundedResolver) error {
	b, e := os.ReadFile(p)
	if e != nil {
		return e
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		return fmt.Errorf("bounded recovery STOP: mapping journal incomplete tail")
	}
	var prepared *struct {
		Call    incremental.StagedMappingCall
		Wire    []byte
		Binding incremental.StagedMappingWireBinding
	}
	intent := false
	seen := map[string]bool{}
	for _, line := range bytes.Split(b[:len(b)-1], []byte{'\n'}) {
		var record struct {
			Event struct {
				Kind     string
				Prepared *struct {
					Call    incremental.StagedMappingCall
					Wire    []byte
					Binding incremental.StagedMappingWireBinding
				}
				Response *incremental.StagedMappingDispatchResult
			}
		}
		if e = json.Unmarshal(line, &record); e != nil {
			return e
		}
		event := record.Event
		switch event.Kind {
		case "prepared":
			prepared = event.Prepared
			intent = false
		case "intent":
			if prepared == nil {
				return fmt.Errorf("bounded: mapping intent without prepared call")
			}
			intent = true
		case "response":
			if prepared == nil || !intent || event.Response == nil {
				return fmt.Errorf("bounded: mapping response without call")
			}
			call := prepared.Call
			meta, e := r.metadata("mapping", call.BatchID, call.CatalogueSHA256, call.Attempt, call.Request, call.Units)
			if e != nil {
				return e
			}
			key := boundedCallKey(meta)
			saved := c.calls[key]
			if seen[key] || saved == nil || saved.Result == nil || !reflect.DeepEqual(saved.Request, call.Request) || !reflect.DeepEqual(saved.Metadata, meta) || !reflect.DeepEqual(saved.MappingUnits, call.Units) || saved.Binding != prepared.Binding || saved.WireSHA256 != boundedDigestBytes(prepared.Wire) || !reflect.DeepEqual(*saved.Result, *event.Response) {
				return fmt.Errorf("bounded recovery STOP: mapping journal/delivery checkpoint mismatch")
			}
			expectedInventory := boundedMappingNextInventory(*saved)
			if actualInventory, ok := c.inventories[expectedInventory.Key]; !ok || boundedDigest(actualInventory) != boundedDigest(expectedInventory) {
				return fmt.Errorf("bounded recovery STOP: historical mapping-next inventory changed")
			}
			if c.authenticated[key] != c.authDigest(saved, bridge) {
				if _, e = bridge.AuthenticateObserved(ctx, stagedBridgeObservedExpectation{Members: saved.MappingUnits, Request: saved.Request, Metadata: meta, Binding: saved.Binding, ReceiptID: saved.Result.ReceiptID, SavedResponse: saved.Result.Response, ResponseSHA256: boundedDigest(saved.Result.Response)}); e != nil {
					return e
				}
			}
			seen[key] = true
			c.replayed[key] = true
			c.authenticated[key] = c.authDigest(saved, bridge)
			intent = false
		}
	}
	if intent {
		return fmt.Errorf("bounded recovery STOP: mapping INTENT has no RESPONSE; local checkpoint gap requires explicit reconciliation")
	}
	for k, saved := range c.calls {
		if saved.Metadata.Stage == "mapping" && !seen[k] {
			return fmt.Errorf("bounded recovery STOP: admitted mapping response missing from #80 journal")
		}
	}
	return nil
}

func boundedCheckpointDatabase(ctx context.Context, path string) error {
	db, e := storage.OpenSQLite(path, storage.SQLiteOptions{ExistingOnly: true, PreserveJournalMode: true})
	if e != nil {
		return e
	}
	var busy, frames, done int
	e = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done)
	var mode string
	if e == nil {
		e = db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode)
		if e == nil && mode != "delete" {
			e = fmt.Errorf("bounded: immutable DB snapshot journal mode did not settle")
		}
	}
	e = errors.Join(e, db.Close())
	if e != nil {
		return e
	}
	if busy != 0 || frames != done {
		return fmt.Errorf("bounded: DB WAL checkpoint incomplete")
	}
	return nil
}

func boundedRequireDetails(doc *dktypes.AnnotatedDocument) error {
	if doc == nil {
		return fmt.Errorf("bounded: nil accepted document")
	}
	for _, item := range doc.Items {
		if strings.TrimSpace(item.Detail) == "" {
			return fmt.Errorf("bounded: current materializer requires nonempty Detail before any spend")
		}
	}
	return nil
}
