package incremental

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// The pre-amortization proof, intentionally using a fresh schema and canonical
// input on every call. It remains an independent setup reference for parity;
// neither path treats an accepted snapshot or persistent stage.db as evidence.
func stagedFreshReplayReference(s *stagedSession) (stagedCatalogue, *alignLLMOutput, error) {
	var zero stagedCatalogue
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
	p, e := tmp.preparePlan(context.Background(), batch, s.attempt.Call.Attempt, feedback)
	if e != nil {
		return zero, nil, e
	}
	if !reflectStagedCallEqual(p.Call, s.attempt.Call) {
		return zero, nil, fmt.Errorf("historical call mismatch")
	}
	if !stagedCompleteResponse(s.response, s.attempt.Binding) {
		return zero, nil, ErrStagedMappingUncertain
	}
	out, groups, e := stagedValidateOutput(s.response.Response.Text, p)
	if e != nil {
		return zero, nil, errStagedResponseSchema
	}
	next, e := tmp.materializeBatch(context.Background(), p, out, groups)
	return next, out, e
}

func stagedReplayHeader(t *testing.T, in StagedMappingInput, dir string) *stagedSession {
	t.Helper()
	s := &stagedSession{input: in, dir: dir}
	if e := s.consume(stagedRecord{Hash: stagedHash("header"), Event: stagedEvent{Kind: "header", Input: &in}}); e != nil {
		t.Fatal(e)
	}
	return s
}
func stagedReplayBind(t *testing.T, s *stagedSession, parent stagedCatalogue, attempt int, invalid bool) {
	t.Helper()
	s.state = parent
	s.attempt = nil
	s.response = nil
	_, units, e := stagedCanonicalInput(s.input)
	if e != nil {
		t.Fatal(e)
	}
	db, e := storage.NewDB(storage.DBOptions{Path: ":memory:"})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	tmp := &stagedSession{db: db, dir: s.dir, input: s.input, state: parent, acceptedHead: s.acceptedHead}
	if e = tmp.project(context.Background()); e != nil {
		t.Fatal(e)
	}
	feedback := ""
	if attempt == 2 {
		feedback = "输出结构校验失败，请仅按 schema 修正。"
	}
	plan, e := tmp.preparePlan(context.Background(), units[:1], attempt, feedback)
	if e != nil {
		t.Fatal(e)
	}
	fake := &stagedFake{pins: s.input.Identity.Admission}
	prepared, e := fake.Prepare(context.Background(), plan.Call)
	if e != nil {
		t.Fatal(e)
	}
	s.attempt = &stagedPrepared{plan.Call, prepared.Wire(), prepared.Binding()}
	out := alignLLMOutput{Decisions: []alignDecision{{Unit: units[0].ID, Action: "fuse", Group: "G1"}}, Groups: []alignGroup{{Group: "G1", NodeName: "new alpha", Summary: "alpha summary", Tag: "entity", Domain: "new domain", Subdomain: "new subdomain"}}}
	if len(plan.Book.nodeByAlias) > 0 {
		out.Decisions = []alignDecision{{Unit: units[0].ID, Action: "merge_into", Target: "n#1"}}
		out.Groups = nil
	}
	raw, _ := json.Marshal(out)
	if invalid {
		raw = []byte(`{"decisions":[],"groups":[]}`)
	}
	s.response = &StagedMappingDispatchResult{Binding: prepared.Binding(), ReceiptID: "offline-reference", Response: llm.CompleteResponse{Text: string(raw), InputTokens: 100, OutputTokens: 100, FinishReason: "stop"}}
}
func stagedReplayRows(t *testing.T, db *storage.DB) string {
	t.Helper()
	var data []any
	for _, query := range []string{`SELECT rowid,id,label,name,summary,domain,subdomain,COALESCE(description,''),COALESCE(synonyms,''),COALESCE(tags,'') FROM nodes ORDER BY rowid`, `SELECT rowid,name,summary,domain,subdomain,COALESCE(description,''),COALESCE(synonyms,''),COALESCE(tags,'') FROM nodes_fts ORDER BY rowid`, `SELECT node_uuid,member_id,skill,file_path,start_line,end_line FROM node_sources ORDER BY node_uuid,member_id,file_path`, `SELECT key,value FROM kg_manifest ORDER BY key`, `SELECT nodes.id,nodes_fts.rank FROM nodes_fts JOIN nodes ON nodes.rowid=nodes_fts.rowid WHERE nodes_fts MATCH '"alpha"' AND nodes.label IN ('Entity','Concept') ORDER BY nodes_fts.rank,nodes.id COLLATE BINARY LIMIT 15`} {
		rows, e := db.Conn().Query(query)
		if e != nil {
			t.Fatal(e)
		}
		columns, e := rows.Columns()
		if e != nil {
			rows.Close()
			t.Fatal(e)
		}
		var values [][]any
		for rows.Next() {
			v := make([]any, len(columns))
			ptr := make([]any, len(v))
			for i := range ptr {
				ptr[i] = &v[i]
			}
			if e = rows.Scan(ptr...); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			values = append(values, v)
		}
		e = errors.Join(rows.Err(), rows.Close())
		if e != nil {
			t.Fatal(e)
		}
		data = append(data, values)
	}
	return stagedHash(data)
}

