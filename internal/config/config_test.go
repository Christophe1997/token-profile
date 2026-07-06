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
		"machineIdPath": "/home/adopter/.token-profile/machine-id"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	want := config.Config{
		TargetRepo:     "/home/adopter/username",
		Breakdown:      config.BreakdownPerTool,
		TrailingWindow: 168 * time.Hour,
		BreakdownLimit: 5,
		MachineIDPath:  "/home/adopter/.token-profile/machine-id",
		RenderMode:     config.RenderModeSVG,
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
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

func TestWriteTemplate_CreatesLoadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if err := config.WriteTemplate(path, ""); err != nil {
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

	if err := config.WriteTemplate(path, ""); err != nil {
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

	if err := config.WriteTemplate(path, ""); err != nil {
		t.Fatalf("WriteTemplate() first call error = %v, want nil", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if err := config.WriteTemplate(path, ""); err == nil {
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

	if err := config.WriteTemplate(path, ""); err != nil {
		t.Fatalf("WriteTemplate() error = %v, want nil", err)
	}

	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("ReadFile() error = %v, want the scaffolded file to be readable", err)
	}
}

func TestWriteTemplate_NonEmptyTargetRepo_RoundTripsThroughLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	const targetRepo = "/home/adopter/.token-profile/repos/octocat"

	if err := config.WriteTemplate(path, targetRepo); err != nil {
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
	const targetRepo = `C:\Users\you\code\"you"`

	if err := config.WriteTemplate(path, targetRepo); err != nil {
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
}
