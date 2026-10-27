package incremental

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

func cancellationReflowFixture(t *testing.T) (*storage.DB, *stores, []*dktypes.Node, *DirtySet, *alignOutcome) {
	t.Helper()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db"), MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := newStores(db)
	dirty := NewDirtySet()
	ao := &alignOutcome{pending: map[string]*pendingNode{}, unitsByID: map[string]*alignUnit{
		"member": {ID: "member", FilePath: "new.md", Content: "The pump runs only above 2 bar."},
	}}
	var nodes []*dktypes.Node
	for _, id := range []string{"11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222"} {
		n := &dktypes.Node{ID: id, Label: dktypes.LabelEntity, Name: "Atlas pump", Summary: "Qualified Atlas pump", Description: "The pump must not run during maintenance.", Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .9}
		if err := st.nodes.Insert(context.Background(), n); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
		dirty.DirtyNodeFused(id, SubdomainKey{n.Domain, n.Subdomain})
		ao.pending[id] = &pendingNode{UUID: id, Domain: n.Domain, Subdomain: n.Subdomain, NewMembers: []string{"member"}}
	}
	return db, st, nodes, dirty, ao
}

func assertCancelledReflow(t *testing.T, r *reflower, c *nodeReflowClient, st *stores, nodes []*dktypes.Node, written map[string]string, wantReason string) {
	t.Helper()
	outcomes := r.nodeDescriptionOutcomes()
	if len(c.requests) != 0 || len(written) != 0 || len(outcomes) != len(nodes) {
		t.Fatalf("cancelled scope performed work or lost outcomes: calls=%d replacements=%v outcomes=%+v", len(c.requests), written, outcomes)
	}
	for i, n := range nodes {
		out := outcomes[i]
		if out.ID != n.ID || out.Domain != n.Domain || out.Subdomain != n.Subdomain || out.Outcome != annotation.DocumentCancelled || !strings.Contains(out.Reason, wantReason) || len(out.Attempts) != 0 || out.Exhausted {
			t.Fatalf("cancelled lookup lost factual identity/reason: %+v", out)
		}
		stored, err := st.nodes.GetByID(context.Background(), n.ID)
		if err != nil || stored == nil || stored.ID != n.ID || stored.Summary != n.Summary || stored.Description != n.Description {
			t.Fatalf("cancelled lookup changed retained pair: node=%+v err=%v", stored, err)
		}
	}
}

// 原先取消/超时的 SQLite 读取被写成 unavailable，丢失了 caller 的真实终止原因。
// Interrupted existing-node reads must report cancellation for every intended node.
func TestNodeReflowExistingReadCallerCancellation(t *testing.T) {
	t.Parallel()
	for _, deadline := range []bool{false, true} {
		name := "cancelled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, st, nodes, dirty, ao := cancellationReflowFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			wantReason := context.Canceled.Error()
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
				wantReason = context.DeadlineExceeded.Error()
			} else {
				cancel()
			}
			defer cancel()
			c := &nodeReflowClient{}
			r := newReflower(c, 4096, st)
			written := r.fuseDescriptions(ctx, dirty, ao, newRunState())
			assertCancelledReflow(t, r, c, st, nodes, written, wantReason)
		})
	}
}

// lookupObservedContext 只观察真实 database/sql 开始读取取消信号，不伪造读取结果。
// It exposes entry into the real SQL lookup without sleeps or timing assumptions.
type lookupObservedContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (ctx *lookupObservedContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Done()
}

func TestNodeReflowExistingReadCancellationInFlight(t *testing.T) {
	t.Parallel()
	db, st, nodes, dirty, ao := cancellationReflowFixture(t)
	// 占住唯一 SQL 连接，确保 GetByID 不能在取消前成功返回。
	// Holding the sole connection prevents lookup success before cancellation.
	held, err := db.Conn().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &lookupObservedContext{Context: base, entered: make(chan struct{})}
	c := &nodeReflowClient{}
	r := newReflower(c, 4096, st)
	result := make(chan map[string]string, 1)
	go func() { result <- r.fuseDescriptions(ctx, dirty, ao, newRunState()) }()
	<-ctx.entered
	cancel()
	written := <-result
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	assertCancelledReflow(t, r, c, st, nodes, written, context.Canceled.Error())
}

// 取消分类修复不得把真正的缺失节点/存储失败改成 cancelled，或发起模型调用。
// Ordinary missing/store failures retain their unavailable outcome.
func TestNodeReflowExistingReadFailuresRemainUnavailable(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing-node", "closed-store", "missing-store"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			db, st, nodes, dirty, ao := cancellationReflowFixture(t)
			switch failure {
			case "missing-node":
				if _, err := db.Conn().Exec("DELETE FROM nodes"); err != nil {
					t.Fatal(err)
				}
			case "closed-store":
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			case "missing-store":
				st = nil
			}
			c := &nodeReflowClient{}
			r := newReflower(c, 4096, st)
			written := r.fuseDescriptions(context.Background(), dirty, ao, newRunState())
			outcomes := r.nodeDescriptionOutcomes()
			if len(c.requests) != 0 || len(written) != 0 || len(outcomes) != len(nodes) {
				t.Fatalf("unavailable scope performed work or lost outcomes: calls=%d replacements=%v outcomes=%+v", len(c.requests), written, outcomes)
			}
			for i, n := range nodes {
				out := outcomes[i]
				if out.ID != n.ID || out.Domain != n.Domain || out.Subdomain != n.Subdomain || out.Outcome != annotation.DocumentUnavailable || out.Reason == "" || len(out.Attempts) != 0 || out.Exhausted {
					t.Fatalf("ordinary lookup failure misclassified: %+v", out)
				}
			}
		})
	}
}