func TestStagedReplayOwnedCanonicalAndScratchFreshParity(t *testing.T) {
	in := stagedTestInput(2)
	canonical, _, e := stagedCanonicalInput(in)
	if e != nil {
		t.Fatal(e)
	}
	s := stagedReplayHeader(t, canonical, t.TempDir())
	defer s.close()
	inputHash, unitsHash := stagedHash(s.input), stagedHash(s.replayState.units)
	// SourceSpan pointers, shared slices and complete source fields must be owned
	// before reuse. Mutating both constructor/header caller values cannot change them.
	canonical.Documents[0].Annotation.Items[0].Detail = "caller mutation"
	canonical.Documents[0].Annotation.Items[0].SourceSpan.Quote = "caller mutation"
	canonical.Roster[0].Skill = "caller mutation"
	if stagedHash(s.input) != inputHash || stagedHash(s.replayState.units) != unitsHash {
		t.Fatal("cached source material aliases caller input")
	}
	var reused *storage.DB
	for _, count := range []int{25, 2, 0, 25} {
		seedDB, e := storage.NewDB(storage.DBOptions{Path: ":memory:"})
		if e != nil {
			t.Fatal(e)
		}
		seed := &stagedSession{db: seedDB, dir: s.dir, input: s.input}
		if count > 0 {
			stagedSeedCatalogue(t, seed, count)
		}
		parent := seed.state
		seedDB.Close()
		// A stale scratch-only UUID would collide with the next generated node
		// if reset leaked previous rows/collision lookups into an empty parent.
		if count == 0 && s.replayState.db != nil {
			_, e = s.replayState.db.Conn().Exec(`INSERT INTO nodes(id,label,name,summary,domain,subdomain) VALUES(?,'Entity','alpha stale','alpha','stale','stale')`, extract.NodeUUID([]string{"unit-000"}))
			if e != nil {
				t.Fatal(e)
			}
		}
		stagedReplayBind(t, s, parent, 1, false)
		before := stagedHash(s.input)
		next, out, e := s.reconstructResponse()
		if e != nil {
			t.Fatal(e)
		}
		freshNext, freshOut, e := stagedFreshReplayReference(s)
		if e != nil || stagedHash(next) != stagedHash(freshNext) || stagedHash(out) != stagedHash(freshOut) {
			t.Fatalf("fresh proof mismatch parent%d: %v", count, e)
		}
		if reused == nil {
			reused = s.replayState.db
		} else if reused != s.replayState.db {
			t.Fatal("historical response recreated scratch schema")
		}
		fresh, e := storage.NewDB(storage.DBOptions{Path: ":memory:"})
		if e != nil {
			t.Fatal(e)
		}
		reference := &stagedSession{db: fresh, dir: s.dir, input: s.input, state: parent, acceptedHead: s.acceptedHead}
		if e = reference.project(context.Background()); e != nil {
			fresh.Close()
			t.Fatal(e)
		}
		if stagedReplayRows(t, fresh) != stagedReplayRows(t, reused) {
			fresh.Close()
			t.Fatal("full reset changed rowid/FTS/BM25 cutoff/source/head projection")
		}
		fresh.Close()
		if stagedHash(s.input) != before || stagedHash(s.replayState.units) != unitsHash {
			t.Fatal("legacy flatten/materialization mutated canonical cache")
		}
	}
	s.close()
	s.close()
	if e = reused.Conn().Ping(); e == nil {
		t.Fatal("scratch outlived owned replay/session")
	}
}

