// Package cli wires the U1-U6 building blocks (config, agentsview,
// snapshot, machineid, summary, render, readme, gitops) into
// token-profile's cobra subcommands.
package cli

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Christophe1997/token-profile/internal/agentsview"
	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/gitops"
	"github.com/Christophe1997/token-profile/internal/machineid"
	"github.com/Christophe1997/token-profile/internal/readme"
	"github.com/Christophe1997/token-profile/internal/render"
	"github.com/Christophe1997/token-profile/internal/snapshot"
	"github.com/Christophe1997/token-profile/internal/summary"
)

// readmeFile is the rendered profile's README path, relative to a target
// repo's root.
const readmeFile = "README.md"

// RunDeps bundles the explicit dependencies the end-to-end refresh flow
// (F1: solo adopter refresh, F2: multi-machine merge) needs. Keeping these
// as plain fields, rather than deriving them from cobra flags/env inside
// Run itself, is what makes Run unit-testable without going through cobra
// command execution.
type RunDeps struct {
	// Config supplies the breakdown mode and trailing window.
	Config config.Config
	// Client resolves this machine's local usage via agentsview.
	Client *agentsview.Client
	// MachineID identifies this machine's snapshot directory within RepoDir.
	MachineID string
	// Now stands in for "the current time" throughout the run: it bounds
	// the trailing-window --since cutoff, the streak's "today", and the
	// rendered "last updated" timestamp. An explicit field (rather than
	// time.Now() called internally) keeps Run deterministic under test.
	Now time.Time
	// RepoDir is the target repo's working-copy path on disk — where the
	// snapshot is written, the README lives, and git operations run.
	// Kept separate from Config.TargetRepo so tests can point it at a
	// scratch git fixture without constructing a full config file.
	RepoDir string
	// Stdout receives a one-line confirmation — the headline summary plus
	// the just-published commit's short hash — after a successful
	// publish. Nil is valid and silently discards this output, so every
	// existing RunDeps literal that doesn't set it keeps behaving exactly
	// as before.
	Stdout io.Writer
}

// Run executes the end-to-end refresh flow: resolve this machine's usage,
// write its snapshot, merge every machine's snapshot in deps.RepoDir,
// compute the summary, render the dashboard card, inject it into the
// target repo's README, and publish both files to the repo's remote.
//
// Before touching anything, Run validates deps.RepoDir is a real git
// working tree and acquires this machine's exclusive run-lock (Fix 2, Fix
// 3) — both fail fast with an actionable error rather than leaving stray
// files behind or racing a second overlapping invocation.
//
// Each stage's error is wrapped with enough context to identify which
// stage failed, since a run can fail at many different points.
func Run(ctx context.Context, deps RunDeps) error {
	if err := requireGitWorkTree(ctx, deps.RepoDir); err != nil {
		return err
	}

	release, err := acquireRunLock(deps.RepoDir)
	if err != nil {
		return err
	}
	defer release()

	return run(ctx, deps)
}

// run is Run's validated, locked core. It's factored out so Init can
// perform its own single lock acquisition — covering both its scaffolding
// steps and this first run — without Run's own acquireRunLock call
// contending against itself when Init delegates here (see Init).
func run(ctx context.Context, deps RunDeps) error {
	since := sinceDate(deps.Now, deps.Config.TrailingWindow)
	dataset, err := deps.Client.Resolve(ctx, agentsview.ResolveOptions{Since: since})
	if err != nil {
		return fmt.Errorf("resolving usage: %w", err)
	}

	rows := toSnapshotRows(dataset.Rows)
	if err := snapshot.Write(deps.RepoDir, deps.MachineID, rows); err != nil {
		return fmt.Errorf("writing snapshot: %w", err)
	}

	if err := mergeRenderInject(deps); err != nil {
		return err
	}

	files := []string{snapshotRelPath(deps.MachineID), readmeFile}
	commitMessage := fmt.Sprintf("chore(token-profile): refresh usage profile as of %s", deps.Now.UTC().Format(time.RFC3339))

	// regenerate lets gitops.Publish re-derive the README after a rebase
	// pulls in another machine's newly-pushed snapshot: without it, a
	// retried push would carry this machine's pre-rebase render, which
	// under-reports the now-merged totals (see gitops.Regenerate).
	regenerate := func() error {
		return mergeRenderInject(deps)
	}
	if err := gitops.Publish(ctx, deps.RepoDir, files, commitMessage, regenerate); err != nil {
		return fmt.Errorf("publishing: %w", err)
	}

	writeSuccessSummary(ctx, deps)
	return nil
}

