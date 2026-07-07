// Package gitops publishes local changes (a rendered README, plus its
// backing snapshot file) to a git remote, shelling out to the real git
// binary the same way internal/agentsview shells out to agentsview. git is
// assumed to already be on PATH.
package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// maxPushAttempts bounds the fetch-rebase-retry loop in Publish. Concurrent
// pushes from an adopter's own other machines are the expected case (KTD8),
// not an exceptional one, so a handful of retries absorbs ordinary races;
// beyond that, something else is wrong and Publish should stop and report it
// rather than loop indefinitely.
const maxPushAttempts = 3

// ErrPushExhausted indicates Publish's bounded fetch-rebase-retry loop never
// landed the local commit on the remote. The local commit is left in place
// (Publish never rolls it back), so the caller can inspect the repo and
// retry later without having lost any work.
var ErrPushExhausted = errors.New("git push rejected after retries")

// ExitError wraps a failed git invocation, preserving stderr for actionable
// error messages, mirroring internal/agentsview's ExitError convention.
type ExitError struct {
	// Args is the git subcommand and arguments that failed, e.g. "push".
	Args []string
	Err  error
	// Stderr is the captured stderr output of the failed invocation.
	Stderr string
}

func (e *ExitError) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	label := strings.Join(e.Args, " ")
	if stderr == "" {
		return fmt.Sprintf("git %s: %v", label, e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", label, e.Err, stderr)
}

func (e *ExitError) Unwrap() error { return e.Err }

// runGit runs `git <args...>` in dir, capturing stderr for error reporting.
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &ExitError{Args: args, Err: err, Stderr: stderr.String()}
	}
	return nil
}

// runGitOutput runs `git <args...>` in dir like runGit, but also returns its
// captured stdout, for subcommands whose result is a data line (e.g. `diff
// --name-only`) rather than just an exit code.
func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &ExitError{Args: args, Err: err, Stderr: stderr.String()}
	}
	return stdout.String(), nil
}

// resolveRebaseConflict attempts to recover from a `git rebase` invocation
// that stopped on a conflict, when every conflicting path is in
// autoResolvePaths. It keeps each conflicting path's replayed-commit
// version — `git checkout --theirs`, since rebase's ours/theirs convention
// is the reverse of merge's: "ours" is HEAD, the branch being rebased onto
// (upstream), and "theirs" is the commit being replayed (the local change)
// — stages it, and continues the rebase.
//
// Returns a non-nil error, leaving the rebase in progress for the caller's
// existing abort path, if any conflicting path falls outside
// autoResolvePaths (so a real conflict on authored content is never
// silently papered over) or if the resolve-and-continue sequence itself
// fails.
func resolveRebaseConflict(ctx context.Context, repoDir string, autoResolvePaths []string) error {
	if len(autoResolvePaths) == 0 {
		return errors.New("no auto-resolvable paths configured for this conflict")
	}

	out, err := runGitOutput(ctx, repoDir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return fmt.Errorf("listing conflicted paths: %w", err)
	}
	conflicted := strings.Fields(out)
	if len(conflicted) == 0 {
		return errors.New("rebase failed but no conflicted paths were found")
	}

	allowed := make(map[string]bool, len(autoResolvePaths))
	for _, p := range autoResolvePaths {
		allowed[p] = true
	}
	for _, p := range conflicted {
		if !allowed[p] {
			return fmt.Errorf("conflict on %s is outside the auto-resolvable set", p)
		}
	}

	for _, p := range conflicted {
		if err := runGit(ctx, repoDir, "checkout", "--theirs", p); err != nil {
			return fmt.Errorf("resolving %s to the local commit's version: %w", p, err)
		}
		if err := runGit(ctx, repoDir, "add", p); err != nil {
			return fmt.Errorf("staging resolved %s: %w", p, err)
		}
	}
	if err := runGit(ctx, repoDir, "rebase", "--continue"); err != nil {
		return fmt.Errorf("continuing rebase after auto-resolving conflicts: %w", err)
	}
	return nil
}

// Regenerate re-derives and rewrites any content that depends on the
// current merged repo state (e.g. re-running merge+render+inject after a
// rebase pulled in new remote data), then returns so Publish can re-stage
// the (possibly-changed) files before the retried push. A nil Regenerate is
// valid: Publish then behaves exactly as if no rebase-driven regeneration
// step existed at all.
type Regenerate func() error

