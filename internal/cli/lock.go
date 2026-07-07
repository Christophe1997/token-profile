package cli

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// runLockPath returns the exclusive run-lock's path: a sibling of repoDir's
// .token-profile directory, not nested inside it (KTD15). `cleanup` deletes
// .token-profile/ while still holding this lock for the remainder of its
// run; nesting the lock file inside would delete the lock out from under
// itself before release() runs, reopening the concurrent-race window the
// lock exists to close.
func runLockPath(repoDir string) string {
	return filepath.Join(repoDir, ".token-profile.lock")
}

// acquireRunLock is a narrow, local safety net — not a distributed lock —
// against two overlapping `token-profile run`/`init`/`cleanup` invocations
// racing on the same target repo's files and git state. It creates an
// exclusive lock file at repoDir's .token-profile.lock containing this
// process's PID, and returns a release func (call via defer) that removes
// it.
//
// If a lock file already exists, acquireRunLock checks whether the PID it
// names is still alive (POSIX: syscall.Kill(pid, 0) — nil or EPERM means
// alive, ESRCH means gone). A dead PID means the lock is stale (e.g. left
// behind by a crashed prior run): it's removed and acquisition is retried
// once. A live PID fails fast with a clear, actionable error rather than
// blocking indefinitely.
func acquireRunLock(repoDir string) (release func(), err error) {
	path := runLockPath(repoDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating lock directory for %s: %w", path, err)
	}

	for range 2 {
		if err := writeLockFile(path); err == nil {
			return func() { os.Remove(path) }, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("creating lock %s: %w", path, err)
		}

		heldPID, err := readLockPID(path)
		if err != nil {
			return nil, fmt.Errorf("reading existing lock %s: %w", path, err)
		}
		if processAlive(heldPID) {
			return nil, fmt.Errorf("another token-profile run is already in progress (pid %d)", heldPID)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("removing stale lock %s (held by dead pid %d): %w", path, heldPID, err)
		}
	}

	return nil, fmt.Errorf("acquiring lock %s: repeatedly contended by a live process", path)
}

// writeLockFile atomically creates path (failing with os.ErrExist if it's
// already there) and writes this process's PID into it.
func writeLockFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(f, "%d", os.Getpid())
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(path)
		return cmp.Or(writeErr, closeErr)
	}
	return nil
}

// readLockPID reads and parses the PID recorded in an existing lock file.
func readLockPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid in lock file %s: %w", path, err)
	}
	return pid, nil
}

// processAlive reports whether pid identifies a still-running process, via
// POSIX's kill(pid, 0) idiom: no error or EPERM (exists, but owned by
// someone else) means alive; ESRCH means the process is gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
