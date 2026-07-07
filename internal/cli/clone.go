package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cloneOrAdopt satisfies dest as the local clone of url: cloning fresh when
// dest is absent or an empty directory, or adopting dest as-is when it's
// already a git working tree whose origin remote matches url. This
// pre-check (KTD4) replaces cloneProfileRepo's unconditional `git clone`,
// which fails outright — after cloneProfileRepo's own os.MkdirAll has
// already created the parent directory — the second time init runs against
// a dest a prior run already populated.
//
// The returned status is a one-line description of which of the three
// outcomes occurred (cloned fresh / adopted existing / failed), meant for
// the caller to surface to the adopter; it's only meaningful when err is
// nil; the caller reports err itself for the failure case.
func cloneOrAdopt(ctx context.Context, url, dest string) (string, error) {
	info, statErr := os.Stat(dest)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		return cloneFresh(ctx, url, dest)
	case statErr != nil:
		return "", fmt.Errorf("checking clone destination %q: %w", dest, statErr)
	case !info.IsDir():
		return "", fmt.Errorf("clone destination %q exists and is not a directory", dest)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		return "", fmt.Errorf("reading clone destination %q: %w", dest, err)
	}
	if len(entries) == 0 {
		return cloneFresh(ctx, url, dest)
	}

	// dest is non-empty: whether it's usable depends on it already being a
	// git working tree for url, checked without invoking git at all (KTD4)
	// — a plain os.Stat sees enough to reject a plain non-empty directory
	// fast, without the cost (and, for garbage input, the risk) of shelling
	// out.
	if !hasGitDir(dest) {
		return "", fmt.Errorf(
			"clone destination %q already exists and is not empty, but is not a git repository — move it aside or point at an empty/nonexistent path, then retry",
			dest,
		)
	}

	origin, err := gitRemoteURL(ctx, dest, "origin")
	if err != nil {
		return "", fmt.Errorf(
			"clone destination %q is an existing git repository, but its %q remote could not be resolved (expected it to match %s): %w",
			dest, "origin", url, err,
		)
	}
	if origin != url {
		return "", fmt.Errorf(
			"clone destination %q already has an %q remote (%s) that does not match the resolved profile repo URL (%s) — refusing to adopt an unrelated repository as the publish target",
			dest, "origin", origin, url,
		)
	}

	return fmt.Sprintf("adopted existing clone at %s (origin already matches %s)", dest, url), nil
}

// hasGitDir reports whether dir looks like a git working tree, checking
// only for a top-level .git entry — a directory for an ordinary clone, or a
// file for a worktree/submodule gitlink — without shelling out to git.
func hasGitDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return info.Mode().IsDir() || info.Mode().IsRegular()
}

// gitRemoteURL resolves remote's URL in the git working tree at dir via
// `git remote get-url`, returning a distinct error (rather than an empty
// string) when remote isn't configured at all, so cloneOrAdopt's caller can
// tell "no such remote" apart from "remote resolved but doesn't match".
func gitRemoteURL(ctx context.Context, dir, remote string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", remote)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git remote get-url %s: %w: %s", remote, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// cloneFresh clones url into dest via `git clone`, creating dest's parent
// directory first like cloneProfileRepo does. GIT_TERMINAL_PROMPT=0 plus
// stdin redirected from os.DevNull (KTD3) keep a clone against a URL
// requiring credentials git doesn't have from blocking indefinitely on a
// prompt no one is present to answer — a missing-credential auth failure
// then fails fast with git's own actionable stderr instead of hanging the
// whole init/run invisibly.
func cloneFresh(ctx context.Context, url, dest string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("creating clone destination directory for %s: %w", dest, err)
	}

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return "", fmt.Errorf("opening %s for git clone's stdin: %w", os.DevNull, err)
	}
	defer devNull.Close()

	cmd := exec.CommandContext(ctx, "git", "clone", url, dest)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = devNull
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone %s %s: %w: %s", url, dest, err, strings.TrimSpace(stderr.String()))
	}
	return fmt.Sprintf("cloned %s into %s", url, dest), nil
}
