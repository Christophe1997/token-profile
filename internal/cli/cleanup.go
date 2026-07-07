package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/readme"
)

// errCleanupRequiresTTY is returned when Cleanup is invoked with no
// interactive terminal to confirm against (KTD12, mirroring R5's
// fail-fast-over-auto-completing rule): unlike run/init, cleanup has no
// non-interactive override, since deleting a repo's footprint is
// exactly the kind of step this plan's "every new surface fails safe"
// principle exists for.
var errCleanupRequiresTTY = errors.New(
	"cleanup requires an interactive terminal to confirm; rerun it from a real terminal session (no non-interactive override is available)",
)

// CleanupDeps bundles Cleanup's dependencies (F4) as a struct, mirroring
// InitDeps/WizardDeps's own rationale: RepoDir/Schedule/Input/Output are
// heterogeneous values a positional signature would invite mixing up.
type CleanupDeps struct {
	// RepoDir is the target repo's local working-copy path — the same
	// Config.TargetRepo value run/init use. Checked for git-repo validity
	// strictly before any lock is acquired (KTD5); a blank, missing, or
	// non-git RepoDir degrades the repo-side steps to a no-op (KTD6)
	// rather than failing the whole command.
	RepoDir string
	// Schedule bundles CheckScheduleState/RemoveSchedule's dependencies.
	// Schedule deregistration is attempted regardless of RepoDir's
	// validity or lock state (KTD5, KTD6).
	Schedule ScheduleDeps
	// Interactive gates the whole command (KTD12): cleanup always shows a
	// confirmation prompt and has no non-interactive override, so it fails
	// fast when no TTY is present, mirroring init.go's isInteractive/R5
	// pattern.
	Interactive bool
	// Accessible drives the confirmation huh.Confirm field in accessible
	// (TTY-free) mode; every test in this package sets it true, mirroring
	// WizardDeps.Accessible (KTD2).
	Accessible bool
	// Input and Output are the confirmation form's IO streams. Nil defers
	// to huh's own defaults (the real terminal, in interactive mode);
	// tests set both to drive the prompt entirely TTY-free.
	Input  io.Reader
	Output io.Writer
}

// CleanupResult reports what Cleanup actually found and did, letting a
// caller (NewCleanupCmd's RunE, or a test) inspect structured outcomes
// instead of scraping printed text.
type CleanupResult struct {
	// Declined is true when the confirmation prompt was declined (or, in
	// production, interrupted via ctrl+c — indistinguishable from a
	// decline in accessible mode, see KTD2). No other field is meaningful
	// when this is true: nothing was touched.
	Declined bool
	// RepoValid reports whether RepoDir was confirmed present and a valid
	// git working tree. False means the repo-side steps below were
	// skipped as a no-op (KTD6): schedule deregistration proceeded
	// regardless.
	RepoValid bool
	// Schedule is the schedule's state as found *before* any removal
	// attempt (KTD7): ScheduleRegistered means something was found and
	// removed (assuming Cleanup's error is nil), ScheduleNotRegistered
	// means there was nothing to remove, and ScheduleCheckFailed means the
	// live state couldn't be determined at all. RemoveSchedule's own
	// return value can't distinguish "removed" from "already absent" (both
	// collapse to ScheduleNotRegistered post-removal) — this field
	// preserves that distinction instead (R11, AE3, AE4).
	Schedule ScheduleState
	// ReadmeStripped is true only if README.md actually had marker
	// interior content cleared (idempotent: a already-clean README leaves
	// this false, not an error).
	ReadmeStripped bool
	// DirRemoved is true only if .token-profile/ actually existed and was
	// removed (idempotent: an already-absent directory leaves this false).
	DirRemoved bool
}

// cleanupFootprint is what Cleanup prints before the single confirmation
// prompt (KTD5's approach: "prints exactly what will be touched"), gathered
// entirely read-only so it can be computed before the user has consented to
// anything.
type cleanupFootprint struct {
	scheduleLabel string
	repoValid     bool
	readmeBytes   int
	fileCount     int
	uncommitted   []string
}

func (f cleanupFootprint) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cleanup will attempt to touch the following:\n")
	fmt.Fprintf(&b, "  - schedule %q: deregister if currently registered\n", f.scheduleLabel)
	if !f.repoValid {
		fmt.Fprintf(&b, "  - target repo: missing or not a git repository — repo-side steps will be skipped as a no-op\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  - README.md: strip %d byte(s) of card content between the token-profile markers\n", f.readmeBytes)
	fmt.Fprintf(&b, "  - .token-profile/: delete (%d file(s))\n", f.fileCount)
	if len(f.uncommitted) > 0 {
		fmt.Fprintf(&b, "  - uncommitted changes already present under README.md/.token-profile/ before cleanup runs:\n")
		for _, line := range f.uncommitted {
			fmt.Fprintf(&b, "      %s\n", line)
		}
	}
	return b.String()
}

