package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type stagedPlan struct {
	Call    StagedMappingCall
	Book    *aliasBook
	Units   []*alignUnit
	Sources map[string][]*alignUnit
}

// stagedRecallQuery 只投影首个规范来源；原内容不被截断或清洗。
// Quote each literal token so FTS operators, quotes and punctuation cannot become query syntax.
func stagedRecallQuery(u stagedUnit) string {
	if len(u.Sources) == 0 {
		return ""
	}
	it := u.Sources[0].Item
	r := []rune(it.Content)
	if len(r) > 40 {
		r = r[:40]
	}
	seen := map[string]bool{}
	var parts []string
	for _, s := range []string{it.Name, string(r)} {
		for _, token := range strings.Fields(s) {
			if !seen[token] {
				seen[token] = true
				parts = append(parts, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
			}
		}
	}
	return strings.Join(parts, " OR ")
}

func (s *stagedSession) recall(ctx context.Context, units []stagedUnit) ([]dktypes.Node, []dktypes.Node, []dktypes.Node, error) {
	index := map[string]dktypes.Node{}
	for _, n := range s.state.Nodes {
		index[n.ID] = n
	}
	selected := map[string]bool{}
	for _, u := range units {
		q := stagedRecallQuery(u)
		if q == "" {
			continue
		}
		for _, tier := range []struct {
			labels string
			k      int
		}{{"'Entity','Concept'", 15}, {"'Subdomain'", 4}, {"'Domain'", 2}} {
			// 限制前过滤标签并按固定身份打破同分；排序截断后的 hits 无法修复边界同分。
			// Sorting only returned hits would lose deterministic cutoff ties.
			rows, e := s.db.Conn().QueryContext(ctx, `SELECT nodes.id FROM nodes_fts JOIN nodes ON nodes.rowid=nodes_fts.rowid WHERE nodes_fts MATCH ? AND nodes.label IN (`+tier.labels+`) ORDER BY nodes_fts.rank, nodes.id COLLATE BINARY LIMIT ?`, q, tier.k)
			if e != nil {
				return nil, nil, nil, fmt.Errorf("staged mapping: literal FTS recall: %w", e)
			}
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					rows.Close()
					return nil, nil, nil, e
				}
				if _, ok := index[id]; !ok {
					rows.Close()
					return nil, nil, nil, fmt.Errorf("staged mapping: unknown FTS node")
				}
				selected[id] = true
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, nil, nil, e
			}
		}
	}
	// 必须有且仅有一个父层；不以名字猜测或退回全量分类目录。
	// Ancestor closure adds every required parent without dropping retrieved hits.
	find := func(label dktypes.Label, d, sub string) (string, error) {
		var id string
		for _, n := range s.state.Nodes {
			if n.Label == label && n.Domain == d && n.Subdomain == sub {
				if id != "" {
					return "", fmt.Errorf("staged mapping: ambiguous ancestor")
				}
				id = n.ID
			}
		}
		if id == "" {
			return "", fmt.Errorf("staged mapping: missing ancestor")
		}
		return id, nil
	}
	var initial []string
	for id := range selected {
		initial = append(initial, id)
	}
	sort.Strings(initial)
	for _, id := range initial {
		n := index[id]
		if n.Label == dktypes.LabelEntity || n.Label == dktypes.LabelConcept {
			p, e := find(dktypes.LabelSubdomain, n.Domain, n.Subdomain)
			if e != nil {
				return nil, nil, nil, e
			}
			selected[p] = true
		}
		if n.Label != dktypes.LabelDomain {
			p, e := find(dktypes.LabelDomain, n.Domain, "")
			if e != nil {
				return nil, nil, nil, e
			}
			selected[p] = true
		}
	}
	var nodes, subs, doms []dktypes.Node
	for _, n := range s.state.Nodes {
		if !selected[n.ID] {
			continue
		}
		switch n.Label {
		case dktypes.LabelEntity, dktypes.LabelConcept:
			nodes = append(nodes, n)
		case dktypes.LabelSubdomain:
			subs = append(subs, n)
		case dktypes.LabelDomain:
			doms = append(doms, n)
		}
	}
	if len(nodes) > 480 || len(subs) > 64 || len(doms) > 32 {
		return nil, nil, nil, ErrStagedMappingOverflow
	}
	return nodes, subs, doms, nil
}

