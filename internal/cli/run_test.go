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

// TestRun_AccumulatesHistoryAcrossRuns covers end-to-end history
// accumulation: a later run whose agentsview window no longer covers an
// earlier run's day must not drop that day from the machine's published
// snapshot — the two runs' disjoint days both survive on the remote.
func TestRun_AccumulatesHistoryAcrossRuns(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)
	work := cloneWorkdir(t, remote, "accum")

	firstBin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-05-01", 1000, 1.5)
	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: firstBin},
		MachineID: "machine-a",
		Now:       time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("first Run() error = %v, want nil", err)
	}

	// A later run: agentsview's own window has moved on and returns only a
	// new, disjoint day — 2026-05-01 is no longer in its response at all.
	secondBin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 500, 0.75)
	deps.Client = &agentsview.Client{BinaryName: secondBin}
	deps.Now = time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("second Run() error = %v, want nil", err)
	}

	verify := cloneWorkdir(t, remote, "verify-accum")
	rows, err := snapshot.Read(verify, "machine-a")
	if err != nil {
		t.Fatalf("snapshot.Read() error = %v, want nil", err)
	}
	want := []snapshot.Row{
		{Date: "2026-05-01", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1000, Cost: 1.5},
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 500, Cost: 0.75},
	}
	if len(rows) != len(want) {
		t.Fatalf("snapshot rows = %+v, want %+v (both runs' disjoint days must survive)", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("snapshot rows[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// TestRun_RendersWindowOverWindowRateAfterAccumulation covers the other
// side of accumulated history: once a machine has data in both the current
// window and the immediately preceding one, the published card must show a
// window-over-window percentage change on tokens.
func TestRun_RendersWindowOverWindowRateAfterAccumulation(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)
	work := cloneWorkdir(t, remote, "rate")

	// First run lands in what will become the *previous* window relative
	// to the second run's Now (50 days later, within a 60-day/2-window span
	// of the default 30-day trailing window).
	firstBin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-05-01", 1000, 10.0)
	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: firstBin},
		MachineID: "machine-a",
		Now:       time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("first Run() error = %v, want nil", err)
	}

	secondBin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1500, 15.0)
	deps.Client = &agentsview.Client{BinaryName: secondBin}
	deps.Now = time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	deps.Stdout = &buf
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("second Run() error = %v, want nil", err)
	}

	got := buf.String()
	if !strings.Contains(got, "(+50%)") {
		t.Errorf("Run() Stdout = %q, want it to contain the window-over-window rate \"(+50%%)\" (1500 vs previous 1000)", got)
	}

	verify := cloneWorkdir(t, remote, "verify-rate")
	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	if !strings.Contains(string(readmeBytes), "(+50%)") {
		t.Errorf("README does not contain the window-over-window rate \"(+50%%)\":\n%s", readmeBytes)
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

// installRacingPeerHook installs a post-commit hook in workDir's
// .git/hooks that pushes peerWorkDir's already-committed-but-not-yet-pushed
// commit immediately after workDir's own local commit lands. This is
// deliberately a post-commit hook rather than a pre-push hook: a pre-push
// hook fires *after* git has already snapshotted the remote's old ref
// value for its own negotiation, so injecting a competing push from inside
// one produces a protocol-level "incorrect old value provided" rejection
// (classified as non-retryable by isNonFastForwardRejection, same as a
// server-side policy rejection) rather than the ordinary client-side
// "(fetch first)" rejection a real independent race produces. A
// post-commit hook fires as part of the earlier `git commit` invocation —
// strictly before workDir's push subprocess is ever spawned — so
// peerWorkDir's push fully lands first, and workDir's own later push
// negotiates against the truly current remote state, reproducing the
// exact rejection a genuine multi-machine race produces (verified
// empirically against real git 2.55: a pre-push-hook-based version of this
// same setup reliably produced the wrong, non-retryable rejection).
func installRacingPeerHook(t *testing.T, workDir, peerWorkDir string) {
	t.Helper()
	hookDir := filepath.Join(workDir, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", hookDir, err)
	}
	// Idempotent by construction: once peerWorkDir's commit has landed, a
	// repeat invocation (e.g. triggered again by the retry's rebase, which
	// also creates a commit) is a push with nothing new, which git no-ops
	// rather than erroring on.
	script := fmt.Sprintf("#!/bin/sh\ngit -C %q push -q >/dev/null 2>&1 || true\n", peerWorkDir)
	hookPath := filepath.Join(hookDir, "post-commit")
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(post-commit hook) error = %v", err)
	}
}

