// Package config defines and loads token-profile's configuration schema:
// where the rendered profile's target repo lives, how usage is broken down,
// the trailing window to query, and where the local machine identity is cached.
package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BreakdownMode selects how the rendered usage breakdown groups data.
type BreakdownMode string

const (
	BreakdownPerModel BreakdownMode = "per-model"
	BreakdownPerTool  BreakdownMode = "per-tool"
	BreakdownCombined BreakdownMode = "combined"
)

// Config is token-profile's configuration schema.
type Config struct {
	// TargetRepo is the path to the repo hosting the rendered README profile.
	TargetRepo string `json:"targetRepo,omitzero"`
	// Breakdown selects the usage grouping shown in the rendered card.
	Breakdown BreakdownMode `json:"breakdown,omitzero"`
	// TrailingWindow bounds how far back usage is queried. Zero means omit
	// agentsview's --since flag entirely, deferring to its own default
	// trailing window (30 days), per KTD10.
	TrailingWindow time.Duration `json:"trailingWindow,omitzero"`
	// MachineIDPath is where this machine's cached identity is stored.
	MachineIDPath string `json:"machineIdPath,omitzero"`
}

// UnmarshalJSON decodes Config, accepting trailingWindow as a
// time.ParseDuration-compatible string (e.g. "168h") rather than a raw
// nanosecond count, since config files are hand-edited.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	aux := struct {
		TrailingWindow string `json:"trailingWindow,omitzero"`
		*plain
	}{
		plain: (*plain)(c),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.TrailingWindow != "" {
		d, err := time.ParseDuration(aux.TrailingWindow)
		if err != nil {
			return fmt.Errorf("invalid trailingWindow %q: %w", aux.TrailingWindow, err)
		}
		c.TrailingWindow = d
	}
	return nil
}

// Default returns the configuration used when no config file is present.
func Default() Config {
	return Config{
		Breakdown:     BreakdownPerModel,
		MachineIDPath: defaultMachineIDPath(),
	}
}

// Load reads config from path, layering it over Default(). A missing file is
// not an error: it means the adopter hasn't configured token-profile yet, so
// Default() is returned as-is.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validating config %s: %w", path, err)
	}

	return cfg, nil
}

// Validate reports whether cfg holds a recognized breakdown mode.
func (c Config) Validate() error {
	switch c.Breakdown {
	case BreakdownPerModel, BreakdownPerTool, BreakdownCombined:
		return nil
	default:
		return fmt.Errorf("invalid breakdown mode %q (want %q, %q, or %q)",
			c.Breakdown, BreakdownPerModel, BreakdownPerTool, BreakdownCombined)
	}
}

// configTemplate is the starter config WriteTemplate scaffolds. targetRepo
// is present but blank — the one field a first-time adopter must fill in —
// and breakdown is spelled out at its real default so Validate() still
// passes on the next Load. trailingWindow and machineIdPath are
// deliberately omitted rather than spelled out blank: UnmarshalJSON
// overwrites Breakdown/MachineIDPath onto Default()'s pre-populated values
// whenever their JSON key is present, even at a zero value, so an explicit
// blank key here would corrupt those defaults the next time this same file
// is loaded.
const configTemplate = `{
  "targetRepo": "",
  "breakdown": "per-model"
}
`

// WriteTemplate scaffolds a starter config file at path, creating parent
// directories as needed. It refuses to overwrite an existing file — same
// atomic O_CREATE|O_EXCL convention as internal/cli/lock.go's
// writeLockFile — so it's only safe to call once the caller has confirmed
// no config exists yet at path.
func WriteTemplate(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("creating config %s: %w", path, err)
	}
	_, writeErr := f.WriteString(configTemplate)
	closeErr := f.Close()
	if err := cmp.Or(writeErr, closeErr); err != nil {
		os.Remove(path)
		return fmt.Errorf("writing config template %s: %w", path, err)
	}
	return nil
}

func defaultMachineIDPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".token-profile", "machine-id")
}
