// Package machineid generates and caches a random identity for the current
// machine, used to namespace this machine's snapshot within a shared target
// repo (KTD6).
package machineid

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// idBytes is the length, in bytes, of the random ID before hex-encoding —
// 16 bytes (32 hex characters) makes independently-generated collisions
// between machines negligible.
const idBytes = 16

// retryAttempts/retryDelay bound how long persist waits for a concurrent
// winner's write to become visible after losing the create race (KTD6) —
// a narrow local-disk race window, not a distributed system, so a short
// bounded retry is enough rather than anything fancier.
const (
	retryAttempts = 20
	retryDelay    = 5 * time.Millisecond
)

// Load returns this machine's cached identity at path, generating and
// persisting a new random ID on first use. Identity is intentionally random
// rather than derived from hostname, since two machines can share a
// hostname (e.g. two laptops both named "MacBook-Pro") — KTD6.
func Load(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return validateCached(path, data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("reading machine id %s: %w", path, err)
	}

	id, err := generate()
	if err != nil {
		return "", fmt.Errorf("generating machine id: %w", err)
	}
	return persist(path, id)
}

// validateCached rejects anything read from an existing machine-id file
// that doesn't match generate's exact shape (32 lowercase hex characters).
// A corrupted or hand-edited file could otherwise carry path separators or
// ".." into snapshot.snapshotPath's filepath.Join — see snapshot's own
// defense-in-depth guard for the case where a malformed id reaches that
// package some other way.
func validateCached(path string, data []byte) (string, error) {
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("machine id file %s is empty", path)
	}
	if !isValidID(id) {
		return "", fmt.Errorf("machine id file %s contains an invalid id (expected 32 lowercase hex characters): corrupted or tampered — delete it to regenerate", path)
	}
	return id, nil
}

// isValidID reports whether id matches generate's output shape: exactly
// idBytes*2 lowercase hex characters.
func isValidID(id string) bool {
	if len(id) != idBytes*2 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func generate() (string, error) {
	buf := make([]byte, idBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// persist atomically writes id to path as this machine's identity and
// returns the id now on disk. It is first-write-wins: two processes can
// both reach here after independently seeing path missing (Load's TOCTOU
// window, KTD6), but O_EXCL lets only one of them actually create the file.
// The loser gets os.ErrExist and returns the winner's id — read back from
// disk — instead of proceeding with its own (now-orphaned) generated id,
// which would otherwise leave two different machine ids in play for one
// machine.
func persist(path, id string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating machine id directory for %s: %w", path, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return readWinnerID(path)
		}
		return "", fmt.Errorf("writing machine id %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.WriteString(id); err != nil {
		return "", fmt.Errorf("writing machine id %s: %w", path, err)
	}
	return id, nil
}

// readWinnerID re-reads path after persist lost the create race, retrying
// briefly since the winning process's write may still be in flight (e.g. a
// concurrent read can observe a momentarily empty file).
func readWinnerID(path string) (string, error) {
	for range retryAttempts {
		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("reading machine id %s: %w", path, err)
			}
		} else if id := strings.TrimSpace(string(data)); isValidID(id) {
			return id, nil
		}
		time.Sleep(retryDelay)
	}
	return "", fmt.Errorf("reading machine id %s: winner's write did not become visible after %d retries", path, retryAttempts)
}
