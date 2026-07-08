package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// shQuote wraps s in double quotes for splicing into a generated shell
// script fixture. Fixture paths all come from t.TempDir(), which never
// contains a quote or backslash, so this is safe without full POSIX
// shell-quoting logic.
func shQuote(s string) string {
	return `"` + s + `"`
}

// fakeLaunchctlBinary writes an executable shell-script fixture standing in
// for the real launchctl binary (darwin). It tracks "loaded" state via a
// marker file at statePath — created by `bootstrap`, removed by `bootout` —
// and appends each invocation's argument line to capturePath, so tests can
// assert the exact domain string used (KTD16). `print` reports registered
// (exit 0) when statePath exists, or "Could not find service" (exit 1)
// otherwise — mirroring real launchctl's not-found phrasing.
func fakeLaunchctlBinary(t *testing.T, statePath, capturePath string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shQuote(capturePath) + "\n" +
		"case \"$1\" in\n" +
		"  print)\n" +
		"    if [ -f " + shQuote(statePath) + " ]; then\n" +
		"      echo running\n" +
		"      exit 0\n" +
		"    fi\n" +
		"    echo \"Could not find service\" >&2\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"  bootstrap)\n" +
		"    touch " + shQuote(statePath) + "\n" +
		"    exit 0\n" +
		"    ;;\n" +
		"  bootout)\n" +
		"    rm -f " + shQuote(statePath) + "\n" +
		"    exit 0\n" +
		"    ;;\n" +
		"  *)\n" +
		"    echo \"unknown launchctl subcommand: $1\" >&2\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"esac\n"
	path := filepath.Join(t.TempDir(), "launchctl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// fakeLaunchctlBinaryAlwaysFails writes a launchctl fixture that always
// fails with a generic error unrelated to "could not find" — standing in
// for a real launchd failure distinct from "service not registered" (e.g.
// launchd itself unreachable), so CheckScheduleState must report
// ScheduleCheckFailed rather than collapsing it into ScheduleNotRegistered.
func fakeLaunchctlBinaryAlwaysFails(t *testing.T, capturePath string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shQuote(capturePath) + "\n" +
		"echo \"launchd: permission denied\" >&2\n" +
		"exit 71\n"
	path := filepath.Join(t.TempDir(), "launchctl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// fakeCrontabBinary writes an executable shell-script fixture standing in
// for the real crontab binary (non-darwin). crontabPath is a scratch file
// standing in for the invoking user's installed crontab: `crontab -l` cats
// it if present or fails with "no crontab for" (cron's own convention for
// "never installed") otherwise; `crontab -` overwrites it from stdin. Each
// invocation's argument line is appended to capturePath.
func fakeCrontabBinary(t *testing.T, crontabPath, capturePath string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shQuote(capturePath) + "\n" +
		"if [ \"$1\" = \"-l\" ]; then\n" +
		"  if [ -f " + shQuote(crontabPath) + " ]; then\n" +
		"    cat " + shQuote(crontabPath) + "\n" +
		"    exit 0\n" +
		"  fi\n" +
		"  echo \"no crontab for testuser\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"if [ \"$1\" = \"-\" ]; then\n" +
		"  cat > " + shQuote(crontabPath) + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"unsupported crontab invocation: $*\" >&2\n" +
		"exit 1\n"
	path := filepath.Join(t.TempDir(), "crontab")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// fakeCrontabBinaryAlwaysFails writes a crontab fixture that always fails
// with a generic error unrelated to "no crontab for" — the cron-side
// equivalent of fakeLaunchctlBinaryAlwaysFails.
func fakeCrontabBinaryAlwaysFails(t *testing.T, capturePath string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shQuote(capturePath) + "\n" +
		"echo \"crontab: permission denied\" >&2\n" +
		"exit 71\n"
	path := filepath.Join(t.TempDir(), "crontab")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// readCaptureFile reads the fixture's captured invocation-argument lines
// (one per invocation) for assertions against exactly what the fixture was
// called with.
func readCaptureFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// darwinDeps builds a ScheduleDeps exercising the launchctl branch against
// a fixture rooted in t.TempDir(), with a 6h interval (the plan's baseline
// example) unless the caller overrides Interval afterward.
func darwinDeps(t *testing.T, launchctlPath string) ScheduleDeps {
	t.Helper()
	return ScheduleDeps{
		GOOS:       "darwin",
		Label:      "dev.token-profile.refresh",
		BinaryPath: "/usr/local/bin/token-profile",
		ConfigPath: "/config.json",
		Interval:   6 * time.Hour,
		PlistPath:  filepath.Join(t.TempDir(), "schedule.plist"),
		Launchctl:  launchctlPath,
	}
}

// cronDeps builds a ScheduleDeps exercising the crontab branch, mirroring
// darwinDeps.
func cronDeps(t *testing.T, crontabPath string) ScheduleDeps {
	t.Helper()
	return ScheduleDeps{
		GOOS:       "linux",
		Label:      "dev.token-profile.refresh",
		BinaryPath: "/usr/local/bin/token-profile",
		ConfigPath: "/config.json",
		Interval:   6 * time.Hour,
		Crontab:    crontabPath,
	}
}

// TestCheckScheduleState_Darwin_NotRegisteredInitially covers a fresh
// machine where the launchd job was never bootstrapped: the state check
// must report ScheduleNotRegistered, not an error.
func TestCheckScheduleState_Darwin_NotRegisteredInitially(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	deps := darwinDeps(t, bin)

	state, err := CheckScheduleState(t.Context(), deps)
	if err != nil {
		t.Fatalf("CheckScheduleState() error = %v, want nil", err)
	}
	if state != ScheduleNotRegistered {
		t.Errorf("CheckScheduleState() = %v, want ScheduleNotRegistered", state)
	}
}

// TestScheduleRoundTrip_Darwin covers the happy path (launchd): install
// then a state-check reports registered; remove then a state-check reports
// not-registered.
func TestScheduleRoundTrip_Darwin(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	deps := darwinDeps(t, bin)

	installState, err := InstallSchedule(t.Context(), deps)
	if err != nil {
		t.Fatalf("InstallSchedule() error = %v, want nil", err)
	}
	if installState != ScheduleRegistered {
		t.Errorf("InstallSchedule() state = %v, want ScheduleRegistered", installState)
	}

	checkState, err := CheckScheduleState(t.Context(), deps)
	if err != nil {
		t.Fatalf("CheckScheduleState() after install error = %v, want nil", err)
	}
	if checkState != ScheduleRegistered {
		t.Errorf("CheckScheduleState() after install = %v, want ScheduleRegistered", checkState)
	}

	removeState, err := RemoveSchedule(t.Context(), deps)
	if err != nil {
		t.Fatalf("RemoveSchedule() error = %v, want nil", err)
	}
	if removeState != ScheduleNotRegistered {
		t.Errorf("RemoveSchedule() state = %v, want ScheduleNotRegistered", removeState)
	}

	checkState, err = CheckScheduleState(t.Context(), deps)
	if err != nil {
		t.Fatalf("CheckScheduleState() after remove error = %v, want nil", err)
	}
	if checkState != ScheduleNotRegistered {
		t.Errorf("CheckScheduleState() after remove = %v, want ScheduleNotRegistered", checkState)
	}
}

// TestRemoveSchedule_Darwin_DeletesPlistFile covers removeLaunchd actually
// deleting the on-disk plist installLaunchd wrote — not just deregistering
// the live launchd job via `bootout`. macOS's launchd reloads every plist
// present under the LaunchAgents directory at each new login regardless of
// a prior `bootout` in the current session, so leaving the file behind
// would let the schedule silently reactivate itself at the user's next
// login, defeating RemoveSchedule's whole purpose.
func TestRemoveSchedule_Darwin_DeletesPlistFile(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	deps := darwinDeps(t, bin)

	if _, err := InstallSchedule(t.Context(), deps); err != nil {
		t.Fatalf("InstallSchedule() error = %v, want nil", err)
	}
	if _, statErr := os.Stat(deps.PlistPath); statErr != nil {
		t.Fatalf("Stat(PlistPath) after install error = %v, want the plist to exist", statErr)
	}

	if _, err := RemoveSchedule(t.Context(), deps); err != nil {
		t.Fatalf("RemoveSchedule() error = %v, want nil", err)
	}
	if _, statErr := os.Stat(deps.PlistPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(PlistPath) after remove error = %v, want os.ErrNotExist (the plist must be deleted, not just booted out)", statErr)
	}
}

// TestScheduleRoundTrip_Cron mirrors TestScheduleRoundTrip_Darwin for the
// non-darwin crontab mechanism.
func TestScheduleRoundTrip_Cron(t *testing.T) {
	dir := t.TempDir()
	crontabPath := filepath.Join(dir, "crontab.txt")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeCrontabBinary(t, crontabPath, capturePath)
	deps := cronDeps(t, bin)

	installState, err := InstallSchedule(t.Context(), deps)
	if err != nil {
		t.Fatalf("InstallSchedule() error = %v, want nil", err)
	}
	if installState != ScheduleRegistered {
		t.Errorf("InstallSchedule() state = %v, want ScheduleRegistered", installState)
	}

	checkState, err := CheckScheduleState(t.Context(), deps)
	if err != nil {
		t.Fatalf("CheckScheduleState() after install error = %v, want nil", err)
	}
	if checkState != ScheduleRegistered {
		t.Errorf("CheckScheduleState() after install = %v, want ScheduleRegistered", checkState)
	}

	removeState, err := RemoveSchedule(t.Context(), deps)
	if err != nil {
		t.Fatalf("RemoveSchedule() error = %v, want nil", err)
	}
	if removeState != ScheduleNotRegistered {
		t.Errorf("RemoveSchedule() state = %v, want ScheduleNotRegistered", removeState)
	}

	checkState, err = CheckScheduleState(t.Context(), deps)
	if err != nil {
		t.Fatalf("CheckScheduleState() after remove error = %v, want nil", err)
	}
	if checkState != ScheduleNotRegistered {
		t.Errorf("CheckScheduleState() after remove = %v, want ScheduleNotRegistered", checkState)
	}
}

// TestScheduleRoundTrip_Cron_PreservesUnrelatedEntries covers install/remove
// against a crontab that already holds an unrelated job: that entry must
// survive both the install and the subsequent remove untouched.
func TestScheduleRoundTrip_Cron_PreservesUnrelatedEntries(t *testing.T) {
	dir := t.TempDir()
	crontabPath := filepath.Join(dir, "crontab.txt")
	capturePath := filepath.Join(dir, "capture")
	const unrelated = "0 3 * * * /usr/bin/other-job\n"
	if err := os.WriteFile(crontabPath, []byte(unrelated), 0o644); err != nil {
		t.Fatalf("WriteFile(crontab.txt) error = %v", err)
	}
	bin := fakeCrontabBinary(t, crontabPath, capturePath)
	deps := cronDeps(t, bin)

	if _, err := InstallSchedule(t.Context(), deps); err != nil {
		t.Fatalf("InstallSchedule() error = %v, want nil", err)
	}
	afterInstall, err := os.ReadFile(crontabPath)
	if err != nil {
		t.Fatalf("ReadFile(crontab.txt) after install error = %v", err)
	}
	if !strings.Contains(string(afterInstall), unrelated) {
		t.Errorf("crontab after install = %q, want it to still contain unrelated entry %q", afterInstall, unrelated)
	}

	if _, err := RemoveSchedule(t.Context(), deps); err != nil {
		t.Fatalf("RemoveSchedule() error = %v, want nil", err)
	}
	afterRemove, err := os.ReadFile(crontabPath)
	if err != nil {
		t.Fatalf("ReadFile(crontab.txt) after remove error = %v", err)
	}
	if !strings.Contains(string(afterRemove), unrelated) {
		t.Errorf("crontab after remove = %q, want it to still contain unrelated entry %q", afterRemove, unrelated)
	}
	if strings.Contains(string(afterRemove), cronMarker(deps.Label)) {
		t.Errorf("crontab after remove = %q, want token-profile's own entry gone", afterRemove)
	}
}

// TestInstallSchedule_Darwin_AlreadyRegistered_NoOp covers re-running
// InstallSchedule against an already-bootstrapped job: it must no-op
// (never invoke `bootstrap` a second time) and still report success.
func TestInstallSchedule_Darwin_AlreadyRegistered_NoOp(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	deps := darwinDeps(t, bin)

	if _, err := InstallSchedule(t.Context(), deps); err != nil {
		t.Fatalf("first InstallSchedule() error = %v, want nil", err)
	}

	state, err := InstallSchedule(t.Context(), deps)
	if err != nil {
		t.Fatalf("second InstallSchedule() error = %v, want nil", err)
	}
	if state != ScheduleRegistered {
		t.Errorf("second InstallSchedule() state = %v, want ScheduleRegistered", state)
	}

	calls := readCaptureFile(t, capturePath)
	bootstrapCalls := 0
	for _, call := range calls {
		if strings.HasPrefix(call, "bootstrap ") {
			bootstrapCalls++
		}
	}
	if bootstrapCalls != 1 {
		t.Errorf("launchctl bootstrap invoked %d times across two InstallSchedule() calls, want exactly 1 (idempotent no-op on the second)", bootstrapCalls)
	}
}

// TestInstallSchedule_Cron_AlreadyRegistered_NoOp mirrors
// TestInstallSchedule_Darwin_AlreadyRegistered_NoOp for the crontab
// mechanism: a second install must not duplicate the managed entry.
func TestInstallSchedule_Cron_AlreadyRegistered_NoOp(t *testing.T) {
	dir := t.TempDir()
	crontabPath := filepath.Join(dir, "crontab.txt")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeCrontabBinary(t, crontabPath, capturePath)
	deps := cronDeps(t, bin)

	if _, err := InstallSchedule(t.Context(), deps); err != nil {
		t.Fatalf("first InstallSchedule() error = %v, want nil", err)
	}
	state, err := InstallSchedule(t.Context(), deps)
	if err != nil {
		t.Fatalf("second InstallSchedule() error = %v, want nil", err)
	}
	if state != ScheduleRegistered {
		t.Errorf("second InstallSchedule() state = %v, want ScheduleRegistered", state)
	}

	got, err := os.ReadFile(crontabPath)
	if err != nil {
		t.Fatalf("ReadFile(crontab.txt) error = %v", err)
	}
	if n := strings.Count(string(got), cronMarker(deps.Label)); n != 1 {
		t.Errorf("crontab contains %d copies of the managed marker after two installs, want exactly 1 (no duplicate entry): %q", n, got)
	}
}

// TestRemoveSchedule_Darwin_NothingRegistered_NoOp covers RemoveSchedule
// against a machine where the job was never installed: it must report
// ScheduleNotRegistered without error, and never invoke `bootout`.
func TestRemoveSchedule_Darwin_NothingRegistered_NoOp(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	deps := darwinDeps(t, bin)

	state, err := RemoveSchedule(t.Context(), deps)
	if err != nil {
		t.Fatalf("RemoveSchedule() error = %v, want nil", err)
	}
	if state != ScheduleNotRegistered {
		t.Errorf("RemoveSchedule() state = %v, want ScheduleNotRegistered", state)
	}

	for _, call := range readCaptureFile(t, capturePath) {
		if strings.HasPrefix(call, "bootout ") {
			t.Errorf("launchctl bootout invoked (%q) though nothing was registered", call)
		}
	}
}

// TestRemoveSchedule_Cron_NothingRegistered_NoOp mirrors
// TestRemoveSchedule_Darwin_NothingRegistered_NoOp for the crontab
// mechanism, including the case of no crontab installed at all.
func TestRemoveSchedule_Cron_NothingRegistered_NoOp(t *testing.T) {
	dir := t.TempDir()
	crontabPath := filepath.Join(dir, "crontab.txt")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeCrontabBinary(t, crontabPath, capturePath)
	deps := cronDeps(t, bin)

	state, err := RemoveSchedule(t.Context(), deps)
	if err != nil {
		t.Fatalf("RemoveSchedule() error = %v, want nil", err)
	}
	if state != ScheduleNotRegistered {
		t.Errorf("RemoveSchedule() state = %v, want ScheduleNotRegistered", state)
	}
	if _, err := os.Stat(crontabPath); !os.IsNotExist(err) {
		t.Errorf("crontab file exists after a no-op RemoveSchedule(), want it left untouched (absent)")
	}
}

// TestCheckScheduleState_Darwin_CheckFailed covers a launchctl invocation
// that fails for a reason other than "not found" (e.g. launchd itself
// unreachable): CheckScheduleState must report ScheduleCheckFailed, a
// distinct outcome from ScheduleNotRegistered (AE4, KTD7).
func TestCheckScheduleState_Darwin_CheckFailed(t *testing.T) {
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinaryAlwaysFails(t, capturePath)
	deps := darwinDeps(t, bin)

	state, err := CheckScheduleState(t.Context(), deps)
	if err == nil {
		t.Fatalf("CheckScheduleState() error = nil, want non-nil for an underlying launchctl failure")
	}
	if state != ScheduleCheckFailed {
		t.Errorf("CheckScheduleState() = %v, want ScheduleCheckFailed", state)
	}
	if state == ScheduleNotRegistered {
		t.Errorf("CheckScheduleState() collapsed a real failure into ScheduleNotRegistered")
	}
}

// TestCheckScheduleState_Cron_CheckFailed mirrors
// TestCheckScheduleState_Darwin_CheckFailed for the crontab mechanism.
func TestCheckScheduleState_Cron_CheckFailed(t *testing.T) {
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "capture")
	bin := fakeCrontabBinaryAlwaysFails(t, capturePath)
	deps := cronDeps(t, bin)

	state, err := CheckScheduleState(t.Context(), deps)
	if err == nil {
		t.Fatalf("CheckScheduleState() error = nil, want non-nil for an underlying crontab failure")
	}
	if state != ScheduleCheckFailed {
		t.Errorf("CheckScheduleState() = %v, want ScheduleCheckFailed", state)
	}
}

// TestInstallSchedule_Darwin_CheckFailed_DoesNotInstall covers
// InstallSchedule when the live state can't be determined: it must
// propagate the check failure rather than blindly attempting install
// against unknown state.
func TestInstallSchedule_Darwin_CheckFailed_DoesNotInstall(t *testing.T) {
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinaryAlwaysFails(t, capturePath)
	deps := darwinDeps(t, bin)

	state, err := InstallSchedule(t.Context(), deps)
	if err == nil {
		t.Fatalf("InstallSchedule() error = nil, want non-nil when the state check fails")
	}
	if state != ScheduleCheckFailed {
		t.Errorf("InstallSchedule() state = %v, want ScheduleCheckFailed", state)
	}
	for _, call := range readCaptureFile(t, capturePath) {
		if strings.HasPrefix(call, "bootstrap ") {
			t.Errorf("launchctl bootstrap invoked (%q) despite a failed state check", call)
		}
	}
}

// TestScheduleIntervalSeconds_4h covers KTD10's interval-to-launchd
// conversion: a 4h interval must produce the raw-seconds StartInterval
// value (14400).
func TestScheduleIntervalSeconds_4h(t *testing.T) {
	got := scheduleIntervalSeconds(4 * time.Hour)
	if got != 14400 {
		t.Errorf("scheduleIntervalSeconds(4h) = %d, want 14400", got)
	}
}

// TestScheduleCronField_4h covers KTD10's interval-to-cron conversion: a 4h
// interval must produce the "*/4" hour-field.
func TestScheduleCronField_4h(t *testing.T) {
	got := scheduleCronField(4 * time.Hour)
	if got != "*/4" {
		t.Errorf("scheduleCronField(4h) = %q, want %q", got, "*/4")
	}
}

// TestInstallSchedule_Darwin_PlistContent_4hInterval is the integration
// check that a configured 4h interval actually lands in the written plist's
// StartInterval, not just in the pure helper function.
func TestInstallSchedule_Darwin_PlistContent_4hInterval(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	deps := darwinDeps(t, bin)
	deps.Interval = 4 * time.Hour

	if _, err := InstallSchedule(t.Context(), deps); err != nil {
		t.Fatalf("InstallSchedule() error = %v, want nil", err)
	}

	plist, err := os.ReadFile(deps.PlistPath)
	if err != nil {
		t.Fatalf("ReadFile(plist) error = %v", err)
	}
	if !strings.Contains(string(plist), "<integer>14400</integer>") {
		t.Errorf("plist = %q, want it to contain StartInterval 14400 for a 4h interval", plist)
	}
}

// TestInstallSchedule_Cron_CronLine_4hInterval is the cron-side counterpart
// of TestInstallSchedule_Darwin_PlistContent_4hInterval: a configured 4h
// interval must produce a "*/4" hour field in the written crontab line.
func TestInstallSchedule_Cron_CronLine_4hInterval(t *testing.T) {
	dir := t.TempDir()
	crontabPath := filepath.Join(dir, "crontab.txt")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeCrontabBinary(t, crontabPath, capturePath)
	deps := cronDeps(t, bin)
	deps.Interval = 4 * time.Hour

	if _, err := InstallSchedule(t.Context(), deps); err != nil {
		t.Fatalf("InstallSchedule() error = %v, want nil", err)
	}

	got, err := os.ReadFile(crontabPath)
	if err != nil {
		t.Fatalf("ReadFile(crontab.txt) error = %v", err)
	}
	if !strings.Contains(string(got), "*/4 * * *") {
		t.Errorf("crontab = %q, want it to contain a */4 hour field for a 4h interval", got)
	}
}

// TestSchedule_Darwin_UsesGUIDomain_NeverSudoOrSystem is the KTD16 hardening
// test: install and remove must invoke launchctl against gui/$(id -u) —
// never `sudo`, never the bare `system` domain — asserted against the
// fixture's captured invocation arguments, the only way to verify this
// without a real macOS launchd.
func TestSchedule_Darwin_UsesGUIDomain_NeverSudoOrSystem(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state")
	capturePath := filepath.Join(dir, "capture")
	bin := fakeLaunchctlBinary(t, statePath, capturePath)
	deps := darwinDeps(t, bin)

	if _, err := InstallSchedule(t.Context(), deps); err != nil {
		t.Fatalf("InstallSchedule() error = %v, want nil", err)
	}
	if _, err := RemoveSchedule(t.Context(), deps); err != nil {
		t.Fatalf("RemoveSchedule() error = %v, want nil", err)
	}

	wantDomain := fmt.Sprintf("gui/%d", os.Getuid())
	calls := readCaptureFile(t, capturePath)
	if len(calls) == 0 {
		t.Fatalf("no launchctl invocations captured")
	}

	sawBootstrap, sawBootout := false, false
	for _, call := range calls {
		fields := strings.Fields(call)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "bootstrap":
			sawBootstrap = true
			if len(fields) < 2 || fields[1] != wantDomain {
				t.Errorf("bootstrap invocation %q, want domain argument %q", call, wantDomain)
			}
		case "bootout":
			sawBootout = true
			if len(fields) < 2 || !strings.HasPrefix(fields[1], wantDomain+"/") {
				t.Errorf("bootout invocation %q, want target prefixed with %q", call, wantDomain+"/")
			}
		}
		if strings.Contains(call, "sudo") {
			t.Errorf("invocation %q references sudo, want it never used (KTD16)", call)
		}
		if fields[len(fields)-1] == "system" || strings.Contains(call, "system/") {
			t.Errorf("invocation %q targets the system domain, want gui/%s only (KTD16)", call, strconv.Itoa(os.Getuid()))
		}
	}
	if !sawBootstrap {
		t.Errorf("no bootstrap invocation captured")
	}
	if !sawBootout {
		t.Errorf("no bootout invocation captured")
	}
}
