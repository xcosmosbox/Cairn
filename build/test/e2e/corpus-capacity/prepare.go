package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func sha(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }

func readJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}

// writeNew refuses historical replacement and syncs recoverable receipts.
func writeNew(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesNew(path, append(raw, '\n'))
}

func writeBytesNew(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type corpusDoc struct {
	Title     string `json:"title"`
	Author    string `json:"author"`
	Source    string `json:"source"`
	Published string `json:"published_at"`
	Category  string `json:"category"`
	URL       string `json:"url"`
	Body      string `json:"body"`
}

type selectedDoc struct {
	ID                 string `json:"doc_id"`
	URL                string `json:"url"`
	Path               string `json:"path"`
	BodySHA            string `json:"body_sha256"`
	BodyBytes          int    `json:"body_bytes"`
	BodyCodepoints     int    `json:"body_codepoints"`
	WrapperSHA         string `json:"wrapper_sha256"`
	WrapperBytes       int    `json:"wrapper_bytes"`
	BodyStartByte      int    `json:"body_start_byte"`
	BodyStartCodepoint int    `json:"body_start_codepoint"`
}

type selection struct {
	Format            string        `json:"format"`
	CorpusSHA         string        `json:"corpus_sha256"`
	IntendedDocuments int           `json:"intended_documents"`
	Documents         []selectedDoc `json:"documents"`
	SkillSHA          string        `json:"skill_sha256"`
}

// Source rows are local units: malformed documents remain in the preparation
// outcome roster without discarding independent originals.
type sourceOutcome struct {
	Index    int             `json:"index"`
	Document string          `json:"document_id,omitempty"`
	Status   string          `json:"status"`
	Reason   string          `json:"reason,omitempty"`
	Raw      json.RawMessage `json:"raw_source"`
}

func parseCorpus(raw []byte) ([]corpusDoc, []sourceOutcome, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, nil, fmt.Errorf("corpus JSON: %w", err)
	}
	seen := map[string]bool{}
	var docs []corpusDoc
	outcomes := make([]sourceOutcome, 0, len(rows))
	for index, row := range rows {
		var d corpusDoc
		outcome := sourceOutcome{Index: index, Status: "invalid", Raw: append(json.RawMessage(nil), row...)}
		decoder := json.NewDecoder(bytes.NewReader(row))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&d); err != nil {
			outcome.Reason = "invalid_document_schema"
		} else if d.URL == "" || strings.TrimSpace(d.Body) == "" {
			outcome.Reason = "missing_url_or_body"
		} else if seen[d.URL] {
			outcome.Document = sha([]byte(d.URL))
			outcome.Reason = "duplicate_url"
		} else {
			seen[d.URL] = true
			outcome.Document = sha([]byte(d.URL))
			outcome.Status = "prepared"
			docs = append(docs, d)
		}
		outcomes = append(outcomes, outcome)
	}
	return docs, outcomes, nil
}

func wrap(d corpusDoc) ([]byte, int, int) {
	metadata, _ := json.Marshal(struct {
		Title     string `json:"title"`
		Source    string `json:"source"`
		Published string `json:"published_at"`
		URL       string `json:"url"`
	}{d.Title, d.Source, d.Published, d.URL})
	header := "# Original source\n\n" + string(metadata) + "\n\n"
	return []byte(header + d.Body), len(header), utf8.RuneCountInString(header)
}

const skill = "---\nname: multihop-public\ndescription: Declared source-only article corpus for construction diagnostics.\n---\n\n# Public articles\n\nOriginal article references; no question or answer data.\n"

