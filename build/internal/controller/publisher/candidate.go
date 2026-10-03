package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/core/kbbundle"
)

type candidateReceipt struct {
	RequestDigest string `json:"request_digest"`
	CreatedAt     string `json:"created_at"`
	BundleDigest  string `json:"bundle_digest"`
}

// freeze 输入后才能打包：重试复用时间和校验通过的文件；同一候选目录的输入漂移
// 必须创建新 run，不能把旧 applied outbox / Release 当作新内容的成功证明。
// prepareCandidate freezes candidate inputs and reuses their immutable artifact on replay.
func prepareCandidate(ctx context.Context, spec CandidateSpec, m kbbundle.Manifest) (*kbbundle.Bundle, error) {
	if err := store.CheckLease(ctx); err != nil {
		return nil, err
	}
	if spec.BundleDir == "" {
		return nil, fmt.Errorf("publisher: BundleDir is empty")
	}
	parent := filepath.Dir(spec.BundleDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	temp, err := os.MkdirTemp(parent, ".candidate-prep-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	kgPath := filepath.Join(temp, "kg.db")
	if err := kbbundle.SnapshotSQLite(ctx, spec.KGDBPath, kgPath); err != nil {
		return nil, err
	}
	dbDigest, err := kbbundle.DigestFile(kgPath)
	if err != nil {
		return nil, err
	}
	evPath, evDigest := "", ""
	if spec.EvolutionDBPath != "" {
		evPath = filepath.Join(temp, "evolution.db")
		if err := kbbundle.SnapshotSQLite(ctx, spec.EvolutionDBPath, evPath); err != nil {
			return nil, err
		}
		evDigest, err = kbbundle.DigestFile(evPath)
		if err != nil {
			return nil, err
		}
	}
	frozen := m
	frozen.CreatedAt = ""
	request, _ := json.Marshal(struct {
		Manifest              kbbundle.Manifest
		DB, Report, Evolution string
		Fingerprint           Fingerprint
	}{
		frozen, dbDigest, kbbundle.DigestBytes(spec.BuildReport), evDigest, spec.Fingerprint,
	})
	digest := kbbundle.DigestPrefix(kbbundle.DigestBytes(request))
	receiptPath := spec.BundleDir + ".candidate.json"
	receipt := candidateReceipt{RequestDigest: digest}
	data, err := os.ReadFile(receiptPath)
	if err == nil {
		if err := json.Unmarshal(data, &receipt); err != nil {
			return nil, fmt.Errorf("publisher: corrupt candidate receipt: %w", err)
		}
		if receipt.RequestDigest != digest {
			return nil, fmt.Errorf("publisher: immutable candidate inputs changed; create a new run")
		}
		if _, err := time.Parse(time.RFC3339, receipt.CreatedAt); err != nil {
			return nil, fmt.Errorf("publisher: corrupt candidate timestamp: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		created := spec.CreatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		receipt.CreatedAt = created.UTC().Format(time.RFC3339)
		data, _ := json.Marshal(receipt)
		if err := writeCheckpoint(ctx, receiptPath, data); err != nil {
			return nil, err
		}
	}
	m.CreatedAt = receipt.CreatedAt
	if existing, err := kbbundle.Verify(spec.BundleDir); err == nil {
		actualDB, err := kbbundle.DigestFile(filepath.Join(spec.BundleDir, "knowledge.db"))
		if err != nil {
			return nil, err
		}
		if actualDB != dbDigest {
			return nil, fmt.Errorf("publisher: existing artifact DB differs from frozen input")
		}
		if receipt.BundleDigest != "" && receipt.BundleDigest != existing.BundleDigest {
			return nil, fmt.Errorf("publisher: existing artifact digest differs from frozen receipt")
		}
		actualReport := ""
		if spec.BuildReport != nil {
			data, err := os.ReadFile(filepath.Join(spec.BundleDir, "build-report.json"))
			if err != nil {
				return nil, err
			}
			actualReport = kbbundle.DigestBytes(data)
			if actualReport != kbbundle.DigestBytes(spec.BuildReport) {
				return nil, fmt.Errorf("publisher: artifact report differs from frozen input")
			}
		}
		if evPath != "" {
			actualEv, err := kbbundle.DigestFile(filepath.Join(spec.BundleDir, "evolution", "evolution.db"))
			if err != nil {
				return nil, err
			}
			if actualEv != evDigest {
				return nil, fmt.Errorf("publisher: artifact evolution differs from frozen input")
			}
		}
		if receipt.BundleDigest == "" {
			receipt.BundleDigest = existing.BundleDigest
			data, _ := json.Marshal(receipt)
			if err := writeCheckpoint(ctx, receiptPath, data); err != nil {
				return nil, err
			}
		}
		compare := *existing
		compare.BundleDigest = ""
		compare.Format = ""
		if compare != m {
			return nil, fmt.Errorf("publisher: existing artifact metadata differs from frozen candidate")
		}
		return &kbbundle.Bundle{Manifest: *existing, Dir: spec.BundleDir}, nil
	}
	stage := filepath.Join(temp, "bundle")
	bundle, err := kbbundle.Pack(kbbundle.PackRequest{Manifest: m, KGDBPath: kgPath, BuildReport: spec.BuildReport, EvolutionDBPath: evPath, OutDir: stage, PreparedSnapshots: true})
	if err != nil {
		return nil, fmt.Errorf("publisher: pack Bundle: %w", err)
	}
	if _, err := kbbundle.Verify(stage); err != nil {
		return nil, err
	}
	if err := store.CheckLease(ctx); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(spec.BundleDir); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, spec.BundleDir); err != nil {
		return nil, err
	}
	receipt.BundleDigest = bundle.Manifest.BundleDigest
	data, _ = json.Marshal(receipt)
	if err := writeCheckpoint(ctx, receiptPath, data); err != nil {
		return nil, err
	}
	bundle.Dir = spec.BundleDir
	return bundle, nil
}

func writeCheckpoint(ctx context.Context, path string, data []byte) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".publication-checkpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (p *Publisher) proposalBase(ctx context.Context, spec CatalogProposalSpec, branch string) (string, error) {
	if err := store.CheckLease(ctx); err != nil {
		return "", err
	}
	path := filepath.Join(p.ws.RunDir(spec.RunID), "catalog-proposal.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	type proposalCheckpoint struct{ Branch, Base, Digest string }
	var checkpoint proposalCheckpoint
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &checkpoint); err != nil {
			return "", err
		}
		if checkpoint.Branch != branch || checkpoint.Digest != spec.Bundle.Manifest.BundleDigest || checkpoint.Base == "" {
			return "", fmt.Errorf("publisher: immutable catalog proposal inputs changed")
		}
		return checkpoint.Base, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	base, err := githubapp.ReadBranchSHA(ctx, p.forge, p.catalogRef.Owner, p.catalogRef.Name, spec.Catalog.Branch)
	if err != nil {
		return "", err
	}
	checkpoint = proposalCheckpoint{branch, base, spec.Bundle.Manifest.BundleDigest}
	data, _ := json.Marshal(checkpoint)
	if err := writeCheckpoint(ctx, path, data); err != nil {
		return "", err
	}
	return base, nil
}
