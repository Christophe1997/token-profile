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

// DefaultTrailingWindow is the concrete window used to scope the rendered
// card (and its window-over-window comparison) to "the current window" when
// TrailingWindow is unset. It matches agentsview's own default trailing
// window (KTD10) so an unset TrailingWindow renders the same period it
// always has; it's a separate constant from the fetch path (sinceDate still
// omits --since entirely on zero, per KTD10) because rendering now filters
// already-accumulated local history (see internal/snapshot.Write) rather
// than querying agentsview live, so it has no "just defer to the API's
// default" option and must know a concrete number itself.
const DefaultTrailingWindow = 30 * 24 * time.Hour

// DefaultBreakdownLimit is the number of top entries the rendered
// breakdown shows when BreakdownLimit is unset (zero) — see
// Config.BreakdownLimit.
const DefaultBreakdownLimit = 3

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
	// BreakdownLimit caps how many entries the rendered breakdown shows,
	// combining the rest into one summary line rather than dropping them
	// silently. Zero (unset) defers to DefaultBreakdownLimit; a negative
	// value shows every entry with no cap.
	BreakdownLimit int `json:"breakdownLimit,omitzero"`
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

// configTemplateData is the JSON shape WriteTemplate scaffolds. It carries
// only targetRepo and breakdown — trailingWindow and machineIdPath are
// deliberately omitted rather than spelled out blank: UnmarshalJSON
// overwrites Breakdown/MachineIDPath onto Default()'s pre-populated values
// whenever their JSON key is present, even at a zero value, so an explicit
// blank key here would corrupt those defaults the next time this same file
// is loaded.
type configTemplateData struct {
	TargetRepo string        `json:"targetRepo"`
	Breakdown  BreakdownMode `json:"breakdown"`
}

// WriteTemplate scaffolds a starter config file at path with targetRepo
// pre-filled (blank if the caller has nothing to suggest yet), creating
// parent directories as needed. It refuses to overwrite an existing file —
// same atomic O_CREATE|O_EXCL convention as internal/cli/lock.go's
// writeLockFile. targetRepo is JSON-encoded via json.MarshalIndent rather
// than spliced into a raw string template, so an arbitrary local path (a
// backslash on Windows, an embedded quote) round-trips safely instead of
// corrupting the JSON.
func WriteTemplate(path, targetRepo string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(configTemplateData{
		TargetRepo: targetRepo,
		Breakdown:  BreakdownPerModel,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config template: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("creating config %s: %w", path, err)
	}
	_, writeErr := f.Write(data)
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