// writeSuccessSummary writes a best-effort one-line confirmation to
// deps.Stdout after a successful publish: the merged headline summary plus
// the just-published commit's short hash. It never fails run() — the
// publish has already landed by this point, so a glitch summarizing it
// (e.g. a transient git failure resolving HEAD) degrades to a fallback
// line rather than making an otherwise-successful run report as an error.
func writeSuccessSummary(ctx context.Context, deps RunDeps) {
	if deps.Stdout == nil {
		return
	}
	merged, err := snapshot.Merge(deps.RepoDir)
	if err != nil {
		fmt.Fprintf(deps.Stdout, "published successfully, but computing the summary failed: %v\n", err)
		return
	}
	window := cmp.Or(deps.Config.TrailingWindow, config.DefaultTrailingWindow)
	sum := summary.Compute(merged, deps.Now, window)
	commit, err := headCommit(ctx, deps.RepoDir)
	if err != nil {
		fmt.Fprintf(deps.Stdout, "%s (resolving the published commit hash failed: %v)\n", render.Headline(sum), err)
		return
	}
	fmt.Fprintf(deps.Stdout, "%s — published as %s\n", render.Headline(sum), commit)
}

// headCommit resolves repoDir's current HEAD as a short hash, mirroring
// requireGitWorkTree's own os/exec invocation pattern in this file. Called
// only after gitops.Publish has already succeeded, so HEAD is the commit
// that was just pushed.
func headCommit(ctx context.Context, repoDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD")
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse --short HEAD: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// requireGitWorkTree verifies repoDir is a real, existing git working tree
// before Run or Init touch anything inside it — including the run-lock's
// own .token-profile directory (Fix 2) or a scaffolded README (Fix 3). A
// misconfigured targetRepo (a typo, a nonexistent path, or a plain
// directory that was never git-initialized) then fails fast with an
// actionable error, rather than leaving stray files behind before
// eventually failing deep inside gitops.Publish's own git invocations.
func requireGitWorkTree(ctx context.Context, repoDir string) error {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("target repo %q is not a git repository — check the targetRepo setting in your config: %w", repoDir, err)
	}
	if strings.TrimSpace(stdout.String()) != "true" {
		return fmt.Errorf("target repo %q is not a git repository — check the targetRepo setting in your config", repoDir)
	}
	return nil
}

// fenceCard wraps card in a plain (no language tag) fenced code block,
// matching README.md's own Quick Start example, so GitHub/CommonMark
// rendering preserves the card's box-drawing characters and column
// alignment verbatim instead of mangling them as inline markdown. Render
// itself stays fence-free (render_test.go's golden file and border-prefix
// assertions depend on Render's raw box() output) — fencing is strictly an
// injection-site concern.
//
// The attribution line (render.GeneratedByLine) is appended after the
// closing fence rather than inside card: CommonMark renders markdown syntax
// literally inside a fenced code block, so a markdown link only renders as
// an actual clickable link placed outside one.
func fenceCard(card string) string {
	return "```\n" + card + "\n```\n\n" + render.GeneratedByLine()
}

// collapsible wraps body in a <details> block so the card doesn't dominate
// a profile README by default, with summaryText as the always-visible
// toggle label. The blank lines around body are required, not cosmetic:
// GitHub's markdown parser only processes markdown (e.g. body's fenced
// code block) nested inside a raw HTML block when it's set off by blank
// lines — without them the fence renders as literal HTML text.
func collapsible(summaryText, body string) string {
	return "<details>\n<summary>" + summaryText + "</summary>\n\n" + body + "\n\n</details>"
}

