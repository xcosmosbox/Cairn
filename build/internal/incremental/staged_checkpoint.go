package incremental

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const stagedManifestKey = "staged_mapping/v1"

type stagedPrepared struct {
	Call    StagedMappingCall
	Wire    []byte
	Binding StagedMappingWireBinding
}
type stagedEvent struct {
	Kind     string
	Input    *StagedMappingInput          `json:",omitempty"`
	Prepared *stagedPrepared              `json:",omitempty"`
	Response *StagedMappingDispatchResult `json:",omitempty"`
	State    *stagedCatalogue             `json:",omitempty"`
	Output   *alignLLMOutput              `json:",omitempty"`
}
type stagedRecord struct {
	Sequence       int
	Previous, Hash string
	Event          stagedEvent
}
type stagedDBHead struct{ InputIdentitySHA256, CatalogueSHA256, AcceptedHeadSHA256 string }
type stagedSession struct {
	db                        *storage.DB
	lock                      *sql.DB
	lockConn                  *sql.Conn
	file                      *os.File
	dir                       string
	opts                      StagedMappingOptions
	input                     StagedMappingInput
	state                     stagedCatalogue
	head, acceptedHead, phase string
	sequence, accepted        int
	attempt                   *stagedPrepared
	response                  *StagedMappingDispatchResult
}

func (s *stagedSession) crash(point string) error {
	if s.opts.fault != nil {
		return s.opts.fault(point)
	}
	return nil
}
func stagedSyncDir(dir string) error {
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

func openStagedSession(ctx context.Context, in StagedMappingInput, opts StagedMappingOptions) (s *stagedSession, err error) {
	if opts.StageDir == "" {
		return nil, fmt.Errorf("staged mapping: explicit isolated stage directory required")
	}
	dir, e := filepath.Abs(opts.StageDir)
	if e != nil {
		return nil, e
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(dir))
	if e != nil || parent != filepath.Dir(dir) {
		return nil, fmt.Errorf("staged mapping: stage path may not alias a symlink")
	}
	if !opts.Resume {
		if e = os.Mkdir(dir, 0700); e != nil {
			return nil, fmt.Errorf("staged mapping: fresh stage directory: %w", e)
		}
		if e = stagedSyncDir(filepath.Dir(dir)); e != nil {
			return nil, e
		}
	} else {
		info, e := os.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("staged mapping: resume requires owned stage directory")
		}
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, e
	}
	for _, f := range entries {
		if f.Type()&os.ModeSymlink != 0 || f.IsDir() {
			return nil, fmt.Errorf("staged mapping: unsafe stage entry")
		}
		switch f.Name() {
		case "owner.lock", "owner.lock-journal", "journal.jsonl", "stage.db", "stage.db-wal", "stage.db-shm":
		default:
			return nil, fmt.Errorf("staged mapping: foreign stage contents")
		}
	}
	s = &stagedSession{dir: dir, opts: opts, input: in}
	owned := s
	defer func() {
		if err != nil {
			owned.close()
		}
	}()
	// SQLite 持有的排他事务是进程锁；进程崩溃由 OS 释放，不留下无法恢复的 O_EXCL 锁。
	// No lock table or schema is introduced, and no global process state is changed.
	s.lock, e = storage.OpenSQLite(filepath.Join(dir, "owner.lock"), storage.SQLiteOptions{JournalMode: "DELETE", BusyTimeoutMs: 1, MaxOpenConns: 1})
	if e != nil {
		return nil, e
	}
	s.lockConn, e = s.lock.Conn(ctx)
	if e != nil {
		return nil, e
	}
	if _, e = s.lockConn.ExecContext(ctx, "BEGIN EXCLUSIVE"); e != nil {
		return nil, fmt.Errorf("staged mapping: stage already in use")
	}
	flags := os.O_RDWR | os.O_APPEND
	if !opts.Resume {
		flags |= os.O_CREATE | os.O_EXCL
	}
	s.file, e = os.OpenFile(filepath.Join(dir, "journal.jsonl"), flags, 0600)
	if e != nil {
		return nil, fmt.Errorf("staged mapping: owned journal required: %w", e)
	}
	if opts.Resume {
		if e = s.replay(); e != nil {
			return nil, e
		}
	} else {
		if e = s.append(stagedEvent{Kind: "header", Input: &in}); e != nil {
			return nil, e
		}
		s.acceptedHead = s.head
	}
	if opts.Resume {
		if _, e = os.Lstat(filepath.Join(dir, "stage.db")); e != nil {
			if !os.IsNotExist(e) || s.sequence != 1 {
				return nil, fmt.Errorf("staged mapping: owned stage database missing")
			}
			// header 持久化后尚未建立数据库的首次崩溃，没有任何可再次付费的调用。
			// A header-only journal may initialize its missing, still-empty owned projection.
			s.db, e = storage.NewDB(storage.DBOptions{Path: filepath.Join(dir, "stage.db")})
		} else {
			s.db, e = storage.OpenExisting(filepath.Join(dir, "stage.db"))
		}
	} else {
		s.db, e = storage.NewDB(storage.DBOptions{Path: filepath.Join(dir, "stage.db")})
	}
	if e != nil {
		return nil, e
	}
	if _, e = s.db.Conn().ExecContext(ctx, "PRAGMA synchronous=FULL"); e != nil {
		return nil, e
	}
	if !opts.Resume {
		if e = s.project(ctx); e != nil {
			return nil, e
		}
	} else {
		if e = s.reconcileDB(ctx); e != nil {
			return nil, e
		}
		if s.phase == "accepted" {
			if e = s.append(stagedEvent{Kind: "committed"}); e != nil {
				return nil, e
			}
			s.phase = ""
		}
	}
	return s, nil
}