// TestRun_MultiMachineMerge_RaceDuringRetry covers the P1 regression: a
// genuine race where another machine's snapshot lands on the remote in the
// exact window between this machine's local commit and its push attempt,
// forcing gitops.Publish's fetch-rebase-retry path. Unlike
// TestRun_MultiMachineMerge (non-racing: machine B clones only after A has
// already pushed, so B's own initial merge already sees A's snapshot), this
// test forces machine A's push to land strictly inside machine B's
// commit-to-push window via a local pre-push hook. The FINAL pushed README
// must reflect BOTH machines' data — proving Run() regenerates the merged
// view after the retry's rebase, rather than publishing the stale
// pre-rebase render (the exact bug Fix 1 addresses).
//
// Machine A's commit here only adds its own snapshot file — it
// deliberately does not also touch README.md, mirroring the bug report's
// own framing ("another machine pushed a new snapshot file in the
// meantime"). A concurrent README edit from both machines' starting from
// the same base is its own, orthogonal merge-conflict concern (verified
// separately: two independent full-card renders from the same base do
// generate a real git conflict, not a silent bad merge); what this test
// isolates is the narrower, definitely-reachable case where the rebase
// itself succeeds cleanly and *still* leaves a stale README unless
// something recomputes it.
func TestRun_MultiMachineMerge_RaceDuringRetry(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	asOf := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	workA := cloneWorkdir(t, remote, "machineA")
	if err := snapshot.Write(workA, "machine-a", []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1000, Cost: 1.5},
	}); err != nil {
		t.Fatalf("snapshot.Write(machine-a) error = %v", err)
	}
	runGitT(t, workA, "add", filepath.Join(".token-profile", "snapshots", "machine-a.json"))
	runGitT(t, workA, "commit", "-q", "-m", "machine-a snapshot")

	workB := cloneWorkdir(t, remote, "machineB")
	installRacingPeerHook(t, workB, workA)

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
		t.Errorf("snapshot.Read(machine-a) error = %v, want machine A's snapshot present", err)
	}
	if _, err := snapshot.Read(verify, "machine-b"); err != nil {
		t.Errorf("snapshot.Read(machine-b) error = %v, want machine B's snapshot committed", err)
	}

	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)

	// 1000 (machine A, learned about only via the retry's rebase) + 500
	// (machine B) = 1,500 combined tokens. Before Fix 1, the pushed README
	// still reflects only B's pre-rebase render (500 tokens only), since
	// the retry never recomputes it after the rebase pulls in A's data.
	if !strings.Contains(got, "1,500") {
		t.Errorf("README missing combined total \"1,500\" tokens after the race (want the retry path to regenerate before the retried push):\n%s", got)
	}
	if !strings.Contains(got, "claude-sonnet-5") || !strings.Contains(got, "gpt-5.4") {
		t.Errorf("README missing one of both machines' models in the breakdown after the race:\n%s", got)
	}
}

