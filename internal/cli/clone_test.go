package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCloneOrAdopt_ClonesIntoAbsentDest covers the happy path: dest doesn't
// exist yet (nor does its parent), so cloneOrAdopt clones url straight into
// it, creating the parent directory along the way like cloneProfileRepo
// does.
func TestCloneOrAdopt_ClonesIntoAbsentDest(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	dest := filepath.Join(t.TempDir(), "state", "repos", "someone")

	status, err := cloneOrAdopt(t.Context(), remote, dest)
	if err != nil {
		t.Fatalf("cloneOrAdopt() error = %v, want nil", err)
	}
	if !strings.Contains(status, "cloned") {
		t.Errorf("status = %q, want it to mention a fresh clone", status)
	}

	out := runGitT(t, dest, "rev-parse", "--is-inside-work-tree")
	if strings.TrimSpace(out) != "true" {
		t.Errorf("dest %s is not a git working tree after clone", dest)
	}
}

// TestCloneOrAdopt_AdoptsMatchingExistingClone covers the case where dest is
// already a valid clone of url (e.g. a prior init run populated it):
// cloneOrAdopt must recognize it as already-correct and no-op, rather than
// erroring or re-cloning over it.
func TestCloneOrAdopt_AdoptsMatchingExistingClone(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)
	dest := cloneWorkdir(t, remote, "existing")
	headBefore := runGitT(t, dest, "rev-parse", "HEAD")

	status, err := cloneOrAdopt(t.Context(), remote, dest)
	if err != nil {
		t.Fatalf("cloneOrAdopt() error = %v, want nil", err)
	}
	if !strings.Contains(status, "adopted") {
		t.Errorf("status = %q, want it to mention adopting the existing clone", status)
	}

	headAfter := runGitT(t, dest, "rev-parse", "HEAD")
	if headBefore != headAfter {
		t.Errorf("HEAD changed from %q to %q; adopting an existing clone must not touch it", headBefore, headAfter)
	}
}

// TestCloneOrAdopt_ClonesIntoExistingEmptyDir covers dest existing as a
// plain empty directory (e.g. left behind by an earlier, aborted attempt):
// git itself is happy to clone into an existing empty directory, so
// cloneOrAdopt must not mistake "exists" for "already populated" and error
// out before ever trying.
func TestCloneOrAdopt_ClonesIntoExistingEmptyDir(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	dest := t.TempDir() // TempDir already exists and is empty.

	status, err := cloneOrAdopt(t.Context(), remote, dest)
	if err != nil {
		t.Fatalf("cloneOrAdopt() error = %v, want nil", err)
	}
	if !strings.Contains(status, "cloned") {
		t.Errorf("status = %q, want it to mention a fresh clone", status)
	}

	out := runGitT(t, dest, "rev-parse", "--is-inside-work-tree")
	if strings.TrimSpace(out) != "true" {
		t.Errorf("dest %s is not a git working tree after clone", dest)
	}
}

// TestCloneOrAdopt_ErrorsOnMismatchedOrigin covers dest already being a git
// working tree, but a clone of a *different* repo than the resolved
// url — e.g. a stale leftover from a previous, differently-configured
// targetRepo. cloneOrAdopt must refuse to silently wire it up as the
// publish target, naming both URLs so the adopter can tell what's wrong.
func TestCloneOrAdopt_ErrorsOnMismatchedOrigin(t *testing.T) {
	wantRemote := initBareRemote(t)
	seedRemote(t, wantRemote, markedReadme)

	otherRemote := initBareRemote(t)
	seedRemote(t, otherRemote, unmarkedReadme)
	dest := cloneWorkdir(t, otherRemote, "mismatched")

	status, err := cloneOrAdopt(t.Context(), wantRemote, dest)
	if err == nil {
		t.Fatalf("cloneOrAdopt() error = nil, status = %q, want a mismatch error", status)
	}
	if !strings.Contains(err.Error(), wantRemote) {
		t.Errorf("error %q does not mention the resolved URL %q", err.Error(), wantRemote)
	}
	if !strings.Contains(err.Error(), otherRemote) {
		t.Errorf("error %q does not mention the existing origin URL %q", err.Error(), otherRemote)
	}
}

