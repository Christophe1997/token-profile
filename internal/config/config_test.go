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
