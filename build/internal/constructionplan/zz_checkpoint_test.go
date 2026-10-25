package constructionplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type checkpointFixture struct {
	input  ArchiveInput
	policy TrustPolicy
}

func fixtureJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func fixture(t *testing.T, accepted, total, units int, shared bool) checkpointFixture {
	t.Helper()
	const prefix = "raw/attempt-01"
	source := strings.Repeat("1", 40)
	f := checkpointFixture{input: ArchiveInput{Files: map[string][]byte{}, Descriptor: ArchiveDescriptor{Prefix: prefix}}, policy: TrustPolicy{ArchiveCommit: strings.Repeat("2", 40), CorpusSHA256: digest([]byte("frozen corpus")), ExpectedDocuments: total, RequiredGatePolicy: "semantic-idempotency/v1"}}
	put := func(rel string, v any) { f.input.Files[prefix+"/"+rel] = fixtureJSON(t, v) }
	put("protocol.json", map[string]any{"format": "cairn-corpus-capacity/v1", "corpus_sha256": f.policy.CorpusSHA256, "corpus_documents": total})
	var selected []selectedSource
	var frozen []map[string]any
	var sources []frozenSource
	var docs []dktypes.AnnotatedDocument
	for i := 0; i < total; i++ {
		url := fmt.Sprintf("https://example.invalid/document/%04d", i)
		docID := digest([]byte(url))
		body := []byte(fmt.Sprintf("Original body %d with its source condition.", i))
		wrapper := append([]byte("# Original source\n\n"), body...)
		d := FrozenDocument{docID, url, "fixture-skill", "references/" + docID + ".md", digest(body)}
		f.policy.Documents = append(f.policy.Documents, d)
		selected = append(selected, selectedSource{docID, url, d.Path, d.BodySHA256, len(body), digest(wrapper), len(wrapper), len(wrapper) - len(body)})
		frozen = append(frozen, map[string]any{"doc_id": docID, "path": d.Path, "body_bytes": len(body), "wrapper_bytes": len(wrapper), "wrapper_sha256": digest(wrapper)})
		if i >= accepted {
			continue
		}
		f.input.Files[prefix+"/original/"+d.Path] = wrapper
		sources = append(sources, frozenSource{d.Skill, d.Path, digest(wrapper), string(wrapper)})
		doc := dktypes.AnnotatedDocument{Skill: d.Skill, FilePath: d.Path}
		for j := 0; j < units; j++ {
			name := fmt.Sprintf("unit %03d source %03d", j, i)
			if shared {
				name = fmt.Sprintf("shared unit %03d", j)
			}
			doc.Items = append(doc.Items, dktypes.AnnotatedItem{Tag: dktypes.TagConcept, Name: name, Content: fmt.Sprintf("Summary %d", i), Detail: fmt.Sprintf("Complete detail from document %d item %d", i, j), Confidence: .9, SourceSpan: &dktypes.SourceSpan{StartLine: 3, EndLine: 3, Quote: fmt.Sprintf("source-%d", i)}})
		}
		round := i%3 + 1
		stem := "references_" + docID
		put(fmt.Sprintf("stages/02_gate_a/%s/%s_r%d.json", d.Skill, stem, round), doc)
		put(fmt.Sprintf("stages/03_gate_b/%s/%s_r%d_pass.json", d.Skill, stem, round), map[string]bool{"passed": true})
		docs = append(docs, *extract.AssignIDs([]*dktypes.AnnotatedDocument{&doc})[0])
		f.input.Descriptor.Documents = append(f.input.Descriptor.Documents, AcceptedDocumentDescriptor{docID, round})
	}
	protocolSHA := digest(f.input.Files[prefix+"/protocol.json"])
	put("selection.json", map[string]any{"format": "cairn-capacity-selection/v1", "corpus_sha256": f.policy.CorpusSHA256, "protocol_sha256": protocolSHA, "documents": selected})
	selectionSHA := digest(f.input.Files[prefix+"/selection.json"])
	put("runtime-overlay.json", map[string]any{"format": "cairn-capacity-runtime-overlay/v1", "base_protocol_sha256": protocolSHA, "selection_sha256": selectionSHA})
	runtimeSHA := digest(f.input.Files[prefix+"/runtime-overlay.json"])
	put("run-start.json", map[string]any{"format": "cairn-capacity-run/v1", "builder_commit": source, "protocol_sha256": protocolSHA, "runtime_overlay_sha256": runtimeSHA, "selection_sha256": selectionSHA, "annotation_attempts": 3, "annotation_rounds": 3})
	put("runtime-freeze.json", map[string]any{"format": "cairn-capacity-runtime-freeze/v1", "base_protocol_sha256": protocolSHA, "runtime_overlay_sha256": runtimeSHA, "selection_sha256": selectionSHA, "documents": frozen})
	put("stages/00_frozen_sources.json", sources)
	put("stages/04_extract_input.json", docs)
	f.input.Descriptor.Identity = MethodIdentity{source, source, "legacy-original-only/v1", f.policy.RequiredGatePolicy, protocolSHA, runtimeSHA}
	f.policy.CompatibleMethods = []MethodIdentity{f.input.Descriptor.Identity}
	f.policy.RosterSHA256 = RosterDigest(f.policy.Documents)
	f.repin(t)
	return f
}
func (f *checkpointFixture) repin(t *testing.T) {
	t.Helper()
	manifest := archiveManifest{Format: "cairn-restricted-calibration-archive/v1", SourceCommit: f.input.Descriptor.Identity.SourceCommit}
	for p, b := range f.input.Files {
		manifest.Files = append(manifest.Files, archiveFile{p, len(b), digest(b)})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	// Actual historical manifests also contain irrelevant empty logs/WAL entries.
	manifest.Files = append(manifest.Files, archiveFile{"raw/unused.log", 0, digest(nil)})
	f.input.Manifest = fixtureJSON(t, manifest)
	f.policy.ManifestSHA256 = digest(f.input.Manifest)
}
func fixturePath(f checkpointFixture, contains string) string {
	for p := range f.input.Files {
		if strings.Contains(p, contains) {
			return p
		}
	}
	return ""
}

// 原失败风险：仅信任04自称的ID或一条passed标记，会把改写正文或错误来源当作有效检查点。
func TestCheckpointRequiredHashesAndBindingsFailClosed(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"original", "02_gate_a", "03_gate_b", "04_extract_input", "00_frozen_sources", "protocol.json", "selection.json", "runtime-overlay.json", "runtime-freeze.json", "run-start.json"} {
		for _, operation := range []string{"missing", "tamper"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				t.Parallel()
				f := fixture(t, 1, 1, 1, false)
				p := fixturePath(f, kind)
				if p == "" {
					t.Fatal("fixture path missing")
				}
				if operation == "missing" {
					delete(f.input.Files, p)
				} else {
					f.input.Files[p] = append(f.input.Files[p], ' ')
				}
				if _, e := ImportCheckpoint(f.input, f.policy); e == nil {
					t.Fatal("accepted missing/tampered dependency")
				}
			})
		}
	}
	for _, change := range []string{"manifest", "empty-pin", "wrong-roster-hash", "wrong-docid", "wrong-round", "wrong-method", "wrong-gate-policy", "null-pass", "false-pass", "missing-pass", "error-pass", "changed-detail", "changed-span", "changed-id", "changed-owner", "duplicate-doc", "duplicate-member", "unknown-field", "empty04", "ambiguous-pass"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f := fixture(t, 1, 1, 1, false)
			repin := false
			switch change {
			case "manifest":
				f.input.Manifest = append(f.input.Manifest, ' ')
			case "empty-pin":
				f.policy.ManifestSHA256 = ""
			case "wrong-roster-hash":
				f.policy.Documents[0].BodySHA256 = digest([]byte("wrong"))
			case "wrong-docid":
				f.policy.Documents[0].DocID = digest([]byte("wrong"))
			case "wrong-round":
				f.input.Descriptor.Documents[0].Round = 2
			case "wrong-method":
				f.input.Descriptor.Identity.AnnotationMethod = "bounded-v1-generated"
			case "wrong-gate-policy":
				f.policy.RequiredGatePolicy = "different gate"
			case "null-pass", "false-pass", "missing-pass", "error-pass":
				p := fixturePath(f, "03_gate_b")
				wire := map[string]string{"null-pass": `{"passed":null}`, "false-pass": `{"passed":false}`, "missing-pass": `{}`, "error-pass": `{"passed":true,"reason":"provider error"}`}[change]
				f.input.Files[p] = []byte(wire)
				repin = true
			case "ambiguous-pass":
				p := fixturePath(f, "03_gate_b")
				f.input.Files[strings.Replace(p, "_r1_pass", "_r2_pass", 1)] = f.input.Files[p]
				repin = true
			default:
				p := fixturePath(f, "04_extract_input")
				var docs []map[string]any
				if e := json.Unmarshal(f.input.Files[p], &docs); e != nil {
					t.Fatal(e)
				}
				items := docs[0]["items"].([]any)
				item := items[0].(map[string]any)
				switch change {
				case "changed-detail":
					item["detail"] = "changed despite freshly recomputed hashes"
				case "changed-span":
					item["source_span"].(map[string]any)["quote"] = "different source quote"
				case "changed-id":
					item["id"] = "invented-ID"
				case "changed-owner":
					docs[0]["skill"] = "other-skill"
				case "duplicate-doc":
					docs = append(docs, docs[0])
				case "duplicate-member":
					docs[0]["items"] = append(items, item)
				case "unknown-field":
					item["extra_source_field"] = "must not silently disappear"
				case "empty04":
					docs = nil
				}
				f.input.Files[p] = fixtureJSON(t, docs)
				repin = true
			}
			// Rehashing counterfeit metadata cannot bypass cross-artifact semantic/identity checks.
			if repin {
				f.repin(t)
			}
			if _, e := ImportCheckpoint(f.input, f.policy); e == nil {
				t.Fatalf("accepted %s", change)
			}
		})
	}
}

