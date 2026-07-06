package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/Christophe1997/token-profile/internal/agentsview"
	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/machineid"
	"github.com/Christophe1997/token-profile/internal/readme"
)

// launchdLabel identifies the scaffolded launchd job, mirroring the reverse-
// DNS convention launchd plists use for their Label key.
const launchdLabel = "dev.token-profile.refresh"

// errTargetRepoMissing is returned by both Init and NewInitCmd when
// Config.TargetRepo is unset — checked in both places so NewInitCmd can
// fail before machineid.Load's side effect (caching a machine ID to disk),
// while Init itself still fails fast for callers that skip the cobra layer
// entirely (notably init_test.go's error-path test).
var errTargetRepoMissing = errors.New(`no target repo configured: set "targetRepo" in your config file (see --config)`)

// InitDeps bundles the one-command setup flow's (F3) explicit dependencies:
// everything RunDeps needs to perform the first run, plus where to scaffold
// the scheduling entry and what command line it should invoke. Kept as
// plain fields for the same reason as RunDeps — unit-testable without going
// through cobra command execution.
type InitDeps struct {
	// Config supplies TargetRepo — checked fail-fast by Init itself, since
	// init_test.go needs to exercise that error path directly — plus the
	// same breakdown/window settings the first run consumes.
	Config config.Config
	// Client resolves this machine's local usage for the first run.
	Client *agentsview.Client
	// MachineID identifies this machine's snapshot file for the first run.
	MachineID string
	// Now stands in for "the current time" for the first run; see RunDeps.Now.
	Now time.Time
	// RepoDir is the target repo's working-copy path. Kept separate from
	// Config.TargetRepo so tests can point it at a scratch git fixture,
	// mirroring RunDeps.RepoDir.
	RepoDir string
	// ScheduleDest is where the scheduling entry snippet is written.
	// Injectable — rather than hardcoded to the real crontab/LaunchAgents
	// location — so tests scaffold and assert against a scratch path
	// without touching the real machine's schedule.
	ScheduleDest string
	// BinaryPath is the token-profile executable the scheduling entry
	// invokes.
	BinaryPath string
	// ConfigPath is the --config value the scheduling entry passes to
	// `token-profile run`.
	ConfigPath string
}

// Init performs one-command setup (R10, R11, F3): it scaffolds the
// target repo's README markers and a scheduling entry — both idempotent, so
// re-running Init against an already-initialized repo is safe — then
// delegates to run for the first commit.
//
// Config.TargetRepo == "" fails fast before touching anything, mirroring
// NewRunCmd's own guard; the check lives here (rather than only in
// NewInitCmd, as NewRunCmd's does for Run) so this error path is directly
// unit-testable without going through cobra.
//
// Like Run, Init validates deps.RepoDir is a git working tree and holds the
// run-lock for its whole duration (Fix 2, Fix 3): one acquisition covers
// both the scaffolding steps below and the first run, rather than calling
// the exported Run (which would try to acquire the same lock a second time
// and immediately self-conflict) — it calls the unlocked run core instead.
func Init(ctx context.Context, deps InitDeps) error {
	if deps.Config.TargetRepo == "" {
		return errTargetRepoMissing
	}

	if err := requireGitWorkTree(ctx, deps.RepoDir); err != nil {
		return err
	}

	release, err := acquireRunLock(deps.RepoDir)
	if err != nil {
		return err
	}
	defer release()

	if err := ensureReadmeMarkers(deps.RepoDir); err != nil {
		return fmt.Errorf("scaffolding README markers: %w", err)
	}

	if err := ensureSchedulingEntry(deps.ScheduleDest, runtime.GOOS, deps.BinaryPath, deps.ConfigPath); err != nil {
		return fmt.Errorf("scaffolding scheduling entry: %w", err)
	}

	return run(ctx, RunDeps{
		Config:    deps.Config,
		Client:    deps.Client,
		MachineID: deps.MachineID,
		Now:       deps.Now,
		RepoDir:   deps.RepoDir,
	})
}

