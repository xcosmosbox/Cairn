package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/pipeline"
)

// Bind the compiled instrumentation itself as well as the production SourceDigest.
//
//go:embed main.go prepare.go counter.go trace.go audit.go runtime.go cross_audit.go
var driverSources embed.FS

func driverSourceDigest() string {
	entries, _ := driverSources.ReadDir(".")
	var content bytes.Buffer
	for _, entry := range entries {
		raw, err := driverSources.ReadFile(entry.Name())
		if err != nil {
			panic(err)
		}
		content.WriteString(entry.Name() + "\n")
		content.Write(raw)
		content.WriteByte('\n')
	}
	return "sha256:" + sha(content.Bytes())
}

// runtimeOverlay 单独冻结新语义容量与覆盖规则，旧协议/选样和旧证据不得被替换。
// runtimeOverlay adds a hash-pinned experiment profile without rewriting historical inputs.
type runtimeOverlay struct {
	Format             string  `json:"format"`
	Status             string  `json:"status"`
	BaseProtocolSHA    string  `json:"base_protocol_sha256"`
	SelectionSHA       string  `json:"selection_sha256"`
	CrossEvidenceBytes int     `json:"cross_relation_evidence_bytes"`
	MinimumIncidence   float64 `json:"minimum_actual_incidence_coverage"`
	Documents          int     `json:"selected_documents"`
	MaxTokens          int     `json:"max_tokens"`
	RequestSeconds     int     `json:"request_timeout_seconds"`
	StageSeconds       int     `json:"stage_timeout_seconds"`
	RunSeconds         int     `json:"run_timeout_seconds"`
	Concurrency        int     `json:"concurrency"`
	WholeRunRetries    int     `json:"whole_run_retries"`
	Admission          string  `json:"admission"`
}

type runtimeDocument struct {
	Document     string `json:"doc_id"`
	Path         string `json:"path"`
	BodyBytes    int    `json:"body_bytes"`
	WrapperBytes int    `json:"wrapper_bytes"`
	WrapperSHA   string `json:"wrapper_sha256"`
}

type runtimeFreeze struct {
	Format             string            `json:"format"`
	OverlaySHA         string            `json:"runtime_overlay_sha256"`
	SelectionSHA       string            `json:"selection_sha256"`
	ProtocolSHA        string            `json:"base_protocol_sha256"`
	CrossEvidenceBytes int               `json:"cross_relation_evidence_bytes"`
	MinimumIncidence   float64           `json:"minimum_actual_incidence_coverage"`
	BodyBytes          int               `json:"body_bytes_sum"`
	OriginalBytes      int               `json:"original_wrapper_bytes_sum"`
	SourceJSONBytes    int               `json:"source_only_json_bytes"`
	Documents          []runtimeDocument `json:"documents"`
	EnvelopeMeaning    string            `json:"envelope_meaning"`
	TokenFit           string            `json:"provider_token_fit"`
}

func loadRuntimeOverlay(path, pin, selectionPin string, selected *selection) (*runtimeOverlay, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil || pin == "" || sha(raw) != pin {
		return nil, nil, fmt.Errorf("reviewed runtime overlay pin required")
	}
	var cfg runtimeOverlay
	if err := decodeStrict(raw, &cfg); err != nil {
		return nil, nil, fmt.Errorf("runtime overlay schema: %w", err)
	}
	if cfg.Format != "cairn-capacity-runtime-overlay/v1" || cfg.BaseProtocolSHA != selected.ProtocolSHA || cfg.SelectionSHA != selectionPin || cfg.Documents != 12 || cfg.CrossEvidenceBytes < 1 || cfg.MinimumIncidence < 0.95 || cfg.MinimumIncidence > 1 || cfg.MaxTokens < 1 || cfg.RequestSeconds < 1 || cfg.StageSeconds < 1 || cfg.RunSeconds < 1 || cfg.Concurrency != 8 || cfg.WholeRunRetries != 0 {
		return nil, nil, fmt.Errorf("runtime overlay identity or bounded profile mismatch")
	}
	return &cfg, raw, nil
}

// freezeRuntime 验证实际原文件而非只信 roster 大小；这是字节预检，不能代替每次 wire token 检查。
// freezeRuntime verifies all actual sources and records a lower bound, not a token-fit guarantee.
func freezeRuntime(out, overlayPin, selectionPin string, selected *selection, cfg *runtimeOverlay) (*runtimeFreeze, error) {
	r := &runtimeFreeze{Format: "cairn-capacity-runtime-freeze/v1", OverlaySHA: overlayPin, SelectionSHA: selectionPin, ProtocolSHA: selected.ProtocolSHA, CrossEvidenceBytes: cfg.CrossEvidenceBytes, MinimumIncidence: cfg.MinimumIncidence, EnvelopeMeaning: "source-only JSON lower bound; generated endpoint/member/relation framing remains bounded by the actual verifier and wire guard", TokenFit: "not established by source bytes; official exact rendered-request token guard remains mandatory"}
	var sourceEnvelope []map[string]any
	for _, d := range selected.Documents {
		raw, err := os.ReadFile(filepath.Join(out, "original", d.Path))
		if err != nil || sha(raw) != d.WrapperSHA || len(raw) != d.WrapperBytes || d.BodyStartByte < 0 || d.BodyStartByte > len(raw) || len(raw[d.BodyStartByte:]) != d.BodyBytes || sha(raw[d.BodyStartByte:]) != d.BodySHA {
			return nil, fmt.Errorf("runtime original source identity mismatch")
		}
		r.BodyBytes += d.BodyBytes
		r.OriginalBytes += len(raw)
		r.Documents = append(r.Documents, runtimeDocument{d.ID, d.Path, d.BodyBytes, len(raw), sha(raw)})
		sourceEnvelope = append(sourceEnvelope, map[string]any{"skill": "multihop-public", "path": d.Path, "sha256": sha(raw), "content": string(raw)})
	}
	encoded, err := json.Marshal(sourceEnvelope)
	if err != nil {
		return nil, err
	}
	r.SourceJSONBytes = len(encoded)
	if r.OriginalBytes > cfg.CrossEvidenceBytes || r.SourceJSONBytes > cfg.CrossEvidenceBytes {
		return r, fmt.Errorf("complete original union exceeds cross-evidence byte budget before paid execution")
	}
	return r, nil
}

func (c *runtimeOverlay) checkLimits(max int, request, stage, run time.Duration) error {
	if max != c.MaxTokens || request != time.Duration(c.RequestSeconds)*time.Second || stage != time.Duration(c.StageSeconds)*time.Second || run != time.Duration(c.RunSeconds)*time.Second {
		return fmt.Errorf("runtime flags differ from pinned overlay")
	}
	return nil
}

func capacityPipelineOptions(c *runtimeOverlay, client llm.Client, dump string) pipeline.Options {
	return pipeline.Options{RepositoryIdentity: "cairn-multihop-public-originals-v1", Client: client, MaxTokens: c.MaxTokens, MaxRetries: 3, MaxRollbacks: 3, MinConfidence: 0.7, DumpDir: dump, StageTimeout: time.Duration(c.StageSeconds) * time.Second, CrossRelationEvidenceBytes: c.CrossEvidenceBytes}
}