func (s *stagedSession) close() {
	if s.db != nil {
		s.db.Close()
	}
	if s.file != nil {
		s.file.Close()
	}
	if s.lockConn != nil {
		s.lockConn.ExecContext(context.Background(), "ROLLBACK")
		s.lockConn.Close()
	}
	if s.lock != nil {
		s.lock.Close()
	}
}

func (s *stagedSession) append(event stagedEvent) error {
	r := stagedRecord{Sequence: s.sequence + 1, Previous: s.head, Event: event}
	r.Hash = stagedHash(struct {
		Sequence int
		Previous string
		Event    stagedEvent
	}{r.Sequence, r.Previous, r.Event})
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	b = append(b, '\n')
	n, e := s.file.Write(b)
	if e == nil && n != len(b) {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = s.crash("before_" + event.Kind + "_sync")
	}
	if e == nil {
		e = s.file.Sync()
	}
	if e != nil {
		return fmt.Errorf("staged mapping: journal persistence: %w", e)
	}
	if e = stagedSyncDir(s.dir); e != nil {
		return e
	}
	s.sequence = r.Sequence
	s.head = r.Hash
	return s.crash("after_" + event.Kind)
}

func (s *stagedSession) replay() error {
	if _, e := s.file.Seek(0, io.SeekStart); e != nil {
		return e
	}
	r := bufio.NewReader(s.file)
	for {
		line, e := r.ReadBytes('\n')
		if e == io.EOF && len(line) == 0 {
			break
		}
		if e != nil {
			return fmt.Errorf("staged mapping: incomplete journal record; reconciliation required")
		}
		var row stagedRecord
		if stagedDecode(line, &row) != nil || row.Sequence != s.sequence+1 || row.Previous != s.head || row.Hash != stagedHash(struct {
			Sequence int
			Previous string
			Event    stagedEvent
		}{row.Sequence, row.Previous, row.Event}) {
			return fmt.Errorf("staged mapping: journal chain mismatch")
		}
		if err := s.consume(row); err != nil {
			return err
		}
		s.sequence = row.Sequence
		s.head = row.Hash
	}
	if s.sequence == 0 {
		return fmt.Errorf("staged mapping: empty journal")
	}
	_, e := s.file.Seek(0, io.SeekEnd)
	return e
}