func TestStagedReplayRequiresValidatedHeaderBeforeCache(t *testing.T) {
	for _, mode := range []string{"unexpected-header", "self-matching-invalid-roots"} {
		t.Run(mode, func(t *testing.T) {
			in := stagedTestInput(1)
			header := in
			if mode == "unexpected-header" {
				header.Identity.MethodSHA256 = stagedHash("unexpected")
			} else {
				in.Identity.InputSHA256 = stagedHash("wrong source root")
				header = in
			}
			s := &stagedSession{input: in, dir: t.TempDir()}
			if e := s.consume(stagedRecord{Event: stagedEvent{Kind: "header", Input: &header}}); e == nil || s.replayState != nil {
				t.Fatal("unverified header entered reusable cache")
			}
		})
	}
	canonical, _, _ := stagedCanonicalInput(stagedTestInput(1))
	s, e := openStagedSession(context.Background(), canonical, StagedMappingOptions{StageDir: filepath.Join(t.TempDir(), "stage")})
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	hash := stagedHash(s.input)
	canonical.Documents[0].Annotation.Items[0].Name = "mutated caller"
	canonical.Documents[0].Annotation.Items[0].SourceSpan.Quote = "mutated caller"
	if hash != stagedHash(s.input) {
		t.Fatal("open session retains caller-owned slices/pointers")
	}
}

func TestStagedReplaySchemaRepairAndPoisonedScratchStop(t *testing.T) {
	canonical, _, _ := stagedCanonicalInput(stagedTestInput(1))
	s := stagedReplayHeader(t, canonical, t.TempDir())
	defer s.close()
	stagedReplayBind(t, s, stagedCatalogue{}, 1, true)
	if _, _, e := s.reconstructResponse(); !errors.Is(e, errStagedResponseSchema) || s.replayState.failure != nil {
		t.Fatal("complete schema failure poisoned legitimate repair")
	}
	db := s.replayState.db
	stagedReplayBind(t, s, stagedCatalogue{}, 2, false)
	if _, _, e := s.reconstructResponse(); e != nil || db != s.replayState.db {
		t.Fatalf("fixed repair did not reuse exact scratch setup: %v", e)
	}
	// An infrastructure error cannot be mistaken for a complete schema failure
	// or cause a replacement DB/retry inside this owned replay.
	db.Close()
	if _, _, e := s.reconstructResponse(); e == nil || errors.Is(e, errStagedResponseSchema) || s.replayState.failure == nil || s.replayState.db != nil {
		t.Fatal("infrastructure failure did not poison/close scratch")
	}
	if _, _, e := s.reconstructResponse(); e == nil || s.replayState.db != nil {
		t.Fatal("poisoned replay silently recreated scratch")
	}
}

func TestStagedReplayScratchClosedOnEveryReplayExit(t *testing.T) {
	for _, mode := range []string{"complete", "project-failure", "accepted-mismatch", "incomplete-tail"} {
		t.Run(mode, func(t *testing.T) {
			in := stagedTestInput(8)
			f := &stagedFake{pins: in.Identity.Admission, invalid: 1}
			_, dir := stagedTestRun(t, in, f)
			before := f.dispatched
			if mode == "accepted-mismatch" {
				stagedRewriteJournal(t, dir, func(row *stagedRecord) {
					if row.Event.Kind == "accepted" {
						row.Event.Output.Decisions[0].Target = "unbound accepted target"
					}
				})
			}
			if mode == "incomplete-tail" {
				file, e := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				_, e = file.WriteString(`{"Sequence":`)
				e = errors.Join(e, file.Close())
				if e != nil {
					t.Fatal(e)
				}
			}
			canonical, _, _ := stagedCanonicalInput(in)
			file, e := os.Open(filepath.Join(dir, "journal.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			defer file.Close()
			s := &stagedSession{input: canonical, dir: dir, file: file}
			var scratch *storage.DB
			projects := 0
			s.opts.fault = func(point string) error {
				if point == "after_replay_project" {
					projects++
					if scratch == nil {
						scratch = s.replayState.db
					} else if scratch != s.replayState.db {
						t.Fatal("multiple replay scratch DBs")
					}
					if mode == "project-failure" && projects == 2 {
						return errors.New("injected replay infrastructure failure")
					}
				}
				return nil
			}
			e = s.replay()
			if (e != nil) != (mode != "complete") || scratch == nil || s.replayState != nil {
				t.Fatalf("replay exit lifetime wrong: %v projects%d", e, projects)
			}
			if e = scratch.Conn().Ping(); e == nil {
				t.Fatal("replay left scratch open")
			}
			if f.dispatched != before {
				t.Fatal("offline replay paid")
			}
		})
	}
}

func TestStagedCheckpointCrashBoundariesNoRepayment(t *testing.T) {
	t.Parallel()
	for _, point := range []string{"before_header_sync", "after_header", "before_prepared_sync", "after_prepared", "before_intent_sync", "after_intent", "before_dispatch", "before_response_sync", "after_response", "before_accepted_sync", "after_accepted", "before_transaction_commit", "after_transaction_commit", "before_committed_sync", "after_committed"} {
		t.Run(point, func(t *testing.T) {
			in := stagedTestInput(8)
			f := &stagedFake{pins: in.Identity.Admission}
			dir := filepath.Join(t.TempDir(), "stage")
			fired := false
			fault := func(p string) error {
				if !fired && p == point && (f.dispatched > 0 || point == "before_header_sync" || point == "after_header" || point == "before_prepared_sync" || point == "after_prepared" || point == "before_intent_sync" || point == "after_intent" || point == "before_dispatch") {
					fired = true
					return errors.New("simulated crash")
				}
				return nil
			}
			_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Admitter: f, fault: fault})
			if e == nil || !fired {
				t.Fatalf("fault not exercised: %v", e)
			}
			before := f.dispatched
			r, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Resume: true, Admitter: f})
			if point == "before_intent_sync" || point == "after_intent" || point == "before_dispatch" {
				if !errors.Is(e, ErrStagedMappingUncertain) || f.dispatched != before {
					t.Fatal("indeterminate intent automatically repaid")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if len(stagedLeaves(r)[0].Members) != 8 || f.dispatched != 2 {
				t.Fatalf("resume repaid or lost batch: dispatched=%d before=%d", f.dispatched, before)
			}
		})
	}
}

