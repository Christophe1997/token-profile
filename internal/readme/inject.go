// Package readme injects rendered content into a README file between a pair
// of marker comments, leaving everything else in the file byte-for-byte
// untouched.
package readme

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

const (
	startMarker = "<!-- token-profile:start -->"
	endMarker   = "<!-- token-profile:end -->"
)

// ErrMarkersMissing indicates the README lacks one or both token-profile
// markers. Inject deliberately refuses to guess an insertion point in this
// case (KTD7) — callers should scaffold the markers via `token-profile init`.
var ErrMarkersMissing = errors.New("token-profile markers not found in README")

// Inject replaces everything strictly between the token-profile start/end
// marker lines in readme with content, returning the updated file. Both
// marker lines, and everything outside them, are left unchanged. If either
// marker is missing (or they appear out of order), Inject returns an error
// wrapping ErrMarkersMissing that points the caller at `token-profile init`
// rather than guessing where to insert the markers.
func Inject(readmeBytes []byte, content string) ([]byte, error) {
	startIdx := bytes.Index(readmeBytes, []byte(startMarker))
	endIdx := bytes.Index(readmeBytes, []byte(endMarker))
	if startIdx == -1 || endIdx == -1 || endIdx < startIdx {
		return nil, fmt.Errorf(
			"%w: run `token-profile init` to scaffold %q and %q into your README",
			ErrMarkersMissing, startMarker, endMarker,
		)
	}

	// betweenStart is right after the newline that terminates the start
	// marker's line (or end-of-file if the marker has no trailing newline).
	betweenStart := len(readmeBytes)
	if nl := bytes.IndexByte(readmeBytes[startIdx+len(startMarker):], '\n'); nl != -1 {
		betweenStart = startIdx + len(startMarker) + nl + 1
	}

	// betweenEnd is the start of the end marker's own line, found by
	// backtracking to the nearest preceding newline (or start-of-file).
	betweenEnd := 0
	if nl := bytes.LastIndexByte(readmeBytes[:endIdx], '\n'); nl != -1 {
		betweenEnd = nl + 1
	}

	var body string
	if trimmed := strings.TrimRight(content, "\n"); trimmed != "" {
		body = trimmed + "\n"
	}

	out := make([]byte, 0, len(readmeBytes)+len(body))
	out = append(out, readmeBytes[:betweenStart]...)
	out = append(out, body...)
	out = append(out, readmeBytes[betweenEnd:]...)
	return out, nil
}