// Cleanup performs F4: it shows a single confirmation naming exactly what
// will be touched, then — on confirm — deregisters the schedule (always,
// regardless of RepoDir's validity, KTD6) and, only when RepoDir is a valid
// git working tree, strips README.md's marker interior and deletes
// .token-profile/ under the protection of the same run-lock run/init use.
//
// RepoDir's validity is checked once, strictly before acquireRunLock is
// ever called (KTD5): acquireRunLock's own os.MkdirAll would otherwise
// silently recreate a deliberately-deleted RepoDir as a side effect of
// taking the lock, undermining the whole point of degrading a missing repo
// to a no-op rather than resurrecting it.
func Cleanup(ctx context.Context, deps CleanupDeps) (CleanupResult, error) {
	if !deps.Interactive {
		return CleanupResult{}, errCleanupRequiresTTY
	}

	repoValid := deps.RepoDir != "" && requireGitWorkTree(ctx, deps.RepoDir) == nil

	footprint, err := inspectFootprint(ctx, deps, repoValid)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("inspecting cleanup footprint: %w", err)
	}

	confirmed, err := confirmCleanup(deps, footprint)
	if err != nil {
		return CleanupResult{}, err
	}
	if !confirmed {
		return CleanupResult{Declined: true}, nil
	}

	if !repoValid {
		state, err := deregisterSchedule(ctx, deps.Schedule)
		return CleanupResult{Schedule: state}, err
	}

	// Schedule deregistration never depends on lock acquisition (KTD5,
	// KTD6): it runs before acquireRunLock is even attempted, so a
	// contended lock still lets the schedule get deregistered.
	scheduleState, scheduleErr := deregisterSchedule(ctx, deps.Schedule)
	result := CleanupResult{RepoValid: true, Schedule: scheduleState}

	release, lockErr := acquireRunLock(deps.RepoDir)
	if lockErr != nil {
		return result, errors.Join(scheduleErr, fmt.Errorf("acquiring run-lock: %w", lockErr))
	}
	defer release()

	stripped, err := stripReadmeFile(deps.RepoDir)
	if err != nil {
		return result, errors.Join(scheduleErr, fmt.Errorf("stripping README markers: %w", err))
	}
	result.ReadmeStripped = stripped

	removed, err := removeTokenProfileDir(deps.RepoDir)
	if err != nil {
		return result, errors.Join(scheduleErr, fmt.Errorf("removing .token-profile: %w", err))
	}
	result.DirRemoved = removed

	return result, scheduleErr
}

// deregisterSchedule reports the schedule's state as found *before* any
// removal attempt: RemoveSchedule's own return value collapses "was
// registered, now removed" and "was already absent" into the same
// ScheduleNotRegistered value (see its doc comment), which loses exactly
// the removed-vs-nothing-to-remove distinction R11/AE3 require. The
// live check is skipped a second time when nothing was found (mirroring
// RemoveSchedule's own no-op-on-not-registered contract) to avoid a
// redundant launchctl/crontab invocation.
func deregisterSchedule(ctx context.Context, deps ScheduleDeps) (ScheduleState, error) {
	before, err := CheckScheduleState(ctx, deps)
	if err != nil {
		return before, err
	}
	if before == ScheduleNotRegistered {
		return before, nil
	}
	if _, err := RemoveSchedule(ctx, deps); err != nil {
		return before, fmt.Errorf("removing schedule: %w", err)
	}
	return before, nil
}

// inspectFootprint gathers everything Cleanup's confirmation prompt reports,
// entirely read-only. When repoValid is false, only the schedule label is
// reported — every repo-side detail is meaningless for a missing/corrupted
// RepoDir.
func inspectFootprint(ctx context.Context, deps CleanupDeps, repoValid bool) (cleanupFootprint, error) {
	footprint := cleanupFootprint{scheduleLabel: deps.Schedule.Label, repoValid: repoValid}
	if !repoValid {
		return footprint, nil
	}

	readmePath := filepath.Join(deps.RepoDir, readmeFile)
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cleanupFootprint{}, fmt.Errorf("reading README %s: %w", readmePath, err)
	}
	if readmeBytes != nil {
		stripped, err := readme.Strip(readmeBytes)
		if err != nil {
			return cleanupFootprint{}, fmt.Errorf("inspecting README markers: %w", err)
		}
		footprint.readmeBytes = len(readmeBytes) - len(stripped)
	}

	fileCount, err := countFiles(filepath.Join(deps.RepoDir, ".token-profile"))
	if err != nil {
		return cleanupFootprint{}, fmt.Errorf("counting .token-profile files: %w", err)
	}
	footprint.fileCount = fileCount

	uncommitted, err := uncommittedPaths(ctx, deps.RepoDir)
	if err != nil {
		return cleanupFootprint{}, fmt.Errorf("checking working tree status: %w", err)
	}
	footprint.uncommitted = uncommitted

	return footprint, nil
}