func (s *stagedSession) consume(row stagedRecord) error {
	e := row.Event
	bad := func() error { return fmt.Errorf("staged mapping: invalid journal transition") }
	switch e.Kind {
	case "header":
		if s.sequence != 0 || e.Input == nil || stagedHash(e.Input) != stagedHash(s.input) {
			return fmt.Errorf("staged mapping: journal input identity mismatch")
		}
		s.acceptedHead = row.Hash
	case "prepared":
		if s.sequence == 0 || e.Prepared == nil || (s.phase != "" && s.phase != "invalid") {
			return bad()
		}
		p := e.Prepared
		if p.Call.CatalogueSHA256 != stagedHash(s.state) || p.Call.Attempt < 1 || p.Call.Attempt > 2 || stagedValidateWire(p.Call, p.Wire, p.Binding, s.input.Identity.Admission) != nil {
			return bad()
		}
		if s.phase == "invalid" {
			if s.attempt == nil || s.attempt.Call.Attempt != 1 || p.Call.Attempt != 2 || p.Call.BatchID != s.attempt.Call.BatchID || !reflect.DeepEqual(p.Call.Units, s.attempt.Call.Units) {
				return bad()
			}
		} else if p.Call.Attempt != 1 {
			return bad()
		}
		s.attempt = p
		s.response = nil
		s.phase = "prepared"
	case "intent":
		if s.phase != "prepared" {
			return bad()
		}
		s.phase = "intent"
	case "response":
		if s.phase != "intent" || e.Response == nil || e.Response.Binding != s.attempt.Binding || e.Response.ReceiptID == "" {
			return bad()
		}
		s.response = e.Response
		s.phase = "response"
	case "invalid":
		if s.phase != "response" {
			return bad()
		}
		if _, _, err := s.reconstructResponse(); !errors.Is(err, errStagedResponseSchema) {
			return fmt.Errorf("staged mapping: invalid history lacks bound complete schema failure")
		}
		s.phase = "invalid"
	case "accepted":
		if s.phase != "response" || e.State == nil || e.Output == nil {
			return bad()
		}
		// 重放必须从此前完整响应、原始来源和旧目录重算；只校验自洽快照会接受重新封装的来源篡改。
		// Replay the exact response offline; a repinned but altered accepted graph is rejected.
		next, output, err := s.reconstructResponse()
		if err != nil || stagedHash(next) != stagedHash(e.State) || stagedHash(output) != stagedHash(e.Output) {
			return fmt.Errorf("staged mapping: accepted response/state binding mismatch")
		}
		if err := stagedValidateCatalogue(*e.State); err != nil {
			return err
		}
		done := map[string]bool{}
		for _, id := range s.state.Done {
			done[id] = true
		}
		for _, id := range s.attempt.Call.Units {
			if done[id] {
				return bad()
			}
			done[id] = true
		}
		if len(done) != len(e.State.Done) {
			return bad()
		}
		for _, id := range e.State.Done {
			if !done[id] {
				return bad()
			}
		}
		s.state = *e.State
		s.accepted++
		s.acceptedHead = row.Hash
		s.attempt = nil
		s.response = nil
		s.phase = "accepted"
	case "committed":
		if s.phase != "accepted" {
			return bad()
		}
		s.phase = ""
	case "abandoned":
		if s.phase != "prepared" {
			return bad()
		}
		s.phase = ""
		s.attempt = nil
	default:
		return bad()
	}
	return nil
}

