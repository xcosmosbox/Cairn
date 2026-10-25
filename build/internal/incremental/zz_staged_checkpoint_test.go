package incremental

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

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