func (s *stagedSession) preparePlan(ctx context.Context, batch []stagedUnit, attempt int, feedback string) (*stagedPlan, error) {
	if len(batch) < 1 || len(batch) > 32 {
		return nil, ErrStagedMappingOverflow
	}
	// 每单元最多一个 decision 和 group，各自规范编码≤2048，含全部分隔符和顶层语法。
	// This conservative bound usually admits at most seven, never claims all 32 fit.
	if len(batch)*4096+len(`{"decisions":[],"groups":[]}`)+2*(len(batch)-1) > stagedVisibleWork {
		return nil, ErrStagedMappingOverflow
	}
	nodes, subs, doms, e := s.recall(ctx, batch)
	if e != nil {
		return nil, e
	}
	book := &aliasBook{nodeByAlias: map[string]*dktypes.Node{}, subByAlias: map[string]*dktypes.Node{}, domByAlias: map[string]*dktypes.Node{}}
	type candidate struct {
		Alias     string        `json:"alias"`
		Label     dktypes.Label `json:"label"`
		Name      string        `json:"name"`
		Summary   string        `json:"summary"`
		Domain    string        `json:"domain,omitempty"`
		Subdomain string        `json:"subdomain,omitempty"`
	}
	domAlias := map[string]string{}
	subAlias := map[string]string{}
	var dc, sc, nc []candidate
	for i := range doms {
		n := doms[i]
		a := fmt.Sprintf("dom#%d", i+1)
		book.domByAlias[a] = &n
		domAlias[n.Domain] = a
		dc = append(dc, candidate{Alias: a, Label: n.Label, Name: n.Name, Summary: n.Summary})
	}
	for i := range subs {
		n := subs[i]
		a := fmt.Sprintf("sd#%d", i+1)
		book.subByAlias[a] = &n
		subAlias[n.Domain+"\x00"+n.Subdomain] = a
		sc = append(sc, candidate{Alias: a, Label: n.Label, Name: n.Name, Summary: n.Summary, Domain: domAlias[n.Domain]})
	}
	for i := range nodes {
		n := nodes[i]
		a := fmt.Sprintf("n#%d", i+1)
		book.nodeByAlias[a] = &n
		nc = append(nc, candidate{a, n.Label, n.Name, n.Summary, domAlias[n.Domain], subAlias[n.Domain+"\x00"+n.Subdomain]})
	}
	// 先复制来源，再复用 flattenUnits；其首条 source 指针会被共享正文聚合修改。
	// Rendering uses original incidences, never the aggregated helper projection.
	var docs []*dktypes.AnnotatedDocument
	for _, u := range batch {
		for _, src := range u.Sources {
			it := src.Item
			if it.SourceSpan != nil {
				span := *it.SourceSpan
				it.SourceSpan = &span
			}
			docs = append(docs, &dktypes.AnnotatedDocument{Skill: src.Skill, FilePath: src.FilePath, Items: []dktypes.AnnotatedItem{it}})
		}
	}
	flat, sources := flattenUnits(docs)
	payload := struct {
		Units                      []stagedUnit `json:"units"`
		Nodes, Subdomains, Domains []candidate
		Feedback                   string `json:"feedback,omitempty"`
	}{batch, nc, sc, dc, feedback}
	raw, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	system := alignSystemPrompt + "\n每个 unit 的 Sources 是全部独立来源，必须完整考虑 Name、Content、Detail；不得只读首条。生成的非空名称规范JSON字符串编码≤256字节，summary非空且编码≤1024字节；每个decision/group规范对象≤2048字节。每单元恰好一条decision，groups不多于单元，每组必须被引用。只使用提供的alias。"
	ids := make([]string, len(batch))
	for i, u := range batch {
		ids[i] = u.ID
	}
	parent := stagedHash(s.state)
	key := stagedHash(struct {
		Parent string
		Units  []string
	}{parent, ids})
	return &stagedPlan{StagedMappingCall{BatchID: key, CatalogueSHA256: parent, Attempt: attempt, Units: ids, Request: llm.CompleteRequest{System: system, User: string(raw), MaxTokens: stagedMaxOutput}}, book, flat, sources}, nil
}

func stagedEncodedWithin(v any, limit int) bool {
	raw, e := json.Marshal(v)
	return e == nil && len(raw) <= limit
}