func TestStagedCheckpointDispatchedErrorStopsPersistently(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(4)
	f := &stagedFake{pins: in.Identity.Admission, dispatchFault: true}
	dir := filepath.Join(t.TempDir(), "stage")
	_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Admitter: f})
	if !errors.Is(e, ErrStagedMappingUncertain) {
		t.Fatal(e)
	}
	before := f.dispatched
	f.dispatchFault = false
	_, e = RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Resume: true, Admitter: f})
	if !errors.Is(e, ErrStagedMappingUncertain) || f.dispatched != before {
		t.Fatal("unknown paid outcome was retried")
	}
}

func stagedRewriteJournal(t *testing.T, dir string, mutate func(*stagedRecord)) {
	t.Helper()
	raw, e := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	previous := ""
	var output []byte
	for _, line := range lines {
		var row stagedRecord
		if e = json.Unmarshal([]byte(line), &row); e != nil {
			t.Fatal(e)
		}
		mutate(&row)
		row.Previous = previous
		row.Hash = stagedHash(struct {
			Sequence int
			Previous string
			Event    stagedEvent
		}{row.Sequence, row.Previous, row.Event})
		previous = row.Hash
		b, _ := json.Marshal(row)
		output = append(output, b...)
		output = append(output, '\n')
	}
	if e = os.WriteFile(filepath.Join(dir, "journal.jsonl"), output, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestStagedCheckpointAcceptedMustReplayExactResponseAndSources(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"name", "source-owner", "birth-members", "saved-output"} {
		t.Run(mode, func(t *testing.T) {
			in := stagedTestInput(4)
			f := &stagedFake{pins: in.Identity.Admission}
			_, dir := stagedTestRun(t, in, f)
			before := f.dispatched
			stagedRewriteJournal(t, dir, func(row *stagedRecord) {
				if row.Event.Kind != "accepted" {
					return
				}
				switch mode {
				case "name":
					for i := range row.Event.State.Nodes {
						if row.Event.State.Nodes[i].Label == dktypes.LabelEntity {
							row.Event.State.Nodes[i].Name = "rehashed unauthorized name"
						}
					}
				case "source-owner":
					row.Event.State.Sources[0].Skill = "unauthorized skill"
				case "birth-members":
					row.Event.State.Births[0].Members = []string{"unauthorized member"}
				case "saved-output":
					row.Event.Output.Groups[0].Summary = "different accepted output"
				}
			})
			_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Resume: true, Admitter: f})
			if e == nil || f.dispatched != before {
				t.Fatal("accepted forged replay or repaid")
			}
		})
	}
}

