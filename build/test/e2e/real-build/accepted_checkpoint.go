package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	buildmeta "github.com/xcosmosbox/cairn/build/internal"
	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

const acceptedSourceCommit = "e7d45faa948353b77c6753ae1987f59498df1a8e"
const acceptedReferenceCount = 69

type checkpointEntry struct {
	Path               string `json:"path"`
	Skill              string `json:"skill"`
	Round              string `json:"round"`
	SourceSHA256       string `json:"source_sha256"`
	SourceGitBlob      string `json:"source_git_blob"`
	AnnotationFile     string `json:"annotation_file"`
	GateAFile          string `json:"gate_a_file"`
	GateBFile          string `json:"gate_b_file"`
	Units              int    `json:"units"`
	sourceContent      []byte
	annotationResponse string
}

type acceptedCheckpointClient struct {
	next                             llm.Client
	root                             string
	annotationSystem, semanticSystem string
	annotations                      map[string]*checkpointEntry
	gates                            map[string][]*checkpointEntry
	proof                            map[string]any
	annotationReplays                atomic.Int64
	gateReplays                      atomic.Int64
}

func (c *acceptedCheckpointClient) ProviderName() string { return c.next.ProviderName() }
func (c *acceptedCheckpointClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.System == c.annotationSystem {
		entry, ok := c.annotations[req.User]
		if !ok {
			return nil, fmt.Errorf("accepted checkpoint: annotation request does not exactly match an accepted source")
		}
		if err := c.checkSource(entry); err != nil {
			return nil, err
		}
		c.annotationReplays.Add(1)
		log.Printf("[e2e-checkpoint] replay real parsed annotation: %s (accepted round %s)", entry.Path, entry.Round)
		return &llm.CompleteResponse{Text: entry.annotationResponse, FinishReason: "stop"}, nil
	}
	if req.System == c.semanticSystem {
		entries, ok := c.gates[req.User]
		if !ok {
			return nil, fmt.Errorf("accepted checkpoint: semantic request does not exactly match recorded original and Gate A items")
		}
		for _, entry := range entries {
			if err := c.checkSource(entry); err != nil {
				return nil, err
			}
		}
		c.gateReplays.Add(1)
		log.Printf("[e2e-checkpoint] cached recorded pass, no new LLM judgement: %s (accepted round %s)", entries[0].Path, entries[0].Round)
		// This is a faithful readout of a real pass checkpoint, NOT the unavailable original LLM verdict/reason.
		return &llm.CompleteResponse{Text: `{"idempotent":true,"reason":"cached recorded pass; no new LLM judgement"}`, FinishReason: "stop"}, nil
	}
	if strings.HasPrefix(req.System, "你是知识结构化标注专家。") || strings.HasPrefix(req.System, "你是知识标注的语义核验专家。") {
		return nil, fmt.Errorf("accepted checkpoint: annotation or Gate B system prompt changed")
	}
	return c.next.Complete(ctx, req)
}
func (c *acceptedCheckpointClient) checkSource(entry *checkpointEntry) error {
	content, err := readConfinedSource(c.root, entry.Path)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, entry.sourceContent) {
		return fmt.Errorf("accepted checkpoint: source changed after verification: %s", entry.Path)
	}
	return nil
}
func (c *acceptedCheckpointClient) summary() map[string]any {
	return map[string]any{"annotation_replays": c.annotationReplays.Load(), "gate_b_recorded_pass_replays": c.gateReplays.Load(), "new_llm_judgement_for_replayed_gate_b": false, "proof": c.proof}
}
func readConfinedSource(root, rel string) ([]byte, error) {
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
		return nil, fmt.Errorf("accepted checkpoint: unsafe source path %q", rel)
	}
	target := filepath.Join(root, rel)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil, err
	}
	if resolved != target {
		return nil, fmt.Errorf("accepted checkpoint: symlink source rejected: %s", rel)
	}
	return os.ReadFile(target)
}
func readCheckpointJSON(path string, value any) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, value); err != nil {
		return nil, fmt.Errorf("accepted checkpoint: decode %s: %w", path, err)
	}
	return raw, nil
}
func sha256Hex(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
func gitBlob(raw []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(raw))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func annotateUser(path string, content []byte) string {
	s := "# 待标注文档\n\n文档路径 / path: " + path + "\n\n文档内容 / content:\n" + string(content)
	if !strings.HasSuffix(string(content), "\n") {
		s += "\n"
	}
	return s
}
func semanticUser(content []byte, items []dktypes.AnnotatedItem) (string, error) {
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", err
	}
	s := "# 语义幂等核验任务\n\n## 原始文档 / original document\n" + string(content)
	if !strings.HasSuffix(string(content), "\n") {
		s += "\n"
	}
	return s + "\n## 标注产出的 entity/concept 单元 / annotated items (JSON)\n" + string(raw) + "\n", nil
}
func sourcePrompt(file, name string) (string, error) {
	tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		return "", err
	}
	var eval func(ast.Expr) (string, error)
	eval = func(e ast.Expr) (string, error) {
		switch value := e.(type) {
		case *ast.BasicLit:
			if value.Kind == token.STRING {
				return strconv.Unquote(value.Value)
			}
		case *ast.BinaryExpr:
			if value.Op == token.ADD {
				left, err := eval(value.X)
				if err != nil {
					return "", err
				}
				right, err := eval(value.Y)
				return left + right, err
			}
		}
		return "", fmt.Errorf("accepted checkpoint: unsupported prompt expression")
	}
	for _, decl := range tree.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range v.Names {
				if n.Name == name && i < len(v.Values) {
					return eval(v.Values[i])
				}
			}
		}
	}
	return "", fmt.Errorf("accepted checkpoint: prompt %s absent from %s", name, file)
}