// ensureReadmeMarkers idempotently scaffolds repoDir's README.md with the
// token-profile marker pair (KTD7): it creates a minimal README containing
// just the markers if none exists, appends the markers after any existing
// content if the README exists but lacks them, and no-ops (leaving the file
// byte-for-byte unchanged) if both markers are already present — so
// re-running init never duplicates the marker pair.
func ensureReadmeMarkers(repoDir string) error {
	path := filepath.Join(repoDir, readmeFile)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading README %s: %w", path, err)
	}

	if bytes.Contains(existing, []byte(readme.StartMarker)) && bytes.Contains(existing, []byte(readme.EndMarker)) {
		return nil
	}

	block := readme.StartMarker + "\n" + readme.EndMarker + "\n"
	updated := make([]byte, 0, len(existing)+len(block)+2)
	updated = append(updated, existing...)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		updated = append(updated, '\n')
	}
	if len(existing) > 0 {
		updated = append(updated, '\n') // blank line separating existing content from the scaffolded block
	}
	updated = append(updated, block...)

	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return fmt.Errorf("writing README %s: %w", path, err)
	}
	return nil
}

// ensureSchedulingEntry writes a platform-detected scheduling snippet to
// dest, describing how to run `token-profile run` on a recurring schedule.
// It overwrites dest deterministically on every call — rather than
// appending — so re-running init never duplicates the entry.
func ensureSchedulingEntry(dest, goos, binaryPath, configPath string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating scheduling entry directory for %s: %w", dest, err)
	}
	content := schedulingEntryContent(goos, binaryPath, configPath)
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing scheduling entry %s: %w", dest, err)
	}
	return nil
}

// schedulingEntryContent renders the scheduling snippet for goos: a launchd
// plist on darwin, or a crontab-line snippet everywhere else. Taking goos as
// a parameter (rather than reading runtime.GOOS internally) keeps this
// function pure and testable across both branches regardless of which OS
// the tests run on.
func schedulingEntryContent(goos, binaryPath, configPath string) string {
	if goos == "darwin" {
		return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>run</string>
		<string>--config</string>
		<string>%s</string>
	</array>
	<key>StartInterval</key>
	<integer>21600</integer>
</dict>
</plist>
`, launchdLabel, binaryPath, configPath)
	}

	return fmt.Sprintf(
		"# token-profile: refresh usage profile every 6 hours\n0 */6 * * * %s run --config %s\n",
		binaryPath, configPath,
	)
}

// defaultScheduleDest returns the default scheduling-entry snippet path,
// named by platform so an adopter recognizes which mechanism it targets:
// ~/.token-profile/schedule.plist on darwin, ~/.token-profile/schedule.cron
// elsewhere. This is a snippet to review and install (via `launchctl load`
// or `crontab`), not the live crontab/LaunchAgents location itself — init
// never edits an adopter's real schedule unattended.
func defaultScheduleDest() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	name := "schedule.cron"
	if runtime.GOOS == "darwin" {
		name = "schedule.plist"
	}
	return filepath.Join(home, ".token-profile", name)
}

// NewInitCmd builds the `token-profile init` cobra command: a thin wrapper
// that loads the real config file and this machine's cached identity, then
// delegates the actual scaffolding-plus-first-run flow to Init. Mirrors
// NewRunCmd's own wiring pattern.
func NewInitCmd() *cobra.Command {
	var configPath string
	var scheduleDest string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold README markers and a scheduling entry, then perform the first run",
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

			binaryPath, err := os.Executable()
			if err != nil {
				binaryPath = "token-profile"
			}

			deps := InitDeps{
				Config:       cfg,
				Client:       &agentsview.Client{},
				MachineID:    machineID,
				Now:          time.Now().UTC(),
				RepoDir:      cfg.TargetRepo,
				ScheduleDest: scheduleDest,
				BinaryPath:   binaryPath,
				ConfigPath:   configPath,
			}
			return Init(cmd.Context(), deps)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(), "path to token-profile's config file")
	cmd.Flags().StringVar(&scheduleDest, "schedule-dest", defaultScheduleDest(), "path to write the scheduling entry snippet")
	return cmd
}
