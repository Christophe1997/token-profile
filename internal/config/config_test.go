package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/config"
)

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := config.Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	want := config.Default()
	if cfg != want {
		t.Errorf("Load() = %+v, want defaults %+v", cfg, want)
	}
}

func TestLoad_ValidFileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"targetRepo": "/home/adopter/username",
		"breakdown": "per-tool",
		"trailingWindow": "168h",
		"breakdownLimit": 5,
		"machineIdPath": "/home/adopter/.token-profile/machine-id",
		"remoteRepo": "git@github.com:adopter/username.git",
		"cloneProtocol": "ssh",
		"scheduleInterval": "12h"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	want := config.Config{
		TargetRepo:       "/home/adopter/username",
		Breakdown:        config.BreakdownPerTool,
		TrailingWindow:   168 * time.Hour,
		BreakdownLimit:   5,
		MachineIDPath:    "/home/adopter/.token-profile/machine-id",
		RenderMode:       config.RenderModeSVG,
		RemoteRepo:       "git@github.com:adopter/username.git",
		CloneProtocol:    config.CloneProtocolSSH,
		ScheduleInterval: 12 * time.Hour,
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

// TestLoad_ExpandsTildeInTargetRepoAndMachineIDPath covers a hand-edited
// config.json using a shell-style "~/..." path: json.Unmarshal never passes
// it through a shell, so without expansion it would reach gitops/machineid
// as a literal directory named "~".
func TestLoad_ExpandsTildeInTargetRepoAndMachineIDPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"targetRepo": "~/code/username",
		"machineIdPath": "~/.token-profile/machine-id"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	wantTargetRepo := filepath.Join(home, "code", "username")
	if cfg.TargetRepo != wantTargetRepo {
		t.Errorf("Load() TargetRepo = %q, want %q", cfg.TargetRepo, wantTargetRepo)
	}
	wantMachineIDPath := filepath.Join(home, ".token-profile", "machine-id")
	if cfg.MachineIDPath != wantMachineIDPath {
		t.Errorf("Load() MachineIDPath = %q, want %q", cfg.MachineIDPath, wantMachineIDPath)
	}
}

// TestLoad_ResolvesRelativeTargetRepoToAbsoluteAtLoadTime covers a
// hand-edited config.json using a bare relative path: since it never
// passes through a shell, Load must anchor it to the process's own working
// directory at load time (the same anchor a shell would use), rather than
// leaving it relative and letting every downstream git/filepath call
// re-interpret it against whatever directory happens to be current then.
func TestLoad_ResolvesRelativeTargetRepoToAbsoluteAtLoadTime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"targetRepo": "relative/username"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	want := filepath.Join(wd, "relative", "username")
	if cfg.TargetRepo != want {
		t.Errorf("Load() TargetRepo = %q, want %q", cfg.TargetRepo, want)
	}
}

// TestResolvePath covers ResolvePath's own contract in isolation: a leading
// "~"/"~/" resolves against home, then anything still relative resolves
// against the process's current working directory, so the result is always
// either blank or absolute.
func TestResolvePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde slash path", "~/code/repo", filepath.Join(home, "code", "repo")},
		{"already absolute", "/already/absolute", "/already/absolute"},
		{"relative path anchors to cwd", "relative/path", filepath.Join(wd, "relative", "path")},
		{"dot anchors to cwd", ".", wd},
		{"blank stays blank", "", ""},
		{"tilde-user form anchors to cwd as a literal path", "~otheruser/path", filepath.Join(wd, "~otheruser", "path")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.ResolvePath(tt.in)
			if err != nil {
				t.Fatalf("ResolvePath(%q) error = %v, want nil", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ResolvePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestLoad_NegativeBreakdownLimitMeansUnlimited covers the sentinel: a
// negative breakdownLimit must load as-is (not rejected, not coerced to the
// default), since callers treat negative as "show every entry."
func TestLoad_NegativeBreakdownLimitMeansUnlimited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"breakdownLimit": -1}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BreakdownLimit != -1 {
		t.Errorf("BreakdownLimit = %d, want -1", cfg.BreakdownLimit)
	}
}

func TestLoad_MissingRenderModeDefaultsToSVG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"targetRepo": "/home/adopter/username"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.RenderMode != config.RenderModeSVG {
		t.Errorf("RenderMode = %q, want %q", cfg.RenderMode, config.RenderModeSVG)
	}
}

