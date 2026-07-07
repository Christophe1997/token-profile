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

// StartMarker and EndMarker delimit the token-profile section of a README
// (KTD7). They are exported so internal/cli's `init` command — the only
// other package that needs to recognize or scaffold these markers — has a
// single source of truth instead of duplicating the literal strings.
const (
	StartMarker = "<!-- token-profile:start -->"
	EndMarker   = "<!-- token-profile:end -->"
)

// Kept as unexported aliases so the rest of this file's logic doesn't need
// a mechanical rename.
const (
	startMarker = StartMarker
	endMarker   = EndMarker
)

// ErrMarkersMissing indicates the README lacks one or both token-profile
// markers. Inject deliberately refuses to guess an insertion point in this
// case (KTD7) — callers should scaffold the markers via `token-profile init`.
var ErrMarkersMissing = errors.New("token-profile markers not found in README")

// ErrMarkersDuplicated indicates a marker appears more than once in the
// README. Inject refuses to guess which occurrence is the intended one
// (e.g. a manually pasted duplicate block) — callers must remove the
// duplicate marker pair by hand before running again.
var ErrMarkersDuplicated = errors.New("token-profile markers appear more than once in README")

// markerBounds locates the token-profile marker lines in readmeBytes and
// returns the byte offsets bounding the interior content: betweenStart is
// right after the newline that terminates the start marker's line (or
// end-of-file if the marker has no trailing newline), and betweenEnd is the
// start of the end marker's own line. If either marker appears more than
// once, it returns an error wrapping ErrMarkersDuplicated rather than
// guessing which occurrence is the intended one. If either marker is
// missing (or they appear out of order), it returns an error wrapping
// ErrMarkersMissing that points the caller at `token-profile init` rather
// than guessing where to insert the markers.
func markerBounds(readmeBytes []byte) (betweenStart, betweenEnd int, err error) {
	startCount := bytes.Count(readmeBytes, []byte(startMarker))
	endCount := bytes.Count(readmeBytes, []byte(endMarker))
	if startCount > 1 || endCount > 1 {
		return 0, 0, fmt.Errorf(
			"%w: found %q %d time(s) and %q %d time(s); manually remove the duplicate marker pair before running again",
			ErrMarkersDuplicated, startMarker, startCount, endMarker, endCount,
		)
	}

	startIdx := bytes.Index(readmeBytes, []byte(startMarker))
	endIdx := bytes.Index(readmeBytes, []byte(endMarker))
	if startIdx == -1 || endIdx == -1 || endIdx < startIdx {
		return 0, 0, fmt.Errorf(
			"%w: run `token-profile init` to scaffold %q and %q into your README",
			ErrMarkersMissing, startMarker, endMarker,
		)
	}

	betweenStart = len(readmeBytes)
	if nl := bytes.IndexByte(readmeBytes[startIdx+len(startMarker):], '\n'); nl != -1 {
		betweenStart = startIdx + len(startMarker) + nl + 1
	}

	betweenEnd = 0
	if nl := bytes.LastIndexByte(readmeBytes[:endIdx], '\n'); nl != -1 {
		betweenEnd = nl + 1
	}

	return betweenStart, betweenEnd, nil
}

// Inject replaces everything strictly between the token-profile start/end
// marker lines in readme with content, returning the updated file. Both
// marker lines, and everything outside them, are left unchanged. If either
// marker is missing (or they appear out of order), Inject returns an error
// wrapping ErrMarkersMissing that points the caller at `token-profile init`
// rather than guessing where to insert the markers. If either marker appears
// more than once, Inject returns an error wrapping ErrMarkersDuplicated
// instead of guessing which occurrence is the intended one.
func Inject(readmeBytes []byte, content string) ([]byte, error) {
	betweenStart, betweenEnd, err := markerBounds(readmeBytes)
	if err != nil {
		return nil, err
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

// Strip is the inverse of Inject: it clears the content between the
// token-profile marker lines in readme, leaving the marker lines themselves,
// and everything outside them, unchanged (KTD9) — a later plain `init` can
// re-inject a card, making cleanup reversible. Strip is idempotent: if the
// interior is already empty, or the markers are absent altogether, it
// returns readme unchanged rather than erroring. If either marker appears
// more than once, Strip returns the same error Inject would
// (ErrMarkersDuplicated) rather than guessing which marker pair to clear.
func Strip(readmeBytes []byte) ([]byte, error) {
	betweenStart, betweenEnd, err := markerBounds(readmeBytes)
	if err != nil {
		if errors.Is(err, ErrMarkersMissing) {
			return readmeBytes, nil
		}
		return nil, err
	}
	if betweenStart == betweenEnd {
		return readmeBytes, nil
	}

	out := make([]byte, 0, len(readmeBytes)-(betweenEnd-betweenStart))
	out = append(out, readmeBytes[:betweenStart]...)
	out = append(out, readmeBytes[betweenEnd:]...)
	return out, nil
}