// TestRun_TargetRepoNotGitRepo_FailsFast covers Fix 3: a RepoDir that
// exists but isn't a git working tree (e.g. a typo'd targetRepo pointing at
// a plain directory) must fail fast with an actionable error, and — the
// concrete proof the check runs *before* any write, not after — must leave
// no .token-profile directory behind at all.
func TestRun_TargetRepoNotGitRepo_FailsFast(t *testing.T) {
	dir := t.TempDir()
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-solo",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   dir,
	}

	err := Run(t.Context(), deps)
	if err == nil {
		t.Fatal("Run() error = nil, want an error when RepoDir isn't a git repository")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Run() error = %q, want it to explain RepoDir isn't a git repository", err.Error())
	}
	if !strings.Contains(err.Error(), "targetRepo") {
		t.Errorf("Run() error = %q, want actionable guidance mentioning %q", err.Error(), "targetRepo")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".token-profile")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(.token-profile) error = %v, want os.ErrNotExist (no writes before the git-repo check runs)", statErr)
	}
}

// TestRun_TargetRepoDoesNotExist_FailsFast covers Fix 3's other edge case:
// a RepoDir that doesn't exist on disk at all must produce the same clear,
// actionable failure rather than a confusing "no such file" error from deep
// inside snapshot.Write or gitops.Publish.
func TestRun_TargetRepoDoesNotExist_FailsFast(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-solo",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   dir,
	}

	err := Run(t.Context(), deps)
	if err == nil {
		t.Fatal("Run() error = nil, want an error when RepoDir doesn't exist")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Run() error = %q, want it to explain RepoDir isn't a git repository", err.Error())
	}
	if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(RepoDir) error = %v, want os.ErrNotExist (RepoDir itself must not be created as a side effect)", statErr)
	}
}

// TestRun_Success_PrintsHeadlineAndCommitHash covers the fix: a successful
// Run must print a one-line confirmation to Stdout — the headline summary
// plus the just-published commit's short hash — so an adopter running the
// command sees what was published rather than silence.
func TestRun_Success_PrintsHeadlineAndCommitHash(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	work := cloneWorkdir(t, remote, "solo")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	var buf bytes.Buffer
	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-solo",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
		Stdout:    &buf,
	}

	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	hash := strings.TrimSpace(runGitT(t, work, "rev-parse", "--short", "HEAD"))
	got := buf.String()
	if !strings.Contains(got, "Tokens:") {
		t.Errorf("Run() Stdout = %q, want it to contain %q", got, "Tokens:")
	}
	if !strings.Contains(got, "Streak:") {
		t.Errorf("Run() Stdout = %q, want it to contain %q", got, "Streak:")
	}
	if !strings.Contains(got, "published as") {
		t.Errorf("Run() Stdout = %q, want it to contain %q", got, "published as")
	}
	if !strings.Contains(got, hash) {
		t.Errorf("Run() Stdout = %q, want it to contain the published commit's short hash %q", got, hash)
	}
}

// TestFenceCard verifies fenceCard wraps its input in a plain (no language
// tag) fenced code block with no trailing newline after the closing fence,
// matching README.md's own Quick Start example.
func TestFenceCard(t *testing.T) {
	got := fenceCard("a\nb")
	want := "```\na\nb\n```"
	if got != want {
		t.Errorf("fenceCard() = %q, want %q", got, want)
	}
}

// TestRun_EndToEnd_CardIsFenced covers the bug fix: the injected card must
// be wrapped in a plain fenced code block, so GitHub/CommonMark rendering
// preserves its box-drawing characters and column alignment verbatim
// instead of mangling them as inline markdown.
func TestRun_EndToEnd_CardIsFenced(t *testing.T) {
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

	verify := cloneWorkdir(t, remote, "verify")
	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)

	startIdx := strings.Index(got, readme.StartMarker)
	endIdx := strings.Index(got, readme.EndMarker)
	if startIdx == -1 || endIdx == -1 || endIdx < startIdx {
		t.Fatalf("README missing token-profile markers:\n%s", got)
	}
	injected := strings.TrimSpace(got[startIdx+len(readme.StartMarker) : endIdx])

	if !strings.HasPrefix(injected, "```") {
		t.Errorf("injected content = %q, want it to start with a fenced code block", injected)
	}
	if !strings.HasSuffix(injected, "```") {
		t.Errorf("injected content = %q, want it to end with a fenced code block", injected)
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