func stagedValidateCatalogue(c stagedCatalogue) error {
	ids := map[string]dktypes.Node{}
	for i, n := range c.Nodes {
		if n.ID == "" || (i > 0 && c.Nodes[i-1].ID >= n.ID) || n.Name == "" || n.Domain == "" || n.Provenance != dktypes.ProvenanceLLMInferred {
			return fmt.Errorf("staged mapping: invalid catalogue identity")
		}
		ids[n.ID] = n
	}
	births := map[string]bool{}
	for i, b := range c.Births {
		n, ok := ids[b.UUID]
		if !ok || (n.Label != dktypes.LabelEntity && n.Label != dktypes.LabelConcept) || len(b.Members) == 0 || extractNodeUUID(b.Members) != b.UUID || b.DomainID != domainNodeID(n.Domain) || b.SubdomainID != subdomainNodeID(n.Domain, n.Subdomain) || (i > 0 && c.Births[i-1].UUID >= b.UUID) {
			return fmt.Errorf("staged mapping: invalid pinned birth")
		}
		births[b.UUID] = true
	}
	members := map[string]string{}
	sources := map[string]bool{}
	for _, r := range c.Sources {
		if !births[r.NodeUUID] || r.MemberID == "" || r.FilePath == "" {
			return fmt.Errorf("staged mapping: orphan source")
		}
		if owner := members[r.MemberID]; owner != "" && owner != r.NodeUUID {
			return fmt.Errorf("staged mapping: member assigned twice")
		}
		members[r.MemberID] = r.NodeUUID
		k := r.NodeUUID + "\x00" + r.MemberID + "\x00" + r.FilePath
		if sources[k] {
			return fmt.Errorf("staged mapping: duplicate source")
		}
		sources[k] = true
	}
	for i, id := range c.Done {
		if members[id] == "" || (i > 0 && c.Done[i-1] >= id) {
			return fmt.Errorf("staged mapping: incomplete catalogue coverage")
		}
	}
	if len(members) != len(c.Done) {
		return fmt.Errorf("staged mapping: extra catalogue member")
	}
	for _, n := range c.Nodes {
		switch n.Label {
		case dktypes.LabelDomain:
			if n.ID != domainNodeID(n.Domain) || n.Subdomain != "" {
				return fmt.Errorf("staged mapping: invalid domain")
			}
		case dktypes.LabelSubdomain:
			if n.ID != subdomainNodeID(n.Domain, n.Subdomain) || ids[domainNodeID(n.Domain)].ID == "" {
				return fmt.Errorf("staged mapping: invalid subdomain")
			}
		case dktypes.LabelEntity, dktypes.LabelConcept:
			if !births[n.ID] || ids[subdomainNodeID(n.Domain, n.Subdomain)].ID == "" {
				return fmt.Errorf("staged mapping: missing parent")
			}
		default:
			return fmt.Errorf("staged mapping: unknown stage label")
		}
	}
	return nil
}

// extractNodeUUID 只校验已保存的 birth，不以当前累计 members 重新派生身份。
// This is a birth-proof check, never an identity update.
func extractNodeUUID(members []string) string { return extract.NodeUUID(members) }

