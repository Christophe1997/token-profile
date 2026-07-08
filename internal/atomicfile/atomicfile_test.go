package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Christophe1997/token-profile/internal/atomicfile"
)

// TestWrite_CreatesFileWithExactContent covers the basic contract: Write
// leaves path containing exactly data, no more, no less.
func TestWrite_CreatesFileWithExactContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	if err := atomicfile.Write(dir, path, []byte("hello")); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v, want nil", err)
	}
	if string(got) != "hello" {
		t.Errorf("ReadFile() = %q, want %q", got, "hello")
	}
}

// TestWrite_OverwritesExistingFile covers the mutate-on-every-run case
// both internal/snapshot and internal/runhistory rely on: a second Write
// to the same path replaces the first write's content entirely.
func TestWrite_OverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	if err := atomicfile.Write(dir, path, []byte("first")); err != nil {
		t.Fatalf("Write() first error = %v, want nil", err)
	}
	if err := atomicfile.Write(dir, path, []byte("second")); err != nil {
		t.Fatalf("Write() second error = %v, want nil", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v, want nil", err)
	}
	if string(got) != "second" {
		t.Errorf("ReadFile() = %q, want %q", got, "second")
	}
}

// TestWrite_NoTempFileLeftBehind covers the atomicity contract: after a
// successful Write, dir contains only the final file — no leftover
// ".tmp-*" file from the intermediate step.
func TestWrite_NoTempFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	if err := atomicfile.Write(dir, path, []byte("hello")); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v, want nil", err)
	}
	if len(entries) != 1 || entries[0].Name() != "out.json" {
		t.Errorf("dir entries = %v, want exactly [out.json]", entries)
	}
}

// TestWrite_UncreatableDir_ReturnsError covers the error path: when dir
// doesn't exist (Write never creates it — that's the caller's job, mirrors
// os.CreateTemp's own contract), Write fails loudly.
func TestWrite_UncreatableDir_ReturnsError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	path := filepath.Join(dir, "out.json")

	if err := atomicfile.Write(dir, path, []byte("hello")); err == nil {
		t.Fatal("Write() error = nil, want non-nil")
	}
}