// TestLoad_MissingCloneProtocolAndScheduleIntervalDefaultFromDefault covers
// the same "unset key leaves Default()'s pre-populated value alone" rule
// TestLoad_MissingRenderModeDefaultsToSVG already exercises for RenderMode.
func TestLoad_MissingCloneProtocolAndScheduleIntervalDefaultFromDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"targetRepo": "/home/adopter/username"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.CloneProtocol != config.CloneProtocolHTTPS {
		t.Errorf("CloneProtocol = %q, want %q", cfg.CloneProtocol, config.CloneProtocolHTTPS)
	}
	if cfg.ScheduleInterval != config.DefaultScheduleInterval {
		t.Errorf("ScheduleInterval = %v, want %v", cfg.ScheduleInterval, config.DefaultScheduleInterval)
	}
}

func TestLoad_ExplicitAsciiRenderMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"renderMode": "ascii"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.RenderMode != config.RenderModeASCII {
		t.Errorf("RenderMode = %q, want %q", cfg.RenderMode, config.RenderModeASCII)
	}
}

// TestLoad_ScheduleIntervalDurationStringRoundTrips isolates the
// scheduleInterval-as-duration-string parsing this unit adds to
// UnmarshalJSON, mirroring TrailingWindow's own hand-edited-string
// round-trip (exercised together with other fields in
// TestLoad_ValidFileOverridesDefaults).
func TestLoad_ScheduleIntervalDurationStringRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"scheduleInterval": "12h"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.ScheduleInterval != 12*time.Hour {
		t.Errorf("ScheduleInterval = %v, want %v", cfg.ScheduleInterval, 12*time.Hour)
	}
}

func TestLoad_InvalidBreakdownIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"breakdown": "per-vibe"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want an error for invalid breakdown mode")
	}
}

func TestLoad_InvalidRenderModeIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"renderMode": "ansi-art"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want an error for invalid render mode")
	}
	for _, want := range []string{string(config.RenderModeSVG), string(config.RenderModeASCII)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %q, want it to name recognized render mode %q", err, want)
		}
	}
}

func TestLoad_InvalidScheduleIntervalIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"scheduleInterval": "5h"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want an error for an unsupported scheduleInterval")
	}
	for _, want := range []string{"1h", "2h", "3h", "4h", "6h", "8h", "12h", "24h"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %q, want it to name accepted divisor %q", err, want)
		}
	}
}

func TestLoad_InvalidCloneProtocolIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"cloneProtocol": "ftp"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want an error for an invalid clone protocol")
	}
	for _, want := range []string{string(config.CloneProtocolHTTPS), string(config.CloneProtocolSSH)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %q, want it to name recognized clone protocol %q", err, want)
		}
	}
}

func TestWriteTemplate_CreatesLoadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if err := config.WriteTemplate(path, config.TemplateFields{}); err != nil {
		t.Fatalf("WriteTemplate() error = %v, want nil", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.TargetRepo != "" {
		t.Errorf("TargetRepo = %q, want empty", cfg.TargetRepo)
	}
	if cfg.Breakdown != config.BreakdownPerModel {
		t.Errorf("Breakdown = %q, want %q", cfg.Breakdown, config.BreakdownPerModel)
	}
}

func TestWriteTemplate_ExplicitlyWritesRenderMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if err := config.WriteTemplate(path, config.TemplateFields{}); err != nil {
		t.Fatalf("WriteTemplate() error = %v, want nil", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `"renderMode": "svg"`) {
		t.Errorf("scaffolded config = %s, want it to explicitly include %q", data, `"renderMode": "svg"`)
	}
}

func TestWriteTemplate_RefusesToClobberExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if err := config.WriteTemplate(path, config.TemplateFields{}); err != nil {
		t.Fatalf("WriteTemplate() first call error = %v, want nil", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if err := config.WriteTemplate(path, config.TemplateFields{}); err == nil {
		t.Fatal("WriteTemplate() second call error = nil, want an error for an existing file")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("file contents changed after clobbered WriteTemplate(): before %q, after %q", before, after)
	}
}

func TestWriteTemplate_CreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "config.json")

	if err := config.WriteTemplate(path, config.TemplateFields{}); err != nil {
		t.Fatalf("WriteTemplate() error = %v, want nil", err)
	}

	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("ReadFile() error = %v, want the scaffolded file to be readable", err)
	}
}