// TestCloneOrAdopt_ErrorsOnNonEmptyNonGitDest covers dest existing,
// non-empty, and not a git repository at all (e.g. a directory an adopter
// pointed targetRepo at by mistake). cloneOrAdopt must reject this before
// ever invoking git — proven here by pointing url at an address that would
// fail differently (a DNS/connect error, not "not a git repository") if a
// clone were actually attempted.
func TestCloneOrAdopt_ErrorsOnNonEmptyNonGitDest(t *testing.T) {
	const unreachableURL = "https://198.51.100.1.invalid/definitely-unreachable/repo.git"

	dest := t.TempDir()
	writeFile(t, dest, "some-file.txt", "not a git repo\n")

	status, err := cloneOrAdopt(t.Context(), unreachableURL, dest)
	if err == nil {
		t.Fatalf("cloneOrAdopt() error = nil, status = %q, want a not-a-git-repo error", status)
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error = %q, want it to say dest is not a git repository", err.Error())
	}
	if strings.Contains(err.Error(), "git clone") {
		t.Errorf("error = %q mentions a git clone attempt; no git subprocess should have been invoked", err.Error())
	}
}

// TestCloneOrAdopt_ErrorsOnInvalidSourceURL covers a clone actually being
// attempted (dest absent, so no pre-check short-circuits it) against a
// source that doesn't exist: the wrapped error must surface git's own
// stderr, not just a bare exit-status.
func TestCloneOrAdopt_ErrorsOnInvalidSourceURL(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dest")

	_, err := cloneOrAdopt(t.Context(), "/nonexistent/path/repo.git", dest)
	if err == nil {
		t.Fatal("cloneOrAdopt() error = nil, want a clone failure")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %q, want it to surface git's own \"does not exist\" stderr", err.Error())
	}
}

// TestCloneOrAdopt_AuthRequiredFailsFastInsteadOfHanging is the KTD3
// integration check: a clone source demanding credentials git doesn't have
// must fail immediately with an actionable error, not hang waiting on a
// prompt no one can answer. authServer stands in for that source, answering
// every request (including git's initial info/refs discovery) with a plain
// 401 — enough to put git's own credential machinery in play without
// needing a real git-smart-http backend.
//
// HOME is redirected to an empty scratch directory so no credential helper
// configured on the machine running this test intercepts the request
// first — otherwise the assertion below would depend on the developer's own
// gitconfig instead of on cloneOrAdopt's behavior.
func TestCloneOrAdopt_AuthRequiredFailsFastInsteadOfHanging(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer authServer.Close()

	t.Setenv("HOME", t.TempDir())
	dest := filepath.Join(t.TempDir(), "dest")

	start := time.Now()
	_, err := cloneOrAdopt(t.Context(), authServer.URL+"/repo.git", dest)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("cloneOrAdopt() error = nil, want an auth failure")
	}
	if !strings.Contains(err.Error(), "terminal prompts disabled") {
		t.Errorf(
			"error = %q, want it to contain git's \"terminal prompts disabled\" message"+
				" (proves GIT_TERMINAL_PROMPT=0 took effect rather than blocking on a prompt)",
			err.Error(),
		)
	}
	if elapsed > 5*time.Second {
		t.Errorf("cloneOrAdopt() took %s against an auth-requiring source; want a fast failure, not a hang", elapsed)
	}
}

// TestCloneOrAdopt_ErrorsOnMissingOriginRemote covers dest already being a
// git working tree that simply has no "origin" remote configured at all —
// distinct from a mismatched origin (per the plan's KTD4 note): resolving
// the remote itself fails, rather than resolving to some other URL, so the
// error must say so instead of misreporting it as a mismatch.
func TestCloneOrAdopt_ErrorsOnMissingOriginRemote(t *testing.T) {
	dest := t.TempDir()
	runGitT(t, dest, "init", "-q", "-b", "main", dest)

	url := initBareRemote(t)

	status, err := cloneOrAdopt(t.Context(), url, dest)
	if err == nil {
		t.Fatalf("cloneOrAdopt() error = nil, status = %q, want a missing-origin error", status)
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error = %q, want it to mention the missing origin remote", err.Error())
	}
}
