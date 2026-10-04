package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// PreparedSearchQuery separates rewritten user text from executable MATCH.
type PreparedSearchQuery struct {
	RewrittenText   string
	MatchExpression string
	Syntax          dktypes.QuerySyntax
	TextProfile     storage.FTSTextProfile
}

// PrepareSearchQuery is shared by production graph retrieval and FTS ablations.
// Advanced syntax passes through verbatim, without abbreviation substitution or
// Han transformation. Users of advanced syntax address the index's real tokens.
func PrepareSearchQuery(ctx context.Context, rw *QueryRewriter, query string, syntax dktypes.QuerySyntax, profile storage.FTSTextProfile) (PreparedSearchQuery, error) {
	result := PreparedSearchQuery{Syntax: syntax, TextProfile: profile}
	if !syntax.IsValid() {
		return result, fmt.Errorf("unsupported query syntax %q (expected text or fts5)", syntax)
	}
	if result.Syntax == "" {
		result.Syntax = dktypes.QuerySyntaxText
	}
	if result.TextProfile == "" {
		result.TextProfile = storage.FTSTextLiteral
	}
	if _, err := storage.NormalizeFTSText(result.TextProfile, ""); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(query) == "" {
		return result, fmt.Errorf("query must not be blank")
	}
	if result.Syntax == dktypes.QuerySyntaxFTS5 {
		result.RewrittenText, result.MatchExpression = query, query
		return result, nil
	}
	if rw == nil {
		rw = NewQueryRewriter(nil)
	}
	var err error
	result.RewrittenText, err = rw.Rewrite(ctx, query)
	if err == nil {
		result.MatchExpression, err = storage.CompileFTSPlainText(result.RewrittenText, result.TextProfile)
	}
	return result, err
}
