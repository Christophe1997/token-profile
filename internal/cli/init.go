package cli

import (
	"bufio"
	"bytes"
	"cmp"
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
	// Stdin is the post-init schedule-registration prompt's input source
	// (R4) — read via confirmYesNo's bufio-scanner y/N pattern.
	Stdin io.Reader
	// PromptSchedule gates whether the schedule-registration prompt is
	// shown at all. Resolved once by NewInitCmd via isInteractive(os.Stdin)
	// rather than Init re-deriving it from Stdin's own concrete type, so
	// tests can drive the prompt through a plain strings.Reader — isInteractive
	// would otherwise always report false for such a fixture.
	PromptSchedule bool
	// Schedule carries the live schedule-registration's install-time
	// parameters not already covered above: PlistPath (the real LaunchAgents
	// location InstallSchedule targets, darwin only) plus Launchctl/Crontab
	// overrides for tests. Label, BinaryPath, ConfigPath, and Interval are
	// filled in by offerScheduleRegistration itself.
	Schedule ScheduleDeps
	// DryRun propagates into the RunDeps Init builds internally, stopping
	// the first run before commit/push (R7-R9), and additionally skips the
	// schedule-registration prompt (R4) entirely (R8, AE2) — registering a
	// live schedule is exactly the kind of non-reversible step a dry run
	// must never reach.
	DryRun bool
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
// run-lock for the scaffolding steps and the first run (Fix 2, Fix 3), via
// initLocked, rather than calling the exported Run (which would try to
// acquire the same lock a second time and immediately self-conflict) — it
// calls the unlocked run core instead.
//
// The lock is released before the schedule-registration prompt that
// follows, not held through it: that prompt can block indefinitely on real
// user input, and holding the lock across an unbounded wait would starve a
// concurrently-scheduled `run` for as long as the adopter takes to answer
// (mirroring Cleanup's own confirm-before-lock ordering).
func Init(ctx context.Context, deps InitDeps) error {
	if deps.Config.TargetRepo == "" {
		return errTargetRepoMissing
	}

	if err := requireGitWorkTree(ctx, deps.RepoDir); err != nil {
		return err
	}

	interval := cmp.Or(deps.Config.ScheduleInterval, config.DefaultScheduleInterval)

	dryRun, err := initLocked(ctx, deps, interval)
	if err != nil {
		return err
	}
	if dryRun {
		return nil
	}

	return offerScheduleRegistration(ctx, deps, interval)
}

// initLocked performs every step that must happen under the run-lock:
// scaffolding README markers and the scheduling-entry snippet, then the
// first run itself. The lock is released as soon as this returns.
func initLocked(ctx context.Context, deps InitDeps, interval time.Duration) (dryRun bool, err error) {
	release, err := acquireRunLock(deps.RepoDir)
	if err != nil {
		return false, err
	}
	defer release()

	if err := ensureReadmeMarkers(deps.RepoDir); err != nil {
		return false, fmt.Errorf("scaffolding README markers: %w", err)
	}

	if err := ensureSchedulingEntry(deps.ScheduleDest, runtime.GOOS, deps.BinaryPath, deps.ConfigPath, interval); err != nil {
		return false, fmt.Errorf("scaffolding scheduling entry: %w", err)
	}

	if err := run(ctx, RunDeps{
		Config:    deps.Config,
		Client:    deps.Client,
		MachineID: deps.MachineID,
		Now:       deps.Now,
		RepoDir:   deps.RepoDir,
		Stdout:    deps.Stdout,
		DryRun:    deps.DryRun,
	}); err != nil {
		return false, err
	}

	return deps.DryRun, nil
}

// offerScheduleRegistration prompts (R4) whether to register the refresh
// schedule after a successful init, installing it via InstallSchedule on
// yes. A failed install attempt degrades to a warning rather than a
// non-zero exit (KTD17): by this point clone, config, and the first publish
// have already succeeded, so scheduling is best-effort auxiliary setup the
// adopter can retry, or install manually from the already-written
// --schedule-dest snippet.
func offerScheduleRegistration(ctx context.Context, deps InitDeps, interval time.Duration) error {
	if !deps.PromptSchedule {
		return nil
	}
	if !confirmYesNo(deps.Stdin, deps.Stdout, "Register the refresh schedule now?") {
		return nil
	}

	sched := deps.Schedule
	sched.Label = launchdLabel
	sched.BinaryPath = deps.BinaryPath
	sched.ConfigPath = deps.ConfigPath
	sched.Interval = interval

	if err := refuseIfPrivileged(); err != nil {
		fmt.Fprintf(deps.Stdout,
			"warning: %v (the snippet at %s is still available to install manually)\n",
			err, deps.ScheduleDest)
		return nil
	}

	if _, err := InstallSchedule(ctx, sched); err != nil {
		fmt.Fprintf(deps.Stdout,
			"warning: failed to register the refresh schedule: %v (the snippet at %s is still available to install manually)\n",
			err, deps.ScheduleDest)
	}
	return nil
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
func ensureSchedulingEntry(dest, goos, binaryPath, configPath string, interval time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating scheduling entry directory for %s: %w", dest, err)
	}
	content := schedulingEntryContent(goos, binaryPath, configPath, interval)
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing scheduling entry %s: %w", dest, err)
	}
	return nil
}

