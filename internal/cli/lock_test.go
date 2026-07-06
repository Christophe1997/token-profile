package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestAcquireRunLock_SecondAttemptWhileHeld_FailsWithClearError covers the
// common case: a lock already held by a live process (in this
// same-process test, that's simply this test process itself, whose PID
// acquireRunLock happily finds "alive" without any mocking needed) must
// make a second acquisition attempt on the same path fail fast, naming the
// holding PID, rather than blocking.
func TestAcquireRunLock_SecondAttemptWhileHeld_FailsWithClearError(t *testing.T) {
	dir := t.TempDir()

	release, err := acquireRunLock(dir)
	if err != nil {
		t.Fatalf("acquireRunLock() first attempt error = %v, want nil", err)
	}
	defer release()

	_, err = acquireRunLock(dir)
	if err == nil {
		t.Fatal("acquireRunLock() second attempt error = nil, want an error while the first lock is still held")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("acquireRunLock() error = %q, want it to explain another run is already in progress", err.Error())
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Errorf("acquireRunLock() error = %q, want it to name the holding pid %d", err.Error(), os.Getpid())
	}
}

// TestAcquireRunLock_StaleLock_DetectedAndCleaned covers the recovery case:
// a lock file left behind by a process that no longer exists (e.g. a
// crashed prior run) must not block a fresh acquisition — it's detected as
// stale, removed, and acquisition succeeds.
func TestAcquireRunLock_StaleLock_DetectedAndCleaned(t *testing.T) {
	dir := t.TempDir()
	lockDir := filepath.Join(dir, ".token-profile")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", lockDir, err)
	}
	// A PID this large is certain to belong to no live process on any
	// realistic system (verified against real darwin syscall.Kill: it
	// reports ESRCH, "no such process", for this value).
	const deadPID = 2147483647
	stalePath := filepath.Join(lockDir, "run.lock")
	if err := os.WriteFile(stalePath, []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatalf("WriteFile(stale lock) error = %v", err)
	}

	release, err := acquireRunLock(dir)
	if err != nil {
		t.Fatalf("acquireRunLock() error = %v, want nil (stale lock should be detected and cleaned up)", err)
	}
	defer release()

	got, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatalf("ReadFile(lock) error = %v", err)
	}
	if strings.TrimSpace(string(got)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file contents = %q, want this process's own pid %d", got, os.Getpid())
	}
}

// TestAcquireRunLock_Release_RemovesLockFile covers the cleanup half: the
// release func returned by a successful acquisition must remove the lock
// file, so a subsequent acquisition on the same path succeeds cleanly.
func TestAcquireRunLock_Release_RemovesLockFile(t *testing.T) {
	dir := t.TempDir()

	release, err := acquireRunLock(dir)
	if err != nil {
		t.Fatalf("acquireRunLock() error = %v, want nil", err)
	}
	release()

	lockPath := filepath.Join(dir, ".token-profile", "run.lock")
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(lock) error = %v, want os.ErrNotExist after release", err)
	}

	// A second acquisition after release must succeed (proves release
	// actually freed the lock rather than merely no-op'ing).
	release2, err := acquireRunLock(dir)
	if err != nil {
		t.Fatalf("acquireRunLock() after release error = %v, want nil", err)
	}
	release2()
}
