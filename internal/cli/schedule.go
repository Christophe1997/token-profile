package cli

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ScheduleState is the live registration state of token-profile's scheduled
// run, as resolved by CheckScheduleState (KTD7): three distinct outcomes,
// never collapsing a check failure into "not registered" (AE4).
type ScheduleState int

const (
	ScheduleNotRegistered ScheduleState = iota
	ScheduleRegistered
	ScheduleCheckFailed
)

func (s ScheduleState) String() string {
	switch s {
	case ScheduleRegistered:
		return "registered"
	case ScheduleCheckFailed:
		return "check failed"
	default:
		return "not registered"
	}
}

// ScheduleDeps bundles the parameters CheckScheduleState, InstallSchedule,
// and RemoveSchedule need against either mechanism (launchd on darwin,
// cron elsewhere). GOOS/Launchctl/Crontab are overridable — mirroring
// agentsview.Client.BinaryName's "resolved by exec.LookPath, defaults to
// the bare name" convention — so tests target fixture scripts regardless
// of which OS actually runs the test.
type ScheduleDeps struct {
	// GOOS selects the darwin (launchctl) vs. other (crontab) mechanism.
	// Empty defaults to runtime.GOOS.
	GOOS string
	// Label uniquely identifies the scheduled entry: the launchd plist's
	// Label key on darwin, or a marker comment tagging the crontab entry
	// elsewhere. Mirrors launchdLabel (init.go).
	Label string
	// BinaryPath is the token-profile executable the scheduled entry
	// invokes. Only read by InstallSchedule.
	BinaryPath string
	// ConfigPath is the --config value the scheduled entry passes to
	// `run`. Only read by InstallSchedule.
	ConfigPath string
	// Interval is the scheduled-run cadence. Assumed already validated
	// against config.Config's divisor set (KTD10) by the time it reaches
	// here — this package never re-validates it. Only read by
	// InstallSchedule.
	Interval time.Duration
	// PlistPath is where InstallSchedule writes the launchd job's plist
	// before bootstrapping it (darwin only).
	PlistPath string
	// Launchctl overrides the launchctl executable resolved via
	// exec.LookPath. Empty defaults to "launchctl".
	Launchctl string
	// Crontab overrides the crontab executable resolved via
	// exec.LookPath. Empty defaults to "crontab".
	Crontab string
}

func (d ScheduleDeps) goos() string {
	return cmp.Or(d.GOOS, runtime.GOOS)
}

// launchctlDomain is the invoking user's own launchd GUI domain
// (KTD16) — os.Getuid() rather than shelling out to `id -u`, since Go
// already exposes it natively. Install/remove target this domain
// exclusively: never `sudo`, never the system domain, since running as
// root breaks gui/<uid>'s binding to the invoking user's own login
// session (confirmed firsthand — see the plan's Problem Frame).
func launchctlDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// geteuid resolves the process's effective UID — os.Geteuid by default,
// overridable in tests so refuseIfPrivileged can be exercised without
// requiring the test suite itself to run as root.
var geteuid = os.Geteuid

// errRunningAsRoot signals a schedule mutation attempted under an
// effective UID of 0 (typically `sudo token-profile ...`). launchd's
// gui/<uid> domain (KTD16, see launchctlDomain) and a user's own crontab
// are both scoped to a real invoking user, not root — running as root
// would silently target a nonexistent gui/0 session or root's own
// (wrong) crontab instead of doing what the adopter actually asked for.
var errRunningAsRoot = errors.New(
	"refusing to register/deregister the schedule while running as root (e.g. via sudo) — re-run without sudo so it targets your own login session and crontab, not root's",
)

// refuseIfPrivileged guards every schedule-mutation entry point
// (offerScheduleRegistration, deregisterSchedule) against running under
// sudo/root — launchctlDomain's own doc comment says this must never
// happen, but nothing previously enforced it.
func refuseIfPrivileged() error {
	if geteuid() == 0 {
		return errRunningAsRoot
	}
	return nil
}

func launchctlServiceTarget(label string) string {
	return launchctlDomain() + "/" + label
}

// scheduleIntervalSeconds converts interval to launchd's StartInterval
// unit (raw seconds).
func scheduleIntervalSeconds(interval time.Duration) int {
	return int(interval.Seconds())
}

// scheduleCronField converts interval to cron's hour-field expression
// (KTD10): interval is assumed already validated against the hourly
// divisor set, so this is always exact (e.g. 4h -> "*/4").
func scheduleCronField(interval time.Duration) string {
	return fmt.Sprintf("*/%d", int(interval.Hours()))
}

// cronMarker tags the managed crontab entry so it can be found and removed
// later without disturbing any other entry already in the user's crontab.
func cronMarker(label string) string {
	return "# token-profile:" + label
}

