package machineid_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Christophe1997/token-profile/internal/machineid"
)

// TestLoad_NoExistingFile_GeneratesAndPersists covers the first-run path: no
// cache file exists yet, so Load must generate a new random ID (KTD6) and
// persist it at path (creating parent directories as needed) before
// returning it.
func TestLoad_NoExistingFile_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "machine-id")

	id, err := machineid.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if id == "" {
		t.Fatal("Load() returned an empty id")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v, want Load to have persisted the id", path, err)
	}
	if string(data) != id {
		t.Errorf("persisted id = %q, want %q", data, id)
	}
}

// TestLoad_ExistingFile_ReturnsCachedID covers the repeat-run path: a second
// Load with the same path must return the exact same ID that was cached by
// the first call, not a freshly generated (and thus different) one.
func TestLoad_ExistingFile_ReturnsCachedID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-id")

	first, err := machineid.Load(path)
	if err != nil {
		t.Fatalf("first Load() error = %v, want nil", err)
	}

	second, err := machineid.Load(path)
	if err != nil {
		t.Fatalf("second Load() error = %v, want nil", err)
	}

	if second != first {
		t.Errorf("second Load() = %q, want cached id %q (not a freshly generated one)", second, first)
	}
}
