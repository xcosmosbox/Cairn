package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const corpusSHA = "20b61b5ab84de84a927420c5d265b7ec8d859ae49980699958a787ade9e4d28f"
const corpusBytes = 6785567

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
	WhitespaceTokens   int    `json:"whitespace_tokens"`
	Stratum            int    `json:"stratum"`
	Rank               int    `json:"rank"`
	WrapperSHA         string `json:"wrapper_sha256"`
	WrapperBytes       int    `json:"wrapper_bytes"`
	BodyStartByte      int    `json:"body_start_byte"`
	BodyStartCodepoint int    `json:"body_start_codepoint"`
}

type selection struct {
	Format      string        `json:"format"`
	CorpusSHA   string        `json:"corpus_sha256"`
	ProtocolSHA string        `json:"protocol_sha256"`
	Documents   []selectedDoc `json:"documents"`
	SkillSHA    string        `json:"skill_sha256"`
}

// Python re \S treats U+001C..U+001F as whitespace in addition to Unicode White_Space.
// Match the frozen selection rule rather than substituting model tokens.
func whitespaceCount(s string) int {
	return len(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }))
}

func parseCorpus(raw []byte) ([]corpusDoc, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("corpus JSON: %w", err)
	}
	seen := map[string]bool{}
	docs := make([]corpusDoc, 0, len(rows))
	for _, row := range rows {
		if len(row) != 7 {
			return nil, fmt.Errorf("corpus exact seven-column schema required")
		}
		for _, key := range []string{"title", "author", "source", "published_at", "category", "url", "body"} {
			if _, ok := row[key]; !ok {
				return nil, fmt.Errorf("corpus schema missing %s", key)
			}
		}
		b, _ := json.Marshal(row)
		var d corpusDoc
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("corpus fields must be strings")
		}
		if d.URL == "" || strings.TrimSpace(d.Body) == "" || seen[d.URL] {
			return nil, fmt.Errorf("empty or duplicate corpus document")
		}
		seen[d.URL] = true
		docs = append(docs, d)
	}
	return docs, nil
}

func choose(docs []corpusDoc) ([]corpusDoc, []selectedDoc, error) {
	if len(docs) < 12 {
		return nil, nil, fmt.Errorf("four strata require at least twelve documents")
	}
	docs = append([]corpusDoc(nil), docs...)
	sort.Slice(docs, func(i, j int) bool {
		a, b := whitespaceCount(docs[i].Body), whitespaceCount(docs[j].Body)
		if a != b {
			return a < b
		}
		return sha([]byte(docs[i].URL)) < sha([]byte(docs[j].URL))
	})
	var chosen []corpusDoc
	var entries []selectedDoc
	for k := 0; k < 4; k++ {
		lo, hi := k*len(docs)/4, (k+1)*len(docs)/4
		for _, offset := range []int{0, (hi - lo - 1) / 2, hi - lo - 1} {
			d := docs[lo+offset]
			id := sha([]byte(d.URL))
			chosen = append(chosen, d)
			entries = append(entries, selectedDoc{ID: id, URL: d.URL, Path: "references/" + id + ".md", BodySHA: sha([]byte(d.Body)), BodyBytes: len(d.Body), BodyCodepoints: utf8.RuneCountInString(d.Body), WhitespaceTokens: whitespaceCount(d.Body), Stratum: k, Rank: lo + offset})
		}
	}
	return chosen, entries, nil
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

const skill = "---\nname: multihop-public\ndescription: Fixed public article corpus for corpus-only capacity calibration.\n---\n\n# Public articles\n\nOriginal article references; no question or answer data.\n"

func prepare(corpusPath, out, protocolPath string) error {
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		return err
	}
	if len(raw) != corpusBytes || sha(raw) != corpusSHA {
		return fmt.Errorf("frozen corpus identity mismatch")
	}
	docs, err := parseCorpus(raw)
	if err != nil {
		return err
	}
	if len(docs) != 609 {
		return fmt.Errorf("expected609 corpus documents")
	}
	chosen, entries, err := choose(docs)
	if err != nil {
		return err
	}
	protocol, err := os.ReadFile(protocolPath)
	if err != nil {
		return err
	}
	if err = os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("new output required: %w", err)
	}
	for i, d := range chosen {
		wrapped, start, runes := wrap(d)
		entries[i].WrapperSHA = sha(wrapped)
		entries[i].WrapperBytes = len(wrapped)
		entries[i].BodyStartByte = start
		entries[i].BodyStartCodepoint = runes
		for _, tree := range []string{"source", "original"} {
			if err = writeBytesNew(filepath.Join(out, tree, entries[i].Path), wrapped); err != nil {
				return err
			}
		}
	}
	for _, tree := range []string{"source", "original"} {
		if err = writeBytesNew(filepath.Join(out, tree, "SKILL.md"), []byte(skill)); err != nil {
			return err
		}
	}
	s := selection{Format: "cairn-capacity-selection/v1", CorpusSHA: corpusSHA, ProtocolSHA: sha(protocol), Documents: entries, SkillSHA: sha([]byte(skill))}
	if err = writeBytesNew(filepath.Join(out, "protocol.json"), protocol); err != nil {
		return err
	}
	return writeNew(filepath.Join(out, "selection.json"), s)
}

func verifyPrepared(out string, mutable bool) (*selection, error) {
	var s selection
	if err := readJSON(filepath.Join(out, "selection.json"), &s); err != nil {
		return nil, err
	}
	if s.Format != "cairn-capacity-selection/v1" || s.CorpusSHA != corpusSHA || len(s.Documents) != 12 || s.SkillSHA != sha([]byte(skill)) {
		return nil, fmt.Errorf("selection identity mismatch")
	}
	protocol, err := os.ReadFile(filepath.Join(out, "protocol.json"))
	if err != nil || sha(protocol) != s.ProtocolSHA {
		return nil, fmt.Errorf("protocol identity mismatch")
	}
	for _, tree := range []string{"original", "source"} {
		if mutable && tree == "source" {
			continue
		}
		expected := map[string]string{"SKILL.md": s.SkillSHA}
		for _, d := range s.Documents {
			if d.ID != sha([]byte(d.URL)) || d.Path != "references/"+d.ID+".md" || expected[d.Path] != "" {
				return nil, fmt.Errorf("invalid selected document identity")
			}
			expected[d.Path] = d.WrapperSHA
			raw, e := os.ReadFile(filepath.Join(out, tree, d.Path))
			if e != nil || len(raw) != d.WrapperBytes || d.BodyStartByte < 0 || d.BodyStartByte > len(raw) || sha(raw[d.BodyStartByte:]) != d.BodySHA {
				return nil, fmt.Errorf("original body identity mismatch")
			}
		}
		found := 0
		err = filepath.WalkDir(filepath.Join(out, tree), func(path string, entry os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("linked source forbidden")
			}
			if entry.IsDir() {
				return nil
			}
			rel, e := filepath.Rel(filepath.Join(out, tree), path)
			if e != nil {
				return e
			}
			want, ok := expected[filepath.ToSlash(rel)]
			if !ok {
				return fmt.Errorf("unexpected source file")
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if sha(raw) != want {
				return fmt.Errorf("source hash mismatch")
			}
			found++
			return nil
		})
		if err != nil {
			return nil, err
		}
		if found != len(expected) {
			return nil, fmt.Errorf("source file missing")
		}
	}
	return &s, nil
}

func decodeStrict(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
