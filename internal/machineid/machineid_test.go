package machineid_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestLoad_ConcurrentCalls_AgreeOnSameID covers the TOCTOU race (KTD6): many
// goroutines calling Load on the same not-yet-existing path concurrently
// must all agree on exactly one machine id — the winner's — rather than
// each generating and persisting its own, which would leave the machine
// with a different id depending on which process ran last.
func TestLoad_ConcurrentCalls_AgreeOnSameID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-id")

	const n = 32
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			ids[i], errs[i] = machineid.Load(path)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Load() [%d] error = %v, want nil", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Errorf("Load() [%d] = %q, want the same id as [0] = %q (concurrent callers must agree on one machine id)", i, ids[i], ids[0])
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if string(data) != ids[0] {
		t.Errorf("persisted id = %q, want %q", data, ids[0])
	}
}

// TestLoad_ExistingFileInvalidFormat_ReturnsError covers defense against a
// corrupted or hand-edited cache file: Load must reject content that isn't
// exactly 32 lowercase hex characters (generate's shape) rather than
// accepting it as-is, since an unvalidated id can carry path separators or
// ".." into snapshot.snapshotPath's filepath.Join (KTD6 path traversal).
func TestLoad_ExistingFileInvalidFormat_ReturnsError(t *testing.T) {
	tests := map[string]string{
		"path traversal":  "../../evil",
		"too short":       "abc123",
		"uppercase hex":   strings.Repeat("AB", 16),
		"contains slash":  "abc/" + strings.Repeat("0", 28),
		"contains dotdot": strings.Repeat("0", 15) + ".." + strings.Repeat("0", 15),
		"non-hex char":    strings.Repeat("0", 31) + "g",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "machine-id")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			_, err := machineid.Load(path)
			if err == nil {
				t.Fatalf("Load() error = nil, want an error for invalid cached content %q", content)
			}
		})
	}
}

// TestLoad_ExistingFileValidFormat_ReturnsIt is a regression check for the
// new format validation: a cache file that already matches generate's shape
// (32 lowercase hex characters) must still load exactly as before.
func TestLoad_ExistingFileValidFormat_ReturnsIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-id")
	want := strings.Repeat("ab", 16) // 32 lowercase hex chars, generate()'s shape
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := machineid.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if got != want {
		t.Errorf("Load() = %q, want %q", got, want)
	}
}
