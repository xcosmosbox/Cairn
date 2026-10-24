package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// The executable, adapter and runtime receipt are pinned by the reviewed run.
// No download, installation or fallback tokenizer occurs on a model-call path.
type externalCounter struct{ Python, PythonSHA, Adapter, AdapterSHA, Receipt, ReceiptSHA string }

func (c externalCounter) Count(ctx context.Context, raw []byte) (tokenCount, error) {
	for _, p := range []struct{ path, hash string }{{c.Python, c.PythonSHA}, {c.Adapter, c.AdapterSHA}, {c.Receipt, c.ReceiptSHA}} {
		data, err := os.ReadFile(p.path)
		if err != nil || p.hash == "" || sha(data) != p.hash {
			return tokenCount{}, fmt.Errorf("token runtime identity mismatch")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, c.Python, "-I", c.Adapter, "--runtime-receipt", c.Receipt, "--runtime-receipt-sha256", c.ReceiptSHA)
	command.Stdin = bytes.NewReader(raw)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	// Never propagate subprocess diagnostics that could include request contents.
	if err := command.Run(); err != nil {
		return tokenCount{}, fmt.Errorf("official token counter unavailable")
	}
	var count tokenCount
	if err := decodeStrict(stdout.Bytes(), &count); err != nil {
		return count, fmt.Errorf("invalid token receipt")
	}
	if count.RuntimeSHA != c.ReceiptSHA || count.CounterID != "deepseek-recipe@935817c66f5d6e64838f419692545f06787e485f/v41" {
		return count, fmt.Errorf("token receipt identity mismatch")
	}
	return count, nil
}