func (s *stagedSession) project(ctx context.Context) error {
	if e := stagedValidateCatalogue(s.state); e != nil {
		return e
	}
	tx, e := s.db.Conn().BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// 仅投影适配器拥有的阶段图；无文件回写、版本发布或查询侧写入。
	// All stage nodes, incidence rows, FTS and head move in one transaction.
	for _, n := range s.state.Nodes {
		_, e = tx.ExecContext(ctx, `INSERT INTO nodes(id,label,name,summary,domain,subdomain,description,confidence,provenance,source_refs,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET source_refs=excluded.source_refs,confidence=excluded.confidence`, n.ID, n.Label, n.Name, n.Summary, n.Domain, n.Subdomain, n.Description, n.Confidence, n.Provenance, n.SourceRefs, "0001-01-01T00:00:00Z", "0001-01-01T00:00:00Z")
		if e != nil {
			return e
		}
	}
	for _, r := range s.state.Sources {
		if _, e = tx.ExecContext(ctx, `INSERT INTO node_sources(node_uuid,member_id,skill,file_path,start_line,end_line) VALUES(?,?,?,?,?,?) ON CONFLICT(node_uuid,member_id,file_path) DO NOTHING`, r.NodeUUID, r.MemberID, r.Skill, r.FilePath, r.StartLine, r.EndLine); e != nil {
			return e
		}
	}
	// Only missing stage rows are indexed; existing node text/identity is immutable during mapping.
	if _, e = tx.ExecContext(ctx, `INSERT INTO nodes_fts(rowid,name,summary,synonyms,tags,description,domain,subdomain) SELECT rowid,name,summary,synonyms,tags,description,domain,subdomain FROM nodes WHERE rowid NOT IN (SELECT rowid FROM nodes_fts)`); e != nil {
		return e
	}
	head := stagedDBHead{stagedHash(s.input.Identity), stagedHash(s.state), s.acceptedHead}
	raw, _ := json.Marshal(head)
	if _, e = tx.ExecContext(ctx, `INSERT INTO kg_manifest(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, stagedManifestKey, string(raw)); e != nil {
		return e
	}
	if e = s.crash("before_transaction_commit"); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	if e = s.crash("after_transaction_commit"); e != nil {
		return e
	}
	return stagedSyncDir(s.dir)
}

func (s *stagedSession) reconcileDB(ctx context.Context) error {
	var raw string
	e := s.db.Conn().QueryRowContext(ctx, `SELECT value FROM kg_manifest WHERE key=?`, stagedManifestKey).Scan(&raw)
	if e != nil {
		if e == sql.ErrNoRows && s.sequence == 1 {
			for _, table := range []string{"nodes", "nodes_fts", "node_sources", "edges", "cross_references", "conflict_reports", "file_states", "kb_version", "uuid_lineage", "kg_manifest"} {
				var count int
				if err := s.db.Conn().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					return fmt.Errorf("staged mapping: foreign unstamped database")
				}
			}
			return s.project(ctx)
		}
		return fmt.Errorf("staged mapping: database ownership missing")
	}
	var head stagedDBHead
	if stagedDecode([]byte(raw), &head) != nil || head.InputIdentitySHA256 != stagedHash(s.input.Identity) {
		return fmt.Errorf("staged mapping: database identity mismatch")
	}
	// Validate the DB against the exact accepted journal state named by its own head.
	var dbState *stagedCatalogue
	if head.AcceptedHeadSHA256 == s.acceptedHead {
		dbState = &s.state
	} else {
		f, e := os.Open(filepath.Join(s.dir, "journal.jsonl"))
		if e != nil {
			return e
		}
		defer f.Close()
		reader := bufio.NewReader(f)
		for {
			line, e := reader.ReadBytes('\n')
			if e != nil {
				break
			}
			var row stagedRecord
			if stagedDecode(line, &row) != nil {
				return fmt.Errorf("staged mapping: invalid journal")
			}
			if row.Hash == head.AcceptedHeadSHA256 {
				if row.Event.Kind == "header" {
					dbState = &stagedCatalogue{}
				} else if row.Event.Kind == "accepted" {
					dbState = row.Event.State
				}
				break
			}
		}
	}
	if dbState == nil || head.CatalogueSHA256 != stagedHash(*dbState) {
		return fmt.Errorf("staged mapping: database head not in journal")
	}
	if e = s.verifyProjection(ctx, *dbState); e != nil {
		return e
	}
	if head.AcceptedHeadSHA256 != s.acceptedHead {
		if e = s.project(ctx); e != nil {
			return e
		}
	}
	return nil
}

func (s *stagedSession) verifyProjection(ctx context.Context, c stagedCatalogue) error {
	nodes, e := storage.NewNodeRepo(s.db).ListAll(ctx)
	if e != nil {
		return e
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	actual := make([]dktypes.Node, 0, len(nodes))
	for _, n := range nodes {
		actual = append(actual, *n)
	}
	expected := append([]dktypes.Node{}, c.Nodes...)
	if len(actual) != len(expected) || len(actual) > 0 && stagedHash(actual) != stagedHash(expected) {
		return fmt.Errorf("staged mapping: stage node projection tampered")
	}
	sources, e := storage.NewNodeSourceRepo(s.db).ListAll(ctx)
	if e != nil {
		return e
	}
	sort.Slice(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if a.NodeUUID != b.NodeUUID {
			return a.NodeUUID < b.NodeUUID
		}
		if a.MemberID != b.MemberID {
			return a.MemberID < b.MemberID
		}
		return a.FilePath < b.FilePath
	})
	if len(sources) != len(c.Sources) || len(sources) > 0 && stagedHash(sources) != stagedHash(c.Sources) {
		return fmt.Errorf("staged mapping: stage source projection tampered")
	}
	for _, table := range []string{"edges", "cross_references", "conflict_reports", "file_states", "kb_version", "uuid_lineage"} {
		var n int
		e = s.db.Conn().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n)
		if e != nil {
			return e
		}
		if n != 0 {
			return fmt.Errorf("staged mapping: foreign stage graph state")
		}
	}
	var manifests int
	if e = s.db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM kg_manifest`).Scan(&manifests); e != nil || manifests != 1 {
		return fmt.Errorf("staged mapping: foreign stage metadata")
	}
	// Compare indexed text and row identity, not only the number of FTS hits.
	var mismatches int
	e = s.db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes_fts f LEFT JOIN nodes n ON n.rowid=f.rowid WHERE n.id IS NULL OR f.name<>n.name OR f.summary<>n.summary OR f.domain<>n.domain OR f.subdomain<>n.subdomain OR COALESCE(f.description,'')<>COALESCE(n.description,'') OR COALESCE(f.synonyms,'')<>COALESCE(n.synonyms,'') OR COALESCE(f.tags,'')<>COALESCE(n.tags,'')`).Scan(&mismatches)
	if e != nil {
		return e
	}
	var count int
	if e = s.db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes_fts`).Scan(&count); e != nil {
		return e
	}
	if mismatches != 0 || count != len(c.Nodes) {
		return fmt.Errorf("staged mapping: FTS projection tampered")
	}
	return nil
}