// schedulingEntryContent renders the scheduling snippet for goos: a launchd
// plist on darwin, or a crontab-line snippet everywhere else, driven by the
// configured refresh cadence (interval) rather than the old hardcoded
// 6-hour cycle (KTD10 supersedes the previous 21600/"0 */6 * * *"
// constants). The darwin branch delegates to schedule.go's own launchdPlist
// so the live-install path (offerScheduleRegistration) and this reviewable
// snippet render from one template and can never drift apart. A zero
// interval — an InitDeps literal built directly by a test, bypassing
// config.Load's Default() layering — falls back to
// config.DefaultScheduleInterval, mirroring resolveRenderMode's own
// zero-value-safe default (run.go). Taking goos as a parameter (rather than
// reading runtime.GOOS internally) keeps this function pure and testable
// across both branches regardless of which OS the tests run on.
func schedulingEntryContent(goos, binaryPath, configPath string, interval time.Duration) string {
	interval = cmp.Or(interval, config.DefaultScheduleInterval)

	if goos == "darwin" {
		return launchdPlist(ScheduleDeps{
			Label:      launchdLabel,
			BinaryPath: binaryPath,
			ConfigPath: configPath,
			Interval:   interval,
		})
	}

	return fmt.Sprintf(
		"# token-profile: refresh usage profile every %d hours\n%s\n",
		int(interval.Hours()), cronJobLine(interval, binaryPath, configPath),
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

// defaultLaunchdPlistPath returns where InstallSchedule/RemoveSchedule
// write and target the live-registered launchd job's plist on darwin —
// distinct from defaultScheduleDest's reviewable snippet path (KTD14): this
// is the file launchctl bootstrap actually loads, following the standard
// per-user LaunchAgents convention.
func defaultLaunchdPlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

// configFileExists reports whether a config file already sits at path,
// treating any stat error other than "not exist" as "exists" —
// resolveInitConfig and requireConfigOrTTY share this conservative rule, so
// neither ever re-triggers the wizard or scaffolds over a file it merely
// couldn't read (e.g. permission denied).
func configFileExists(path string) bool {
	_, statErr := os.Stat(path)
	return !errors.Is(statErr, os.ErrNotExist)
}

// errNoConfigNoTTY is R5/AE1's fail-fast error: a config-needing command
// (`run`, or the guided-init entry point resolveInitConfig) invoked with no
// config file yet and no interactive terminal to create one via the wizard
// fails immediately, naming the missing path and pointing at interactive
// `init` — never silently scaffolding a default config (R5).
func errNoConfigNoTTY(configPath string) error {
	return fmt.Errorf(
		`no config file found at %s and no interactive terminal available — run "token-profile init" interactively to create one`,
		configPath,
	)
}

// requireConfigOrTTY is NewRunCmd's own R5/AE1 gate, factored out so it's
// testable without depending on the real process's os.Stdin (whose
// TTY-ness a plain `go test` run can't deterministically control) — mirrors
// resolveInitConfig's identical fail-fast rule for the init path.
func requireConfigOrTTY(configPath string, interactive bool) error {
	if configFileExists(configPath) || interactive {
		return nil
	}
	return errNoConfigNoTTY(configPath)
}

// isInteractive reports whether r is a real terminal — so the guided setup
// wizard and schedule-registration prompt only offer themselves at an
// interactive session, never during a scheduled cron/launchd invocation
// (which has no TTY to prompt on). Only a concrete *os.File character
// device counts; any other io.Reader (every test fixture included) is
// non-interactive.
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
// unavailable, since that just means the wizard's pre-filled defaults stay
// blank (see RunWizard).
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
// name/name under protocol's scheme (the username/username convention).
func profileRepoURL(protocol config.CloneProtocol, name string) (string, error) {
	switch protocol {
	case config.CloneProtocolSSH:
		return fmt.Sprintf("git@github.com:%s/%s.git", name, name), nil
	case config.CloneProtocolHTTPS:
		return fmt.Sprintf("https://github.com/%s/%s.git", name, name), nil
	default:
		return "", fmt.Errorf("invalid clone protocol %q (want %q or %q)", protocol, config.CloneProtocolHTTPS, config.CloneProtocolSSH)
	}
}

// confirmYesNo prompts once via stdout/stdin, reading a single line of
// free-form confirmation. Any answer other than a case-insensitive "y" or
// "yes" — including no input at all, or a nil stdin — is "no": every prompt
// this backs (the old auto-clone shortcut, now the post-init
// schedule-registration offer) is optional and must never surprise the
// operator on an ambiguous read. bufio.Scanner (rather than fmt.Fscanln)
// reads exactly one line regardless of a trailing newline's presence, and
// never errors on an empty line.
func confirmYesNo(stdin io.Reader, stdout io.Writer, prompt string) bool {
	fmt.Fprintf(stdout, "%s [y/N] ", prompt)
	if stdin == nil {
		return false
	}
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

// initConfigDeps bundles resolveInitConfig's dependencies: whether a config
// file already exists at ConfigPath decides everything else (R1) — Wizard,
// Stdout, and ResolveCloneURL are only consulted on a fresh machine, where
// no config file exists yet.
type initConfigDeps struct {
	// ConfigPath is where the resolved config is loaded from, or scaffolded
	// to on a fresh machine.
	ConfigPath string
	// Interactive reports whether a real interactive terminal is present —
	// resolved by the caller via isInteractive(os.Stdin), decoupling "is
	// this session interactive" from Wizard.Input's own concrete type so
	// tests can drive the wizard through a plain strings.Reader while still
	// exercising the guided-setup path (isInteractive itself would always
	// report false for such a fixture).
	Interactive bool
	// Wizard collects the three clone-related fields (R2) when no config
	// file exists yet and Interactive is true.
	Wizard WizardDeps
	// Stdout receives the clone step's one-line status (KTD4) — cloned
	// fresh, adopted existing, or (via the returned error) failed.
	Stdout io.Writer
	// ResolveCloneURL constructs the remote URL to clone from the wizard's
	// chosen protocol/repo name. Defaults to profileRepoURL (the real
	// GitHub username/username convention) when nil; tests override it to
	// target a local bare-repo fixture instead of a real network call,
	// matching this repo's real-git-fixture testing convention.
	ResolveCloneURL func(protocol config.CloneProtocol, repoName string) (string, error)
}

// resolveInitConfig resolves the config `init` should run with (R1, R3): an
// already-existing config file is loaded as-is, the wizard never invoked
// (AE6 — even a targetRepo that turns out to be missing or not a git
// repository is Init's own requireGitWorkTree check to catch, not a reason
// to re-run the wizard). Only a genuinely fresh machine (no config file
// yet) triggers the guided wizard flow: collect the three fields, clone or
// adopt the repo (U2), and write a full starter config (U1) before
// reloading it. No interactive terminal and no config file is R5/AE1's
// fail-fast case — nothing is ever written silently.
func resolveInitConfig(ctx context.Context, deps initConfigDeps) (config.Config, error) {
	if configFileExists(deps.ConfigPath) {
		return config.Load(deps.ConfigPath)
	}
	if !deps.Interactive {
		return config.Config{}, errNoConfigNoTTY(deps.ConfigPath)
	}

	result, err := RunWizard(ctx, deps.Wizard)
	if err != nil {
		return config.Config{}, err
	}

	resolveURL := deps.ResolveCloneURL
	if resolveURL == nil {
		resolveURL = profileRepoURL
	}
	url, err := resolveURL(result.CloneProtocol, result.RepoName)
	if err != nil {
		return config.Config{}, err
	}

	status, err := cloneOrAdopt(ctx, url, result.LocalPath)
	if err != nil {
		return config.Config{}, err
	}
	if deps.Stdout != nil {
		fmt.Fprintln(deps.Stdout, status)
	}

	if err := config.WriteTemplate(deps.ConfigPath, config.TemplateFields{
		TargetRepo:    result.LocalPath,
		RemoteRepo:    url,
		CloneProtocol: result.CloneProtocol,
	}); err != nil {
		return config.Config{}, fmt.Errorf("writing config after guided setup: %w", err)
	}

	return config.Load(deps.ConfigPath)
}

// NewInitCmd builds the `token-profile init` cobra command: a thin wrapper
// that resolves the config (loading it as-is, or running the guided wizard
// on a fresh machine — resolveInitConfig) and this machine's cached
// identity, then delegates the actual scaffolding-plus-first-run flow to
// Init. Mirrors NewRunCmd's own wiring pattern.
func NewInitCmd() *cobra.Command {
	var configPath string
	var scheduleDest string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Guide first-time setup (clone, config, schedule), then perform the first run",
		RunE: func(cmd *cobra.Command, args []string) error {
			interactive := isInteractive(os.Stdin)

			cfg, err := resolveInitConfig(cmd.Context(), initConfigDeps{
				ConfigPath:  configPath,
				Interactive: interactive,
				Wizard: WizardDeps{
					GitUserName: gitGlobalUserName,
				},
				Stdout: cmd.OutOrStdout(),
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
				Config:         cfg,
				Client:         &agentsview.Client{},
				MachineID:      machineID,
				Now:            time.Now().UTC(),
				RepoDir:        cfg.TargetRepo,
				ScheduleDest:   scheduleDest,
				BinaryPath:     binaryPath,
				ConfigPath:     configPath,
				Stdout:         cmd.OutOrStdout(),
				Stdin:          os.Stdin,
				PromptSchedule: interactive,
				Schedule: ScheduleDeps{
					PlistPath: defaultLaunchdPlistPath(),
				},
				DryRun: dryRun,
			}
			return Init(cmd.Context(), deps)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath(), "path to token-profile's config file")
	cmd.Flags().StringVar(&scheduleDest, "schedule-dest", defaultScheduleDest(), "path to write the scheduling entry snippet")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "perform every write (clone, config, README) but stop before committing/pushing, and skip the schedule-registration prompt")
	return cmd
}
