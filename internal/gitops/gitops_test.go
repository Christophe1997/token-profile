package gitops

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNonFastForwardRejection(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "rejected due to remote work not fetched yet",
			stderr: "To ../remote.git\n ! [rejected]        main -> main (fetch first)\nerror: failed to push some refs to '../remote.git'\n",
			want:   true,
		},
		{
			name:   "rejected as non-fast-forward after a failed rebase attempt",
			stderr: "To ../remote.git\n ! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs to '../remote.git'\n",
			want:   true,
		},
		{
			name:   "no remote configured at all",
			stderr: "fatal: 'origin' does not appear to be a git repository\nfatal: Could not read from remote repository.\n",
			want:   false,
		},
		{
			name:   "remote-side policy rejection (e.g. pre-receive hook decline), not a fast-forward issue",
			stderr: "remote: policy violation\nTo ../remote.git\n ! [remote rejected] main -> main (pre-receive hook declined)\nerror: failed to push some refs to '../remote.git'\n",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNonFastForwardRejection(tt.stderr); got != tt.want {
				t.Errorf("isNonFastForwardRejection(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// initBareRemote creates a bare repo standing in for the shared GitHub
// remote and returns its path.
func initBareRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	runGitT(t, "", "init", "--bare", "-q", "-b", "main", dir)
	return dir
}

// seedRemote bootstraps remoteDir with a single initial commit, so
// subsequent clones get their upstream tracking branch configured
// automatically (matching a real, already-existing GitHub repo).
func seedRemote(t *testing.T, remoteDir string) {
	t.Helper()
	dir := cloneWorkdirFromEmpty(t, remoteDir, "seed")
	writeFile(t, dir, "README.md", "# seed\n")
	runGitT(t, dir, "add", "README.md")
	runGitT(t, dir, "commit", "-q", "-m", "seed")
	runGitT(t, dir, "push", "-q", "-u", "origin", "main")
}

// cloneWorkdirFromEmpty clones an as-yet-empty bare remote, which leaves the
// clone on an unborn branch with no upstream tracking configured. Only used
// to bootstrap the initial seed commit.
func cloneWorkdirFromEmpty(t *testing.T, remoteDir, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	runGitT(t, "", "clone", "-q", remoteDir, dir)
	runGitT(t, dir, "config", "user.email", name+"@example.com")
	runGitT(t, dir, "config", "user.name", name)
	return dir
}

// cloneWorkdir clones remoteDir (which must already have at least one
// commit, via seedRemote) into a fresh working directory, configured with a
// throwaway test identity. The clone's branch automatically tracks its
// upstream, mirroring how an adopter's real `username/username` clone
// behaves.
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

func TestPublish_NoContention_CommitsAndPushes(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote)

	solo := cloneWorkdir(t, remote, "solo")
	writeFile(t, solo, "solo.txt", "hello from solo\n")

	if err := Publish(t.Context(), solo, []string{"solo.txt"}, "add solo file", nil); err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	verify := cloneWorkdir(t, remote, "verify")
	got, err := os.ReadFile(filepath.Join(verify, "solo.txt"))
	if err != nil {
		t.Fatalf("ReadFile(solo.txt) error = %v", err)
	}
	if string(got) != "hello from solo\n" {
		t.Errorf("remote solo.txt = %q, want %q", got, "hello from solo\n")
	}
}

func TestPublish_ConcurrentPush_RetriesAndSucceeds(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote)

	machineA := cloneWorkdir(t, remote, "machineA")
	machineB := cloneWorkdir(t, remote, "machineB")

	// Machine A commits and pushes first: no contention, succeeds immediately.
	writeFile(t, machineA, "a-file.txt", "a change\n")
	if err := Publish(t.Context(), machineA, []string{"a-file.txt"}, "a change", nil); err != nil {
		t.Fatalf("Publish() on machineA error = %v, want nil", err)
	}

	// Machine B's clone is now behind the remote. Its first push attempt
	// should be rejected (non-fast-forward); Publish's retry loop must
	// fetch, rebase onto A's commit, and succeed on the retried push.
	writeFile(t, machineB, "b-file.txt", "b change\n")
	if err := Publish(t.Context(), machineB, []string{"b-file.txt"}, "b change", nil); err != nil {
		t.Fatalf("Publish() on machineB error = %v, want nil (should retry past the rejected push)", err)
	}

	verify := cloneWorkdir(t, remote, "verify")
	for name, want := range map[string]string{
		"a-file.txt": "a change\n",
		"b-file.txt": "b change\n",
	} {
		got, err := os.ReadFile(filepath.Join(verify, name))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v, want the remote to have both machines' changes", name, err)
		}
		if string(got) != want {
			t.Errorf("remote %s = %q, want %q", name, got, want)
		}
	}
}

// installAlwaysRejectingHook installs a pre-receive hook on remoteDir that
// unconditionally rejects every future push, echoing a message shaped like
// git's own non-fast-forward rejection. This deterministically forces
// Publish's retry loop to exhaust all attempts (each fetch/rebase is a
// no-op since the remote never actually accepts anything), without relying
// on a timing race against a concurrent adversary process.
func installAlwaysRejectingHook(t *testing.T, remoteDir string) {
	t.Helper()
	hook := "#!/bin/sh\necho ' ! [rejected]        main -> main (fetch first)' >&2\nexit 1\n"
	hookPath := filepath.Join(remoteDir, "hooks", "pre-receive")
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatalf("WriteFile(pre-receive hook) error = %v", err)
	}
}

