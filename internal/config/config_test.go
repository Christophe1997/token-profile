package config_test

import (
	"os"
	"path/filepath"
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
		MachineIDPath:  "/home/adopter/.token-profile/machine-id",
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
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
