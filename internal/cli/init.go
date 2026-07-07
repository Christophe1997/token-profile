package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	// Stdout receives the same post-publish confirmation as RunDeps.Stdout
	// — propagated into the RunDeps Init builds internally below, so init
	// gets this output through the same shared run() core, no separate
	// implementation.
	Stdout io.Writer
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
		Stdout:    deps.Stdout,
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
	name := "schedule.cron"
	if runtime.GOOS == "darwin" {
		name = "schedule.plist"
	}
	return defaultStateFile(name)
}

// configFileExists reports whether a config file already sits at path,
// treating any stat error other than "not exist" as "exists" —
// loadOrScaffoldConfig and bootstrapConfig share this conservative rule, so
// neither ever scaffolds over a file it merely couldn't read (e.g.
// permission denied).
func configFileExists(path string) bool {
	_, statErr := os.Stat(path)
	return !errors.Is(statErr, os.ErrNotExist)
}

// loadOrScaffoldConfig loads the config file at configPath for `init`,
// distinguishing "no config file exists yet" (first-time adopter) from "a
// config file exists but targetRepo is simply blank" (mis-set config): only
// the former scaffolds a starter template and returns a guided error; the
// latter returns the loaded config as-is (with TargetRepo == ""), so the
// caller's existing errTargetRepoMissing check applies unchanged.
func loadOrScaffoldConfig(configPath string) (config.Config, error) {
	configExists := configFileExists(configPath)

	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, err
	}

	if cfg.TargetRepo == "" && !configExists {
		if err := config.WriteTemplate(configPath, config.TemplateFields{}); err != nil {
			return config.Config{}, fmt.Errorf("scaffolding starter config: %w", err)
		}
		return config.Config{}, fmt.Errorf(
			`created a starter config at %s — edit it to set "targetRepo", then re-run "token-profile init"`,
			configPath,
		)
	}

	return cfg, nil
}

const (
	cloneProtocolSSH   = "ssh"
	cloneProtocolHTTPS = "https"
)

// isInteractive reports whether r is a real terminal — so the auto-clone
// shortcut only offers itself at an interactive session, never during a
// scheduled cron/launchd invocation (which has no TTY to prompt on). Only a
// concrete *os.File character device counts; any other io.Reader (every
// test fixture included) is non-interactive.
func isInteractive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// gitGlobalUserName resolves the operator's global git user.name — a guess
// at their GitHub handle, per GitHub's own username/username profile-repo
// convention (see README.md). Returns "" (not an error) if unset or git is
// unavailable, since that just means the auto-clone shortcut doesn't apply.
func gitGlobalUserName(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "git", "config", "--global", "user.name")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

// validAutoCloneName reports whether name is safe as a single path
// component in filepath.Join, mirroring snapshot.validateMachineID's own
// guard for this same class of untrusted-shaped local input. "." is
// rejected explicitly — unlike the machineID case, whose ".json" suffix
// already disambiguates it, this destination has no such suffix, so a bare
// "." would collapse to the containing directory itself.
func validAutoCloneName(name string) bool {
	if name == "" || name == "." {
		return false
	}
	return !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..")
}

// profileRepoURL constructs the guessed GitHub profile-repo URL for
// name/name under protocol's scheme.
func profileRepoURL(protocol, name string) (string, error) {
	switch protocol {
	case cloneProtocolSSH:
		return fmt.Sprintf("git@github.com:%s/%s.git", name, name), nil
	case cloneProtocolHTTPS:
		return fmt.Sprintf("https://github.com/%s/%s.git", name, name), nil
	default:
		return "", fmt.Errorf("invalid --clone-protocol %q (want %q or %q)", protocol, cloneProtocolSSH, cloneProtocolHTTPS)
	}
}

// cloneProfileRepo clones url into dest via a plain `git clone`, creating
// dest's parent directory first — mirroring ensureSchedulingEntry's own
// os.MkdirAll-before-write convention — since ~/.token-profile/repos may
// not exist yet on a machine running `init` for the first time.
func cloneProfileRepo(ctx context.Context, url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating clone destination directory for %s: %w", dest, err)
	}
	cmd := exec.CommandContext(ctx, "git", "clone", url, dest)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s %s: %w: %s", url, dest, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// confirmAutoClone prompts once via stdout/stdin, reading a single line of
