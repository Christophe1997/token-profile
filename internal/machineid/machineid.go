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
)

// idBytes is the length, in bytes, of the random ID before hex-encoding —
// 16 bytes (32 hex characters) makes independently-generated collisions
// between machines negligible.
const idBytes = 16

// Load returns this machine's cached identity at path, generating and
// persisting a new random ID on first use. Identity is intentionally random
// rather than derived from hostname, since two machines can share a
// hostname (e.g. two laptops both named "MacBook-Pro") — KTD6.
func Load(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id == "" {
			return "", fmt.Errorf("machine id file %s is empty", path)
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("reading machine id %s: %w", path, err)
	}

	id, err := generate()
	if err != nil {
		return "", fmt.Errorf("generating machine id: %w", err)
	}
	if err := persist(path, id); err != nil {
		return "", err
	}
	return id, nil
}

func generate() (string, error) {
	buf := make([]byte, idBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func persist(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating machine id directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return fmt.Errorf("writing machine id %s: %w", path, err)
	}
	return nil
}
