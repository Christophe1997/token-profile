// Package cli wires the U1-U6 building blocks (config, agentsview,
// snapshot, machineid, summary, render, readme, gitops) into
// token-profile's cobra subcommands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// MachineID identifies this machine's snapshot file within RepoDir.
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
}

// Run executes the end-to-end refresh flow: resolve this machine's usage,
// write its snapshot, merge every machine's snapshot in deps.RepoDir,
// compute the summary, render the dashboard card, inject it into the
// target repo's README, and publish both files to the repo's remote.
//
// Each stage's error is wrapped with enough context to identify which
// stage failed, since a run can fail at many different points.
func Run(ctx context.Context, deps RunDeps) error {
	since := sinceDate(deps.Now, deps.Config.TrailingWindow)
	dataset, err := deps.Client.Resolve(ctx, agentsview.ResolveOptions{Since: since})
	if err != nil {
		return fmt.Errorf("resolving usage: %w", err)
	}

	rows := toSnapshotRows(dataset.Rows)
	if err := snapshot.Write(deps.RepoDir, deps.MachineID, rows); err != nil {
		return fmt.Errorf("writing snapshot: %w", err)
	}

	merged, err := snapshot.Merge(deps.RepoDir)
	if err != nil {
		return fmt.Errorf("merging snapshots: %w", err)
	}

	sum := summary.Compute(merged, deps.Now)
	card := render.Render(merged, sum, deps.Config.Breakdown, deps.Now)

	readmePath := filepath.Join(deps.RepoDir, readmeFile)
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("reading README %s: %w", readmePath, err)
	}

	updated, err := readme.Inject(readmeBytes, card)
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

	files := []string{snapshotRelPath(deps.MachineID), readmeFile}
	commitMessage := fmt.Sprintf("chore(token-profile): refresh usage profile as of %s", deps.Now.UTC().Format(time.RFC3339))
	if err := gitops.Publish(ctx, deps.RepoDir, files, commitMessage); err != nil {
		return fmt.Errorf("publishing: %w", err)
	}

	return nil
}

// snapshotRelPath returns machineID's snapshot file path relative to a
// target repo's root, matching snapshot.Write's on-disk layout
// (.token-profile/snapshots/<machine-id>.json). The snapshot package
// doesn't export this path (it's an internal file-layout detail), so it's
// reconstructed here for gitops.Publish, which needs repo-relative paths.
func snapshotRelPath(machineID string) string {
	return filepath.Join(".token-profile", "snapshots", machineID+".json")
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
				return errors.New(`no target repo configured: set "targetRepo" in your config file (see --config)`)
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
			}
			return Run(cmd.Context(), deps)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(), "path to token-profile's config file")
	return cmd
}

// defaultConfigPath returns the default config file location,
// ~/.token-profile/config.json, mirroring config.defaultMachineIDPath's own
// convention for where token-profile keeps its local state.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".token-profile", "config.json")
}
