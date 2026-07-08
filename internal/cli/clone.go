package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
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
			dest, "origin", redactCredentials(url), err,
		)
	}
	if origin != url {
		return "", fmt.Errorf(
			"clone destination %q already has an %q remote (%s) that does not match the resolved profile repo URL (%s) — refusing to adopt an unrelated repository as the publish target",
			dest, "origin", redactCredentials(origin), redactCredentials(url),
		)
	}

	// A matching origin alone isn't sufficient to adopt: an interrupted or
	// never-completed clone (e.g. `git init && git remote add origin <url>`,
	// or a `git clone` killed before the object transfer/checkout finished)
	// satisfies hasGitDir and the origin check above with zero commits ever
	// fetched. Silently adopting that empty shell would let
	// ensureReadmeMarkers fabricate a brand-new README as if this were a
	// genuinely fresh repo, discarding all context that the real remote
	// already has history.
	if _, err := headCommit(ctx, dest); err != nil {
		return "", fmt.Errorf(
			"clone destination %q has a matching %q remote but no commits — it looks like an interrupted or incomplete clone; remove it and retry, or fetch it manually before re-running init: %w",
			dest, "origin", err,
		)
	}

	return fmt.Sprintf("adopted existing clone at %s (origin already matches %s)", dest, url), nil
}

// redactCredentials strips a URL's userinfo component before it's echoed
// into error output — an existing remote (e.g. a headless/CI clone's
// origin) may embed a personal access token in https://<token>@host/...
// form, and that must never reach a terminal or log. Falls back to the
// original string when it doesn't parse as a URL, which covers scp-style
// git@host:path addresses — those never carry a secret in their user
// portion, so there's nothing to strip.
func redactCredentials(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User == nil {
		return rawURL
	}
	parsed.User = nil
	return parsed.String()
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