func stagedValidateOutput(raw string, plan *stagedPlan) (*alignLLMOutput, map[string]*alignGroup, error) {
	var out alignLLMOutput
	if stagedDecode([]byte(raw), &out) != nil {
		return nil, nil, fmt.Errorf("invalid schema")
	}
	if len(out.Decisions) != len(plan.Units) || len(out.Groups) > len(plan.Units) || !stagedEncodedWithin(out, stagedVisibleWork) {
		return nil, nil, fmt.Errorf("invalid coverage or output bound")
	}
	check := func(name, summary string) bool {
		return stagedEncodedWithin(name, 256) && stagedEncodedWithin(summary, 1024)
	}
	for _, d := range out.Decisions {
		if !stagedEncodedWithin(d, 2048) || !check(d.NodeName, d.Summary) || !stagedEncodedWithin(d.Domain, 256) || !stagedEncodedWithin(d.Subdomain, 256) {
			return nil, nil, fmt.Errorf("decision bound")
		}
		if (d.Action == "attach" || d.Action == "new_subdomain") && strings.TrimSpace(d.Summary) == "" {
			return nil, nil, fmt.Errorf("summary required")
		}
	}
	used := map[string]bool{}
	for _, d := range out.Decisions {
		if d.Action == "fuse" {
			used[d.Group] = true
		}
	}
	for _, g := range out.Groups {
		if !used[g.Group] || !stagedEncodedWithin(g, 2048) || !check(g.NodeName, g.Summary) || !stagedEncodedWithin(g.Domain, 256) || !stagedEncodedWithin(g.Subdomain, 256) || strings.TrimSpace(g.Summary) == "" {
			return nil, nil, fmt.Errorf("group bound or unused group")
		}
	}
	groups, e := validateAlignOutput(&out, plan.Units, plan.Book)
	if e != nil {
		return nil, nil, fmt.Errorf("invalid schema")
	}
	for _, d := range out.Decisions {
		if !check(d.NodeName, d.Summary) || ((d.Action == "attach" || d.Action == "new_subdomain") && strings.TrimSpace(d.Summary) == "") {
			return nil, nil, fmt.Errorf("normalized field bound")
		}
	}
	for _, g := range out.Groups {
		if !check(g.NodeName, g.Summary) || strings.TrimSpace(g.Summary) == "" {
			return nil, nil, fmt.Errorf("normalized group bound")
		}
	}
	sort.Slice(out.Decisions, func(i, j int) bool { return out.Decisions[i].Unit < out.Decisions[j].Unit })
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Group < out.Groups[j].Group })
	// Sorting a slice invalidates pointers returned by the legacy validator.
	groups = map[string]*alignGroup{}
	for i := range out.Groups {
		groups[out.Groups[i].Group] = &out.Groups[i]
	}
	return &out, groups, nil
}

func (s *stagedSession) finishResponse(ctx context.Context, all []stagedUnit) error {
	p := s.attempt
	if p == nil || s.response == nil || p.Call.CatalogueSHA256 != stagedHash(s.state) {
		return fmt.Errorf("staged mapping: response parent mismatch")
	}
	batch, e := stagedPendingBatch(s.state, all, p.Call.Units)
	if e != nil {
		return e
	}
	feedback := ""
	if p.Call.Attempt == 2 {
		feedback = "输出结构校验失败，请仅按 schema 修正。"
	}
	plan, e := s.preparePlan(ctx, batch, p.Call.Attempt, feedback)
	if e != nil {
		return e
	}
	if !reflectStagedCallEqual(plan.Call, p.Call) {
		return fmt.Errorf("staged mapping: frozen request mismatch")
	}
	if !stagedCompleteResponse(s.response, p.Binding) {
		return ErrStagedMappingUncertain
	}
	out, groups, e := stagedValidateOutput(s.response.Response.Text, plan)
	if e != nil {
		if e = s.append(stagedEvent{Kind: "invalid"}); e != nil {
			return e
		}
		s.phase = "invalid"
		return nil
	}
	next, e := s.materializeBatch(ctx, plan, out, groups)
	if e != nil {
		return e
	}
	if e = s.append(stagedEvent{Kind: "accepted", State: &next, Output: out}); e != nil {
		return e
	}
	s.state = next
	s.accepted++
	s.acceptedHead = s.head
	s.attempt = nil
	s.response = nil
	s.phase = "accepted"
	if e = s.project(ctx); e != nil {
		return e
	}
	if e = s.append(stagedEvent{Kind: "committed"}); e != nil {
		return e
	}
	s.phase = ""
	return nil
}

func reflectStagedCallEqual(a, b StagedMappingCall) bool { return stagedHash(a) == stagedHash(b) }

