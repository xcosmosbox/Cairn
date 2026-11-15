package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/incremental"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
)

// A published valid subset is not permission to consume an incomplete source.
// Retain its exact raw input outside managed blocks for the ordinary C4 path;
// leave the sidecar and editable baseline untouched until that work completes.
func retainIncompleteFullSources(ctx context.Context, root string, originals []extract.FrozenSource, relations *extract.RelationProcessReport) ([]string, error) {
	if relations == nil {
		return nil, nil
	}
	byKey := map[string]extract.FrozenSource{}
	for _, source := range originals {
		byKey[source.Skill+"\x00"+source.Path] = source
	}
	seen := map[string]bool{}
	var retained []string
	var failures []error
	for _, scope := range relations.Scopes {
		if scope.Outcome == annotation.DocumentComplete {
			continue
		}
		for _, binding := range scope.Sources {
			key := binding.Skill + "\x00" + binding.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			source, ok := byKey[key]
			if !ok || source.SHA256 != binding.SHA256 || extract.FreezeSource(source.Skill, source.Path, source.Content).SHA256 != source.SHA256 {
				failures = append(failures, fmt.Errorf("pending original identity unavailable: %s", binding.Path))
				continue
			}
			if err := retainFullSource(ctx, root, source); err != nil {
				failures = append(failures, fmt.Errorf("pending original %s: %w", binding.Path, err))
				continue
			}
			retained = append(retained, binding.Path)
		}
	}
	return retained, errors.Join(failures...)
}

func retainFullSource(ctx context.Context, root string, source extract.FrozenSource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := writeback.ValidateRepositoryPath(root, source.Path)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(current) == source.Content || source.Content == "" {
		return nil
	}
	sidecar, err := writeback.ReadSidecar(path)
	if err != nil {
		return err
	}
	diff := incremental.DiffDoc(source.Path, sidecar, string(current), false)
	if len(diff.C3) != 0 {
		return fmt.Errorf("managed baseline is not valid")
	}
	// Text inside a KG description is not retryable raw prose. Reuse the
	// actual normal parser rather than mistaking a substring in a block for C4.
	if strings.Contains(diff.C4Text, source.Content) {
		return nil
	}
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cairn-pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	data := append(append(append([]byte(nil), current...), '\n', '\n'), []byte(source.Content)...)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = store.CheckLease(ctx); err != nil {
		return err
	}
	if _, err = writeback.ValidateRepositoryPath(root, source.Path); err != nil {
		return err
	}
	latest, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(latest, current) {
		return fmt.Errorf("source changed during pending preservation")
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