// mergeRenderInject re-derives the merged dataset from every machine's
// snapshot currently on disk under deps.RepoDir, computes the summary,
// renders the dashboard card, and injects it into the target repo's
// README, writing the updated file back. Both Run's initial pass and
// gitops.Publish's post-rebase regenerate callback call this same helper,
// so a rebase that pulls in another machine's data always gets reflected
// in the README that's actually pushed.
//
// merged can now span far more than one window (Write accumulates history
// across runs — see internal/snapshot), so it's scoped down to the current
// window before rendering; summary.Compute takes the full merged dataset
// instead, since it needs the preceding window too for TokenChangePct/
// CostChangePct, and streak deliberately looks past the window entirely.
func mergeRenderInject(deps RunDeps) error {
	merged, err := snapshot.Merge(deps.RepoDir)
	if err != nil {
		return fmt.Errorf("merging snapshots: %w", err)
	}

	window := cmp.Or(deps.Config.TrailingWindow, config.DefaultTrailingWindow)
	current := snapshot.FilterSince(merged, deps.Now.Add(-window))
	breakdownLimit := cmp.Or(deps.Config.BreakdownLimit, config.DefaultBreakdownLimit)

	sum := summary.Compute(merged, deps.Now, window)
	card := render.Render(current, sum, deps.Config.Breakdown, breakdownLimit, deps.Now)

	readmePath := filepath.Join(deps.RepoDir, readmeFile)
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("reading README %s: %w", readmePath, err)
	}

	summaryText := render.CardTitle + " — " + render.Headline(sum)
	updated, err := readme.Inject(readmeBytes, collapsible(summaryText, fenceCard(card)))
	if err != nil {
		// readme.Inject's own error already wraps ErrMarkersMissing with
		// guidance to run `token-profile init`; wrapping again here only
		// adds which Run stage failed, preserving errors.Is(err,
		// readme.ErrMarkersMissing) for callers.
		return fmt.Errorf("injecting card into README: %w", err)
	}

	if err := os.WriteFile(readmePath, updated, 0o644); err != nil {
		return fmt.Errorf("writing README %s: %w", readmePath, err)
	}

	return nil
}

// snapshotRelPath returns machineID's snapshot directory path relative to a
// target repo's root, matching snapshot.Write's on-disk layout
// (.token-profile/snapshots/<machine-id>/<start-date>-<end-date>.json,
// chunked by calendar month). `git add` on a directory recursively stages
// every chunk file beneath it — including new ones — so this stays correct
// without tracking which specific chunk(s) a run touched. The snapshot
// package doesn't export this path (it's an internal file-layout detail),
// so it's reconstructed here for gitops.Publish, which needs repo-relative
// paths.
func snapshotRelPath(machineID string) string {
	return filepath.Join(".token-profile", "snapshots", machineID)
}

// toSnapshotRows converts agentsview.Row (the resolved usage dataset's row
// type) to snapshot.Row (the persisted snapshot's row type). The two types
// are structurally identical but intentionally distinct Go types (see
// internal/snapshot's package doc), so this is a plain explicit field copy
// rather than an embedding or generic trick for just two struct types.
func toSnapshotRows(rows []agentsview.Row) []snapshot.Row {
	out := make([]snapshot.Row, len(rows))
	for i, r := range rows {
		out[i] = snapshot.Row{
			Date:   r.Date,
			Agent:  r.Agent,
			Model:  r.Model,
			Tokens: r.Tokens,
			Cost:   r.Cost,
		}
	}
	return out
}

// sinceDate computes agentsview's --since cutoff date from now and window,
// or "" to omit --since entirely and defer to agentsview's own default
// trailing window (30 days, KTD10) when window is zero or negative.
func sinceDate(now time.Time, window time.Duration) string {
	if window <= 0 {
		return ""
	}
	return now.Add(-window).UTC().Format(time.DateOnly)
}

// NewRunCmd builds the `token-profile run` cobra command: a thin wrapper
// that loads the real config file, this machine's cached identity, and the
// system clock, then delegates the actual refresh flow to Run. Keeping
// this wiring separate from Run itself is what lets Run be exercised in
// tests without going through cobra command execution.
func NewRunCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Resolve local usage, merge machine snapshots, and publish the refreshed profile card",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("loading config %s: %w", configPath, err)
			}
			if cfg.TargetRepo == "" {
				return errTargetRepoMissing
			}

			machineID, err := machineid.Load(cfg.MachineIDPath)
			if err != nil {
				return fmt.Errorf("loading machine id: %w", err)
			}

			deps := RunDeps{
				Config:    cfg,
				Client:    &agentsview.Client{},
				MachineID: machineID,
				Now:       time.Now().UTC(),
				RepoDir:   cfg.TargetRepo,
				Stdout:    cmd.OutOrStdout(),
			}
			return Run(cmd.Context(), deps)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(), "path to token-profile's config file")
	return cmd
}

// defaultConfigPath returns the default config file location,
// ~/.token-profile/config.json.
func defaultConfigPath() string {
	return defaultStateFile("config.json")
}

// defaultStateFile returns name's path under token-profile's local state
// directory, ~/.token-profile, mirroring config.defaultMachineIDPath's own
// convention for where token-profile keeps its local state. Returns "" if
// the home directory can't be resolved, same as callers already handle.
func defaultStateFile(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".token-profile", name)
}
