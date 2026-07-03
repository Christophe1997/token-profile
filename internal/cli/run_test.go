package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/agentsview"
	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/readme"
	"github.com/Christophe1997/token-profile/internal/snapshot"
)

// markedReadme is a minimal README already scaffolded with the
// token-profile markers (as if `token-profile init` had already run),
// matching the assumption Run is built on: it refreshes an
// already-initialized repo rather than scaffolding one itself.
const markedReadme = `# username

<!-- token-profile:start -->
placeholder
<!-- token-profile:end -->
`

// unmarkedReadme has no token-profile markers at all, standing in for a
// repo that has never been initialized.
const unmarkedReadme = "# username\n\nNo markers here.\n"

// initBareRemote creates a bare repo standing in for the shared GitHub
// remote hosting the rendered profile, mirroring
// internal/gitops/gitops_test.go's fixture pattern.
func initBareRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	runGitT(t, "", "init", "--bare", "-q", "-b", "main", dir)
	return dir
}

// seedRemote bootstraps remoteDir with a single initial commit containing
// readmeContent as README.md, so subsequent clones get their upstream
// tracking branch configured automatically.
func seedRemote(t *testing.T, remoteDir, readmeContent string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "seed")
	runGitT(t, "", "clone", "-q", remoteDir, dir)
	runGitT(t, dir, "config", "user.email", "seed@example.com")
	runGitT(t, dir, "config", "user.name", "seed")
	writeFile(t, dir, "README.md", readmeContent)
	runGitT(t, dir, "add", "README.md")
	runGitT(t, dir, "commit", "-q", "-m", "seed")
	runGitT(t, dir, "push", "-q", "-u", "origin", "main")
}

// cloneWorkdir clones remoteDir (which must already have at least one
// commit, via seedRemote) into a fresh working directory configured with a
// throwaway test identity.
func cloneWorkdir(t *testing.T, remoteDir, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	runGitT(t, "", "clone", "-q", remoteDir, dir)
	runGitT(t, dir, "config", "user.email", name+"@example.com")
	runGitT(t, dir, "config", "user.name", name)
	return dir
}

// writeFile writes a fixture file inside a test working directory.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

// runGitT runs git for test setup/assertions (not the code under test),
// failing the test immediately on a non-zero exit.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s (dir=%s): %v: %s", strings.Join(args, " "), dir, err, out.String())
	}
	return out.String()
}

// fakeAgentsviewBinary writes an executable shell-script fixture standing
// in for the real agentsview binary, answering both `session list --json`
// (with a single active agent) and `usage daily --json --breakdown
// --offline ...` (with a single fixed usage row), mirroring
// internal/agentsview's fakeAgentsview/fakeMultiAgentBinary fixture
// convention. The script ignores --agent/--since args entirely: since
// ListActiveAgents always returns exactly this one agent, Resolve only ever
// asks this fixture for that same agent's usage.
func fakeAgentsviewBinary(t *testing.T, agent, model, date string, tokens int64, cost float64) string {
	t.Helper()
	sessionJSON := fmt.Sprintf(`{"sessions": [{"agent": %q}], "next_cursor": ""}`, agent)
	// Real usage-daily schema (see internal/agentsview/testdata): nested
	// modelBreakdowns, not a flat agent/model/tokens row. All of tokens is
	// modeled as inputTokens (outputTokens: 0) since only their sum matters
	// to DailyRow.Tokens.
	usageJSON := fmt.Sprintf(
		`{"daily": [{"date": %q, "modelBreakdowns": [{"modelName": %q, "inputTokens": %d, "outputTokens": 0, "cost": %v}]}], "totals": {"inputTokens": %d, "outputTokens": 0, "totalCost": %v}}`,
		date, model, tokens, cost, tokens, cost,
	)

	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"session\" ]; then\n" +
		"  cat <<'EOF'\n" + sessionJSON + "\nEOF\n" +
		"  exit 0\n" +
		"fi\n" +
		"cat <<'EOF'\n" + usageJSON + "\nEOF\n"

	path := filepath.Join(t.TempDir(), "agentsview")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// TestRun_EndToEnd_SoloAdopterRefresh covers F1: a solo adopter's run
// resolves usage, writes a snapshot, renders the card into the README, and
// publishes both to the remote.
func TestRun_EndToEnd_SoloAdopterRefresh(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	work := cloneWorkdir(t, remote, "solo")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-solo",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}

	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	// Verify against a *fresh* clone of the remote, so the assertions prove
	// the commit actually landed and was pushed, not just written locally.
	verify := cloneWorkdir(t, remote, "verify")

	rows, err := snapshot.Read(verify, "machine-solo")
	if err != nil {
		t.Fatalf("snapshot.Read() error = %v, want nil", err)
	}
	wantRows := []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1000, Cost: 1.5},
	}
	if len(rows) != len(wantRows) || rows[0] != wantRows[0] {
		t.Errorf("snapshot rows = %+v, want %+v", rows, wantRows)
	}

	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)
	if !strings.Contains(got, "Tokens:") {
		t.Errorf("README = %q, want rendered card content containing \"Tokens:\"", got)
	}
	if !strings.Contains(got, "claude-sonnet-5") {
		t.Errorf("README missing model breakdown entry:\n%s", got)
	}
	if strings.Contains(got, "placeholder") {
		t.Errorf("README still contains the pre-run placeholder, want it replaced:\n%s", got)
	}

	log := runGitT(t, verify, "log", "--oneline")
	if !strings.Contains(log, "token-profile") {
		t.Errorf("git log = %q, want the run's commit to have landed on the remote", log)
	}
}

