// cairn-eval-retrieve exposes production retrieval as one JSON response for
// evaluation runners. It never calls an LLM, migrates a DB, reads source files,
// or implements a second graph traversal/ranking algorithm.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/service/internal/service"
)

type source struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	MemberID  string `json:"member_id"`
	Skill     string `json:"skill"`
}

type relation struct {
	Source      string   `json:"source"`
	Target      string   `json:"target"`
	SourceName  string   `json:"source_name"`
	TargetName  string   `json:"target_name"`
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	Provenance  string   `json:"provenance"`
	SourceRefs  []string `json:"source_refs"`
}

type evidence struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Label      string     `json:"label"`
	Body       string     `json:"body"`
	SourceRefs []string   `json:"source_refs"`
	Provenance string     `json:"provenance"`
	Score      float64    `json:"score"`
	Sources    []source   `json:"sources"`
	Edges      []relation `json:"edges"`
	Domain     string     `json:"domain"`
	Subdomain  string     `json:"subdomain"`
	Confidence float64    `json:"confidence"`
	// BM25Rank is SQLite's raw rank (lower is better). FTS evidence uses
	// -BM25Rank as Score; graph Score is the unmodified production score.
	BM25Rank   *float64 `json:"bm25_rank,omitempty"`
	BM25Score  *float64 `json:"bm25_score,omitempty"`
	GraphScore *float64 `json:"graph_score,omitempty"`
	Depth      *int     `json:"depth,omitempty"`
}

type seed struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	BM25Rank float64 `json:"bm25_rank"`
}

type diagnostics struct {
	API                  string                 `json:"api"`
	QueryRewriter        string                 `json:"query_rewriter"`
	EvaluationScope      string                 `json:"evaluation_scope"`
	BypassedStages       []string               `json:"bypassed_stages"`
	RewrittenQuery       string                 `json:"rewritten_query"`
	MatchExpression      string                 `json:"match_expression"`
	QuerySyntax          dktypes.QuerySyntax    `json:"query_syntax"`
	TextProfile          storage.FTSTextProfile `json:"text_profile"`
	IndexProfile         string                 `json:"index_profile,omitempty"`
	IndexManifestSHA256  string                 `json:"index_manifest_sha256,omitempty"`
	DatabaseSHA256       string                 `json:"database_sha256,omitempty"`
	QueryMs              int64                  `json:"query_ms"`
	ReturnedBlocks       int                    `json:"returned_blocks"`
	FTSHits              int                    `json:"fts_hits"`
	FTSSeeds             []seed                 `json:"fts_seeds"`
	TraversalNodes       int                    `json:"traversal_nodes"`
	TraversalEdges       int                    `json:"traversal_edges"`
	TraversalBM25Entries int                    `json:"traversal_bm25_entries"`
	MaxBFSDepth          int                    `json:"max_bfs_depth"`
	MaxTraversalNodes    int                    `json:"max_traversal_nodes"`
	ScoreConvention      string                 `json:"score_convention"`
	ReadOnly             bool                   `json:"read_only"`
	Warnings             []string               `json:"warnings"`
}