// TestPublish_RegenerateCalledAfterRebase_ReflectsInFinalPush covers Fix 1's
// core mechanism: when a rejected push forces a fetch+rebase, Publish must
// call regenerate exactly once after that rebase succeeds, re-stage the
// files it touches, and fold the result into the same (amended) commit, so
// the eventual successful push carries the regenerated content rather than
// whatever was committed before the rebase pulled in new remote data.
func TestPublish_RegenerateCalledAfterRebase_ReflectsInFinalPush(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote)

	machineA := cloneWorkdir(t, remote, "machineA")
	machineB := cloneWorkdir(t, remote, "machineB")

	writeFile(t, machineA, "a-file.txt", "a change\n")
	if err := Publish(t.Context(), machineA, []string{"a-file.txt"}, "a change", nil); err != nil {
		t.Fatalf("Publish() on machineA error = %v, want nil", err)
	}

	// Machine B's clone is now behind, so its first push attempt is
	// rejected and Publish must fetch+rebase before retrying. regenerate
	// simulates re-deriving b-file.txt's content from the freshly-rebased
	// state (e.g. a re-render that now accounts for machine A's data).
	writeFile(t, machineB, "b-file.txt", "b change (stale)\n")
	regenerateCalls := 0
	regenerate := func() error {
		regenerateCalls++
		return os.WriteFile(filepath.Join(machineB, "b-file.txt"), []byte("b change (regenerated)\n"), 0o644)
	}
	if err := Publish(t.Context(), machineB, []string{"b-file.txt"}, "b change", regenerate); err != nil {
		t.Fatalf("Publish() on machineB error = %v, want nil (should retry past the rejected push)", err)
	}
	if regenerateCalls != 1 {
		t.Errorf("regenerate called %d times, want exactly 1 (once after the rebase pulled in machine A's push)", regenerateCalls)
	}

	verify := cloneWorkdir(t, remote, "verify")
	got, err := os.ReadFile(filepath.Join(verify, "b-file.txt"))
	if err != nil {
		t.Fatalf("ReadFile(b-file.txt) error = %v", err)
	}
	if string(got) != "b change (regenerated)\n" {
		t.Errorf("pushed b-file.txt = %q, want the regenerated content (proving regenerate's output was re-staged and pushed, not the stale pre-rebase commit)", got)
	}

	// A rebase-then-amend keeps a single commit for machine B's change
	// rather than stacking a second one on top.
	count := strings.TrimSpace(runGitT(t, verify, "rev-list", "--count", "HEAD"))
	if count != "3" {
		t.Errorf("git rev-list --count HEAD = %s, want 3 (seed + machineA's commit + machineB's single amended commit)", count)
	}
}

// TestPublish_NilRegenerate_BehavesAsBefore covers the nil-tolerant default:
// passing a nil Regenerate must behave exactly as Publish did before Fix 1
// introduced the parameter — no regeneration step, just the ordinary
// fetch-rebase-retry loop.
func TestPublish_NilRegenerate_BehavesAsBefore(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote)

	machineA := cloneWorkdir(t, remote, "machineA")
	machineB := cloneWorkdir(t, remote, "machineB")

	writeFile(t, machineA, "a-file.txt", "a change\n")
	if err := Publish(t.Context(), machineA, []string{"a-file.txt"}, "a change", nil); err != nil {
		t.Fatalf("Publish() on machineA error = %v, want nil", err)
	}

	writeFile(t, machineB, "b-file.txt", "b change\n")
	if err := Publish(t.Context(), machineB, []string{"b-file.txt"}, "b change", nil); err != nil {
		t.Fatalf("Publish() on machineB error = %v, want nil (should retry past the rejected push)", err)
	}

	verify := cloneWorkdir(t, remote, "verify")
	for name, want := range map[string]string{
		"a-file.txt": "a change\n",
		"b-file.txt": "b change\n",
	} {
		got, err := os.ReadFile(filepath.Join(verify, name))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v, want the remote to have both machines' changes", name, err)
		}
		if string(got) != want {
			t.Errorf("remote %s = %q, want %q", name, got, want)
		}
	}
}

func TestPublish_RetriesExhausted_PreservesLocalCommit(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote)

	work := cloneWorkdir(t, remote, "work")
	installAlwaysRejectingHook(t, remote)
	writeFile(t, work, "work-file.txt", "work change\n")

	err := Publish(t.Context(), work, []string{"work-file.txt"}, "work change", nil)
	if err == nil {
		t.Fatal("Publish() error = nil, want an error after exhausting retries")
	}
	if !errors.Is(err, ErrPushExhausted) {
		t.Errorf("Publish() error = %v, want it to wrap ErrPushExhausted", err)
	}

	log := runGitT(t, work, "log", "--oneline")
	if !strings.Contains(log, "work change") {
		t.Errorf("git log = %q, want it to still contain the local commit (no data loss)", log)
	}

	status := runGitT(t, work, "status", "--porcelain")
	if status != "" {
		t.Errorf("git status --porcelain = %q, want empty (working tree clean, commit intact)", status)
	}
}
