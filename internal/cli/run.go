// Package cli wires the U1-U6 building blocks (config, agentsview,
// snapshot, machineid, summary, render, readme, gitops) into
// token-profile's cobra subcommands.
package cli

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"html"
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

// svgLightRelPath and svgDarkRelPath are the light/dark dashboard-card SVG
// files' paths relative to a target repo's root (KTD8), sibling to
// snapshotRelPath's snapshots directory. Plain forward-slash string
// constants rather than filepath.Join like snapshotRelPath: unlike a
// snapshot directory (git-add-only), these paths also become the <picture>
// markup's srcset/src attribute values, which need "/" regardless of OS.
const (
	svgLightRelPath = ".token-profile/card-light.svg"
	svgDarkRelPath  = ".token-profile/card-dark.svg"
)

// resolveRenderMode returns mode's effective render mode: mode itself when
// explicitly set, or config.RenderModeSVG (matching config.Default's own
// choice, R5/R7) when mode is the zero value. Both run (deciding which SVG
// paths to commit) and mergeRenderInject (deciding which card to render)
// call this same helper so the two decisions can't drift apart — RunDeps
// built directly, e.g. in tests, without going through config.Load/Default
// otherwise carries a zero RenderMode.
func resolveRenderMode(mode config.RenderMode) config.RenderMode {
	return cmp.Or(mode, config.RenderModeSVG)
}

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
	// DryRun stops run before gitops.Publish's stage/commit/push (R7, R9):
	// usage resolution, the snapshot write, the card render, and the README
	// injection all still happen for real, leaving the working tree with
	// real, inspectable changes — only the commit and push are skipped, in
	// favor of a printed summary of what would have been committed.
	DryRun bool
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
	// svgFiles is also passed to gitops.Publish as its auto-resolve set: the
	// two are always regenerated together, so a rebase conflict confined to
	// them carries no information worth a manual resolution (see
	// gitops.Publish's autoResolvePaths doc).
	var svgFiles []string
	if resolveRenderMode(deps.Config.RenderMode) != config.RenderModeASCII {
		svgFiles = []string{svgLightRelPath, svgDarkRelPath}
		files = append(files, svgFiles...)
	}
	commitMessage := fmt.Sprintf("chore(token-profile): refresh usage profile as of %s", deps.Now.UTC().Format(time.RFC3339))

	if deps.DryRun {
		return printDryRunSummary(ctx, deps, files, commitMessage)
	}

	// regenerate lets gitops.Publish re-derive the README after a rebase
	// pulls in another machine's newly-pushed snapshot: without it, a
	// retried push would carry this machine's pre-rebase render, which
	// under-reports the now-merged totals (see gitops.Regenerate).
	regenerate := func() error {
		return mergeRenderInject(deps)
	}
	if err := gitops.Publish(ctx, deps.RepoDir, files, commitMessage, regenerate, svgFiles); err != nil {
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

// printDryRunSummary reports (R7's "printing a summary of what would have
// been committed") which of files currently carry uncommitted working-tree
// changes — exactly what a non-dry-run gitops.Publish would have staged and
// committed under commitMessage — without ever invoking `git add`, `commit`,
// or `push` (R9). A nil deps.Stdout still returns nil: the caller's contract
// is "no error, no publish", not "print or fail".
func printDryRunSummary(ctx context.Context, deps RunDeps, files []string, commitMessage string) error {
	changed, err := gitPorcelainStatus(ctx, deps.RepoDir, files)
	if err != nil {
		return fmt.Errorf("computing dry-run summary: %w", err)
	}
	if deps.Stdout == nil {
		return nil
	}
	if len(changed) == 0 {
		fmt.Fprintf(deps.Stdout, "dry run: nothing to publish — the working tree already matches commit %q, so it would have been a no-op\n", commitMessage)
		return nil
	}
	fmt.Fprintf(deps.Stdout, "dry run: stopped before committing/pushing; would commit %q, containing:\n", commitMessage)
	for _, line := range changed {
		fmt.Fprintf(deps.Stdout, "  %s\n", line)
	}
	return nil
}

// gitPorcelainStatus reports paths' working-tree status lines (`git status
// --porcelain`) scoped to paths — a read-only, TTY-free check shared by
// run.go's dry-run summary and cleanup.go's pre-confirmation footprint.
func gitPorcelainStatus(ctx context.Context, repoDir string, paths []string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"status", "--porcelain", "--"}, paths...)...)
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
// toggle label. open controls whether the section starts expanded (KTD6:
// the SVG card defaults open since it's the new default presentation; the
// ASCII opt-in stays closed, matching its pre-existing behavior). The blank
// lines around body are required, not cosmetic: GitHub's markdown parser
// only processes markdown (e.g. body's fenced code block) nested inside a
// raw HTML block when it's set off by blank lines — without them the fence
// renders as literal HTML text.
func collapsible(summaryText, body string, open bool) string {
	tag := "<details>"
	if open {
		tag = "<details open>"
	}
	return tag + "\n<summary>" + summaryText + "</summary>\n\n" + body + "\n\n</details>"
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
	hasHistory := len(merged.Rows) > 0
	breakdownLimit := cmp.Or(deps.Config.BreakdownLimit, config.DefaultBreakdownLimit)

	sum := summary.Compute(merged, deps.Now, window)

	readmePath := filepath.Join(deps.RepoDir, readmeFile)
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("reading README %s: %w", readmePath, err)
	}

	var body string
	var open bool
	switch resolveRenderMode(deps.Config.RenderMode) {
	case config.RenderModeASCII:
		card := render.Render(current, sum, deps.Config.Breakdown, breakdownLimit, deps.Now)
		body = fenceCard(card)
	default: // config.RenderModeSVG
		body, err = svgCardBody(deps, current, hasHistory, sum, breakdownLimit)
		if err != nil {
			return err
		}
		open = true // KTD6: the new default presentation starts expanded
	}

	summaryText := render.CardTitle + " — " + render.Headline(sum)
	updated, err := readme.Inject(readmeBytes, collapsible(summaryText, body, open))
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