// countFiles counts the regular files (recursively) under dir, reporting 0
// rather than an error when dir doesn't exist — an already-cleaned repo's
// footprint is legitimately absent, not a fault.
func countFiles(dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	return count, nil
}

// uncommittedPaths reports the working tree's uncommitted changes scoped to
// README.md and .token-profile/ (KTD5's approach: named explicitly rather
// than folded silently into the plain file count), one porcelain status
// line per change. An empty result means the working tree is clean for
// those paths.
func uncommittedPaths(ctx context.Context, repoDir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "--", readmeFile, ".token-profile")
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git status --porcelain: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	text := strings.TrimRight(stdout.String(), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// confirmCleanup prints footprint (KTD5's approach: everything that will be
// touched, printed before the prompt) then shows a single huh.Confirm field,
// reusing the wizard's cancellation contract (KTD2, U3): huh's accessible
// mode never surfaces huh.ErrUserAborted, so an interrupted/aborted confirm
// and a declined confirm both read as the field's zero value, false — both
// treated identically as "declined" here. The huh.ErrUserAborted check
// below remains as a production-only safeguard for a real interactive
// ctrl+c, not exercised by this package's accessible-mode tests, matching
// RunWizard's own documented rationale.
func confirmCleanup(deps CleanupDeps, footprint cleanupFootprint) (bool, error) {
	out := deps.Output
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprint(out, footprint.String())

	var confirmed bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Proceed with cleanup?").
				Value(&confirmed),
		),
	).WithAccessible(deps.Accessible)

	if deps.Input != nil {
		form = form.WithInput(deps.Input)
	}
	if deps.Output != nil {
		form = form.WithOutput(deps.Output)
	}

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("running cleanup confirmation: %w", err)
	}
	return confirmed, nil
}

// stripReadmeFile clears README.md's marker interior (U7's readme.Strip) in
// place, reporting whether it actually changed anything — idempotent, so a
// re-run against an already-stripped README reports false rather than
// rewriting an identical file.
func stripReadmeFile(repoDir string) (bool, error) {
	path := filepath.Join(repoDir, readmeFile)
	existing, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("reading README %s: %w", path, err)
	}

	stripped, err := readme.Strip(existing)
	if err != nil {
		return false, err
	}
	if bytes.Equal(existing, stripped) {
		return false, nil
	}
	if err := os.WriteFile(path, stripped, 0o644); err != nil {
		return false, fmt.Errorf("writing README %s: %w", path, err)
	}
	return true, nil
}

// removeTokenProfileDir deletes repoDir's .token-profile/ directory,
// reporting whether it actually existed beforehand — idempotent, so a
// re-run against an already-deleted directory reports false rather than an
// error.
func removeTokenProfileDir(repoDir string) (bool, error) {
	path := filepath.Join(repoDir, ".token-profile")
	info, statErr := os.Stat(path)
	existed := statErr == nil && info.IsDir()
	if err := os.RemoveAll(path); err != nil {
		return false, err
	}
	return existed, nil
}

// printCleanupResult writes result's outcome as a short, human-readable
// summary — the production counterpart to the structured CleanupResult
// tests assert against directly.
func printCleanupResult(w io.Writer, result CleanupResult) {
	if result.Declined {
		fmt.Fprintln(w, "cleanup cancelled — nothing changed")
		return
	}

	switch result.Schedule {
	case ScheduleRegistered:
		fmt.Fprintln(w, "schedule: removed")
	case ScheduleCheckFailed:
		fmt.Fprintln(w, "schedule: could not determine live state")
	default:
		fmt.Fprintln(w, "schedule: nothing to remove")
	}

	if !result.RepoValid {
		fmt.Fprintln(w, "target repo: missing or not a git repository — repo-side steps skipped")
		return
	}
	if result.ReadmeStripped {
		fmt.Fprintln(w, "README.md: markers stripped")
	} else {
		fmt.Fprintln(w, "README.md: nothing to strip")
	}
	if result.DirRemoved {
		fmt.Fprintln(w, ".token-profile/: removed")
	} else {
		fmt.Fprintln(w, ".token-profile/: nothing to remove")
	}
}

// NewCleanupCmd builds the `token-profile cleanup` cobra command: a thin
// wrapper that loads the real config file, then delegates to Cleanup.
// Mirrors NewRunCmd/NewInitCmd's own wiring pattern.
func NewCleanupCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Deregister the schedule and strip token-profile's footprint from the target repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("loading config %s: %w", configPath, err)
			}

			result, err := Cleanup(cmd.Context(), CleanupDeps{
				RepoDir: cfg.TargetRepo,
				Schedule: ScheduleDeps{
					Label: launchdLabel,
				},
				Interactive: isInteractive(os.Stdin),
				Output:      cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			printCleanupResult(cmd.OutOrStdout(), result)
			return nil
		},
	}

	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(), "path to token-profile's config file")
	return cmd
}