// checkScheduleState resolves live registration state and, for the cron
// mechanism, returns the crontab content already fetched to determine it —
// letting install/remove reuse that content instead of invoking `crontab -l`
// a second time. The darwin mechanism has no equivalent content to reuse
// (checkLaunchd's launchctl print is the single source of truth), so its
// content return is always empty.
func checkScheduleState(ctx context.Context, deps ScheduleDeps) (ScheduleState, string, error) {
	if deps.goos() == "darwin" {
		state, err := checkLaunchd(ctx, deps)
		return state, "", err
	}
	return checkCronState(ctx, deps)
}

// CheckScheduleState resolves the live registration state of the scheduled
// run: registered, not registered, or check failed (KTD7) — the last one
// distinct from "not registered" so a real failure (e.g. launchd
// unreachable, crontab misconfigured) is never silently treated as
// "nothing to remove"/"safe to install" (AE4).
func CheckScheduleState(ctx context.Context, deps ScheduleDeps) (ScheduleState, error) {
	state, _, err := checkScheduleState(ctx, deps)
	return state, err
}

// InstallSchedule idempotently registers the scheduled run: a state check
// runs first, so an already-registered job is a no-op that still reports
// success (KTD13) rather than failing on launchctl bootstrap's "already
// loaded" error. A failed state check is propagated rather than risking a
// blind install against unknown live state.
func InstallSchedule(ctx context.Context, deps ScheduleDeps) (ScheduleState, error) {
	state, content, err := checkScheduleState(ctx, deps)
	if err != nil {
		return state, err
	}
	if state == ScheduleRegistered {
		return state, nil
	}
	if deps.goos() == "darwin" {
		return installLaunchd(ctx, deps)
	}
	return installCron(ctx, deps, content)
}

// RemoveSchedule idempotently deregisters the scheduled run: a state check
// runs first, so "nothing registered" is a no-op reporting
// ScheduleNotRegistered without error (R11) rather than failing on
// launchctl bootout's "not found" error. A failed state check is
// propagated rather than risking a blind removal against unknown live
// state.
func RemoveSchedule(ctx context.Context, deps ScheduleDeps) (ScheduleState, error) {
	state, content, err := checkScheduleState(ctx, deps)
	if err != nil {
		return state, err
	}
	return removeGivenState(ctx, deps, state, content)
}

// removeGivenState performs the removal implied by a state (and, for the
// cron path, crontab content) the caller already resolved — shared by
// RemoveSchedule and cleanup.go's deregisterSchedule so a caller that
// already knows the live state doesn't force a second launchctl/crontab
// round-trip just to re-derive what it already has.
func removeGivenState(ctx context.Context, deps ScheduleDeps, state ScheduleState, cronContent string) (ScheduleState, error) {
	if state == ScheduleNotRegistered {
		return state, nil
	}
	if deps.goos() == "darwin" {
		return removeLaunchd(ctx, deps)
	}
	return removeCron(ctx, deps, cronContent)
}

// launchctlPath resolves deps.Launchctl (or "launchctl") via exec.LookPath
// — an absolute fixture path in tests is returned as-is (LookPath treats
// any name containing a slash as a direct path, not a PATH search).
func launchctlPath(deps ScheduleDeps) (string, error) {
	path, err := exec.LookPath(cmp.Or(deps.Launchctl, "launchctl"))
	if err != nil {
		return "", fmt.Errorf("locating launchctl: %w", err)
	}
	return path, nil
}

func checkLaunchd(ctx context.Context, deps ScheduleDeps) (ScheduleState, error) {
	bin, err := launchctlPath(deps)
	if err != nil {
		return ScheduleCheckFailed, err
	}
	target := launchctlServiceTarget(deps.Label)
	cmd := exec.CommandContext(ctx, bin, "print", target)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if strings.Contains(strings.ToLower(out.String()), "could not find") {
			return ScheduleNotRegistered, nil
		}
		return ScheduleCheckFailed, fmt.Errorf("launchctl print %s: %w: %s", target, err, strings.TrimSpace(out.String()))
	}
	return ScheduleRegistered, nil
}