func (s *stagedSession) materializeBatch(ctx context.Context, p *stagedPlan, out *alignLLMOutput, groups map[string]*alignGroup) (stagedCatalogue, error) {
	var next stagedCatalogue
	raw, _ := json.Marshal(s.state)
	_ = json.Unmarshal(raw, &next)
	ao := &alignOutcome{pending: map[string]*pendingNode{}, unitsByID: map[string]*alignUnit{}}
	for _, u := range p.Units {
		ao.unitsByID[u.ID] = u
	}
	rs := newRunState()
	a := &aligner{st: newStores(s.db)}
	// materialize 自身为 warn/skip 风格；阶段适配器必须将任何警告或覆盖缺失转为整体失败。
	// The stage never invokes legacy align/judge or accepts its partial-result degradation.
	a.materialize(ctx, ao, out.Decisions, groups, p.Book, p.Sources, rs)
	if len(rs.warningsSnapshot()) > 0 {
		return next, fmt.Errorf("staged mapping: materialization warning rejected")
	}
	if e := normalizePendingForPersistence(ao); e != nil {
		return next, e
	}
	idx := map[string]int{}
	for i, n := range next.Nodes {
		idx[n.ID] = i
	}
	covered := map[string]string{}
	for _, id := range sortedPendingKeys(ao) {
		pn := ao.pending[id]
		for _, m := range pn.NewMembers {
			if covered[m] != "" {
				return next, fmt.Errorf("staged mapping: duplicate materialized member")
			}
			covered[m] = id
		}
		ensureLayer := func(id string, label dktypes.Label, name, d, sub string) error {
			if n, ok := idx[id]; ok {
				old := next.Nodes[n]
				if old.Label != label || old.Domain != d || old.Subdomain != sub || (name != "" && old.Name != name) {
					return fmt.Errorf("staged mapping: ambiguous generated layer")
				}
				return nil
			}
			if name == "" {
				return fmt.Errorf("staged mapping: absent generated layer name")
			}
			idx[id] = len(next.Nodes)
			next.Nodes = append(next.Nodes, dktypes.Node{ID: id, Label: label, Name: name, Summary: name, Domain: d, Subdomain: sub, Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred})
			return nil
		}
		if e := ensureLayer(domainNodeID(pn.Domain), dktypes.LabelDomain, pn.DomainName, pn.Domain, ""); e != nil {
			return next, e
		}
		if e := ensureLayer(subdomainNodeID(pn.Domain, pn.Subdomain), dktypes.LabelSubdomain, pn.SubdomainName, pn.Domain, pn.Subdomain); e != nil {
			return next, e
		}
		if i, ok := idx[id]; ok {
			if pn.IsNew {
				return next, fmt.Errorf("staged mapping: unexpected UUID collision")
			}
			if next.Nodes[i].Domain != pn.Domain || next.Nodes[i].Subdomain != pn.Subdomain {
				return next, fmt.Errorf("staged mapping: moved pinned parent")
			}
			for _, m := range pn.NewMembers {
				if u := ao.unitsByID[m]; u != nil && u.Confidence < next.Nodes[i].Confidence {
					next.Nodes[i].Confidence = u.Confidence
				}
			}
		} else {
			if !pn.IsNew {
				return next, fmt.Errorf("staged mapping: missing pinned merge target")
			}
			confidence := 1.0
			for _, m := range pn.NewMembers {
				if u := ao.unitsByID[m]; u != nil && u.Confidence < confidence {
					confidence = u.Confidence
				}
			}
			idx[id] = len(next.Nodes)
			next.Nodes = append(next.Nodes, dktypes.Node{ID: id, Label: dktypes.Label(pn.Label), Name: pn.Name, Summary: pn.Summary, Confidence: confidence, Domain: pn.Domain, Subdomain: pn.Subdomain, Provenance: dktypes.ProvenanceLLMInferred})
			members := append([]string(nil), pn.NewMembers...)
			sort.Strings(members)
			next.Births = append(next.Births, StagedMappingBirth{id, domainNodeID(pn.Domain), subdomainNodeID(pn.Domain, pn.Subdomain), members})
		}
	}
	if len(covered) != len(p.Units) {
		return next, fmt.Errorf("staged mapping: incomplete materialized coverage")
	}
	for _, u := range p.Units {
		if covered[u.ID] == "" {
			return next, fmt.Errorf("staged mapping: missing materialized unit")
		}
		next.Done = append(next.Done, u.ID)
	}
	want := map[string]bool{}
	for member, srcs := range p.Sources {
		for _, src := range srcs {
			want[covered[member]+"\x00"+member+"\x00"+src.FilePath] = true
		}
	}
	for _, row := range ao.sourceRows {
		k := row.NodeUUID + "\x00" + row.MemberID + "\x00" + row.FilePath
		if !want[k] {
			return next, fmt.Errorf("staged mapping: unexpected source row")
		}
		delete(want, k)
		next.Sources = append(next.Sources, row)
	}
	if len(want) > 0 {
		return next, fmt.Errorf("staged mapping: missing source incidence")
	}
	// 完整累计 members 从来源行重建，birth members 永不改写。
	// Stable source order also fixes provenance and catalogue digests across resume.
	sort.Slice(next.Nodes, func(i, j int) bool { return next.Nodes[i].ID < next.Nodes[j].ID })
	sort.Slice(next.Births, func(i, j int) bool { return next.Births[i].UUID < next.Births[j].UUID })
	sort.Strings(next.Done)
	sort.Slice(next.Sources, func(i, j int) bool {
		a, b := next.Sources[i], next.Sources[j]
		if a.NodeUUID != b.NodeUUID {
			return a.NodeUUID < b.NodeUUID
		}
		if a.MemberID != b.MemberID {
			return a.MemberID < b.MemberID
		}
		return a.FilePath < b.FilePath
	})
	for i := range next.Nodes {
		skills := map[string]bool{}
		n := &next.Nodes[i]
		for _, r := range next.Sources {
			if r.NodeUUID == n.ID {
				skills[r.Skill] = true
			}
		}
		// Mapping leaves retain all contributing skills.
		var refs []string
		for skill := range skills {
			refs = append(refs, skill)
		}
		sort.Strings(refs)
		if len(refs) > 0 {
			n.SourceRefs = strings.Join(refs, ",")
		}
	}
	return next, nil
}

