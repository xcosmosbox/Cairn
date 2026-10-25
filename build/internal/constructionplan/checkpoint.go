// Package constructionplan 提供离线、无副作用的历史标注导入与有界计划。
// It verifies supplied bytes; it never dispatches construction or establishes external trust.
package constructionplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// MethodIdentity 保留旧方法，不把已接受的旧标注改称新方法产物。
// Historical identity must match an independently supplied compatibility allowlist.
type MethodIdentity struct {
	SourceCommit         string `json:"source_commit"`
	BuilderCommit        string `json:"builder_commit"`
	AnnotationMethod     string `json:"annotation_method"`
	GatePolicy           string `json:"gate_policy"`
	ProtocolSHA256       string `json:"protocol_sha256"`
	RuntimeOverlaySHA256 string `json:"runtime_overlay_sha256"`
}

// FrozenDocument 是外部冻结语料名册的一项；URL 与正文 hash 不来源于待验证的04声明。
// The caller must independently authenticate this roster; a hash string alone is not trust.
type FrozenDocument struct {
	DocID      string `json:"doc_id"`
	URL        string `json:"url"`
	Skill      string `json:"skill"`
	Path       string `json:"path"`
	BodySHA256 string `json:"body_sha256"`
}

// TrustPolicy 是调用方独立提供的预期，不能由不可信归档自我签发。
// ArchiveCommit is a provenance pin, not a signature verified by this pure byte validator.
type TrustPolicy struct {
	ManifestSHA256     string
	ArchiveCommit      string
	CorpusSHA256       string
	RosterSHA256       string
	ExpectedDocuments  int
	Documents          []FrozenDocument
	CompatibleMethods  []MethodIdentity
	RequiredGatePolicy string
}

// AcceptedDocumentDescriptor 明确绑定同一文档与同一通过轮次；文件名由代码推导。
// A pass artifact for another document/round cannot qualify this document.
type AcceptedDocumentDescriptor struct {
	DocID string
	Round int
}

// ArchiveDescriptor 只描述已有归档，不表示其真实性已被本包证明。
// Prefix is the archived attempt directory, e.g. raw/attempt-01 in the actual #63 format.
type ArchiveDescriptor struct {
	Prefix    string
	Identity  MethodIdentity
	Documents []AcceptedDocumentDescriptor
}

// ArchiveInput 只包含所需依赖的原始字节；不要求复制或声称核验所有归档文件。
// Missing required bytes fail closed; no filesystem/network access occurs here.
type ArchiveInput struct {
	Manifest   []byte
	Files      map[string][]byte
	Descriptor ArchiveDescriptor
}