// launchdPlist renders the job's plist body: Label, ProgramArguments
// (BinaryPath, "run", "--config", ConfigPath), and StartInterval in raw
// seconds — mirroring init.go's schedulingEntryContent darwin branch,
// which a later unit rewires to call scheduleIntervalSeconds/
// scheduleCronField instead of its current hardcoded 21600/"0 */6 * * *".
func launchdPlist(deps ScheduleDeps) string {
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
	<integer>%d</integer>
</dict>
</plist>
`, deps.Label, deps.BinaryPath, deps.ConfigPath, scheduleIntervalSeconds(deps.Interval))
}

func installLaunchd(ctx context.Context, deps ScheduleDeps) (ScheduleState, error) {
	if err := os.MkdirAll(filepath.Dir(deps.PlistPath), 0o755); err != nil {
		return ScheduleNotRegistered, fmt.Errorf("creating plist directory for %s: %w", deps.PlistPath, err)
	}
	if err := os.WriteFile(deps.PlistPath, []byte(launchdPlist(deps)), 0o644); err != nil {
		return ScheduleNotRegistered, fmt.Errorf("writing plist %s: %w", deps.PlistPath, err)
	}

	bin, err := launchctlPath(deps)
	if err != nil {
		return ScheduleNotRegistered, err
	}
	domain := launchctlDomain()
	cmd := exec.CommandContext(ctx, bin, "bootstrap", domain, deps.PlistPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return ScheduleNotRegistered, fmt.Errorf("launchctl bootstrap %s %s: %w: %s", domain, deps.PlistPath, err, strings.TrimSpace(out.String()))
	}
	return ScheduleRegistered, nil
}

func removeLaunchd(ctx context.Context, deps ScheduleDeps) (ScheduleState, error) {
	bin, err := launchctlPath(deps)
	if err != nil {
		return ScheduleRegistered, err
	}
	target := launchctlServiceTarget(deps.Label)
	cmd := exec.CommandContext(ctx, bin, "bootout", target)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return ScheduleRegistered, fmt.Errorf("launchctl bootout %s: %w: %s", target, err, strings.TrimSpace(out.String()))
	}
	return ScheduleNotRegistered, nil
}

// crontabPath resolves deps.Crontab (or "crontab") via exec.LookPath,
// mirroring launchctlPath.
func crontabPath(deps ScheduleDeps) (string, error) {
	path, err := exec.LookPath(cmp.Or(deps.Crontab, "crontab"))
	if err != nil {
		return "", fmt.Errorf("locating crontab: %w", err)
	}
	return path, nil
}

// currentCrontab returns the invoking user's current crontab content,
// treating "no crontab installed yet" as a valid empty result — cron's own
// convention for a user who has never run `crontab -e` — rather than an
// error.
func currentCrontab(ctx context.Context, deps ScheduleDeps) (string, error) {
	bin, err := crontabPath(deps)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, "-l")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(strings.ToLower(stderr.String()), "no crontab") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// checkCronState resolves cron's live registration state and returns the
// crontab content fetched to do so, so a caller that goes on to install or
// remove an entry can reuse it instead of running `crontab -l` again.
func checkCronState(ctx context.Context, deps ScheduleDeps) (ScheduleState, string, error) {
	content, err := currentCrontab(ctx, deps)
	if err != nil {
		return ScheduleCheckFailed, "", err
	}
	if strings.Contains(content, cronMarker(deps.Label)) {
		return ScheduleRegistered, content, nil
	}
	return ScheduleNotRegistered, content, nil
}

// writeCrontab replaces the invoking user's entire crontab with content
// via `crontab -` (install from stdin) — the same read-modify-write shape
// as `crontab -l | ... | crontab -`, just without the intermediate shell
// pipeline.
func writeCrontab(ctx context.Context, deps ScheduleDeps, content string) error {
	bin, err := crontabPath(deps)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "-")
	cmd.Stdin = strings.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("crontab -: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// cronJobLine renders the crontab line invoking token-profile run on
// interval's cadence — shared by appendCronEntry (the managed, marker-paired
// live entry) and init.go's schedulingEntryContent (the reviewable snippet),
// so the two can never drift on the actual schedule expression.
func cronJobLine(interval time.Duration, binaryPath, configPath string) string {
	return fmt.Sprintf("0 %s * * * %s run --config %s", scheduleCronField(interval), binaryPath, configPath)
}

// appendCronEntry appends the managed marker-plus-job-line pair to
// existing, preserving every pre-existing entry untouched and normalizing
// to exactly one trailing newline regardless of existing's own trailing
// newline count.
func appendCronEntry(existing string, deps ScheduleDeps) string {
	entry := cronMarker(deps.Label) + "\n" + cronJobLine(deps.Interval, deps.BinaryPath, deps.ConfigPath)
	if existing == "" {
		return entry + "\n"
	}
	return strings.TrimRight(existing, "\n") + "\n" + entry + "\n"
}

// stripCronEntry removes the managed marker line and the job line
// immediately following it, leaving every other entry in existing
// untouched — the inverse of appendCronEntry.
func stripCronEntry(existing, label string) string {
	marker := cronMarker(label)
	lines := strings.Split(existing, "\n")
	kept := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if lines[i] == marker {
			i++ // also drop the job line this marker precedes
			continue
		}
		kept = append(kept, lines[i])
	}
	return strings.Join(kept, "\n")
}

// installCron takes existing (the crontab content the caller already
// fetched via checkCronState) rather than re-fetching it.
func installCron(ctx context.Context, deps ScheduleDeps, existing string) (ScheduleState, error) {
	if err := writeCrontab(ctx, deps, appendCronEntry(existing, deps)); err != nil {
		return ScheduleNotRegistered, err
	}
	return ScheduleRegistered, nil
}

// removeCron takes existing (the crontab content the caller already
// fetched via checkCronState) rather than re-fetching it.
func removeCron(ctx context.Context, deps ScheduleDeps, existing string) (ScheduleState, error) {
	if err := writeCrontab(ctx, deps, stripCronEntry(existing, deps.Label)); err != nil {
		return ScheduleRegistered, err
	}
	return ScheduleNotRegistered, nil
}