// reconstructResponse 使用临时内存索引重建历史父目录，绝不调用 admitter 或提供商。
// In-memory projection makes BM25 cutoff/alias reconstruction identical to the original stage.
func (s *stagedSession) reconstructResponse() (stagedCatalogue, *alignLLMOutput, error) {
	var zero stagedCatalogue
	if s.attempt == nil || s.response == nil {
		return zero, nil, fmt.Errorf("missing response")
	}
	_, all, e := stagedCanonicalInput(s.input)
	if e != nil {
		return zero, nil, e
	}
	batch, e := stagedPendingBatch(s.state, all, s.attempt.Call.Units)
	if e != nil {
		return zero, nil, e
	}
	db, e := storage.NewDB(storage.DBOptions{Path: ":memory:"})
	if e != nil {
		return zero, nil, e
	}
	defer db.Close()
	tmp := &stagedSession{db: db, dir: s.dir, input: s.input, state: s.state, acceptedHead: s.acceptedHead}
	if e = tmp.project(context.Background()); e != nil {
		return zero, nil, e
	}
	feedback := ""
	if s.attempt.Call.Attempt == 2 {
		feedback = "输出结构校验失败，请仅按 schema 修正。"
	}
	plan, e := tmp.preparePlan(context.Background(), batch, s.attempt.Call.Attempt, feedback)
	if e != nil {
		return zero, nil, e
	}
	if !reflectStagedCallEqual(plan.Call, s.attempt.Call) {
		return zero, nil, fmt.Errorf("historical call mismatch")
	}
	if !stagedCompleteResponse(s.response, s.attempt.Binding) {
		return zero, nil, ErrStagedMappingUncertain
	}
	out, groups, e := stagedValidateOutput(s.response.Response.Text, plan)
	if e != nil {
		return zero, nil, errStagedResponseSchema
	}
	next, e := tmp.materializeBatch(context.Background(), plan, out, groups)
	return next, out, e
}
