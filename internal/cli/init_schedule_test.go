package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/agentsview"
	"github.com/Christophe1997/token-profile/internal/config"
)

func TestInit_ScheduleRegistrationAccepted_InstallsSchedule(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-accept")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	schedDir := t.TempDir()
	statePath := filepath.Join(schedDir, "state")
	capturePath := filepath.Join(schedDir, "capture")
	launchctlBin := fakeLaunchctlBinary(t, statePath, capturePath)

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work, ScheduleInterval: 4 * time.Hour},
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-sched-accept",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        work,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     "/config.json",
		Stdout:         &stdout,
		Stdin:          strings.NewReader("y\n"),
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			GOOS:      "darwin",
			PlistPath: filepath.Join(schedDir, "schedule.plist"),
			Launchctl: launchctlBin,
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}

	if !strings.Contains(stdout.String(), "Register the refresh schedule now?") {
		t.Errorf("Init() Stdout = %q, want the schedule-registration prompt to have been shown", stdout.String())
	}

	captured := readCaptureFile(t, capturePath)
	foundBootstrap := false
	for _, line := range captured {
		if strings.HasPrefix(line, "bootstrap ") {
			foundBootstrap = true
		}
	}
	if !foundBootstrap {
		t.Errorf("captured launchctl invocations = %v, want a bootstrap call", captured)
	}
}

// TestInit_ScheduleRegistrationAccepted_InstallFails_ReportsWarningExitsZero
// covers KTD17: a failed live install attempt (e.g. a permission-restricted
// LaunchAgents directory, simulated here by a launchctl fixture that always
// fails) must degrade to a reported warning rather than a non-zero exit —
// clone/config/first-publish have already succeeded by this point.
func TestInit_ScheduleRegistrationAccepted_InstallFails_ReportsWarningExitsZero(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-fail")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")
	capturePath := filepath.Join(t.TempDir(), "capture")
	launchctlBin := fakeLaunchctlBinaryAlwaysFails(t, capturePath)

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-sched-fail",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        work,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     "/config.json",
		Stdout:         &stdout,
		Stdin:          strings.NewReader("y\n"),
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			GOOS:      "darwin",
			PlistPath: filepath.Join(t.TempDir(), "schedule.plist"),
			Launchctl: launchctlBin,
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil (a failed schedule install must degrade to a warning, KTD17)", err)
	}
	if !strings.Contains(stdout.String(), "warning") {
		t.Errorf("Init() Stdout = %q, want a warning about the failed schedule install", stdout.String())
	}
}

// TestInit_ScheduleRegistrationAccepted_RunningAsRoot_WarnsAndSkipsInstall
// covers KTD16's own claim ("never sudo, never the system domain") being
// enforced: accepting the prompt while running under an effective UID of 0
// must degrade to a warning and never attempt InstallSchedule at all,
// rather than silently targeting gui/0 or root's own crontab.
func TestInit_ScheduleRegistrationAccepted_RunningAsRoot_WarnsAndSkipsInstall(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-root")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")
	capturePath := filepath.Join(t.TempDir(), "capture")
	launchctlBin := fakeLaunchctlBinary(t, filepath.Join(t.TempDir(), "state"), capturePath)

	geteuid = func() int { return 0 }
	defer func() { geteuid = os.Geteuid }()

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-sched-root",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        work,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     "/config.json",
		Stdout:         &stdout,
		Stdin:          strings.NewReader("y\n"),
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			GOOS:      "darwin",
			PlistPath: filepath.Join(t.TempDir(), "schedule.plist"),
			Launchctl: launchctlBin,
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil (running as root must degrade to a warning, not fail init)", err)
	}
	if !strings.Contains(stdout.String(), "warning") || !strings.Contains(stdout.String(), "root") {
		t.Errorf("Init() Stdout = %q, want a warning mentioning root/sudo", stdout.String())
	}
	if captured := readCaptureFile(t, capturePath); len(captured) != 0 {
		t.Errorf("launchctl invocations = %v, want none — install must never be attempted while running as root", captured)
	}
}

// acquireOnReadReader wraps r, attempting acquireRunLock(repoDir) the first
// time Read is called — used to prove a lock is already released by the
// time a blocking prompt (backed by r) starts reading input, without
// needing real concurrency or timing.
type acquireOnReadReader struct {
	r         io.Reader
	repoDir   string
	attempted bool
	lockErr   error
}

func (a *acquireOnReadReader) Read(p []byte) (int, error) {
	if !a.attempted {
		a.attempted = true
		release, err := acquireRunLock(a.repoDir)
		a.lockErr = err
		if err == nil {
			release()
		}
	}
	return a.r.Read(p)
}