type constantResponse struct{ text string }

func (c constantResponse) ProviderName() string { return "parsed-checkpoint-validation" }
func (c constantResponse) Complete(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return &llm.CompleteResponse{Text: c.text, FinishReason: "stop"}, nil
}

func newAcceptedCheckpointClient(ctx context.Context, repo, dump, historicalCodeRoot, codeRoot string, next llm.Client) (*acceptedCheckpointClient, error) {
	root, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	dump, err = filepath.Abs(dump)
	if err != nil {
		return nil, err
	}
	var builder struct {
		Model            string `json:"model"`
		MaxTokens        int    `json:"max_tokens"`
		PromptSetVersion string `json:"prompt_set_version"`
	}
	if _, err = readCheckpointJSON(filepath.Join(filepath.Dir(dump), "builder.json"), &builder); err != nil {
		return nil, err
	}
	historicalPromptDigest, err := diskSourceDigest(historicalCodeRoot, "annotation/", "extract/")
	if err != nil {
		return nil, err
	}
	if builder.Model != "deepseek-flash" || builder.MaxTokens != 65536 || builder.PromptSetVersion != historicalPromptDigest {
		return nil, fmt.Errorf("accepted checkpoint: model, annotation budget or historical source digest does not match original builder")
	}
	// Only annotations and their rule-assigned IDs are reused. Extraction and all
	// downstream stages run afresh, so changes to repair must not falsify the old
	// annotation identity or force another paid annotation pass.
	reusedPrefixes := []string{"annotation/", "extract/identify.go"}
	historicalAnnotationDigest, err := diskSourceDigest(historicalCodeRoot, reusedPrefixes...)
	if err != nil {
		return nil, err
	}
	if historicalAnnotationDigest != buildmeta.SourceDigest(reusedPrefixes...) {
		return nil, fmt.Errorf("accepted checkpoint: compiled annotation or ID-assignment implementation changed")
	}
	annotatePrompt, err := sourcePrompt(filepath.Join(codeRoot, "build/internal/annotation/llm_annotator.go"), "annotateSystemPrompt")
	if err != nil {
		return nil, err
	}
	gatePrompt, err := sourcePrompt(filepath.Join(codeRoot, "build/internal/annotation/semantic_gate.go"), "semanticSystemPrompt")
	if err != nil {
		return nil, err
	}
	client := &acceptedCheckpointClient{next: next, root: root, annotationSystem: annotatePrompt, semanticSystem: gatePrompt, annotations: map[string]*checkpointEntry{}, gates: map[string][]*checkpointEntry{}}
	head, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != acceptedSourceCommit {
		return nil, fmt.Errorf("accepted checkpoint: source Git HEAD must remain %s", acceptedSourceCommit)
	}
	var scan []struct {
		Name           string
		ReferenceFiles []string
	}
	scanRaw, err := readCheckpointJSON(filepath.Join(dump, "00_scan.json"), &scan)
	if err != nil {
		return nil, err
	}
	references := map[string]string{}
	for _, skill := range scan {
		for _, path := range skill.ReferenceFiles {
			if _, exists := references[path]; exists {
				return nil, fmt.Errorf("accepted checkpoint: duplicate scanned source %s", path)
			}
			references[path] = skill.Name
		}
	}
	var accepted []*dktypes.AnnotatedDocument
	acceptedRaw, err := readCheckpointJSON(filepath.Join(dump, "04_extract_input.json"), &accepted)
	if err != nil {
		return nil, err
	}
	if len(references) != acceptedReferenceCount || len(accepted) != acceptedReferenceCount {
		return nil, fmt.Errorf("accepted checkpoint: expected complete 69-document scan and acceptance, got %d/%d", len(references), len(accepted))
	}
	var manifest []struct {
		Path string `json:"path"`
		SHA  string `json:"sha"`
	}
	if _, err = readCheckpointJSON(filepath.Join(filepath.Dir(filepath.Dir(dump)), "input-manifest.json"), &manifest); err != nil {
		return nil, err
	}
	expectedBlobs := map[string]string{}
	for _, record := range manifest {
		if _, dup := expectedBlobs[record.Path]; dup {
			return nil, fmt.Errorf("accepted checkpoint: duplicate input manifest path")
		}
		expectedBlobs[record.Path] = record.SHA
	}
	var cleanedFiles []string
	if err = filepath.WalkDir(filepath.Join(dump, "02_gate_a"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".json") {
			cleanedFiles = append(cleanedFiles, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	proofs := []checkpointEntry{}
	hashes := map[string]string{"00_scan.json": sha256Hex(scanRaw), "04_extract_input.json": sha256Hex(acceptedRaw)}
	seen := map[string]bool{}
	units := 0
	for _, doc := range accepted {
		if doc == nil || seen[doc.FilePath] || references[doc.FilePath] != doc.Skill {
			return nil, fmt.Errorf("accepted checkpoint: accepted document does not uniquely match scan")
		}
		seen[doc.FilePath] = true
		source, err := readConfinedSource(root, doc.FilePath)
		if err != nil {
			return nil, err
		}
		committed, err := exec.CommandContext(ctx, "git", "-C", root, "show", acceptedSourceCommit+":"+doc.FilePath).Output()
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(source, committed) || gitBlob(source) != expectedBlobs[doc.FilePath] {
			return nil, fmt.Errorf("accepted checkpoint: source differs from original Git/input manifest: %s", doc.FilePath)
		}
		matches := 0
		for _, cleanedFile := range cleanedFiles {
			var clean dktypes.AnnotatedDocument
			cleanRaw, err := readCheckpointJSON(cleanedFile, &clean)
			if err != nil {
				return nil, err
			}
			if clean.Skill != doc.Skill || clean.FilePath != doc.FilePath {
				continue
			}
			rel, err := filepath.Rel(filepath.Join(dump, "02_gate_a"), cleanedFile)
			if err != nil {
				return nil, err
			}
			stem := strings.TrimSuffix(rel, ".json")
			gateFile := filepath.Join(dump, "03_gate_b", stem+"_pass.json")
			if _, err = os.Stat(gateFile); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return nil, err
			}
			var gate struct {
				Passed bool `json:"passed"`
			}
			gateRaw, err := readCheckpointJSON(gateFile, &gate)
			if err != nil {
				return nil, err
			}
			if !gate.Passed {
				continue
			}
			assigned := extract.AssignIDs([]*dktypes.AnnotatedDocument{&clean})
			if len(assigned) != 1 || !reflect.DeepEqual(assigned[0], doc) {
				continue
			}
			annotationFile := filepath.Join(dump, "01_annotated", rel)
			var annotated dktypes.AnnotatedDocument
			annotatedRaw, err := readCheckpointJSON(annotationFile, &annotated)
			if err != nil {
				return nil, err
			}
			if annotated.Skill != doc.Skill || annotated.FilePath != doc.FilePath {
				return nil, fmt.Errorf("accepted checkpoint: round's annotation source differs")
			}
			reconstructed, err := json.Marshal(struct {
				Items []dktypes.AnnotatedItem `json:"items"`
			}{annotated.Items})
			if err != nil {
				return nil, err
			}
			parsed, err := annotation.NewLLMAnnotator(constantResponse{string(reconstructed)}, 65536, 1).Annotate(ctx, doc.Skill, doc.FilePath, string(source))
			if err != nil || !reflect.DeepEqual(parsed, &annotated) {
				return nil, fmt.Errorf("accepted checkpoint: production annotation parser does not reproduce recorded items: %s", doc.FilePath)
			}
			checked, err := annotation.NewFormatGate(0).Check(parsed)
			if err != nil || !reflect.DeepEqual(checked, &clean) {
				return nil, fmt.Errorf("accepted checkpoint: production Gate A does not reproduce recorded cleaned items: %s", doc.FilePath)
			}
			roundSplit := strings.LastIndex(stem, "_r")
			if roundSplit < 0 {
				return nil, fmt.Errorf("accepted checkpoint: round suffix absent")
			}
			entry := &checkpointEntry{Path: doc.FilePath, Skill: doc.Skill, Round: stem[roundSplit+2:], SourceSHA256: sha256Hex(source), SourceGitBlob: gitBlob(source), AnnotationFile: filepath.Join("01_annotated", rel), GateAFile: filepath.Join("02_gate_a", rel), GateBFile: filepath.Join("03_gate_b", stem+"_pass.json"), Units: len(clean.Items), sourceContent: source, annotationResponse: string(reconstructed)}
			annotationRequest := annotateUser(doc.FilePath, source)
			if _, dup := client.annotations[annotationRequest]; dup {
				return nil, fmt.Errorf("accepted checkpoint: duplicate annotation replay request")
			}
			client.annotations[annotationRequest] = entry
			gateRequest, err := semanticUser(source, clean.Items)
			if err != nil {
				return nil, err
			}
			client.gates[gateRequest] = append(client.gates[gateRequest], entry)
			hashes[entry.AnnotationFile] = sha256Hex(annotatedRaw)
			hashes[entry.GateAFile] = sha256Hex(cleanRaw)
			hashes[entry.GateBFile] = sha256Hex(gateRaw)
			proofs = append(proofs, *entry)
			units += len(clean.Items)
			matches++
		}
		if matches != 1 {
			return nil, fmt.Errorf("accepted checkpoint: %s has %d unique complete accepted chains", doc.FilePath, matches)
		}
	}
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].Path < proofs[j].Path })
	proofRaw, err := json.Marshal(proofs)
	if err != nil {
		return nil, err
	}
	client.proof = map[string]any{"checkpoint_dump": dump, "source_commit": acceptedSourceCommit, "source_digest_sha256": sha256Hex(proofRaw), "documents": len(proofs), "units": units, "original_annotation_max_tokens": builder.MaxTokens, "historical_prompt_set_version": builder.PromptSetVersion, "reused_annotation_implementation_digest": historicalAnnotationDigest, "current_prompt_set_version": buildmeta.SourceDigest("annotation/", "extract/"), "checkpoint_files_sha256": hashes, "accepted_sources": proofs, "gate_b_evidence": "recorded successful production SemanticGate; original raw LLM verdict/reason was not dumped", "extraction_reused": false}
	return client, nil
}

// diskSourceDigest reproduces buildmeta.SourceDigest for the explicitly reused
// historical source paths. These are source files, never credentials or git config.
func diskSourceDigest(root string, prefixes ...string) (string, error) {
	base := filepath.Join(root, "build", "internal")
	var names []string
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.Contains(rel, "/zz_") {
			return nil
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(rel, prefix) {
				names = append(names, rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("accepted checkpoint: historical source selection is empty")
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(name)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n", name)
		h.Write(raw)
		h.Write([]byte("\n"))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
