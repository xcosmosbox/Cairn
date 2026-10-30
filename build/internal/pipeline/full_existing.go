package pipeline

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// fullExistingOutcome只描述实际保留后的身份和受影响文件，不参与语义推断。
// Exact source/member ownership is evidence of identity; names are never a fallback.
type fullExistingOutcome struct {
	remap              map[string]string
	docs               map[string]bool
	refresh            map[string]bool
	empty              map[string]bool
	updates            []writeback.NodeUpdate
	conflicts          []string
	semanticUnverified []string
}

func fullClosedDatabase(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return fmt.Errorf("pipeline: cannot replace DB with existing %s sidecar; close/checkpoint existing connections first", suffix)
		}
	}
	return nil
}

// mergeFullExisting在新临时库中承接人工和范围外对象；旧库始终不写。
// Copy only a closed main file: opening a WAL DB read-only can itself create sidecars.
// All mutations target the disposable candidate and remain behind publication fences.
func mergeFullExisting(ctx context.Context, db *storage.DB, path, repoPath, identity string, sources map[string][]ingest.MemberSource, represented map[string]bool) (*fullExistingOutcome, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := fullClosedDatabase(path); err != nil {
		return nil, err
	}
	oldFile, err := os.CreateTemp(filepath.Dir(path), ".cairn-full-old-*.db")
	if err != nil {
		return nil, err
	}
	oldPath := oldFile.Name()
	defer os.Remove(oldPath)
	defer os.Remove(oldPath + "-wal")
	defer os.Remove(oldPath + "-shm")
	source, err := os.Open(path)
	if err != nil {
		oldFile.Close()
		return nil, err
	}
	_, copyErr := io.Copy(oldFile, source)
	source.Close()
	closeErr := oldFile.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := fullClosedDatabase(path); err != nil {
		return nil, err
	}
	old, err := storage.OpenReadOnly(oldPath)
	if err != nil {
		return nil, fmt.Errorf("pipeline: existing DB: %w", err)
	}
	defer old.Close()
	priorIdentity, err := storage.NewRepositoryIdentityRepo(old).Get(ctx)
	if err != nil {
		return nil, err
	}
	if priorIdentity != "" && priorIdentity != identity {
		return nil, fmt.Errorf("pipeline: repository ownership cannot change from %q to %q", priorIdentity, identity)
	}
	oldNodes, err := storage.NewNodeRepo(old).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	oldSources, err := storage.NewNodeSourceRepo(old).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	oldEdges, err := storage.NewEdgeRepo(old).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	newSources, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	newEdges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	newNodes, err := storage.NewNodeRepo(db).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	outcome := &fullExistingOutcome{remap: map[string]string{}, docs: map[string]bool{}, refresh: map[string]bool{}, empty: map[string]bool{}}
	// Accepted empty documents are represented too; no member row is not a failed source.
	for file := range represented {
		outcome.docs[file] = true
	}
	for _, ss := range sources {
		for _, s := range ss {
			outcome.docs[s.FilePath] = true
		}
	}
	type memberKey struct{ skill, file, member string }
	owners := map[memberKey]map[string]bool{}
	oldByID := map[string]*dktypes.Node{}
	byNode := map[string][]storage.NodeSource{}
	for _, n := range oldNodes {
		oldByID[n.ID] = n
	}
	for _, s := range oldSources {
		key := memberKey{s.Skill, s.FilePath, s.MemberID}
		if owners[key] == nil {
			owners[key] = map[string]bool{}
		}
		owners[key][s.NodeUUID] = true
		byNode[s.NodeUUID] = append(byNode[s.NodeUUID], s)
	}
	targets := map[string]map[string]bool{}
	for _, s := range newSources {
		for owner := range owners[memberKey{s.Skill, s.FilePath, s.MemberID}] {
			if oldByID[owner] == nil {
				return nil, fmt.Errorf("pipeline: existing source/member ownership points to missing node %s; prior state retained", owner)
			}
			if targets[s.NodeUUID] == nil {
				targets[s.NodeUUID] = map[string]bool{}
			}
			targets[s.NodeUUID][owner] = true
		}
	}
	claimed := map[string]string{}
	for id, ids := range targets {
		if len(ids) != 1 {
			return nil, fmt.Errorf("pipeline: ambiguous existing source/member ownership for candidate %s", id)
		}
		for owner := range ids {
			if previous := claimed[owner]; previous != "" && previous != id {
				return nil, fmt.Errorf("pipeline: ambiguous split of existing UUID %s", owner)
			}
			claimed[owner] = id
			outcome.remap[id] = owner
		}
	}
	// R8不能使新语义因旧人工正文原样保留而获得虚假coverage。
	// New members need their own representation; refuse unsupported credit before publishing.
	for _, s := range newSources {
		owner := outcome.remap[s.NodeUUID]
		if owner == "" {
			owner = s.NodeUUID
		}
		if n := oldByID[owner]; n != nil && n.Provenance == dktypes.ProvenanceHumanCurated {
			knownMember := false
			for _, prior := range byNode[owner] {
				if prior.Skill == s.Skill && prior.MemberID == s.MemberID {
					knownMember = true
				}
			}
			if !knownMember {
				return nil, fmt.Errorf("pipeline: human_curated UUID %s cannot certify new source/member %s/%s; prior state retained", owner, s.FilePath, s.MemberID)
			}
		}
	}
	for _, n := range oldNodes {
		if n.Provenance == dktypes.ProvenanceHumanCurated && claimed[n.ID] == "" {
			for _, s := range byNode[n.ID] {
				if outcome.docs[s.FilePath] {
					return nil, fmt.Errorf("pipeline: unmatched human_curated UUID %s in rebuilt source %s; no exact member identity, prior state retained", n.ID, s.FilePath)
				}
			}
		}
	}
	// 无来源普通旧行不被当作clean对象；缺失文件是明确删除，权限错误是unknown。
	// An extant, unrepresented source is untouched (including a failed source sibling).
	untouched := map[string]bool{}
	for _, s := range oldSources {
		if !outcome.docs[s.FilePath] {
			exists := true
			if repoPath != "" {
				abs, e := writeback.ValidateRepositoryPath(repoPath, s.FilePath)
				if e != nil {
					return nil, e
				}
				_, e = os.Stat(abs)
				if os.IsNotExist(e) {
					exists = false
				} else if e != nil {
					return nil, fmt.Errorf("pipeline: unknown previous source %s: %w", s.FilePath, e)
				}
			}
			if exists {
				untouched[s.FilePath] = true
			}
		}
	}
	keep := map[string]bool{}
	protected := map[string]bool{}
	clean := map[string]bool{}
	for _, n := range oldNodes {
		if n.Provenance == dktypes.ProvenanceHumanCurated {
			keep[n.ID] = true
			protected[n.ID] = true
		}
		represented := false
		outside := false
		for _, s := range byNode[n.ID] {
			if outcome.docs[s.FilePath] {
				represented = true
			}
			if untouched[s.FilePath] {
				outside = true
				keep[n.ID] = true
			}
		}
		clean[n.ID] = outside && !represented
	}
	edgeKey := func(e *dktypes.Edge) string { return e.SourceID + "\x00" + e.TargetID + "\x00" + string(e.Kind) }
	humanKeys := map[string]bool{}
	var maxOldEdge int64
	for _, e := range oldEdges {
		if e.ID > maxOldEdge {
			maxOldEdge = e.ID
		}
		if e.Provenance == dktypes.ProvenanceHumanCurated {
			humanKeys[edgeKey(e)] = true
			keep[e.SourceID] = true
			keep[e.TargetID] = true
		}
	}
	for _, e := range newEdges {
		translated := *e
		if id := outcome.remap[e.SourceID]; id != "" {
			translated.SourceID = id
		}
		if id := outcome.remap[e.TargetID]; id != "" {
			translated.TargetID = id
		}
		if !humanKeys[edgeKey(&translated)] {
			continue
		}
		applied := false
		for _, prior := range oldEdges {
			if edgeKey(prior) == edgeKey(&translated) && prior.Description == e.Description && prior.Confidence == e.Confidence {
				applied = true
			}
		}
		if !applied {
			outcome.conflicts = append(outcome.conflicts, fmt.Sprintf("human_curated relation %s -> %s (%s) retained; generated payload not applied", translated.SourceID, translated.TargetID, translated.Kind))
		}
	}
	// 层级祖先只随实际保留端点承接，不能顺语义边扩散成复制整个旧图。
	for changed := true; changed; {
		changed = false
		for _, e := range oldEdges {
			if fullHierarchyEdge(e, oldByID) && keep[e.TargetID] && !keep[e.SourceID] {
				keep[e.SourceID] = true
				changed = true
			}
		}
	}
	newIDs := map[string]bool{}
	for _, n := range newNodes {
		id := n.ID
		if target := outcome.remap[id]; target != "" {
			id = target
		}
		if newIDs[id] {
			return nil, fmt.Errorf("pipeline: candidate UUID collision after existing identity translation: %s", id)
		}
		newIDs[id] = true
		if oldNode := oldByID[id]; oldNode != nil && oldNode.Label != n.Label {
			return nil, fmt.Errorf("pipeline: existing UUID %s changes node label", id)
		}
		if oldNode := oldByID[id]; oldNode != nil && keep[id] && (oldNode.Domain != n.Domain || oldNode.Subdomain != n.Subdomain) {
			return nil, fmt.Errorf("pipeline: protected taxonomy conflict for existing UUID %s; prior state retained", id)
		}
		// 人工正文原样保留，但候选的核验收据不能证明覆盖后的不同正文。
		// Compare only selected human nodes; untouched human data needs no new assessment.
		if prior := oldByID[id]; prior != nil && protected[id] {
			var fields []string
			for _, field := range []struct{ name, candidate, final string }{
				{"name", n.Name, prior.Name},
				{"summary", n.Summary, prior.Summary},
				{"description", n.Description, prior.Description},
				{"synonyms", n.Synonyms, prior.Synonyms},
			} {
				if field.candidate != field.final {
					fields = append(fields, field.name)
				}
			}
			if len(fields) > 0 {
				// Report identities/field names only, never protected text or a verdict on it.
				outcome.semanticUnverified = append(outcome.semanticUnverified, fmt.Sprintf("post-materialization semantic-unverified: human_curated UUID %s retained different %s; candidate verification does not certify final text", id, strings.Join(fields, ",")))
			}
		}
	}
	old.Close()
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(oldPath)}
	q := url.Values{}
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	if _, err := db.Conn().ExecContext(ctx, "ATTACH DATABASE ? AS full_previous", u.String()); err != nil {
		return nil, err
	}
	defer db.Conn().ExecContext(context.Background(), "DETACH DATABASE full_previous")
	tx, err := db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
		return nil, err
	}
	// Translate through temporary names so swaps cannot collide with another candidate.
	keys := make([]string, 0, len(outcome.remap))
	for id := range outcome.remap {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for i, id := range keys {
		target := outcome.remap[id]
		if id == target {
			continue
		}
		tmp := fmt.Sprintf("cairn-full-existing:%d", i)
		for _, stmt := range []string{"UPDATE nodes SET id=? WHERE id=?", "UPDATE edges SET source_id=? WHERE source_id=?", "UPDATE edges SET target_id=? WHERE target_id=?", "UPDATE node_sources SET node_uuid=? WHERE node_uuid=?"} {
			if _, err := tx.ExecContext(ctx, stmt, tmp, id); err != nil {
				return nil, err
			}
		}
	}
	for i, id := range keys {
		target := outcome.remap[id]
		if id == target {
			continue
		}
		tmp := fmt.Sprintf("cairn-full-existing:%d", i)
		for _, stmt := range []string{"UPDATE nodes SET id=? WHERE id=?", "UPDATE edges SET source_id=? WHERE source_id=?", "UPDATE edges SET target_id=? WHERE target_id=?", "UPDATE node_sources SET node_uuid=? WHERE node_uuid=?"} {
			if _, err := tx.ExecContext(ctx, stmt, target, tmp); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range keys {
		target := outcome.remap[id]
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET created_at=(SELECT created_at FROM full_previous.nodes WHERE id=?),file_slug=COALESCE((SELECT NULLIF(file_slug,'') FROM full_previous.nodes WHERE id=?),file_slug) WHERE id=?`, target, target, target); err != nil {
			return nil, err
		}
	}
	columns := "id,label,name,summary,synonyms,domain,subdomain,description,properties,tags,related_entities,confidence,provenance,source_refs,file_slug,created_at,updated_at"
	mutable := strings.Split(columns, ",")[1:]
	for id := range keep {
		if oldByID[id] == nil {
			return nil, fmt.Errorf("pipeline: protected old edge has missing endpoint %s", id)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO nodes ("+columns+") SELECT "+columns+" FROM full_previous.nodes WHERE id=? ON CONFLICT(id) DO NOTHING", id); err != nil {
			return nil, err
		}
		if protected[id] || clean[id] {
			set := strings.Join(mutable, ",")
			if _, err := tx.ExecContext(ctx, "UPDATE nodes SET ("+set+")=(SELECT "+set+" FROM full_previous.nodes WHERE id=?) WHERE id=?", id, id); err != nil {
				return nil, err
			}
		}
		// Only untouched source incidences are carried; selected current rows already
		// have current source coordinates, and removed/in-scope old members stay removed.
		for _, s := range byNode[id] {
			if untouched[s.FilePath] {
				if _, err := tx.ExecContext(ctx, `INSERT INTO node_sources(node_uuid,member_id,skill,file_path,start_line,end_line) SELECT node_uuid,member_id,skill,file_path,start_line,end_line FROM full_previous.node_sources WHERE node_uuid=? AND member_id=? AND file_path=? ON CONFLICT(node_uuid,member_id,file_path) DO NOTHING`, id, s.MemberID, s.FilePath); err != nil {
					return nil, err
				}
			}
		}
	}
	// Old IDs/NULLs/duplicate rows remain exact; generated IDs occupy a separate range.
	var maxNew int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM edges").Scan(&maxNew); err != nil {
		return nil, err
	}
	offset := maxOldEdge + maxNew + 1
	if _, err := tx.ExecContext(ctx, "UPDATE edges SET id=id+?", offset); err != nil {
		return nil, err
	}
	generatedKeys := map[string]bool{}
	edgeRows, err := tx.QueryContext(ctx, "SELECT source_id,target_id,kind FROM edges")
	if err != nil {
		return nil, err
	}
	for edgeRows.Next() {
		var source, target, kind string
		if err := edgeRows.Scan(&source, &target, &kind); err != nil {
			edgeRows.Close()
			return nil, err
		}
		generatedKeys[source+"\x00"+target+"\x00"+kind] = true
	}
	if err := edgeRows.Err(); err != nil {
		edgeRows.Close()
		return nil, err
	}
	edgeRows.Close()
	edgeColumns := "id,source_id,target_id,kind,description,properties,provenance,confidence,source_refs,bidirectional,cardinality,created_at"
	for _, e := range oldEdges {
		retain := humanKeys[edgeKey(e)] || (keep[e.SourceID] && keep[e.TargetID] && ((clean[e.SourceID] && clean[e.TargetID]) || (fullHierarchyEdge(e, oldByID) && !generatedKeys[edgeKey(e)])))
		if !retain {
			continue
		}
		if humanKeys[edgeKey(e)] {
			if _, err := tx.ExecContext(ctx, "DELETE FROM edges WHERE source_id=? AND target_id=? AND kind=? AND id>?", e.SourceID, e.TargetID, string(e.Kind), maxOldEdge); err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO edges ("+edgeColumns+") SELECT "+edgeColumns+" FROM full_previous.edges WHERE id=?", e.ID); err != nil {
			return nil, err
		}
	}
	// Only changed source bookkeeping is reconciled. Untouched raw rows (including
	// legacy NULLs or human spelling/order of source_refs) must remain byte-faithful.
	sourceRows, err := tx.QueryContext(ctx, `SELECT node_uuid,member_id,skill,file_path,COALESCE(start_line,0),COALESCE(end_line,0) FROM node_sources ORDER BY node_uuid,member_id,file_path`)
	if err != nil {
		return nil, err
	}
	mergedSources := map[string][]storage.NodeSource{}
	for sourceRows.Next() {
		var row storage.NodeSource
		if err := sourceRows.Scan(&row.NodeUUID, &row.MemberID, &row.Skill, &row.FilePath, &row.StartLine, &row.EndLine); err != nil {
			sourceRows.Close()
			return nil, err
		}
		mergedSources[row.NodeUUID] = append(mergedSources[row.NodeUUID], row)
	}
	if err := sourceRows.Err(); err != nil {
		sourceRows.Close()
		return nil, err
	}
	sourceRows.Close()
	reconcileNodes, reconcileDomains := map[string]bool{}, map[string]bool{}
	for _, n := range newNodes {
		if n.Label == dktypes.LabelDomain {
			reconcileDomains[n.ID] = true
		}
		if n.Label != dktypes.LabelEntity && n.Label != dktypes.LabelConcept {
			continue
		}
		id := n.ID
		if target := outcome.remap[id]; target != "" {
			id = target
		}
		if !protected[id] && (len(mergedSources[id]) > 0 || len(byNode[id]) > 0) {
			reconcileNodes[id] = true
		}
	}
	for id := range keep {
		if fullSourceSkills(byNode[id]) != fullSourceSkills(mergedSources[id]) {
			reconcileNodes[id] = true
			reconcileDomains["domain::"+oldByID[id].Domain] = true
		}
	}
	for id := range reconcileNodes {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET source_refs=(SELECT group_concat(skill,',') FROM (SELECT DISTINCT skill FROM node_sources WHERE node_uuid=nodes.id ORDER BY skill)) WHERE id=? AND label IN ('Entity','Concept')`, id); err != nil {
			return nil, err
		}
	}
	for id := range reconcileDomains {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET source_refs=(SELECT group_concat(skill,',') FROM (SELECT DISTINCT ns.skill FROM node_sources ns JOIN nodes leaf ON leaf.id=ns.node_uuid WHERE leaf.domain=nodes.domain ORDER BY ns.skill)) WHERE id=? AND label='Domain' AND (EXISTS(SELECT 1 FROM node_sources ns JOIN nodes leaf ON leaf.id=ns.node_uuid WHERE leaf.domain=nodes.domain) OR EXISTS(SELECT 1 FROM full_previous.node_sources ns JOIN full_previous.nodes leaf ON leaf.id=ns.node_uuid WHERE leaf.domain=nodes.domain))`, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := storage.NewFTSIndex(db).RebuildAll(ctx); err != nil {
		return nil, err
	}
	// Every surviving neighbor whose incident snapshot changed needs a sidecar refresh.
	before := map[string][]*dktypes.Edge{}
	after := map[string][]*dktypes.Edge{}
	for _, e := range oldEdges {
		before[e.SourceID] = append(before[e.SourceID], e)
		before[e.TargetID] = append(before[e.TargetID], e)
	}
	current, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range current {
		after[e.SourceID] = append(after[e.SourceID], e)
		after[e.TargetID] = append(after[e.TargetID], e)
	}
	currentSources, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
	if err != nil {
		return nil, err
	}
	currentFiles := map[string]bool{}
	for _, row := range currentSources {
		currentFiles[row.FilePath] = true
	}
	for _, row := range oldSources {
		if represented[row.FilePath] && !currentFiles[row.FilePath] {
			outcome.empty[row.FilePath] = true
		}
	}
	currentByNode := map[string][]storage.NodeSource{}
	for _, s := range currentSources {
		currentByNode[s.NodeUUID] = append(currentByNode[s.NodeUUID], s)
	}
	for id := range keep {
		if !sameFullEdges(before[id], after[id]) || !sameFullSources(byNode[id], currentByNode[id]) {
			for _, s := range byNode[id] {
				if untouched[s.FilePath] {
					outcome.refresh[s.FilePath] = true
				}
			}
		}
	}
	outcome.updates, err = writeback.AuthoritativeNodeUpdates(ctx, db)
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

func sameFullEdges(a, b []*dktypes.Edge) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Slice(a, func(i, j int) bool { return a[i].ID < a[j].ID })
	sort.Slice(b, func(i, j int) bool { return b[i].ID < b[j].ID })
	for i := range a {
		if *a[i] != *b[i] {
			return false
		}
	}
	return true
}

// Count the actual merged candidate graph; source counters remain the selected current associations.
func countFullMergedGraph(ctx context.Context, db *storage.DB, result *ingest.IngestDomainsResult) error {
	nodes, err := storage.NewNodeRepo(db).ListAll(ctx)
	if err != nil {
		return err
	}
	edges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		return err
	}
	result.SkillNodes, result.DomainNodes, result.SubdomainNodes, result.EntityNodes, result.ConceptNodes = 0, 0, 0, 0, 0
	for _, n := range nodes {
		switch n.Label {
		case dktypes.LabelSkill:
			result.SkillNodes++
		case dktypes.LabelDomain:
			result.DomainNodes++
		case dktypes.LabelSubdomain:
			result.SubdomainNodes++
		case dktypes.LabelEntity:
			result.EntityNodes++
		case dktypes.LabelConcept:
			result.ConceptNodes++
		}
	}
	result.NodesInserted = len(nodes)
	result.EdgesInserted = len(edges)
	result.ProvidesEdges, result.ComposesEdges, result.SemanticEdges = 0, 0, 0
	byID := map[string]*dktypes.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, e := range edges {
		if fullHierarchyEdge(e, byID) {
			if e.Kind == dktypes.KindProvides {
				result.ProvidesEdges++
			} else {
				result.ComposesEdges++
			}
		} else {
			result.SemanticEdges++
		}
	}
	return nil
}

func sameFullSources(a, b []storage.NodeSource) bool {
	if len(a) != len(b) {
		return false
	}
	less := func(a, b storage.NodeSource) bool {
		if a.MemberID != b.MemberID {
			return a.MemberID < b.MemberID
		}
		return a.FilePath < b.FilePath
	}
	sort.Slice(a, func(i, j int) bool { return less(a[i], a[j]) })
	sort.Slice(b, func(i, j int) bool { return less(b[i], b[j]) })
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func fullSourceSkills(rows []storage.NodeSource) string {
	set := map[string]bool{}
	for _, row := range rows {
		set[row.Skill] = true
	}
	skills := make([]string, 0, len(set))
	for skill := range set {
		skills = append(skills, skill)
	}
	sort.Strings(skills)
	return strings.Join(skills, ",")
}

// composes也能连接事实节点；只有实际层级标签和归属才能证明结构祖先。
// Human semantic identities are protected separately, regardless of their kind.
func fullHierarchyEdge(edge *dktypes.Edge, nodes map[string]*dktypes.Node) bool {
	source, target := nodes[edge.SourceID], nodes[edge.TargetID]
	if source == nil || target == nil {
		return false
	}
	switch edge.Kind {
	case dktypes.KindProvides:
		return source.Label == dktypes.LabelSkill && target.Label == dktypes.LabelDomain
	case dktypes.KindComposes:
		if source.Domain == "" || source.Domain != target.Domain {
			return false
		}
		if source.Label == dktypes.LabelDomain && target.Label == dktypes.LabelSubdomain {
			return target.Subdomain != ""
		}
		return source.Label == dktypes.LabelSubdomain && source.Subdomain != "" && source.Subdomain == target.Subdomain && (target.Label == dktypes.LabelEntity || target.Label == dktypes.LabelConcept)
	}
	return false
}
