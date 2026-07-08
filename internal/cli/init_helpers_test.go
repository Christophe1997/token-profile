package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Christophe1997/token-profile/internal/config"
)

func TestIsInteractive_NonFileReader_ReturnsFalse(t *testing.T) {
	if isInteractive(strings.NewReader("y\n")) {
		t.Error("isInteractive(strings.Reader) = true, want false")
	}
}

// TestIsInteractive_RegularFile_ReturnsFalse covers a real *os.File that
// isn't a character device (unlike a terminal) — e.g. redirecting stdin
// from a plain file, as a scheduled cron/launchd invocation effectively
// does.
func TestIsInteractive_RegularFile_ReturnsFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("y\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	regular, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer regular.Close()

	if isInteractive(regular) {
		t.Error("isInteractive(regular file) = true, want false")
	}
}

// TestGitGlobalUserName_Configured_ReturnsIt and
// TestGitGlobalUserName_Unset_ReturnsEmpty both point git's --global config
// at a scratch HOME (via t.Setenv, auto-restored) so they never read or
// mutate the real developer machine's ~/.gitconfig.
func TestGitGlobalUserName_Configured_ReturnsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runGitT(t, "", "config", "--global", "user.name", "octocat")

	if got := gitGlobalUserName(t.Context()); got != "octocat" {
		t.Errorf("gitGlobalUserName() = %q, want %q", got, "octocat")
	}
}

func TestGitGlobalUserName_Unset_ReturnsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := gitGlobalUserName(t.Context()); got != "" {
		t.Errorf("gitGlobalUserName() = %q, want empty", got)
	}
}

func TestValidAutoCloneName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{`a\b`, false},
		{"a..b", false},
		{"octocat", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validAutoCloneName(tt.name); got != tt.want {
				t.Errorf("validAutoCloneName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestProfileRepoURL(t *testing.T) {
	tests := []struct {
		protocol config.CloneProtocol
		want     string
		wantErr  bool
	}{
		{config.CloneProtocolSSH, "git@github.com:octocat/octocat.git", false},
		{config.CloneProtocolHTTPS, "https://github.com/octocat/octocat.git", false},
		{"bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.protocol), func(t *testing.T) {
			got, err := profileRepoURL(tt.protocol, "octocat")
			if (err != nil) != tt.wantErr {
				t.Fatalf("profileRepoURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("profileRepoURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfirmYesNo(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase y", "y\n", true},
		{"uppercase Y", "Y\n", true},
		{"lowercase yes", "yes\n", true},
		{"uppercase YES", "YES\n", true},
		{"y with no trailing newline", "y", true},
		{"n", "n\n", false},
		{"empty line", "\n", false},
		{"garbage", "maybe\n", false},
		{"no input at all", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			got := confirmYesNo(strings.NewReader(tt.input), &stdout, "Proceed?")
			if got != tt.want {
				t.Errorf("confirmYesNo(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestConfirmYesNo_NilStdin_ReturnsFalse covers offerScheduleRegistration's
// own defensive use: a nil deps.Stdin (e.g. a test InitDeps literal that
// never sets it) must read as a plain "no" rather than panicking on a nil
// io.Reader.
func TestConfirmYesNo_NilStdin_ReturnsFalse(t *testing.T) {
	if confirmYesNo(nil, &bytes.Buffer{}, "Proceed?") {
		t.Error("confirmYesNo(nil stdin) = true, want false")
	}
}

func TestConfirmYesNo_PromptContainsQuestion(t *testing.T) {
	var stdout bytes.Buffer
	confirmYesNo(strings.NewReader("n\n"), &stdout, "Register the refresh schedule now?")

	if !strings.Contains(stdout.String(), "Register the refresh schedule now?") {
		t.Errorf("confirmYesNo() prompt = %q, want it to contain the question", stdout.String())
	}
}

// TestRequireConfigOrTTY_NoConfigNoTTY_ReturnsActionableErrorNothingWritten
// covers R5/AE1: a config-needing command (run, or the guided-init entry
// point) invoked with no config file yet and no interactive terminal must
// fail immediately, naming the missing path and pointing at interactive
// init, without writing anything to disk. requireConfigOrTTY is tested
// directly (rather than through NewRunCmd's cobra wiring) since a plain `go
// test` run can't deterministically control the real process's os.Stdin
// TTY-ness.
func TestRequireConfigOrTTY_NoConfigNoTTY_ReturnsActionableErrorNothingWritten(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	err := requireConfigOrTTY(configPath, false)
	if err == nil {
		t.Fatal("requireConfigOrTTY() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), configPath) {
		t.Errorf("requireConfigOrTTY() error = %q, want it to mention the config path %q", err.Error(), configPath)
	}
	if !strings.Contains(err.Error(), "init") {
		t.Errorf("requireConfigOrTTY() error = %q, want it to point at interactive init", err.Error())
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(config) error = %v, want os.ErrNotExist (nothing written)", statErr)
	}
}

func TestRequireConfigOrTTY_ConfigExists_ReturnsNilRegardlessOfTTY(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.json", `{}`)
	configPath := filepath.Join(dir, "config.json")

	if err := requireConfigOrTTY(configPath, false); err != nil {
		t.Errorf("requireConfigOrTTY() error = %v, want nil when a config file already exists", err)
	}
}

func TestRequireConfigOrTTY_NoConfigButInteractive_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	if err := requireConfigOrTTY(configPath, true); err != nil {
		t.Errorf("requireConfigOrTTY() error = %v, want nil when interactive", err)
	}
}

// TestResolveInitConfig_ConfigAlreadyExists_DelegatesStraightToLoad covers
// the edge case: a config file already sitting at ConfigPath must be loaded