// TestRun_MultiMachineMerge covers F2: a second machine's run must merge
// cleanly with the first machine's already-pushed snapshot, so the rendered
// card reflects combined totals from both machines, and both snapshot files
// end up committed to the remote.
func TestRun_MultiMachineMerge(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	workA := cloneWorkdir(t, remote, "machineA")
	binA := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	depsA := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: binA},
		MachineID: "machine-a",
		Now:       asOf,
		RepoDir:   workA,
	}
	if err := Run(t.Context(), depsA); err != nil {
		t.Fatalf("Run() on machine A error = %v, want nil", err)
	}

	// Machine B clones the remote *after* A has already pushed, so its
	// clone starts out carrying A's snapshot and README update already —
	// mirroring a real second machine joining later.
	workB := cloneWorkdir(t, remote, "machineB")
	binB := fakeAgentsviewBinary(t, "codex", "gpt-5.4", "2026-06-21", 500, 0.75)
	depsB := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: binB},
		MachineID: "machine-b",
		Now:       asOf,
		RepoDir:   workB,
	}
	if err := Run(t.Context(), depsB); err != nil {
		t.Fatalf("Run() on machine B error = %v, want nil", err)
	}

	verify := cloneWorkdir(t, remote, "verify")

	if _, err := snapshot.Read(verify, "machine-a"); err != nil {
		t.Errorf("snapshot.Read(machine-a) error = %v, want machine A's snapshot still present after B's run", err)
	}
	if _, err := snapshot.Read(verify, "machine-b"); err != nil {
		t.Errorf("snapshot.Read(machine-b) error = %v, want machine B's snapshot committed", err)
	}

	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)

	// 1000 (machine A) + 500 (machine B) = 1,500 combined tokens. If the
	// merge only reflected machine B's own snapshot, this combined total
	// would be absent and only "500" would appear.
	if !strings.Contains(got, "1,500") {
		t.Errorf("README missing combined total \"1,500\" tokens (want merge across both machines):\n%s", got)
	}
	if !strings.Contains(got, "claude-sonnet-5") || !strings.Contains(got, "gpt-5.4") {
		t.Errorf("README missing one of both machines' models in the breakdown:\n%s", got)
	}
}

// TestRun_ReadmeMissingMarkers_SurfacesErrMarkersMissing covers the error
// path: a target repo whose README lacks the token-profile markers must
// fail with an actionable error surfacing readme.ErrMarkersMissing, rather
// than panicking or silently skipping the README update.
func TestRun_ReadmeMissingMarkers_SurfacesErrMarkersMissing(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, unmarkedReadme)

	work := cloneWorkdir(t, remote, "solo")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-solo",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}

	err := Run(t.Context(), deps)
	if err == nil {
		t.Fatal("Run() error = nil, want an error when the README lacks token-profile markers")
	}
	if !errors.Is(err, readme.ErrMarkersMissing) {
		t.Errorf("Run() error = %v, want it to wrap readme.ErrMarkersMissing", err)
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("Run() error = %q, want guidance to run `token-profile init`", err.Error())
	}
}
