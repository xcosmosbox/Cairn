#!/usr/bin/env python3
"""Create a source-hash-checked, observation-only Go test overlay."""
import hashlib
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
source = ROOT / "core/evolve/record.go"
text = source.read_text()
original_hash = hashlib.sha256(source.read_bytes()).hexdigest()
assert original_hash == "7c72c980a133ff68631186cdfe7e4cb1eb8a8ae10f411be3e0e320f90752f201", original_hash
def once(old, new):
    global text
    assert text.count(old) == 1, (old, text.count(old))
    text = text.replace(old, new)

once('\t"strings"\n', '\t"strings"\n\t"sync/atomic"\n')
once('func Record(ctx context.Context, kgDBPath, evolutionDir, tool string) (summary string, err error) {', '''func Record(ctx context.Context, kgDBPath, evolutionDir, tool string) (summary string, err error) {
    diagnosticStart := time.Now()
    diagnostic := map[string]any{"start_unix_ns": diagnosticStart.UnixNano(), "tool": tool, "manifest_calls_at_start": diagnosticManifestCalls.Load()}
    defer func() {
        diagnostic["end_unix_ns"] = time.Now().UnixNano()
        diagnostic["elapsed_ns"] = time.Since(diagnosticStart).Nanoseconds()
        diagnostic["error"] = fmt.Sprint(err)
        diagnostic["manifest_calls_at_end"] = diagnosticManifestCalls.Load()
        data, _ := json.Marshal(diagnostic)
        fmt.Printf("LOCK_DIAGNOSTIC %s\\n", data)
    }()''')
once('\ttx, err := evDB.BeginTx(ctx, nil)', '''    diagnosticBegin := time.Now()
    diagnostic["begin_request_unix_ns"] = diagnosticBegin.UnixNano()
    diagnostic["active_manifests_at_begin"] = diagnosticManifestActive.Load()
    tx, err := evDB.BeginTx(ctx, nil)
    diagnosticAcquired := time.Now()
    diagnostic["begin_return_unix_ns"] = diagnosticAcquired.UnixNano()
    diagnostic["begin_wait_ns"] = diagnosticAcquired.Sub(diagnosticBegin).Nanoseconds()
    diagnostic["begin_error"] = fmt.Sprint(err)''')
once('\tdefer tx.Rollback()\n\tcommitted := false', '''    defer func() {
        rollbackError := tx.Rollback()
        diagnostic["rollback_return_unix_ns"] = time.Now().UnixNano()
        diagnostic["rollback_error"] = fmt.Sprint(rollbackError)
    }()
    committed := false''')
for variable, call in [('snapshot', 'snapFile, err := archiveSnapshot(ctx, kgDBPath, snapDir, seq)'), ('diff', 'diff, err := observe.Diff(ctx, from, kg)'), ('metrics', 'snap, cumulative, err := sampleMetrics(ctx, kg)')]:
    once('\t' + call, f'    diagnostic["{variable}_enter_unix_ns"] = time.Now().UnixNano()\n\t{call}\n    diagnostic["{variable}_return_unix_ns"] = time.Now().UnixNano()')
once('\tif err := tx.Commit(); err != nil {', '''    diagnostic["commit_request_unix_ns"] = time.Now().UnixNano()
    diagnosticCommitError := tx.Commit()
    diagnostic["commit_return_unix_ns"] = time.Now().UnixNano()
    diagnostic["hold_upper_ns"] = time.Since(diagnosticAcquired).Nanoseconds()
    if err := diagnosticCommitError; err != nil {''')
once('func ReadManifest(ctx context.Context, evolutionDir string) (*Manifest, error) {', '''func ReadManifest(ctx context.Context, evolutionDir string) (*Manifest, error) {
    diagnosticManifestCalls.Add(1)
    diagnosticManifestActive.Add(1)
    defer diagnosticManifestActive.Add(-1)''')
text += '\nvar diagnosticManifestCalls atomic.Int64\nvar diagnosticManifestActive atomic.Int64\n'
instrumented = HERE / "instrumented_record.go.txt"
instrumented.write_text(text)
overlay = {"Replace": {str(source): str(instrumented)}}
(HERE / "overlay.json").write_text(json.dumps(overlay, indent=2) + "\n")
print(json.dumps({"source_sha256": original_hash, "overlay_sha256": hashlib.sha256(instrumented.read_bytes()).hexdigest(), "source": str(source)}))
