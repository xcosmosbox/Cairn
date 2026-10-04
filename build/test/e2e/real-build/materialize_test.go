package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockedMaterializationInputRejectsMutationAndSymlinks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "stage.json")
	original := []byte(`{"real":"checkpoint"}`)
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockedFile(root, "stage.json", sha256Hex(original)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mutated":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockedFile(root, "stage.json", sha256Hex(original)); err == nil {
		t.Fatal("mutated checkpoint accepted")
	}
	if err := os.Symlink(path, filepath.Join(root, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := lockedFile(root, "linked.json", sha256Hex([]byte(`{"mutated":true}`))); err == nil {
		t.Fatal("symlink checkpoint accepted")
	}
	if _, err := lockedFile(root, "../stage.json", sha256Hex(original)); err == nil {
		t.Fatal("escaping checkpoint accepted")
	}
}
