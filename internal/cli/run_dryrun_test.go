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
	"github.com/Christophe1997/token-profile/internal/snapshot"
)

// TestRun_DryRun_WritesFilesButNoCommit covers R7/R9's happy path: --dry-run
// must still perform the real snapshot write, render, and README injection,
// leaving real inspectable changes on disk, but must stop before
// staging/committing/pushing, printing a summary of what would have been
// committed instead.
func TestRun_DryRun_WritesFilesButNoCommit(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)
	work := cloneWorkdir(t, remote, "dry-run")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	headBefore := strings.TrimSpace(runGitT(t, work, "rev-parse", "HEAD"))

	var buf bytes.Buffer
	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel, RenderMode: config.RenderModeASCII},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-dry",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
		Stdout:    &buf,
		DryRun:    true,
	}

	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	rows, err := snapshot.Read(work, "machine-dry")
	if err != nil {
		t.Fatalf("snapshot.Read() error = %v, want the snapshot written to disk even in dry-run mode", err)
	}
	if len(rows) != 1 {
		t.Fatalf("snapshot rows = %+v, want exactly 1 row written", rows)
	}

	readmeBytes, err := os.ReadFile(filepath.Join(work, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)
	if !strings.Contains(got, "claude-sonnet-5") {
		t.Errorf("README = %q, want the rendered card written even in dry-run mode", got)
	}
	if strings.Contains(got, "placeholder") {
		t.Errorf("README still contains the pre-run placeholder, want it replaced even in dry-run mode:\n%s", got)
	}

	headAfter := strings.TrimSpace(runGitT(t, work, "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("HEAD moved from %s to %s, want no commit created in dry-run mode", headBefore, headAfter)
	}

	status := runGitT(t, work, "status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		t.Error("git status --porcelain is empty, want uncommitted changes left in the working tree after a dry run")
	}

	summary := buf.String()
	if !strings.Contains(summary, "dry run") {
		t.Errorf("Run() Stdout = %q, want a dry-run summary", summary)
	}
	if !strings.Contains(summary, snapshotRelPath("machine-dry")) {
		t.Errorf("Run() Stdout = %q, want it to name the snapshot path that would have been committed", summary)
	}
	if !strings.Contains(summary, "README.md") {
		t.Errorf("Run() Stdout = %q, want it to name README.md as a file that would have been committed", summary)
	}
}

// TestRun_DryRun_NoNewUsage_SummaryReflectsNoOp covers the edge case (R7): a
// dry run against a working tree with nothing new to publish — an
// immediate second dry-run invocation with identical inputs right after a
// real run already published the same data — must report an accurate no-op
// summary rather than falsely naming files as pending.
func TestRun_DryRun_NoNewUsage_SummaryReflectsNoOp(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)
	work := cloneWorkdir(t, remote, "dry-run-noop")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel, RenderMode: config.RenderModeASCII},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-dry-noop",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("first (real) Run() error = %v, want nil", err)
	}

	var buf bytes.Buffer
	deps.Stdout = &buf
	deps.DryRun = true
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("second (dry-run) Run() error = %v, want nil", err)
	}

	status := runGitT(t, work, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("git status --porcelain = %q, want a clean working tree (an identical rerun produces no new changes)", status)
	}

	summary := buf.String()
	if !strings.Contains(summary, "dry run") {
		t.Errorf("Run() Stdout = %q, want a dry-run summary", summary)
	}
	if !strings.Contains(summary, "nothing to publish") {
		t.Errorf("Run() Stdout = %q, want it to report no-op accurately", summary)
	}
	if strings.Contains(summary, snapshotRelPath("machine-dry-noop")) {
		t.Errorf("Run() Stdout = %q, want it to NOT name any pending file when there's nothing new to publish", summary)
	}
}

// TestRun_DryRun_ThenRealRun_PublishesDescribedChanges covers the
// integration scenario: re-running the same command without --dry-run
// afterward must produce the real commit the dry-run summary described,
// publishing exactly the content the dry run already wrote to disk.
func TestRun_DryRun_ThenRealRun_PublishesDescribedChanges(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)
	work := cloneWorkdir(t, remote, "dry-run-then-real")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel, RenderMode: config.RenderModeASCII},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-dry-real",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
		DryRun:    true,
	}
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("dry-run Run() error = %v, want nil", err)
	}

	dryRunReadme, err := os.ReadFile(filepath.Join(work, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) after dry run error = %v", err)
	}

	deps.DryRun = false
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("real Run() error = %v, want nil", err)
	}

	verify := cloneWorkdir(t, remote, "dry-run-then-real-verify")
	log := runGitT(t, verify, "log", "--oneline")
	if !strings.Contains(log, "token-profile") {
		t.Errorf("git log = %q, want the real run's commit to have landed on the remote", log)
	}

	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if string(readmeBytes) != string(dryRunReadme) {
		t.Errorf("published README = %q, want it identical to what the dry run already wrote to disk:\n%q", readmeBytes, dryRunReadme)
	}

	if _, err := snapshot.Read(verify, "machine-dry-real"); err != nil {
		t.Errorf("snapshot.Read() error = %v, want the snapshot committed by the real run", err)
	}
}

// TestNewRunCmd_HasDryRunFlag covers R7: `run` must expose a --dry-run flag.
func TestNewRunCmd_HasDryRunFlag(t *testing.T) {
	cmd := NewRunCmd()
	if f := cmd.Flags().Lookup("dry-run"); f == nil {
		t.Error("NewRunCmd() does not register a --dry-run flag, want one (R7)")
	}
}