// TestInit_ScheduleRegistrationPrompt_RunLockAlreadyReleased covers Init
// releasing its run-lock before offering schedule registration, rather
// than holding it through that prompt's unbounded wait on real user input.
// Held-through-the-prompt would starve a concurrently-scheduled `run`
// (whose own acquireRunLock would fail immediately) for as long as the
// adopter takes to answer "Register the refresh schedule now?" — this
// proves the lock is already free by simulating a second acquirer reading
// from the very same Stdin the prompt itself is blocked on.
func TestInit_ScheduleRegistrationPrompt_RunLockAlreadyReleased(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-lock")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")
	capturePath := filepath.Join(t.TempDir(), "capture")
	launchctlBin := fakeLaunchctlBinary(t, filepath.Join(t.TempDir(), "state"), capturePath)

	input := &acquireOnReadReader{r: strings.NewReader("y\n"), repoDir: work}

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-sched-lock",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        work,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     "/config.json",
		Stdout:         &stdout,
		Stdin:          input,
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			GOOS:      "darwin",
			PlistPath: filepath.Join(t.TempDir(), "schedule.plist"),
			Launchctl: launchctlBin,
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if input.lockErr != nil {
		t.Errorf("acquireRunLock() during the schedule-registration prompt error = %v, want nil", input.lockErr)
	}
}

// TestInit_ScheduleRegistrationDeclined_SnippetWrittenNoInstallAttempted
// covers the declined-prompt integration scenario: the reviewable snippet
// at --schedule-dest is still written regardless, but no live install is
// ever attempted.
func TestInit_ScheduleRegistrationDeclined_SnippetWrittenNoInstallAttempted(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-decline")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:         config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:         &agentsview.Client{BinaryName: bin},
		MachineID:      "machine-sched-decline",
		Now:            time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:        work,
		ScheduleDest:   scheduleDest,
		BinaryPath:     "/usr/local/bin/token-profile",
		ConfigPath:     "/config.json",
		Stdout:         &stdout,
		Stdin:          strings.NewReader("n\n"),
		PromptSchedule: true,
		Schedule: ScheduleDeps{
			// A deliberately-broken path: if InstallSchedule were ever
			// invoked despite the decline, resolving this would fail loudly
			// rather than silently succeeding.
			Launchctl: filepath.Join(t.TempDir(), "no-such-launchctl"),
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if _, err := os.Stat(scheduleDest); err != nil {
		t.Errorf("scheduling entry %s missing after decline: %v", scheduleDest, err)
	}
	if strings.Contains(stdout.String(), "warning") {
		t.Errorf("Init() Stdout = %q, want no install-failure warning when declined", stdout.String())
	}
}

// TestInit_RegisterScheduleFlag_InstallsWithoutPromptEvenWithoutTTY covers
// the agent-native gap: with PromptSchedule false (no TTY, e.g. an
// unattended re-run of init from a provisioning script) and RegisterSchedule
// true, the schedule must still be installed directly, with no interactive
// prompt shown — the explicit flag is the escape hatch A2 (an unattended
// scheduler) needs to opt into InstallSchedule without a terminal.
func TestInit_RegisterScheduleFlag_InstallsWithoutPromptEvenWithoutTTY(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)
	work := cloneWorkdir(t, remote, "sched-unattended")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	scheduleDest := filepath.Join(t.TempDir(), "schedule")

	schedDir := t.TempDir()
	statePath := filepath.Join(schedDir, "state")
	capturePath := filepath.Join(schedDir, "capture")
	launchctlBin := fakeLaunchctlBinary(t, statePath, capturePath)

	var stdout bytes.Buffer
	deps := InitDeps{
		Config:       config.Config{Breakdown: config.BreakdownPerModel, TargetRepo: work},
		Client:       &agentsview.Client{BinaryName: bin},
		MachineID:    "machine-sched-unattended",
		Now:          time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:      work,
		ScheduleDest: scheduleDest,
		BinaryPath:   "/usr/local/bin/token-profile",
		ConfigPath:   "/config.json",
		Stdout:       &stdout,
		Stdin:        nil, // no TTY at all -- an unattended invocation
		// PromptSchedule intentionally left false: no interactive session.
		RegisterSchedule: true,
		Schedule: ScheduleDeps{
			GOOS:      "darwin",
			PlistPath: filepath.Join(schedDir, "schedule.plist"),
			Launchctl: launchctlBin,
		},
	}

	if err := Init(t.Context(), deps); err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	if strings.Contains(stdout.String(), "Register the refresh schedule now?") {
		t.Errorf("Init() Stdout = %q, want the interactive prompt skipped when RegisterSchedule is set", stdout.String())
	}

	captured := readCaptureFile(t, capturePath)
	foundBootstrap := false
	for _, line := range captured {
		if strings.HasPrefix(line, "bootstrap ") {
			foundBootstrap = true
		}
	}
	if !foundBootstrap {
		t.Errorf("captured launchctl invocations = %v, want a bootstrap call despite no TTY being present", captured)
	}
}
