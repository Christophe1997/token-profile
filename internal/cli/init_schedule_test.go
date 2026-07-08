package cli

import (
	"bytes"
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