func prepare(corpusPath, out string) error {
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		return err
	}
	docs, outcomes, err := parseCorpus(raw)
	if err != nil {
		return err
	}
	if err = os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("new output required: %w", err)
	}
	entries := make([]selectedDoc, 0, len(docs))
	for _, d := range docs {
		wrapped, start, runes := wrap(d)
		id := sha([]byte(d.URL))
		entry := selectedDoc{ID: id, URL: d.URL, Path: "references/" + id + ".md", BodySHA: sha([]byte(d.Body)), BodyBytes: len(d.Body), BodyCodepoints: utf8.RuneCountInString(d.Body), WrapperSHA: sha(wrapped), WrapperBytes: len(wrapped), BodyStartByte: start, BodyStartCodepoint: runes}
		entries = append(entries, entry)
		for _, tree := range []string{"source", "original"} {
			if err = writeBytesNew(filepath.Join(out, tree, entry.Path), wrapped); err != nil {
				return err
			}
		}
	}
	for _, tree := range []string{"source", "original"} {
		if err = writeBytesNew(filepath.Join(out, tree, "SKILL.md"), []byte(skill)); err != nil {
			return err
		}
	}
	if err = writeNew(filepath.Join(out, "preparation-outcomes.json"), outcomes); err != nil {
		return err
	}
	return writeNew(filepath.Join(out, "selection.json"), selection{Format: "cairn-capacity-selection/v2", CorpusSHA: sha(raw), Documents: entries, SkillSHA: sha([]byte(skill)), IntendedDocuments: len(outcomes)})
}

// bindPrepared checks current sources once. An unavailable document only removes
// its own derived input; independent originals keep their declared denominator.
func bindPrepared(out string) (*selection, error) {
	var s selection
	if err := readJSON(filepath.Join(out, "selection.json"), &s); err != nil {
		return nil, err
	}
	if s.Format != "cairn-capacity-selection/v2" || len(s.CorpusSHA) != 64 || s.IntendedDocuments < len(s.Documents) || s.SkillSHA != sha([]byte(skill)) {
		return nil, fmt.Errorf("selection identity mismatch")
	}
	expected := map[string]bool{"SKILL.md": true}
	for _, d := range s.Documents {
		expected["references/"+sha([]byte(d.URL))+".md"] = true
	}
	var outcomes []sourceOutcome
	if err := filepath.WalkDir(filepath.Join(out, "source"), func(path string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(filepath.Join(out, "source"), path)
		if e != nil {
			return e
		}
		if !expected[filepath.ToSlash(rel)] || entry.Type()&os.ModeSymlink != 0 {
			outcomes = append(outcomes, sourceOutcome{Status: "invalid", Reason: "unrostered_or_linked_source", Document: filepath.ToSlash(rel)})
			return os.Remove(path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(out, "source", "SKILL.md"))
	if err != nil || sha(raw) != s.SkillSHA {
		return nil, fmt.Errorf("source discovery identity unavailable")
	}
	valid := make([]selectedDoc, 0, len(s.Documents))
	seen := map[string]bool{}
	for index, d := range s.Documents {
		safePath := "references/" + sha([]byte(d.URL)) + ".md"
		outcome := sourceOutcome{Index: index, Document: d.ID, Status: "unavailable"}
		if d.ID != sha([]byte(d.URL)) || d.Path != safePath || seen[safePath] {
			outcome.Reason = "invalid_document_identity"
		} else {
			for _, tree := range []string{"original", "source"} {
				raw, e := os.ReadFile(filepath.Join(out, tree, safePath))
				if e != nil || sha(raw) != d.WrapperSHA || len(raw) != d.WrapperBytes || d.BodyStartByte < 0 || d.BodyStartByte > len(raw) || sha(raw[d.BodyStartByte:]) != d.BodySHA {
					outcome.Reason = "source_identity_unavailable"
					break
				}
			}
			if outcome.Reason == "" {
				outcome.Status = "available"
				seen[safePath] = true
				valid = append(valid, d)
			}
		}
		if outcome.Status != "available" && !seen[safePath] {
			if e := os.Remove(filepath.Join(out, "source", safePath)); e != nil && !os.IsNotExist(e) {
				return nil, e
			}
		}
		outcomes = append(outcomes, outcome)
	}
	s.Documents = valid
	if err := writeNew(filepath.Join(out, "source-outcomes.json"), outcomes); err != nil {
		log.Printf("WARN source outcome sink unavailable: %v", err)
	}
	return &s, nil
}