type response struct {
	Mode        string      `json:"mode"`
	Query       string      `json:"query"`
	Evidence    []evidence  `json:"evidence"`
	Diagnostics diagnostics `json:"diagnostics"`
	Error       string      `json:"error,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("cairn-eval-retrieve", flag.ContinueOnError)
	flags.SetOutput(errOut)
	dbPath := flags.String("db", "", "existing knowledge DB (opened read-only)")
	mode := flags.String("mode", "", "fts or graph")
	query := flags.String("query", "", "query passed to the production QueryRewriter")
	querySyntax := flags.String("query-syntax", "text", "text (literal AND) or fts5 (unchanged advanced MATCH)")
	textProfile := flags.String("text-profile", "literal", "paired index/query profile: literal or han-v1")
	indexManifest := flags.String("index-manifest", "", "external index-copy manifest; required for han-v1")
	limit := flags.Int("limit", 10, "maximum evidence blocks (1..100)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	res := response{Mode: *mode, Query: *query, Evidence: []evidence{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	var binding indexBinding
	switch {
	case flags.NArg() != 0:
		err = errors.New("unexpected positional arguments")
	case *mode != "fts" && *mode != "graph":
		err = errors.New("--mode must be fts or graph")
	case strings.TrimSpace(*query) == "":
		err = errors.New("--query must not be blank")
	case *limit < 1 || *limit > 100:
		err = errors.New("--limit must be between 1 and 100")
	case *dbPath == "":
		err = errors.New("--db is required for fts and graph")
	case *querySyntax != "text" && *querySyntax != "fts5":
		err = errors.New("--query-syntax must be text or fts5")
	default:
		var db *storage.DB
		binding, err = validateIndexBinding(*dbPath, *indexManifest, storage.FTSTextProfile(*textProfile))
		if err == nil {
			path := *dbPath
			if binding.DatabasePath != "" {
				path = binding.DatabasePath
			}
			db, err = openDB(ctx, path)
		}
		if err == nil {
			res, err = retrieve(ctx, db, *mode, *query, *limit, dktypes.QuerySyntax(*querySyntax), storage.FTSTextProfile(*textProfile))
			res.Diagnostics.IndexProfile = binding.IndexProfile
			res.Diagnostics.IndexManifestSHA256 = binding.ManifestSHA256
			res.Diagnostics.DatabaseSHA256 = binding.DatabaseSHA256
			if closeErr := db.Close(); err == nil {
				err = closeErr
			}
			if err == nil && *indexManifest != "" {
				after, bindErr := validateIndexBinding(binding.DatabasePath, *indexManifest, storage.FTSTextProfile(*textProfile))
				if bindErr != nil {
					err = bindErr
				} else if after != binding {
					err = errors.New("index binding changed during retrieval")
				}
			}
		}
	}
	if err != nil {
		res.Error = err.Error()
		// Never return partial evidence as a successful zero-hit response.
		res.Evidence = []evidence{}
	}
	if encodeErr := json.NewEncoder(out).Encode(res); encodeErr != nil {
		return encodeErr
	}
	return err
}

func openDB(ctx context.Context, path string) (*storage.DB, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database: %w", err)
	}
	db, err := storage.OpenReadOnly(resolved)
	if err != nil {
		return nil, err
	}
	if err := storage.ValidateQueryContract(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("validate database: %w", err)
	}
	return db, nil
}

func retrieve(ctx context.Context, db *storage.DB, mode, query string, limit int, syntax dktypes.QuerySyntax, profile storage.FTSTextProfile) (response, error) {
	start := time.Now()
	res := response{Mode: mode, Query: query, Evidence: []evidence{}}
	res.Diagnostics = diagnostics{
		ReadOnly: true, FTSSeeds: []seed{}, Warnings: []string{},
		QueryRewriter:   "service.PrepareSearchQuery + storage.CompileFTSPlainText: literal AND by default; explicit fts5 passes through unchanged",
		EvaluationScope: "same-DB, same-FTS-seeds retrieval ablation: FTS versus production BFS/ranking; evidence formatting is shared by both modes",
		BypassedStages:  []string{"ResponseBuilder", "token packing", "MCP transport"},
	}
	rw := service.NewQueryRewriter(nil)
	prepared, err := service.PrepareSearchQuery(ctx, rw, query, syntax, profile)
	if err != nil {
		return res, err
	}
	res.Diagnostics.RewrittenQuery = prepared.RewrittenText
	res.Diagnostics.MatchExpression = prepared.MatchExpression
	res.Diagnostics.QuerySyntax = prepared.Syntax
	res.Diagnostics.TextProfile = prepared.TextProfile
	sources := storage.NewNodeSourceRepo(db)
	if mode == "fts" {
		res.Diagnostics.API = "service.PrepareSearchQuery + storage.FTSIndex.Search (all labels, same seed limit as graph)"
		res.Diagnostics.ScoreConvention = "negative SQLite BM25 rank; higher is better"
		idx := storage.NewFTSIndex(db)
		// Exactly the seed search used by SearchPipeline.Execute: no label
		// filter, extra query expansion, or adapter-side reranking.
		hits, err := idx.Search(ctx, prepared.MatchExpression, nil, "", limit)
		if err != nil {
			return res, err
		}
		res.Diagnostics.FTSHits = len(hits)
		for _, hit := range hits {
			block, err := makeEvidence(ctx, sources, hit.Node, nil, nil)
			if err != nil {
				return res, err
			}
			rank := hit.BM25Rank
			block.Score, block.BM25Rank = -rank, &rank
			res.Evidence = append(res.Evidence, block)
			res.Diagnostics.FTSSeeds = append(res.Diagnostics.FTSSeeds, seed{hit.Node.ID, string(hit.Node.Label), rank})
		}
	} else {
		res.Diagnostics.API = "service.SearchPipeline.Execute (QueryRewriter.Rewrite -> FTSIndex.Search -> TraverseBFS -> ScoreAndRank)"
		res.Diagnostics.ScoreConvention = "unmodified production ScoreAndRank FinalScore; higher is better"
		cfg := service.DefaultServiceConfig()
		cfg.MaxSearchResults = limit
		cfg.TextProfile = profile
		res.Diagnostics.MaxBFSDepth = cfg.MaxBFSDepth
		res.Diagnostics.MaxTraversalNodes = cfg.MaxTraversalNodes
		pipeline := service.NewSearchPipeline(db, rw, cfg)
		sc, err := pipeline.Execute(ctx, &dktypes.QueryRequest{Query: query, QuerySyntax: syntax})
		if err != nil {
			return res, err
		}
		res.Diagnostics.RewrittenQuery = sc.RewrittenQuery
		res.Diagnostics.FTSHits = len(sc.FTS5Hits)
		ranks := make(map[string]float64)
		for _, hit := range sc.FTS5Hits {
			if hit != nil && hit.Node != nil {
				ranks[hit.Node.ID] = hit.BM25Rank
				res.Diagnostics.FTSSeeds = append(res.Diagnostics.FTSSeeds, seed{hit.Node.ID, string(hit.Node.Label), hit.BM25Rank})
			}
		}
		tr := sc.TraversalResult
		res.Diagnostics.TraversalNodes = len(tr.Nodes)
		res.Diagnostics.TraversalEdges = len(tr.Edges)
		res.Diagnostics.TraversalBM25Entries = len(tr.FTS5Hits)
		if len(sc.FTS5Hits) > 0 && len(tr.FTS5Hits) == 0 {
			res.Diagnostics.Warnings = append(res.Diagnostics.Warnings, "production traversal contains no BM25 scores; adapter does not alter production ranking")
		}
		for _, hit := range sc.ScoredResults {
			if hit == nil || hit.Node == nil {
				return res, errors.New("production pipeline returned a nil scored node")
			}
			block, err := makeEvidence(ctx, sources, hit.Node, tr.Edges, tr.Nodes)
			if err != nil {
				return res, err
			}
			block.Score = hit.FinalScore
			bm25, graph, depth := hit.BM25Score, hit.GraphScore, tr.Depths[hit.Node.ID]
			block.BM25Score, block.GraphScore, block.Depth = &bm25, &graph, &depth
			if rank, ok := ranks[hit.Node.ID]; ok {
				block.BM25Rank = &rank
			}
			res.Evidence = append(res.Evidence, block)
		}
	}
	res.Diagnostics.ReturnedBlocks = len(res.Evidence)
	res.Diagnostics.QueryMs = time.Since(start).Milliseconds()
	return res, nil
}

func makeEvidence(ctx context.Context, repo *storage.NodeSourceRepo, node *dktypes.Node, edges []*dktypes.Edge, nodes map[string]*dktypes.Node) (evidence, error) {
	block := evidence{
		ID: node.ID, Name: node.Name, Label: string(node.Label),
		SourceRefs: splitRefs(node.SourceRefs), Provenance: string(node.Provenance),
		Domain: node.Domain, Subdomain: node.Subdomain, Confidence: node.Confidence,
		Sources: []source{}, Edges: []relation{},
	}
	rows, err := repo.ListByNode(ctx, node.ID)
	if err != nil {
		return block, fmt.Errorf("load evidence sources: %w", err)
	}
	for _, row := range rows {
		block.Sources = append(block.Sources, source{row.FilePath, row.StartLine, row.EndLine, row.MemberID, row.Skill})
	}
	parts := []string{node.Name}
	if node.Summary != "" {
		parts = append(parts, node.Summary)
	}
	if node.Description != "" && node.Description != node.Summary {
		parts = append(parts, node.Description)
	}
	for _, edge := range edges {
		if edge.SourceID != node.ID && edge.TargetID != node.ID {
			continue
		}
		block.Edges = append(block.Edges, relation{
			Source: edge.SourceID, Target: edge.TargetID,
			SourceName: nodeName(nodes, edge.SourceID), TargetName: nodeName(nodes, edge.TargetID),
			Kind: string(edge.Kind), Description: edge.Description,
			Provenance: string(edge.Provenance), SourceRefs: splitRefs(edge.SourceRefs),
		})
	}
	// Stable presentation only; graph discovery and ranking remain production code.
	sort.Slice(block.Edges, func(i, j int) bool {
		a, b := block.Edges[i], block.Edges[j]
		return strings.Join([]string{a.Source, a.Target, a.Kind, a.Description}, "\x00") < strings.Join([]string{b.Source, b.Target, b.Kind, b.Description}, "\x00")
	})
	for _, edge := range block.Edges {
		parts = append(parts, fmt.Sprintf("Relation: %s --[%s]--> %s: %s", edge.SourceName, edge.Kind, edge.TargetName, edge.Description))
	}
	block.Body = strings.Join(parts, "\n\n")
	return block, nil
}

func nodeName(nodes map[string]*dktypes.Node, id string) string {
	if node := nodes[id]; node != nil {
		return node.Name
	}
	return id
}

func splitRefs(value string) []string {
	refs := []string{}
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			refs = append(refs, part)
		}
	}
	return refs
}
