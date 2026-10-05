package main

import (
	"context"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/service/internal/service"
)

type captureFindQuery struct {
	service.KnowledgeService
	keyword string
	opts    service.SearchOptions
	calls   int
}

func (s *captureFindQuery) Search(_ context.Context, keyword string, opts service.SearchOptions) (*service.SearchResult, error) {
	s.keyword, s.opts, s.calls = keyword, opts, s.calls+1
	return &service.SearchResult{}, nil
}

func TestFindQuerySyntaxFlag(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		syntax  dktypes.QuerySyntax
		keyword string
	}{
		{[]string{"net.ops-worker"}, dktypes.QuerySyntaxText, "net.ops-worker"},
		{[]string{"--query-syntax", "fts5", "alpha OR beta"}, dktypes.QuerySyntaxFTS5, "alpha OR beta"},
	} {
		svc := &captureFindQuery{}
		if err := runFind(svc, tc.args); err != nil {
			t.Fatal(err)
		}
		if svc.calls != 1 || svc.opts.QuerySyntax != tc.syntax || svc.keyword != tc.keyword {
			t.Fatalf("flag not passed to service: %+v", svc)
		}
	}
	svc := &captureFindQuery{}
	if err := runFind(svc, []string{"--query-syntax", "guess", "alpha"}); err == nil || svc.calls != 0 {
		t.Fatal("invalid syntax passed to service")
	}
	if err := runFind(svc, []string{"alpha OR beta", "--query-syntax", "fts5"}); err == nil || svc.calls != 0 {
		t.Fatal("keyword-first options were silently ignored")
	}
}