func TestCheckpointPreservesSharedMembersAndCopies(t *testing.T) {
	t.Parallel()
	f := fixture(t, 2, 2, 1, true)
	before := fixtureJSON(t, f.input)
	c, e := ImportCheckpoint(f.input, f.policy)
	if e != nil {
		t.Fatal(e)
	}
	docs := c.Documents()
	if len(docs) != 2 || docs[0].Annotation.Items[0].ID != docs[1].Annotation.Items[0].ID || docs[0].Annotation.Items[0].Detail == docs[1].Annotation.Items[0].Detail || c.Identity() != f.input.Descriptor.Identity {
		t.Fatal("shared member/source identity lost")
	}
	docs[0].Annotation.Items[0].Detail = "mutated copy"
	docs[0].Annotation.Items[0].SourceSpan.Quote = "mutated copy"
	if reflect.DeepEqual(docs, c.Documents()) || string(before) != string(fixtureJSON(t, f.input)) {
		t.Fatal("mutation/side effect through import or accessor")
	}
}

// 原风险：结构体比较会把缺省字段、null或重复键压成同一个值，超出只删派生ID的许可。
// Rehashed ambiguity or presence changes must fail instead of silently normalizing annotations.
func TestCheckpointExactJSONPreservesFieldsAndRejectsAmbiguity(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"absent-span", "zero-default", "numeric-spelling", "duplicate-pass", "case-variant-pass", "duplicate-detail", "wrong-items-case", "same-wrong-case"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f := fixture(t, 1, 1, 1, false)
			gateAPath := fixturePath(f, "02_gate_a")
			inputPath := fixturePath(f, "04_extract_input")
			switch change {
			case "absent-span", "zero-default":
				var gateA map[string]any
				var docs []map[string]any
				_ = json.Unmarshal(f.input.Files[gateAPath], &gateA)
				_ = json.Unmarshal(f.input.Files[inputPath], &docs)
				a := gateA["items"].([]any)[0].(map[string]any)
				b := docs[0]["items"].([]any)[0].(map[string]any)
				if change == "absent-span" {
					delete(a, "source_span")
					b["source_span"] = nil
				} else {
					delete(a, "confidence")
					b["confidence"] = 0
				}
				f.input.Files[gateAPath] = fixtureJSON(t, gateA)
				f.input.Files[inputPath] = fixtureJSON(t, docs)
			case "numeric-spelling":
				f.input.Files[gateAPath] = []byte(strings.Replace(string(f.input.Files[gateAPath]), `"confidence":0.9`, `"confidence":0.90`, 1))
			case "duplicate-pass":
				f.input.Files[fixturePath(f, "03_gate_b")] = []byte(`{"passed":false,"passed":true}`)
			case "case-variant-pass":
				f.input.Files[fixturePath(f, "03_gate_b")] = []byte(`{"passed":false,"Passed":true}`)
			case "duplicate-detail":
				f.input.Files[gateAPath] = []byte(strings.Replace(string(f.input.Files[gateAPath]), `"detail":`, `"detail":"discarded","detail":`, 1))
			case "wrong-items-case":
				f.input.Files[inputPath] = []byte(strings.Replace(string(f.input.Files[inputPath]), `"items":`, `"Items":`, 1))
			case "same-wrong-case":
				for _, p := range []string{gateAPath, inputPath} {
					f.input.Files[p] = []byte(strings.Replace(string(f.input.Files[p]), `"detail":`, `"Detail":`, 1))
				}
			}
			f.repin(t)
			if _, err := ImportCheckpoint(f.input, f.policy); err == nil {
				t.Fatalf("accepted normalization beyond derived ID: %s", change)
			}
		})
	}
	f := fixture(t, 1, 1, 1, false)
	p := fixturePath(f, "02_gate_a")
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, f.input.Files[p], "", "  "); err != nil {
		t.Fatal(err)
	}
	f.input.Files[p] = formatted.Bytes()
	f.repin(t)
	if _, err := ImportCheckpoint(f.input, f.policy); err != nil {
		t.Fatalf("rejected harmless JSON formatting: %v", err)
	}
}

