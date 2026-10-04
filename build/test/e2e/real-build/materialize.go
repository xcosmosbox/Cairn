package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	buildmeta "github.com/xcosmosbox/cairn/build/internal"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/evolve"
	"github.com/xcosmosbox/cairn/core/storage"
)

var materializationInputs = []string{
	"builder.json", "effective-config.json", "frozen-builder-fingerprint.json",
	"accepted-checkpoint-proof.json", "checkpoint-replay-summary.json",
	"build-report.json", "usage-summary.json", "llm-usage.jsonl",
	"stages/00_scan.json", "stages/04_extract_input.json", "stages/05_extract_result.json",
	"stages/05b_extract_repaired.json", "stages/05c1_coverage_report.json", "stages/05c_repair_report.json",
	"stages/05d_extract_described.json", "stages/05d_describe_report.json",
	"stages/05e_extract_related.json", "stages/05e_relate_report.json",
	"stages/05f_uuid_assigned.json", "stages/05g_file_slugs.json",
}

type materializationManifest struct {
	Format string            `json:"format"`
	Files  map[string]string `json:"files"`
}

func lockedFile(root, relative, expected string) ([]byte, error) {
	if len(expected) != 64 || strings.Trim(expected, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("checkpoint: invalid SHA-256 pin for %s", relative)
	}
	raw, err := readConfinedSource(root, relative)
	if err != nil {
		return nil, err
	}
	if sha256Hex(raw) != expected {
		return nil, fmt.Errorf("checkpoint: SHA-256 mismatch for %s", relative)
	}
	return raw, nil
}

func originalFingerprint(files map[string][]byte) (publisher.Fingerprint, error) {
	var fp publisher.Fingerprint
	if err := json.Unmarshal(files["frozen-builder-fingerprint.json"], &fp); err != nil {
		return fp, err
	}
	var record struct {
		Version     string `json:"builder_version"`
		Commit      string `json:"builder_commit"`
		Prompt      string `json:"prompt_set_version"`
		Config      string `json:"config_digest"`
		Fingerprint string `json:"fingerprint_digest"`
		Model       string `json:"model"`
		Provider    string `json:"provider"`
		Proof       string `json:"accepted_checkpoint_proof_sha256"`
		Paid        bool   `json:"paid_execution_requested"`
	}
	if err := json.Unmarshal(files["builder.json"], &record); err != nil {
		return fp, err
	}
	var cfg struct {
		Model            string  `json:"model"`
		Provider         string  `json:"provider"`
		Source           string  `json:"input_git_snapshot"`
		Proof            string  `json:"accepted_checkpoint_proof_sha256"`
		Confidence       float64 `json:"min_confidence"`
		ExtractionReused bool    `json:"extraction_reused"`
		DownstreamReused bool    `json:"downstream_reused"`
	}
	if err := json.Unmarshal(files["effective-config.json"], &cfg); err != nil {
		return fp, err
	}
	if record.Version != fp.BuilderVersion || record.Commit != fp.BuilderCommit || record.Prompt != fp.PromptSetVersion || record.Config != fp.ConfigDigest || record.Fingerprint != fp.Digest() || record.Model != fp.Model || record.Provider != fp.Provider || !record.Paid {
		return fp, errors.New("checkpoint: original builder record does not match frozen LLM fingerprint")
	}
	if fp.ConfigDigest != "sha256:"+sha256Hex(files["effective-config.json"]) || record.Proof != sha256Hex(files["accepted-checkpoint-proof.json"]) || cfg.Proof != record.Proof {
		return fp, errors.New("checkpoint: original config/proof hash does not match original builder")
	}
	if cfg.Source != acceptedSourceCommit || cfg.Model != fp.Model || cfg.Provider != fp.Provider || cfg.Confidence != 0.7 || cfg.ExtractionReused || cfg.DownstreamReused || fp.Model != dkconfig.DeepSeekFlashModel || fp.Provider != "openai_compatible" {
		return fp, errors.New("checkpoint: original source/model/provider/stage configuration is not the accepted real run")
	}
	if fp.DBSchemaVersion != storage.LatestSchemaVersion() || fp.AnnotationSchemaVer != 1 || fp.IdentityAlgorithmVer != buildmeta.SourceDigest("extract/uuid.go") {
		return fp, errors.New("checkpoint: materializer schema or UUID algorithm is incompatible with original LLM checkpoint")
	}
	return fp, nil
}