// svgCardBody renders the SVG dashboard card's light/dark variants, writes
// them under deps.RepoDir at svgLightRelPath/svgDarkRelPath (KTD8), and
// returns the <picture>+attribution markup that becomes fenceCard's
// SVG-mode counterpart. The attribution line sits after a blank line,
// outside the <picture> block, matching fenceCard's own placement of
// render.GeneratedByLine() outside the ASCII card's fence (R2).
func svgCardBody(deps RunDeps, current snapshot.MergedDataset, hasHistory bool, sum summary.Summary, breakdownLimit int) (string, error) {
	light, dark, err := render.RenderSVG(current, hasHistory, sum, deps.Config.Breakdown, breakdownLimit, deps.Now)
	if err != nil {
		return "", fmt.Errorf("rendering SVG card: %w", err)
	}

	if err := writeCardFile(deps.RepoDir, svgLightRelPath, light); err != nil {
		return "", err
	}
	if err := writeCardFile(deps.RepoDir, svgDarkRelPath, dark); err != nil {
		return "", err
	}

	alt := html.EscapeString(render.AltText(current, hasHistory, sum))
	picture := fmt.Sprintf(
		`<picture><source media="(prefers-color-scheme: dark)" srcset="%s"><img src="%s" alt="%s" width="100%%"></picture>`,
		svgDarkRelPath, svgLightRelPath, alt,
	)
	return picture + "\n\n" + render.GeneratedByLine(), nil
}

// writeCardFile writes content to relPath under repoDir, creating any
// missing parent directories first: mergeRenderInject can run before
// .token-profile/ exists on disk (a machine's very first run), so this
// can't assume the directory snapshot.Write happens to also use is already
// there.
func writeCardFile(repoDir, relPath, content string) error {
	path := filepath.Join(repoDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", relPath, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", relPath, err)
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
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Resolve local usage, merge machine snapshots, and publish the refreshed profile card",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireConfigOrTTY(configPath, isInteractive(os.Stdin)); err != nil {
				return err
			}

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
				DryRun:    dryRun,
			}
			return Run(cmd.Context(), deps)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(), "path to token-profile's config file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "perform every write but stop before committing/pushing, printing a summary instead")
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