// Publish stages files, commits them with commitMessage, and pushes to the
// current branch's configured upstream. If the push is rejected because the
// remote has commits the local repo hasn't fetched yet — an expected
// occurrence when an adopter runs token-profile from more than one machine
// (KTD8) — Publish fetches, rebases onto the now-current upstream, and
// retries the push, up to maxPushAttempts pushes total.
//
// A successful rebase can pull in another machine's newly-pushed data,
// which may make the commit's already-staged content (e.g. a rendered
// README) stale relative to that new data. If regenerate is non-nil,
// Publish calls it once after each successful rebase — before retrying the
// push — then re-stages files and folds the result into the existing
// commit via `commit --amend`, so the retried push carries fresh content
// instead of stacking a second commit. regenerate may be nil, in which case
// Publish performs no such step (matching its behavior before this
// parameter existed).
//
// The rebase step itself can conflict, when two machines' commits touch the
// same line of the same file — ordinarily unrecoverable without manual
// intervention. autoResolvePaths names paths where that's not true: fully
// regenerated (not authored) content whose pre-rebase diff carries no
// information worth preserving, since regenerate immediately overwrites it
// with the real post-rebase-merged data anyway. When every conflicting path
// is in autoResolvePaths, Publish resolves the conflict by keeping the
// replayed commit's version of each and continues the rebase; a conflict
// touching any other path still aborts, exactly as before. autoResolvePaths
// may be nil, in which case any rebase conflict aborts (matching Publish's
// behavior before this parameter existed).
//
// If every attempt is exhausted, or the push fails for a reason that isn't a
// non-fast-forward rejection (e.g. no remote configured, auth failure),
// Publish returns a descriptive error. In all such cases the local commit
// created here is left intact — Publish never resets or rolls it back — so
// no work is lost even when the push itself never lands.
func Publish(ctx context.Context, repoDir string, files []string, commitMessage string, regenerate Regenerate, autoResolvePaths []string) error {
	if len(files) == 0 {
		return errors.New("gitops: Publish requires at least one file to stage")
	}

	if err := runGit(ctx, repoDir, append([]string{"add"}, files...)...); err != nil {
		return fmt.Errorf("staging files: %w", err)
	}
	if err := runGit(ctx, repoDir, "commit", "-m", commitMessage); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	var lastPushErr error
	for attempt := 1; attempt <= maxPushAttempts; attempt++ {
		pushErr := runGit(ctx, repoDir, "push")
		if pushErr == nil {
			return nil
		}

		exitErr, ok := errors.AsType[*ExitError](pushErr)
		if !ok {
			return fmt.Errorf("pushing: %w", pushErr)
		}
		lastPushErr = exitErr

		if !isNonFastForwardRejection(exitErr.Stderr) {
			return fmt.Errorf(
				"pushing: %w (not a non-fast-forward rejection, so not retrying; the local commit is preserved)",
				exitErr,
			)
		}
		if attempt == maxPushAttempts {
			break
		}

		if err := runGit(ctx, repoDir, "fetch"); err != nil {
			return fmt.Errorf("fetching before retrying push: %w (local commit preserved)", err)
		}
		if err := runGit(ctx, repoDir, "rebase", "@{u}"); err != nil {
			if resolveErr := resolveRebaseConflict(ctx, repoDir, autoResolvePaths); resolveErr != nil {
				if abortErr := runGit(ctx, repoDir, "rebase", "--abort"); abortErr != nil {
					return fmt.Errorf(
						"rebasing before retrying push: %w; additionally failed to abort the rebase: %v (repo may be left mid-rebase; resolve manually, the local commit is still present in the rebase todo)",
						err, abortErr,
					)
				}
				return fmt.Errorf(
					"rebasing before retrying push: %w (rebase aborted; local commit preserved on the branch, resolve the conflict manually and retry)",
					err,
				)
			}
		}

		if regenerate != nil {
			if err := regenerate(); err != nil {
				return fmt.Errorf("regenerating content after rebase: %w (local commit preserved, unpushed)", err)
			}
			if err := runGit(ctx, repoDir, append([]string{"add"}, files...)...); err != nil {
				return fmt.Errorf("re-staging regenerated files: %w (local commit preserved, unpushed)", err)
			}
			if err := runGit(ctx, repoDir, "commit", "--amend", "--no-edit"); err != nil {
				return fmt.Errorf("amending commit with regenerated content: %w (local commit preserved, unpushed)", err)
			}
		}
	}

	return fmt.Errorf("%w after %d attempts: %w (local commit preserved, unpushed)", ErrPushExhausted, maxPushAttempts, lastPushErr)
}

// isNonFastForwardRejection reports whether a `git push` failure's stderr
// looks like git's own non-fast-forward rejection — i.e. "the remote has
// commits we don't have locally" — as opposed to some other hard failure.
//
// Design decision: git always prefixes its *local* ref-update-rejected lines
// with "! [rejected]" for both of its non-fast-forward variants ("fetch
// first" when the remote has commits absent locally, and "non-fast-forward"
// after a rebase still leaves history diverged). Server-side policy
// rejections (e.g. a pre-receive hook decline, branch protection) instead
// print "! [remote rejected]" — a different literal string — and hard
// failures like a missing remote or auth error contain neither phrase. So a
// plain substring check on "! [rejected]" cleanly separates "retry-worthy"
// from "not retry-worthy" without needing to parse exit codes or hook
// output, and was verified against real git 2.55 output for both cases.
func isNonFastForwardRejection(stderr string) bool {
	return strings.Contains(stderr, "! [rejected]")
}