type archiveFile struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type archiveManifest struct {
	Format       string        `json:"format"`
	SourceCommit string        `json:"source_commit"`
	Files        []archiveFile `json:"files"`
}
type selectedSource struct {
	DocID         string `json:"doc_id"`
	URL           string `json:"url"`
	Path          string `json:"path"`
	BodySHA256    string `json:"body_sha256"`
	BodyBytes     int    `json:"body_bytes"`
	WrapperSHA256 string `json:"wrapper_sha256"`
	WrapperBytes  int    `json:"wrapper_bytes"`
	BodyStartByte int    `json:"body_start_byte"`
}
type frozenSource struct {
	Skill   string `json:"skill"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// VerifiedCheckpoint 的内部记录不导出，避免调用者把未校验的04直接送给计划器。
// Accessors return copies; a zero checkpoint is invalid and cannot become an admissible plan.
type VerifiedCheckpoint struct {
	verified                                         bool
	identity                                         MethodIdentity
	archiveCommit, manifestSHA, corpusSHA, rosterSHA string
	roster                                           []FrozenDocument
	documents                                        []VerifiedDocument
}

type VerifiedDocument struct {
	Source             FrozenDocument            `json:"source"`
	Round              int                       `json:"accepted_round"`
	OriginalSHA256     string                    `json:"original_sha256"`
	GateASHA256        string                    `json:"gate_a_sha256"`
	GateBSHA256        string                    `json:"gate_b_sha256"`
	ExtractInputSHA256 string                    `json:"extract_input_sha256"`
	Annotation         dktypes.AnnotatedDocument `json:"annotation"`
}

func (c *VerifiedCheckpoint) Documents() []VerifiedDocument {
	if c == nil {
		return nil
	}
	out := make([]VerifiedDocument, len(c.documents))
	copy(out, c.documents)
	for i := range out {
		out[i].Annotation = cloneDocument(out[i].Annotation)
	}
	return out
}
func (c *VerifiedCheckpoint) Identity() MethodIdentity {
	if c == nil {
		return MethodIdentity{}
	}
	return c.identity
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// RosterDigest 对docID排序后编码元数据；只计算身份，不授予名册外部真实性。
// Callers must authenticate the resulting pin through an independent frozen roster root.
func RosterDigest(documents []FrozenDocument) string {
	canonical := append([]FrozenDocument(nil), documents...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].DocID < canonical[j].DocID })
	raw, _ := json.Marshal(canonical)
	return digest(raw)
}

func validHex(value string, n int) bool {
	b, e := hex.DecodeString(value)
	return e == nil && len(b) == n && value == strings.ToLower(value)
}
func validPath(value string) bool {
	return value != "" && value != "." && value != ".." && path.Clean(value) == value && !path.IsAbs(value) && !strings.HasPrefix(value, "../") && !strings.Contains(value, "\\")
}
func cloneDocument(d dktypes.AnnotatedDocument) dktypes.AnnotatedDocument {
	d.Items = append([]dktypes.AnnotatedItem(nil), d.Items...)
	for i := range d.Items {
		if d.Items[i].SourceSpan != nil {
			span := *d.Items[i].SourceSpan
			d.Items[i].SourceSpan = &span
		}
	}
	return d
}
func decode(data []byte, out any, strict bool) error {
	raw, err := exactJSON(data)
	if err != nil {
		return err
	}
	if strict {
		if err := exactKeys(raw, reflect.TypeOf(out)); err != nil {
			return err
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON or prose")
	}
	return nil
}

// encoding/json容忍字段大小写差异；来源比较只接受schema中精确拼写的键。
// Validate exact schema keys before typed decoding can fold case distinctions away.
func exactKeys(raw any, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if object, ok := raw.(map[string]any); ok && typ.Kind() == reflect.Struct {
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
		for key, value := range object {
			fieldType, exists := fields[key]
			if !exists {
				return fmt.Errorf("unknown/noncanonical JSON key %q", key)
			}
			if err := exactKeys(value, fieldType); err != nil {
				return err
			}
		}
	}
	if array, ok := raw.([]any); ok && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
		for _, value := range array {
			if err := exactKeys(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// 精确比较不能经由结构体补零或折叠null；重复字段也不能采用“最后一个获胜”。
// Preserve field presence and numeric spelling, rejecting ambiguous duplicate JSON keys.
func exactJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value func() (any, error)
	value = func() (any, error) {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch token {
		case json.Delim('{'):
			object := map[string]any{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("invalid object key")
				}
				if _, exists := object[name]; exists {
					return nil, fmt.Errorf("duplicate JSON key %q", name)
				}
				child, err := value()
				if err != nil {
					return nil, err
				}
				object[name] = child
			}
			_, err = dec.Token()
			return object, err
		case json.Delim('['):
			array := []any{}
			for dec.More() {
				child, err := value()
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			_, err = dec.Token()
			return array, err
		default:
			return token, nil
		}
	}
	result, err := value()
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON or prose")
	}
	return result, nil
}

// ImportCheckpoint 验证外部钉住的 manifest、必需文件和02→04一致性，保留所有来源关联。
// Success proves internal consistency relative to supplied pins, not cryptographic authenticity/admission.
func ImportCheckpoint(input ArchiveInput, policy TrustPolicy) (*VerifiedCheckpoint, error) {
	fail := func(format string, args ...any) (*VerifiedCheckpoint, error) {
		return nil, fmt.Errorf("constructionplan.ImportCheckpoint: "+format, args...)
	}
	if !validHex(policy.ManifestSHA256, 32) || !validHex(policy.ArchiveCommit, 20) || !validHex(policy.CorpusSHA256, 32) || !validHex(policy.RosterSHA256, 32) || strings.TrimSpace(policy.RequiredGatePolicy) == "" {
		return fail("missing/invalid independent trust pins")
	}
	if len(input.Manifest) == 0 || digest(input.Manifest) != policy.ManifestSHA256 {
		return fail("manifest hash mismatch or missing manifest")
	}
	if policy.ExpectedDocuments <= 0 || len(policy.Documents) != policy.ExpectedDocuments || RosterDigest(policy.Documents) != policy.RosterSHA256 {
		return fail("incomplete frozen roster: got %d expected %d", len(policy.Documents), policy.ExpectedDocuments)
	}
	roster := make(map[string]FrozenDocument, len(policy.Documents))
	ownership := map[string]bool{}
	for _, d := range policy.Documents {
		key := d.Skill + "\x00" + d.Path
		if !validHex(d.DocID, 32) || strings.TrimSpace(d.URL) == "" || digest([]byte(d.URL)) != d.DocID || !validHex(d.BodySHA256, 32) || strings.TrimSpace(d.Skill) == "" || d.Path != "references/"+d.DocID+".md" || roster[d.DocID].DocID != "" || ownership[key] {
			return fail("invalid/duplicate frozen document identity %q", d.DocID)
		}
		roster[d.DocID] = d
		ownership[key] = true
	}
	id := input.Descriptor.Identity
	builderSourceMatches := id.BuilderCommit == id.SourceCommit || (strings.HasPrefix(id.BuilderCommit, id.SourceCommit+"+sha256:") && validHex(strings.TrimPrefix(id.BuilderCommit, id.SourceCommit+"+sha256:"), 32))
	compatible := false
	for _, allowed := range policy.CompatibleMethods {
		if allowed == id {
			compatible = true
		}
	}
	if !compatible || !builderSourceMatches || !validHex(id.SourceCommit, 20) || id.BuilderCommit == "" || strings.TrimSpace(id.AnnotationMethod) == "" || id.GatePolicy != policy.RequiredGatePolicy || !validHex(id.ProtocolSHA256, 32) || !validHex(id.RuntimeOverlaySHA256, 32) {
		return fail("incompatible or incomplete historical method identity")
	}
	var manifest archiveManifest
	if err := decode(input.Manifest, &manifest, false); err != nil {
		return fail("manifest JSON: %v", err)
	}
	if manifest.Format != "cairn-restricted-calibration-archive/v1" || manifest.SourceCommit != id.SourceCommit || len(manifest.Files) == 0 {
		return fail("manifest format/source identity mismatch")
	}
	files := map[string]archiveFile{}
	passCounts := map[string]int{}
	for _, f := range manifest.Files {
		if !validPath(f.Path) || f.Bytes < 0 || !validHex(f.SHA256, 32) || files[f.Path].Path != "" {
			return fail("invalid/duplicate manifest entry %q", f.Path)
		}
		files[f.Path] = f
		if strings.HasSuffix(f.Path, "_pass.json") {
			separator := strings.LastIndex(f.Path, "_r")
			if separator > 0 {
				roundText := strings.TrimSuffix(f.Path[separator+2:], "_pass.json")
				if round, err := strconv.Atoi(roundText); err == nil && round > 0 {
					passCounts[f.Path[:separator]]++
				}
			}
		}
	}
	prefix := input.Descriptor.Prefix
	if !validPath(prefix) {
		return fail("invalid archive prefix")
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		data, present := input.Files[name]
		if !ok || !present || len(data) == 0 || len(data) != f.Bytes || digest(data) != f.SHA256 {
			return nil, fmt.Errorf("missing/tampered required artifact %s", name)
		}
		return data, nil
	}
	required := func(rel string, out any, strict bool) error {
		data, e := read(prefix + "/" + rel)
		if e != nil {
			return e
		}
		if e = decode(data, out, strict); e != nil {
			return fmt.Errorf("%s: %w", rel, e)
		}
		return nil
	}
	// 实际归档的 profile/协议/选择记录是依赖；仅使用其相关字段，但完整字节仍由 manifest 校验。
	// Preserve actual archive metadata shapes; unknown unrelated metadata is not discarded evidence.
	var protocol struct {
		Format          string `json:"format"`
		CorpusSHA       string `json:"corpus_sha256"`
		CorpusDocuments int    `json:"corpus_documents"`
	}
	if err := required("protocol.json", &protocol, false); err != nil {
		return fail("%v", err)
	}
	if files[prefix+"/protocol.json"].SHA256 != id.ProtocolSHA256 || protocol.Format != "cairn-corpus-capacity/v1" || protocol.CorpusSHA != policy.CorpusSHA256 || protocol.CorpusDocuments != policy.ExpectedDocuments {
		return fail("protocol/corpus identity mismatch")
	}
	var selection struct {
		Format      string           `json:"format"`
		CorpusSHA   string           `json:"corpus_sha256"`
		ProtocolSHA string           `json:"protocol_sha256"`
		Documents   []selectedSource `json:"documents"`
	}
	if err := required("selection.json", &selection, false); err != nil {
		return fail("%v", err)
	}
	if selection.Format != "cairn-capacity-selection/v1" || selection.CorpusSHA != policy.CorpusSHA256 || selection.ProtocolSHA != id.ProtocolSHA256 || len(selection.Documents) == 0 {
		return fail("selection identity mismatch")
	}
	selected := map[string]selectedSource{}
	for _, s := range selection.Documents {
		d, ok := roster[s.DocID]
		if !ok || selected[s.DocID].DocID != "" || s.URL != d.URL || s.Path != d.Path || s.BodySHA256 != d.BodySHA256 || s.BodyBytes <= 0 || s.WrapperBytes <= s.BodyBytes || s.BodyStartByte != s.WrapperBytes-s.BodyBytes || !validHex(s.WrapperSHA256, 32) {
			return fail("invalid selection binding %s", s.DocID)
		}
		selected[s.DocID] = s
	}
	selectionSHA := files[prefix+"/selection.json"].SHA256
	var overlay struct {
		Format       string `json:"format"`
		ProtocolSHA  string `json:"base_protocol_sha256"`
		SelectionSHA string `json:"selection_sha256"`
	}
	if err := required("runtime-overlay.json", &overlay, false); err != nil {
		return fail("%v", err)
	}
	if files[prefix+"/runtime-overlay.json"].SHA256 != id.RuntimeOverlaySHA256 || overlay.Format != "cairn-capacity-runtime-overlay/v1" || overlay.ProtocolSHA != id.ProtocolSHA256 || overlay.SelectionSHA != selectionSHA {
		return fail("runtime overlay identity mismatch")
	}
	var run struct {
		Format        string `json:"format"`
		BuilderCommit string `json:"builder_commit"`
		ProtocolSHA   string `json:"protocol_sha256"`
		RuntimeSHA    string `json:"runtime_overlay_sha256"`
		SelectionSHA  string `json:"selection_sha256"`
		Attempts      int    `json:"annotation_attempts"`
		Rounds        int    `json:"annotation_rounds"`
	}
	if err := required("run-start.json", &run, false); err != nil {
		return fail("%v", err)
	}
	if run.Format != "cairn-capacity-run/v1" || run.BuilderCommit != id.BuilderCommit || run.ProtocolSHA != id.ProtocolSHA256 || run.RuntimeSHA != id.RuntimeOverlaySHA256 || run.SelectionSHA != selectionSHA || run.Attempts <= 0 || run.Rounds <= 0 {
		return fail("run/method identity mismatch")
	}
	var freeze struct {
		Format       string `json:"format"`
		RuntimeSHA   string `json:"runtime_overlay_sha256"`
		SelectionSHA string `json:"selection_sha256"`
		ProtocolSHA  string `json:"base_protocol_sha256"`
		Documents    []struct {
			DocID        string `json:"doc_id"`
			Path         string `json:"path"`
			BodyBytes    int    `json:"body_bytes"`
			WrapperBytes int    `json:"wrapper_bytes"`
			WrapperSHA   string `json:"wrapper_sha256"`
		} `json:"documents"`
	}
	if err := required("runtime-freeze.json", &freeze, false); err != nil {
		return fail("%v", err)
	}
	if freeze.Format != "cairn-capacity-runtime-freeze/v1" || freeze.RuntimeSHA != id.RuntimeOverlaySHA256 || freeze.SelectionSHA != selectionSHA || freeze.ProtocolSHA != id.ProtocolSHA256 || len(freeze.Documents) != len(selected) {
		return fail("runtime freeze identity mismatch")
	}
	seenFreeze := map[string]bool{}
	for _, f := range freeze.Documents {
		s, ok := selected[f.DocID]
		if !ok || seenFreeze[f.DocID] || f.Path != s.Path || f.BodyBytes != s.BodyBytes || f.WrapperBytes != s.WrapperBytes || f.WrapperSHA != s.WrapperSHA256 {
			return fail("runtime frozen document mismatch %s", f.DocID)
		}
		seenFreeze[f.DocID] = true
	}
	var sources []frozenSource
	var docs []dktypes.AnnotatedDocument
	if err := required("stages/00_frozen_sources.json", &sources, true); err != nil {
		return fail("%v", err)
	}
	if err := required("stages/04_extract_input.json", &docs, true); err != nil {
		return fail("%v", err)
	}
	// decode已经验证对象形状，另外保留原始字段存在性供02→04精确比较。
	// Typed decoding alone would equate missing fields with null or zero defaults.
	if len(docs) == 0 || len(docs) != len(sources) || len(docs) != len(input.Descriptor.Documents) {
		return fail("empty/incomplete accepted checkpoint bindings")
	}
	raw04, _ := exactJSON(input.Files[prefix+"/stages/04_extract_input.json"])
	rawDocuments := raw04.([]any)
	sourcesByPath := map[string]frozenSource{}
	for _, s := range sources {
		key := s.Skill + "\x00" + s.Path
		if sourcesByPath[key].Path != "" || s.Content == "" || digest([]byte(s.Content)) != s.SHA256 {
			return fail("invalid/duplicate frozen source %s", s.Path)
		}
		sourcesByPath[key] = s
	}
	descriptors := map[string]int{}
	for _, d := range input.Descriptor.Documents {
		if descriptors[d.DocID] != 0 || d.Round <= 0 || d.Round > run.Rounds {
			return fail("invalid/duplicate accepted round %s", d.DocID)
		}
		descriptors[d.DocID] = d.Round
	}
	result := &VerifiedCheckpoint{verified: true, identity: id, archiveCommit: policy.ArchiveCommit, manifestSHA: policy.ManifestSHA256, corpusSHA: policy.CorpusSHA256, rosterSHA: policy.RosterSHA256, roster: append([]FrozenDocument(nil), policy.Documents...)}
	seenDocs := map[string]bool{}
	for docIndex, doc := range docs {
		docID := strings.TrimSuffix(strings.TrimPrefix(doc.FilePath, "references/"), ".md")
		d, ok := roster[docID]
		s, chosen := selected[docID]
		round := descriptors[docID]
		if !ok || !chosen || seenDocs[docID] || round == 0 || doc.Skill != d.Skill || doc.FilePath != d.Path || len(doc.Items) == 0 {
			return fail("04 document binding mismatch %s", doc.FilePath)
		}
		seenDocs[docID] = true
		source, ok := sourcesByPath[d.Skill+"\x00"+d.Path]
		if !ok {
			return fail("missing frozen source for %s", docID)
		}
		wrapperPath := prefix + "/original/" + d.Path
		wrapper, err := read(wrapperPath)
		if err != nil {
			return fail("%v", err)
		}
		if digest(wrapper) != s.WrapperSHA256 || len(wrapper) != s.WrapperBytes || !bytes.Equal(wrapper, []byte(source.Content)) || source.SHA256 != s.WrapperSHA256 || digest(wrapper[s.BodyStartByte:]) != d.BodySHA256 {
			return fail("original/frozen/body binding mismatch %s", docID)
		}
		stem := strings.ReplaceAll(strings.ReplaceAll(d.Path, "/", "_"), "\\", "_")
		stem = strings.TrimSuffix(stem, ".md")
		skill := strings.ReplaceAll(strings.ReplaceAll(d.Skill, "/", "_"), "\\", "_")
		gateAPath := prefix + fmt.Sprintf("/stages/02_gate_a/%s/%s_r%d.json", skill, stem, round)
		gateBPath := prefix + fmt.Sprintf("/stages/03_gate_b/%s/%s_r%d_pass.json", skill, stem, round)
		if passCounts[prefix+fmt.Sprintf("/stages/03_gate_b/%s/%s", skill, stem)] != 1 {
			return fail("ambiguous/missing accepted pass artifact for %s", docID)
		}
		gateABytes, err := read(gateAPath)
		if err != nil {
			return fail("%v", err)
		}
		gateBBytes, err := read(gateBPath)
		if err != nil {
			return fail("%v", err)
		}
		var gateA dktypes.AnnotatedDocument
		var pass struct {
			Passed *bool `json:"passed"`
		}
		if err := decode(gateABytes, &gateA, true); err != nil {
			return fail("Gate A JSON: %v", err)
		}
		if err := decode(gateBBytes, &pass, true); err != nil || pass.Passed == nil || !*pass.Passed {
			return fail("Gate B not verifiably passed for %s round %d", docID, round)
		}
		rawGateA, _ := exactJSON(gateABytes)
		normalized := rawDocuments[docIndex].(map[string]any)
		normalizedItems, ok := normalized["items"].([]any)
		if !ok || len(normalizedItems) != len(doc.Items) {
			return fail("noncanonical 04 items for %s", docID)
		}
		members := map[string]bool{}
		for i, it := range doc.Items {
			if it.ID == "" || members[it.ID] || it.ID != extract.Build04ID(doc.Skill, it.Tag, it.Name) || !it.Tag.IsValid() || strings.TrimSpace(it.Name) == "" || strings.TrimSpace(it.Content) == "" || strings.TrimSpace(it.Detail) == "" || math.IsNaN(it.Confidence) || math.IsInf(it.Confidence, 0) || it.Confidence < 0 || it.Confidence > 1 {
				return fail("invalid/duplicate/frozen member ID %q", it.ID)
			}
			members[it.ID] = true
			rawItem, ok := normalizedItems[i].(map[string]any)
			if !ok || rawItem["id"] != it.ID {
				return fail("noncanonical 04 member ID for %s", docID)
			}
			delete(rawItem, "id")
		}
		// 只去除04的代码派生 ID；不排序、归一化、丢弃来源跨度或其他字段来制造一致。
		// Gate A must be exactly equal in all other fields and item order; no repair is performed.
		if !reflect.DeepEqual(rawGateA, normalized) {
			return fail("02 to 04 differs beyond derived item ID for %s", docID)
		}
		result.documents = append(result.documents, VerifiedDocument{d, round, s.WrapperSHA256, files[gateAPath].SHA256, files[gateBPath].SHA256, files[prefix+"/stages/04_extract_input.json"].SHA256, cloneDocument(doc)})
	}
	sort.Slice(result.documents, func(i, j int) bool { return result.documents[i].Source.DocID < result.documents[j].Source.DocID })
	sort.Slice(result.roster, func(i, j int) bool { return result.roster[i].DocID < result.roster[j].DocID })
	return result, nil
}