func (s *stagedSession) result(in StagedMappingInput) *StagedMappingResult {
	r := &StagedMappingResult{Extraction: &extract.Result{}, Documents: in.Documents, Sources: s.state.Sources, Births: s.state.Births, Receipt: StagedMappingReceipt{in.Identity, stagedHash(s.state), s.head, s.accepted}}
	memberSkills := map[string]map[string]bool{}
	members := map[string]map[string]bool{}
	for _, src := range s.state.Sources {
		if members[src.NodeUUID] == nil {
			members[src.NodeUUID] = map[string]bool{}
			memberSkills[src.NodeUUID] = map[string]bool{}
		}
		members[src.NodeUUID][src.MemberID] = true
		memberSkills[src.NodeUUID][src.Skill] = true
	}
	for _, d := range s.state.Nodes {
		if d.Label != dktypes.LabelDomain {
			continue
		}
		domain := extract.Domain{Name: d.Name, Summary: d.Summary, Slug: d.Domain, Provenance: string(d.Provenance)}
		domainSkills := map[string]bool{}
		for _, sd := range s.state.Nodes {
			if sd.Label != dktypes.LabelSubdomain || sd.Domain != d.Domain {
				continue
			}
			sub := extract.Subdomain{Name: sd.Name, Summary: sd.Summary, Slug: sd.Subdomain}
			for _, n := range s.state.Nodes {
				if (n.Label != dktypes.LabelEntity && n.Label != dktypes.LabelConcept) || n.Domain != d.Domain || n.Subdomain != sd.Subdomain {
					continue
				}
				node := extract.Node{ID: n.ID, Label: string(n.Label), Name: n.Name, Summary: n.Summary, Confidence: n.Confidence}
				for m := range members[n.ID] {
					node.Members = append(node.Members, m)
				}
				for skill := range memberSkills[n.ID] {
					node.SourceSkills = append(node.SourceSkills, skill)
					domainSkills[skill] = true
				}
				sort.Strings(node.Members)
				sort.Strings(node.SourceSkills)
				if n.Label == dktypes.LabelEntity {
					sub.Entities = append(sub.Entities, node)
				} else {
					sub.Concepts = append(sub.Concepts, node)
				}
			}
			domain.Subdomains = append(domain.Subdomains, sub)
		}
		for skill := range domainSkills {
			domain.SourceSkills = append(domain.SourceSkills, skill)
		}
		sort.Strings(domain.SourceSkills)
		r.Extraction.Domains = append(r.Extraction.Domains, domain)
	}
	return r
}