func runMaterialize(ctx context.Context, repo, out, acceptedDump, historicalCode, codeRoot, checkpointRoot, manifestPath, manifestSHA string) error {
	if manifestPath == "" || manifestSHA == "" {
		return errors.New("materialization requires an explicit manifest and SHA-256 pin")
	}
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if sha256Hex(manifestRaw) != manifestSHA {
		return errors.New("checkpoint manifest SHA-256 mismatch")
	}
	var manifest materializationManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return err
	}
	if manifest.Format != "cairn-real-llm-checkpoint/v1" || len(manifest.Files) != len(materializationInputs) {
		return errors.New("checkpoint manifest format or exact file set is invalid")
	}
	checkpointRoot, err = filepath.Abs(checkpointRoot)
	if err != nil {
		return err
	}
	files := make(map[string][]byte, len(materializationInputs))
	for _, name := range materializationInputs {
		raw, err := lockedFile(checkpointRoot, name, manifest.Files[name])
		if err != nil {
			return err
		}
		files[name] = raw
	}
	original, err := originalFingerprint(files)
	if err != nil {
		return err
	}
	// Re-run the original parsed-annotation/Gate-A proof and verify source bytes
	// against the original Git commit. No client that can access a provider exists.
	verified, err := newAcceptedCheckpointClient(ctx, repo, acceptedDump, historicalCode, codeRoot, denyNetwork{})
	if err != nil {
		return err
	}
	status, err := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil || len(bytes.TrimSpace(status)) != 0 {
		return errors.New("materialization requires a clean original source worktree")
	}
	var proof struct {
		Source  string            `json:"source_commit"`
		Sources []checkpointEntry `json:"accepted_sources"`
		Hashes  map[string]string `json:"checkpoint_files_sha256"`
	}
	if err := json.Unmarshal(files["accepted-checkpoint-proof.json"], &proof); err != nil {
		return err
	}
	if proof.Source != acceptedSourceCommit || len(proof.Sources) != acceptedReferenceCount {
		return errors.New("checkpoint: original annotation proof source/count mismatch")
	}
	for _, entry := range proof.Sources {
		raw, err := readConfinedSource(repo, entry.Path)
		if err != nil {
			return err
		}
		if sha256Hex(raw) != entry.SourceSHA256 || gitBlob(raw) != entry.SourceGitBlob {
			return fmt.Errorf("checkpoint: source bytes changed: %s", entry.Path)
		}
	}
	for name, expected := range proof.Hashes {
		if _, err := lockedFile(acceptedDump, name, expected); err != nil {
			return err
		}
	}
	var replays struct {
		Annotations int  `json:"annotation_replays"`
		Gates       int  `json:"gate_b_recorded_pass_replays"`
		Fresh       bool `json:"new_llm_judgement_for_replayed_gate_b"`
	}
	if err := json.Unmarshal(files["checkpoint-replay-summary.json"], &replays); err != nil {
		return err
	}
	if replays.Annotations != acceptedReferenceCount || replays.Gates != acceptedReferenceCount || replays.Fresh {
		return errors.New("checkpoint: accepted annotation replay counts/evidence mismatch")
	}
	var docs, historicalDocs []*dktypes.AnnotatedDocument
	if err := json.Unmarshal(files["stages/04_extract_input.json"], &docs); err != nil {
		return err
	}
	if _, err := readCheckpointJSON(filepath.Join(acceptedDump, "04_extract_input.json"), &historicalDocs); err != nil {
		return err
	}
	if err := sameDocuments(docs, historicalDocs); err != nil {
		return err
	}
	var res extract.Result
	if err := json.Unmarshal(files["stages/05g_file_slugs.json"], &res); err != nil {
		return err
	}
	// These derived fields are intentionally json:"-" in the production types.
	for di := range res.Domains {
		d := &res.Domains[di]
		d.Slug = extract.Slugify(d.Name)
		d.Provenance = "llm_inferred"
		for si := range d.Subdomains {
			d.Subdomains[si].Slug = extract.Slugify(d.Subdomains[si].Name)
		}
	}
	var scan []struct {
		Name       string
		DomainHint string
	}
	if err := json.Unmarshal(files["stages/00_scan.json"], &scan); err != nil {
		return err
	}
	metas := make([]ingest.SkillMeta, 0, len(scan))
	for _, skill := range scan {
		metas = append(metas, ingest.SkillMeta{Name: skill.Name, Summary: skill.DomainHint})
	}
	var originalReport pipeline.RunReport
	if err := json.Unmarshal(files["build-report.json"], &originalReport); err != nil {
		return err
	}
	if originalReport.DocsTotal != acceptedReferenceCount || originalReport.DocsRewritten != acceptedReferenceCount || len(originalReport.Skipped) != 0 || originalReport.Describe == nil || len(originalReport.Describe.Failures) != 0 || originalReport.Relate == nil || len(originalReport.Relate.Failures) != 0 {
		return errors.New("checkpoint: original real LLM stage report is incomplete")
	}
	if err := os.WriteFile(filepath.Join(out, "materialization-input-manifest.json"), manifestRaw, 0644); err != nil {
		return err
	}
	if err := save(filepath.Join(out, "revalidated-annotation-proof.json"), verified.proof); err != nil {
		return err
	}
	// Preserve exact immutable original evidence; never rewrite it to the current SHA.
	for name, raw := range files {
		path := filepath.Join(out, "original-llm", name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, raw, 0644); err != nil {
			return err
		}
	}
	driverDigest, err := driverSourceDigest(filepath.Join(codeRoot, "build/test/e2e/real-build"))
	if err != nil {
		return err
	}
	effective := map[string]any{
		"mode": "deterministic-materialization", "no_new_llm_calls": true, "min_confidence": 0.7,
		"repository_identity": "git:github.com/xcosmosbox/kd_manifeat", "input_git_snapshot": acceptedSourceCommit,
		"checkpoint_manifest_sha256": manifestSHA, "completed_llm_checkpoint_sha256": manifest.Files["stages/05g_file_slugs.json"],
		"original_llm_fingerprint_digest": original.Digest(), "original_llm_builder_record_sha256": manifest.Files["builder.json"],
		"original_llm_effective_config_sha256": manifest.Files["effective-config.json"], "runner_source_sha256": driverDigest,
		"reused_stages": []string{"annotation", "Gate A", "Gate B", "extract", "coverage", "repair", "description", "relation", "UUID", "file_slug"},
		"fresh_stages":  []string{"candidate selection", "full provenance validation", "ingest", "writeback", "evolution", "candidate validation"},
	}
	if err := save(filepath.Join(out, "effective-config.json"), effective); err != nil {
		return err
	}
	configRaw, err := os.ReadFile(filepath.Join(out, "effective-config.json"))
	if err != nil {
		return err
	}
	version, commit := buildmeta.BuilderVersion()
	fp := publisher.Fingerprint{BuilderVersion: version, BuilderCommit: commit, ControllerSchemaVer: store.SchemaVersion(), DBSchemaVersion: storage.LatestSchemaVersion(), PromptSetVersion: buildmeta.SourceDigest("annotation/", "extract/", "incremental/"), IdentityAlgorithmVer: buildmeta.SourceDigest("extract/uuid.go"), DiscoveryRulesDigest: buildmeta.SourceDigest("discovery/"), Model: original.Model, Provider: original.Provider, AnnotationSchemaVer: 1, ConfigDigest: "sha256:" + sha256Hex(configRaw)}
	if err := save(filepath.Join(out, "frozen-builder-fingerprint.json"), fp); err != nil {
		return err
	}
	if err := save(filepath.Join(out, "builder.json"), map[string]any{
		"mode": "deterministic-materialization", "builder_version": version, "builder_commit": commit,
		"model": original.Model, "provider": original.Provider, "prompt_set_version": fp.PromptSetVersion,
		"config_digest": fp.ConfigDigest, "fingerprint_digest": fp.Digest(), "no_new_llm_calls": true,
		"original_llm_builder": original, "original_llm_builder_record_sha256": manifest.Files["builder.json"],
		"checkpoint_manifest_sha256": manifestSHA, "started_at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	orch, err := pipeline.NewOrchestrator(pipeline.Options{RepositoryIdentity: "git:github.com/xcosmosbox/kd_manifeat", Client: denyNetwork{}, MinConfidence: 0.7, DumpDir: filepath.Join(out, "stages")})
	if err != nil {
		return err
	}
	db := filepath.Join(out, "knowledge.db")
	report, materializeErr := orch.MaterializeCompletedExtraction(ctx, repo, db, &res, docs, metas)
	if report != nil {
		originalReport.Selection, originalReport.Ingest, originalReport.Writeback = report.Selection, report.Ingest, report.Writeback
	}
	// The Release contains build-report.json. Carry the mixed-stage provenance
	// inside that artifact instead of relying solely on adjacent local receipts.
	reportJSON, err := json.Marshal(&originalReport)
	if err != nil {
		return err
	}
	var reportWithProvenance map[string]any
	if err := json.Unmarshal(reportJSON, &reportWithProvenance); err != nil {
		return err
	}
	var originalUsage any
	if err := json.Unmarshal(files["usage-summary.json"], &originalUsage); err != nil {
		return err
	}
	reportWithProvenance["e2e_provenance"] = map[string]any{
		"mode": "deterministic-materialization", "no_new_llm_calls": true,
		"original_llm_fingerprint": original, "materializer_fingerprint": fp,
		"original_llm_builder_record_sha256": manifest.Files["builder.json"],
		"checkpoint_manifest_sha256":         manifestSHA, "completed_llm_checkpoint_sha256": manifest.Files["stages/05g_file_slugs.json"],
		"source_commit": acceptedSourceCommit, "accepted_documents": len(docs),
		"original_llm_usage": originalUsage, "original_usage_ledger_sha256": manifest.Files["llm-usage.jsonl"],
		"reused_stages": effective["reused_stages"], "fresh_stages": effective["fresh_stages"],
	}
	if err := save(filepath.Join(out, "build-report.json"), reportWithProvenance); err != nil {
		return err
	}
	if err := save(filepath.Join(out, "usage-summary.json"), map[string]any{"fresh_http_attempts": 0, "fresh_input_tokens": 0, "fresh_output_tokens": 0, "original_usage_summary_sha256": manifest.Files["usage-summary.json"], "original_usage_ledger_sha256": manifest.Files["llm-usage.jsonl"]}); err != nil {
		return err
	}
	if materializeErr != nil {
		return materializeErr
	}
	if _, err := evolve.Record(ctx, db, filepath.Join(out, "evolution"), "nicedata-deterministic-materialization"); err != nil {
		return err
	}
	rr := runner.NewRunnerWithClient(denyNetwork{}, dkconfig.LLMConfig{Model: original.Model})
	validation, err := rr.Validate(ctx, runner.ValidateRequest{RepoPath: repo, DBPath: db})
	if saveErr := save(filepath.Join(out, "validation.json"), validation); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return err
	}
	if !validation.OK {
		return fmt.Errorf("materialized candidate validation failed: %v", validation.Errors)
	}
	if err := save(filepath.Join(out, "materialization-proof.json"), map[string]any{"ok": true, "no_new_llm_calls": true, "checkpoint_manifest_sha256": manifestSHA, "original_llm_fingerprint": original, "materialized_fingerprint": fp, "source_commit": acceptedSourceCommit, "accepted_documents": len(docs), "node_count": validation.NodeCount, "edge_count": validation.EdgeCount, "selection": report.Selection}); err != nil {
		return err
	}
	log.Printf("deterministic materialization PASS: docs=%d nodes=%d edges=%d; new LLM calls=0", len(docs), validation.NodeCount, validation.EdgeCount)
	return nil
}

func sameDocuments(current, historical []*dktypes.AnnotatedDocument) error {
	if len(current) != acceptedReferenceCount || len(historical) != acceptedReferenceCount {
		return errors.New("checkpoint: exact 69-document set is required")
	}
	byPath := make(map[string]*dktypes.AnnotatedDocument, len(historical))
	for _, d := range historical {
		if d == nil || byPath[d.FilePath] != nil {
			return errors.New("checkpoint: duplicate or nil historical document")
		}
		byPath[d.FilePath] = d
	}
	for _, d := range current {
		if d == nil || !reflect.DeepEqual(d, byPath[d.FilePath]) {
			return errors.New("checkpoint: full parsed annotations differ from accepted historical source")
		}
		delete(byPath, d.FilePath)
	}
	if len(byPath) != 0 {
		return errors.New("checkpoint: historical documents omitted")
	}
	return nil
}
