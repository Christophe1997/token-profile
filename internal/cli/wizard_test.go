package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Christophe1997/token-profile/internal/config"
)

// scriptedInput wraps lines into an io.Reader suitable for driving huh's
// accessible mode across multiple fields. Each field's RunAccessible builds
// its own bufio.Scanner over the same reader; a plain strings.Reader lets
// the first field's Scanner read-ahead and drain the whole remaining
// script in one Read call (its buffer defaults to 4096 bytes), silently
// starving every later field. Wrapping with iotest.OneByteReader caps every
// Read at one byte, so each field's Scanner only ever consumes the bytes it
// actually asks for.
func scriptedInput(lines ...string) io.Reader {
	return iotest.OneByteReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func TestRunWizard_HappyPath_ScriptedInputReturnsResult(t *testing.T) {
	deps := WizardDeps{
		GitUserName: func(_ context.Context) string { return "octocat" },
		Accessible:  true,
		Input:       scriptedInput("myhandle", "2", "/tmp/wizard-test/custom", "y"),
		Output:      &bytes.Buffer{},
	}

	got, err := RunWizard(t.Context(), deps)
	if err != nil {
		t.Fatalf("RunWizard() error = %v, want nil", err)
	}

	want := WizardResult{
		RepoName:      "myhandle",
		CloneProtocol: config.CloneProtocolSSH,
		LocalPath:     "/tmp/wizard-test/custom",
	}
	if got != want {
		t.Errorf("RunWizard() = %+v, want %+v", got, want)
	}
}

func TestRunWizard_DefaultsAcceptedUnchanged_ResolvableGitUserName(t *testing.T) {
	deps := WizardDeps{
		GitUserName: func(_ context.Context) string { return "octocat" },
		Accessible:  true,
		Input:       scriptedInput("", "", "", "y"),
		Output:      &bytes.Buffer{},
	}

	got, err := RunWizard(t.Context(), deps)
	if err != nil {
		t.Fatalf("RunWizard() error = %v, want nil", err)
	}

	want := WizardResult{
		RepoName:      "octocat",
		CloneProtocol: config.CloneProtocolHTTPS,
		LocalPath:     defaultStateFile(filepath.Join("repos", "octocat")),
	}
	if got != want {
		t.Errorf("RunWizard() = %+v, want %+v", got, want)
	}
}

func TestRunWizard_UnresolvableGitUserName_StartsBlankButStillFunctions(t *testing.T) {
	deps := WizardDeps{
		GitUserName: func(_ context.Context) string { return "" },
		Accessible:  true,
		// Blank submissions for repo/local path prove the pre-filled
		// defaults were themselves blank -- a non-blank default would
		// have been adopted instead (see PromptString's cmp.Or fallback).
		Input:  scriptedInput("", "", "", "y"),
		Output: &bytes.Buffer{},
	}

	got, err := RunWizard(t.Context(), deps)
	if err != nil {
		t.Fatalf("RunWizard() error = %v, want nil", err)
	}

	want := WizardResult{
		RepoName:      "",
		CloneProtocol: config.CloneProtocolHTTPS,
		LocalPath:     "",
	}
	if got != want {
		t.Errorf("RunWizard() = %+v, want %+v", got, want)
	}
}

func TestRunWizard_InvalidGitHubUsernameShape_ReturnsValidationError(t *testing.T) {
	deps := WizardDeps{
		GitUserName: func(_ context.Context) string { return "" },
		Accessible:  true,
		Input:       scriptedInput("John Smith", "1", "/tmp/wizard-test/dest", "y"),
		Output:      &bytes.Buffer{},
	}

	_, err := RunWizard(t.Context(), deps)
	if err == nil {
		t.Fatal("RunWizard() error = nil, want a validation error")
	}
	if errors.Is(err, ErrWizardCancelled) {
		t.Errorf("RunWizard() error = %v, want a validation error distinct from ErrWizardCancelled", err)
	}
}

func TestRunWizard_ConfirmDeclined_ReturnsCancellationSentinel(t *testing.T) {
	deps := WizardDeps{
		GitUserName: func(_ context.Context) string { return "octocat" },
		Accessible:  true,
		Input:       scriptedInput("alice", "1", "/tmp/wizard-test/dest", "n"),
		Output:      &bytes.Buffer{},
	}

	_, err := RunWizard(t.Context(), deps)
	if !errors.Is(err, ErrWizardCancelled) {
		t.Errorf("RunWizard() error = %v, want ErrWizardCancelled", err)
	}
}
