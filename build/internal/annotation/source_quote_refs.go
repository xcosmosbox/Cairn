package annotation

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

const sourceQuoteIndexMarker = "\n## 原文逐行定位索引 / exact source quote index\n"

type sourceQuoteLine struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

// These are byte locators, not selected assertions or semantic judgments. Every
// original byte, including blank lines and original line endings, is indexed.
type sourceQuoteIndex struct {
	Scope        string            `json:"source_quote_scope"`
	SourceSHA256 string            `json:"source_bytes_sha256"`
	Skill        string            `json:"skill"`
	Path         string            `json:"file_path"`
	Lines        []sourceQuoteLine `json:"lines"`
	original     string
	spans        [][2]int
}

func makeSourceQuoteIndex(original string, doc *dktypes.AnnotatedDocument) *sourceQuoteIndex {
	h := sha256.New()
	for _, field := range []string{"cairn-source-quote-v1", doc.Skill, doc.FilePath, original} {
		fmt.Fprintf(h, "%d:", len(field))
		h.Write([]byte(field))
	}
	index := &sourceQuoteIndex{Scope: fmt.Sprintf("%x", h.Sum(nil)), SourceSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(original))), Skill: doc.Skill, Path: doc.FilePath, Lines: []sourceQuoteLine{}, original: original}
	at := 0
	for _, line := range strings.SplitAfter(original, "\n") {
		if line == "" {
			continue
		} // A zero-byte trailing entry contains no original byte.
		index.Lines = append(index.Lines, sourceQuoteLine{ID: len(index.Lines) + 1, Text: line})
		index.spans = append(index.spans, [2]int{at, at + len(line)})
		at += len(line)
	}
	return index
}

func (index *sourceQuoteIndex) validateScope(raw json.RawMessage, required bool) error {
	if len(raw) == 0 && !required {
		return nil
	}
	var scope string
	if err := json.Unmarshal(raw, &scope); err != nil || index == nil || scope != index.Scope {
		return fmt.Errorf("missing or wrong source_quote_scope")
	}
	return nil
}

func subjectQuote(fields map[string]json.RawMessage, scope json.RawMessage, index *sourceQuoteIndex) (string, error) {
	_, literal := fields["source_quote"]
	ref, reference := fields["source_quote_ref"]
	if literal == reference {
		return "", fmt.Errorf("expected exactly one literal source_quote or source_quote_ref")
	}
	if literal {
		return coverageString(fields, "source_quote")
	}
	if err := index.validateScope(scope, true); err != nil {
		return "", err
	}
	parts, err := decodeUniqueObject(ref)
	if err != nil || len(parts) != 2 {
		return "", fmt.Errorf("source_quote_ref requires only start/end identifiers")
	}
	var start, end *int
	if json.Unmarshal(parts["start"], &start) != nil || json.Unmarshal(parts["end"], &end) != nil || start == nil || end == nil || *start < 1 || *end < *start || *end > len(index.spans) {
		return "", fmt.Errorf("invalid, reversed or out-of-bounds source_quote_ref")
	}
	quote := index.original[index.spans[*start-1][0]:index.spans[*end-1][1]]
	if strings.TrimSpace(quote) == "" {
		return "", fmt.Errorf("source_quote_ref has no non-whitespace source text")
	}
	return quote, nil
}