func TestWriteTemplate_NonEmptyTargetRepo_RoundTripsThroughLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	const targetRepo = "/home/adopter/.token-profile/repos/octocat"

	if err := config.WriteTemplate(path, config.TemplateFields{TargetRepo: targetRepo}); err != nil {
		t.Fatalf("WriteTemplate() error = %v, want nil", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.TargetRepo != targetRepo {
		t.Errorf("TargetRepo = %q, want %q", cfg.TargetRepo, targetRepo)
	}
	if cfg.Breakdown != config.BreakdownPerModel {
		t.Errorf("Breakdown = %q, want %q", cfg.Breakdown, config.BreakdownPerModel)
	}
}

func TestWriteTemplate_TargetRepoWithBackslashesAndQuotes_RoundTripsThroughLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// An absolute path (ResolvePath is a no-op on it) still carrying the
	// backslashes/quotes raw string templating would have corrupted.
	const targetRepo = `/home/you/weird\code\"you"`

	if err := config.WriteTemplate(path, config.TemplateFields{TargetRepo: targetRepo}); err != nil {
		t.Fatalf("WriteTemplate() error = %v, want nil", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.TargetRepo != targetRepo {
		t.Errorf("TargetRepo = %q, want %q (raw string templating would have corrupted embedded backslashes/quotes)", cfg.TargetRepo, targetRepo)
	}
}

// TestWriteTemplate_ZeroValueCloneProtocolAndScheduleInterval_RoundTripsThroughLoad
// covers the freshly-scaffolded-config integration scenario: a caller that
// hasn't collected RemoteRepo/CloneProtocol/ScheduleInterval yet (the
// TemplateFields zero value) still gets a file that loads and validates
// cleanly, with the same defaults Default() would apply.
func TestWriteTemplate_ZeroValueCloneProtocolAndScheduleInterval_RoundTripsThroughLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if err := config.WriteTemplate(path, config.TemplateFields{TargetRepo: "/home/adopter/username"}); err != nil {
		t.Fatalf("WriteTemplate() error = %v, want nil", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
	if cfg.CloneProtocol != config.CloneProtocolHTTPS {
		t.Errorf("CloneProtocol = %q, want %q", cfg.CloneProtocol, config.CloneProtocolHTTPS)
	}
	if cfg.ScheduleInterval != config.DefaultScheduleInterval {
		t.Errorf("ScheduleInterval = %v, want %v", cfg.ScheduleInterval, config.DefaultScheduleInterval)
	}
	if cfg.RemoteRepo != "" {
		t.Errorf("RemoteRepo = %q, want empty", cfg.RemoteRepo)
	}
}

// TestWriteTemplate_WizardCollectedFields_RoundTripThroughLoad covers the
// non-default case: a wizard that collected all three new fields gets them
// back verbatim after Load(), and the scaffolded file validates cleanly.
func TestWriteTemplate_WizardCollectedFields_RoundTripThroughLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	fields := config.TemplateFields{
		TargetRepo:       "/home/adopter/.token-profile/repos/octocat",
		RemoteRepo:       "git@github.com:octocat/octocat.git",
		CloneProtocol:    config.CloneProtocolSSH,
		ScheduleInterval: 12 * time.Hour,
	}

	if err := config.WriteTemplate(path, fields); err != nil {
		t.Fatalf("WriteTemplate() error = %v, want nil", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
	if cfg.RemoteRepo != fields.RemoteRepo {
		t.Errorf("RemoteRepo = %q, want %q", cfg.RemoteRepo, fields.RemoteRepo)
	}
	if cfg.CloneProtocol != fields.CloneProtocol {
		t.Errorf("CloneProtocol = %q, want %q", cfg.CloneProtocol, fields.CloneProtocol)
	}
	if cfg.ScheduleInterval != fields.ScheduleInterval {
		t.Errorf("ScheduleInterval = %v, want %v", cfg.ScheduleInterval, fields.ScheduleInterval)
	}
}

func TestDefault_UsesPerModelBreakdownAndZeroTrailingWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := config.Default()

	if cfg.Breakdown != config.BreakdownPerModel {
		t.Errorf("Breakdown = %q, want %q", cfg.Breakdown, config.BreakdownPerModel)
	}
	if cfg.TrailingWindow != 0 {
		t.Errorf("TrailingWindow = %v, want 0 (omitted --since, per KTD10)", cfg.TrailingWindow)
	}
	want := filepath.Join(home, ".token-profile", "machine-id")
	if cfg.MachineIDPath != want {
		t.Errorf("MachineIDPath = %q, want %q", cfg.MachineIDPath, want)
	}
	if cfg.CloneProtocol != config.CloneProtocolHTTPS {
		t.Errorf("CloneProtocol = %q, want %q", cfg.CloneProtocol, config.CloneProtocolHTTPS)
	}
	if cfg.ScheduleInterval != config.DefaultScheduleInterval {
		t.Errorf("ScheduleInterval = %v, want %v", cfg.ScheduleInterval, config.DefaultScheduleInterval)
	}
}
