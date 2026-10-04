package storage

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FTSTextProfile describes the paired index/query text transformation. It does
// not change MATCH syntax or choose AND/OR. The index must use the same profile.
type FTSTextProfile string

const (
	FTSTextLiteral FTSTextProfile = "literal"
	// FTSTextHanV1 is an opt-in experiment for a separately rebuilt index.
	// It creates Han-character phrase matches, not byte-exact substrings:
	// unicode61 can match across punctuation and serialized synonym boundaries.
	FTSTextHanV1 FTSTextProfile = "han-v1"
)

// NormalizeFTSText is shared by query compilation and experimental index copies.
// It preserves ASCII tokenization and uses the Go toolchain's Unicode Han table.
// Production indexes remain literal unless explicitly rebuilt by the caller.
func NormalizeFTSText(profile FTSTextProfile, text string) (string, error) {
	switch profile {
	case FTSTextLiteral:
		return text, nil
	case FTSTextHanV1:
		var out strings.Builder
		for _, r := range text {
			if unicode.Is(unicode.Han, r) {
				out.WriteByte(' ')
				out.WriteRune(r)
				out.WriteByte(' ')
			} else {
				out.WriteRune(r)
			}
		}
		return out.String(), nil
	default:
		return "", fmt.Errorf("unsupported FTS text profile %q (expected literal or han-v1)", profile)
	}
}

// CompileFTSPlainText quotes each original whitespace-delimited atom and joins
// atoms with AND. All user punctuation and uppercase operators are literal;
// SQLite's tokenizer still defines matching, so punctuation need not be exact.
// Splitting must precede normalization: splitting Han output would destroy the
// ordered phrase and incorrectly turn individual characters into AND terms.
func CompileFTSPlainText(query string, profile FTSTextProfile) (string, error) {
	if _, err := NormalizeFTSText(profile, ""); err != nil {
		return "", err
	}
	if !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return "", fmt.Errorf("plain query must be valid UTF-8 without NUL bytes")
	}
	atoms := strings.Fields(query)
	if len(atoms) == 0 {
		return "", fmt.Errorf("query must not be blank")
	}
	for i, atom := range atoms {
		text, err := NormalizeFTSText(profile, atom)
		if err != nil {
			return "", err
		}
		atoms[i] = `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
	}
	return strings.Join(atoms, " AND "), nil
}