// 可选本地兼容性检查只读真实归档和原始语料；CI不需要私人文件，也不会构造假的passed产物。
// This verifies required legacy dependencies only; external provenance trust remains caller responsibility.
func TestLegacyArchiveCompatibilityFromSuppliedFiles(t *testing.T) {
	manifestPath, root, corpusPath, pin := os.Getenv("CAIRN_PLAN_MANIFEST"), os.Getenv("CAIRN_PLAN_ARCHIVE_ROOT"), os.Getenv("CAIRN_PLAN_CORPUS"), os.Getenv("CAIRN_PLAN_MANIFEST_SHA256")
	if manifestPath == "" && root == "" && corpusPath == "" && pin == "" {
		t.Skip("optional independently supplied legacy archive")
	}
	if manifestPath == "" || root == "" || corpusPath == "" || pin == "" {
		t.Fatal("all four external fixture inputs required")
	}
	read := func(p string) []byte {
		t.Helper()
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	manifestBytes := read(manifestPath)
	var manifest archiveManifest
	if e := decode(manifestBytes, &manifest, false); e != nil {
		t.Fatal(e)
	}
	corpus := read(corpusPath)
	if digest(corpus) != "20b61b5ab84de84a927420c5d265b7ec8d859ae49980699958a787ade9e4d28f" {
		t.Fatal("frozen original-only corpus mismatch")
	}
	var rows []struct {
		URL  string `json:"url"`
		Body string `json:"body"`
	}
	if e := json.Unmarshal(corpus, &rows); e != nil || len(rows) != 609 {
		t.Fatal("corpus roster mismatch")
	}
	input := ArchiveInput{Manifest: manifestBytes, Files: map[string][]byte{}, Descriptor: ArchiveDescriptor{Prefix: "raw/attempt-01"}}
	policy := TrustPolicy{ManifestSHA256: pin, ArchiveCommit: "1a44fa27db4221778da3986efa5f4414c6f27611", CorpusSHA256: digest(corpus), ExpectedDocuments: 609, RequiredGatePolicy: "semantic-idempotency/v1"}
	for _, row := range rows {
		id := digest([]byte(row.URL))
		policy.Documents = append(policy.Documents, FrozenDocument{id, row.URL, "multihop-public", "references/" + id + ".md", digest([]byte(row.Body))})
	}
	policy.RosterSHA256 = RosterDigest(policy.Documents)
	add := func(p string) { input.Files[p] = read(filepath.Join(root, filepath.FromSlash(p))) }
	for _, rel := range []string{"protocol.json", "selection.json", "runtime-overlay.json", "runtime-freeze.json", "run-start.json", "stages/00_frozen_sources.json", "stages/04_extract_input.json"} {
		add(input.Descriptor.Prefix + "/" + rel)
	}
	var run struct {
		BuilderCommit string `json:"builder_commit"`
	}
	if e := json.Unmarshal(input.Files["raw/attempt-01/run-start.json"], &run); e != nil {
		t.Fatal(e)
	}
	identity := MethodIdentity{manifest.SourceCommit, run.BuilderCommit, "legacy-original-only/v1", policy.RequiredGatePolicy, digest(input.Files["raw/attempt-01/protocol.json"]), digest(input.Files["raw/attempt-01/runtime-overlay.json"])}
	input.Descriptor.Identity = identity
	policy.CompatibleMethods = []MethodIdentity{identity}
	var docs []dktypes.AnnotatedDocument
	if e := json.Unmarshal(input.Files["raw/attempt-01/stages/04_extract_input.json"], &docs); e != nil {
		t.Fatal(e)
	}
	for _, doc := range docs {
		id := strings.TrimSuffix(strings.TrimPrefix(doc.FilePath, "references/"), ".md")
		add("raw/attempt-01/original/" + doc.FilePath)
		round := 0
		for n := 1; n <= 3; n++ {
			pass := fmt.Sprintf("raw/attempt-01/stages/03_gate_b/%s/references_%s_r%d_pass.json", doc.Skill, id, n)
			for _, f := range manifest.Files {
				if f.Path == pass {
					if round != 0 {
						t.Fatal("ambiguous pass")
					}
					round = n
					add(pass)
				}
			}
		}
		input.Descriptor.Documents = append(input.Descriptor.Documents, AcceptedDocumentDescriptor{id, round})
		add(fmt.Sprintf("raw/attempt-01/stages/02_gate_a/%s/references_%s_r%d.json", doc.Skill, id, round))
	}
	c, e := ImportCheckpoint(input, policy)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := BuildPlan(c)
	if e != nil {
		t.Fatal(e)
	}
	if plan.AcceptedDocuments != 11 || plan.UniqueUnits != 298 || plan.SourceIncidences != 298 || len(plan.MissingDocuments) != 598 || plan.Admissible || plan.Status != NotAdmissible || plan.WireFit != Unknown || plan.OutputFit != Unknown {
		t.Fatalf("legacy counters/status mismatch: docs=%d units=%d incidences=%d missing=%d", plan.AcceptedDocuments, plan.UniqueUnits, plan.SourceIncidences, len(plan.MissingDocuments))
	}
	t.Logf("manifest=%s roster=%s historical_source=%s method=%s", policy.ManifestSHA256, policy.RosterSHA256, c.Identity().SourceCommit, c.Identity().AnnotationMethod)
	t.Logf("legacy dependencies verified: docs=%d units=%d incidences=%d batches=%d missing=%d/%d status=%s wire=%s output=%s", plan.AcceptedDocuments, plan.UniqueUnits, plan.SourceIncidences, len(plan.Batches), len(plan.MissingDocuments), plan.ExpectedDocuments, plan.Status, plan.WireFit, plan.OutputFit)
}