func TestStagedCheckpointRejectsForeignSeedTamperAndChangedRoots(t *testing.T) {
	t.Parallel()
	in := stagedTestInput(4)
	for _, mode := range []string{"node", "source", "fts", "changed-root", "trailing-journal", "foreign-file"} {
		t.Run(mode, func(t *testing.T) {
			f := &stagedFake{pins: in.Identity.Admission}
			_, dir := stagedTestRun(t, in, f)
			before := f.dispatched
			if mode == "changed-root" {
				in.Identity.MethodSHA256 = stagedHash("changed-method")
				defer func() { in.Identity.MethodSHA256 = stagedHash("method") }()
			} else if mode == "trailing-journal" {
				file, e := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				_, _ = file.WriteString("incomplete")
				file.Close()
			} else if mode == "foreign-file" {
				_ = os.WriteFile(filepath.Join(dir, "foreign.db"), []byte("foreign"), 0600)
			} else {
				db, e := storage.OpenExisting(filepath.Join(dir, "stage.db"))
				if e != nil {
					t.Fatal(e)
				}
				query := map[string]string{"node": "UPDATE nodes SET name='tampered' WHERE label='Entity'", "source": "UPDATE node_sources SET skill='tampered'", "fts": "UPDATE nodes_fts SET summary='tampered'"}[mode]
				_, e = db.Conn().Exec(query)
				db.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
			_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Resume: true, Admitter: f})
			if e == nil || f.dispatched != before {
				t.Fatal("tampered stage adopted or dispatched")
			}
		})
	}
	dir := filepath.Join(t.TempDir(), "existing")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	f := &stagedFake{pins: in.Identity.Admission}
	if _, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Admitter: f}); e == nil || f.prepared != 0 {
		t.Fatal("fresh run accepted existing seed directory")
	}
	dir2 := filepath.Join(t.TempDir(), "stage")
	canonical, _, _ := stagedCanonicalInput(in)
	s, e := openStagedSession(context.Background(), canonical, StagedMappingOptions{StageDir: dir2})
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	if _, e = openStagedSession(context.Background(), canonical, StagedMappingOptions{StageDir: dir2, Resume: true}); e == nil {
		t.Fatal("concurrent stage owner accepted")
	}
}

// 原本任意历史重建错误都会被当作 schema 失败，篡改请求仍可消耗第二次付费尝试。
// Only a positively classified schema error after canonical-history validation permits repair.
func TestStagedCheckpointInvalidHistoryCannotHideRequestTamper(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"source-request", "noncanonical-prefix", "parent", "incomplete-response"} {
		t.Run(mode, func(t *testing.T) {
			in := stagedTestInput(2)
			f := &stagedFake{pins: in.Identity.Admission, invalid: 1}
			dir := filepath.Join(t.TempDir(), "stage")
			_, e := RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Admitter: f, fault: func(point string) error {
				if point == "after_invalid" {
					return errors.New("persisted invalid response")
				}
				return nil
			}})
			if e == nil || f.dispatched != 1 {
				t.Fatal("fixture did not persist one schema-invalid response")
			}
			var replacement StagedMappingWireBinding
			stagedRewriteJournal(t, dir, func(row *stagedRecord) {
				if row.Event.Kind == "prepared" {
					p := row.Event.Prepared
					switch mode {
					case "source-request":
						p.Call.Request.User = strings.Replace(p.Call.Request.User, "alpha name", "TAMPERED source", 1)
					case "noncanonical-prefix":
						p.Call.Units = p.Call.Units[1:]
					case "parent":
						p.Call.CatalogueSHA256 = stagedHash("foreign parent")
					}
					var wire map[string]any
					_ = json.Unmarshal(p.Wire, &wire)
					wire["messages"].([]any)[1].(map[string]any)["content"] = p.Call.Request.User
					p.Wire, _ = json.Marshal(wire)
					p.Binding.RequestSHA256 = stagedBytesHash(p.Wire)
					replacement = p.Binding
				}
				if row.Event.Kind == "response" {
					row.Event.Response.Binding = replacement
					if mode == "incomplete-response" {
						row.Event.Response.Response.FinishReason = "length"
					}
				}
			})
			before := f.dispatched
			_, e = RunStagedMapping(context.Background(), in, StagedMappingOptions{StageDir: dir, Resume: true, Admitter: f})
			if e == nil || f.dispatched != before {
				t.Fatalf("forged invalid history retried: %v calls%d", e, f.dispatched)
			}
		})
	}
}