// free-form confirmation. Any answer other than a case-insensitive "y" or
// "yes" — including no input at all — is "no": auto-clone is an optional
// shortcut that must never surprise the operator on an ambiguous read.
// bufio.Scanner (rather than fmt.Fscanln) reads exactly one line regardless
// of a trailing newline's presence, and never errors on an empty line.
func confirmAutoClone(stdin io.Reader, stdout io.Writer, url, dest string) bool {
	fmt.Fprintf(stdout, "No config found. Clone %s into %s and use it as your target repo? [y/N] ", url, dest)
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

// bootstrapDeps bundles bootstrapConfig's dependencies as a struct, unlike
// this file's plain-multi-arg helpers (loadOrScaffoldConfig,
// ensureSchedulingEntry): at 7 heterogeneous fields — including two
// same-shaped io values (Stdin/Stdout) and two func values
// (GitUserName/Clone) — plain positional args would invite mixups, the same
// rationale RunDeps/InitDeps document for themselves.
type bootstrapDeps struct {
	ConfigPath    string
	Interactive   bool
	Stdin         io.Reader
	Stdout        io.Writer
	CloneProtocol string
	// GitUserName resolves the operator's git global user.name. Injected so
	// tests don't depend on (or mutate) the real machine's global git
	// config.
	GitUserName func(ctx context.Context) string
	// Clone clones url into dest. Injected so tests can exercise both the
	// success and failure paths without a real network call to github.com.
	Clone func(ctx context.Context, url, dest string) error
}

// bootstrapConfig loads configPath for `init`, offering the interactive
// auto-clone shortcut first when it applies: no config file exists yet,
// deps.Interactive is true, and deps.GitUserName resolves to a path-safe
// name. Any ineligible, declined, or failed step falls back to
// loadOrScaffoldConfig's existing scaffold-and-guide behavior unchanged.
func bootstrapConfig(ctx context.Context, deps bootstrapDeps) (config.Config, error) {
	if !deps.Interactive || configFileExists(deps.ConfigPath) {
		return loadOrScaffoldConfig(deps.ConfigPath)
	}

	name := deps.GitUserName(ctx)
	if !validAutoCloneName(name) {
		return loadOrScaffoldConfig(deps.ConfigPath)
	}

	url, err := profileRepoURL(deps.CloneProtocol, name)
	if err != nil {
		return config.Config{}, err
	}
	dest := defaultStateFile(filepath.Join("repos", name))

	if !confirmAutoClone(deps.Stdin, deps.Stdout, url, dest) {
		return loadOrScaffoldConfig(deps.ConfigPath)
	}

	if err := deps.Clone(ctx, url, dest); err != nil {
		fmt.Fprintf(deps.Stdout, "auto-clone failed (%v); falling back to a starter config template\n", err)
		return loadOrScaffoldConfig(deps.ConfigPath)
	}

	if err := config.WriteTemplate(deps.ConfigPath, config.TemplateFields{TargetRepo: dest}); err != nil {
		return config.Config{}, fmt.Errorf("writing config after auto-clone: %w", err)
	}
	return config.Load(deps.ConfigPath)
}

// NewInitCmd builds the `token-profile init` cobra command: a thin wrapper
// that loads the real config file and this machine's cached identity, then
// delegates the actual scaffolding-plus-first-run flow to Init. Mirrors
// NewRunCmd's own wiring pattern.
func NewInitCmd() *cobra.Command {
	var configPath string
	var scheduleDest string
	var cloneProtocol string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold README markers and a scheduling entry, then perform the first run",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := bootstrapConfig(cmd.Context(), bootstrapDeps{
				ConfigPath:    configPath,
				Interactive:   isInteractive(os.Stdin),
				Stdin:         os.Stdin,
				Stdout:        cmd.OutOrStdout(),
				CloneProtocol: cloneProtocol,
				GitUserName:   gitGlobalUserName,
				Clone:         cloneProfileRepo,
			})
			if err != nil {
				return err
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
				Stdout:       cmd.OutOrStdout(),
			}
			return Init(cmd.Context(), deps)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(), "path to token-profile's config file")
	cmd.Flags().StringVar(&scheduleDest, "schedule-dest", defaultScheduleDest(), "path to write the scheduling entry snippet")
	cmd.Flags().StringVar(&cloneProtocol, "clone-protocol", cloneProtocolHTTPS, `protocol for the interactive auto-clone shortcut's git URL ("ssh" or "https")`)
	return cmd
}
