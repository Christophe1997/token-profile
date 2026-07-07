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
	"slices"
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

// DefaultScheduleInterval is the scheduled-run cadence used when
// ScheduleInterval is unset (zero) — see Config.ScheduleInterval.
const DefaultScheduleInterval = 6 * time.Hour

// validScheduleIntervals are the hourly divisors of 24 that produce a clean
// "every N hours" cron/launchd schedule (KTD10): each one divides a day
// evenly, so a run always lands on the same wall-clock hours day over day.
// Any other interval (e.g. 5h) would drift the run time across days.
var validScheduleIntervals = []time.Duration{
	1 * time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour,
	6 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// BreakdownMode selects how the rendered usage breakdown groups data.
type BreakdownMode string

const (
	BreakdownPerModel BreakdownMode = "per-model"
	BreakdownPerTool  BreakdownMode = "per-tool"
	BreakdownCombined BreakdownMode = "combined"
)

// RenderMode selects which dashboard card the rendered profile shows.
type RenderMode string

const (
	RenderModeSVG   RenderMode = "svg"
	RenderModeASCII RenderMode = "ascii"
)

// CloneProtocol selects the URL scheme used to clone RemoteRepo.
type CloneProtocol string

const (
	CloneProtocolHTTPS CloneProtocol = "https"
	CloneProtocolSSH   CloneProtocol = "ssh"
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
	// RenderMode selects which dashboard card gets rendered into the README.
	RenderMode RenderMode `json:"renderMode,omitzero"`
	// RemoteRepo is the git remote URL `init`'s wizard clones TargetRepo
	// from. Blank means TargetRepo already exists locally.
	RemoteRepo string `json:"remoteRepo,omitzero"`
	// CloneProtocol selects the URL scheme used to clone RemoteRepo. Zero
	// (unset) defers to Default()'s "https".
	CloneProtocol CloneProtocol `json:"cloneProtocol,omitzero"`
	// ScheduleInterval is how often the scheduled run invokes
	// token-profile. Zero (unset) defers to DefaultScheduleInterval; any
	// non-zero value must be one of validScheduleIntervals (KTD10).
	ScheduleInterval time.Duration `json:"scheduleInterval,omitzero"`
}

// UnmarshalJSON decodes Config, accepting trailingWindow and
// scheduleInterval as time.ParseDuration-compatible strings (e.g. "168h",
// "12h") rather than raw nanosecond counts, since config files are
// hand-edited.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	aux := struct {
		TrailingWindow   string `json:"trailingWindow,omitzero"`
		ScheduleInterval string `json:"scheduleInterval,omitzero"`
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
	if aux.ScheduleInterval != "" {
		d, err := time.ParseDuration(aux.ScheduleInterval)
		if err != nil {
			return fmt.Errorf("invalid scheduleInterval %q: %w", aux.ScheduleInterval, err)
		}
		c.ScheduleInterval = d
	}
	return nil
}

// Default returns the configuration used when no config file is present.
func Default() Config {
	return Config{
		Breakdown:        BreakdownPerModel,
		MachineIDPath:    defaultMachineIDPath(),
		RenderMode:       RenderModeSVG,
		CloneProtocol:    CloneProtocolHTTPS,
		ScheduleInterval: DefaultScheduleInterval,
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

// Validate reports whether cfg holds a recognized breakdown mode, render
// mode, clone protocol, and schedule interval.
func (c Config) Validate() error {
	switch c.Breakdown {
	case BreakdownPerModel, BreakdownPerTool, BreakdownCombined:
	default:
		return fmt.Errorf("invalid breakdown mode %q (want %q, %q, or %q)",
			c.Breakdown, BreakdownPerModel, BreakdownPerTool, BreakdownCombined)
	}
	switch c.RenderMode {
	case RenderModeSVG, RenderModeASCII:
	default:
		return fmt.Errorf("invalid render mode %q (want %q or %q)",
			c.RenderMode, RenderModeSVG, RenderModeASCII)
	}
	switch c.CloneProtocol {
	case CloneProtocolHTTPS, CloneProtocolSSH:
	default:
		return fmt.Errorf("invalid clone protocol %q (want %q or %q)",
			c.CloneProtocol, CloneProtocolHTTPS, CloneProtocolSSH)
	}
	if !slices.Contains(validScheduleIntervals, c.ScheduleInterval) {
		return fmt.Errorf("invalid scheduleInterval %s (want one of %v)",
			c.ScheduleInterval, validScheduleIntervals)
	}
	return nil
}

// configTemplateData is the JSON shape WriteTemplate scaffolds. It carries
// only targetRepo, breakdown, renderMode, remoteRepo, cloneProtocol, and
// scheduleInterval — trailingWindow and machineIdPath are deliberately
// omitted rather than spelled out blank: UnmarshalJSON overwrites
// Breakdown/MachineIDPath onto Default()'s pre-populated values whenever
// their JSON key is present, even at a zero value, so an explicit blank key
// here would corrupt those defaults the next time this same file is loaded.
// ScheduleInterval is carried as a string (its Config.String() form, e.g.
// "6h0m0s") rather than time.Duration's raw nanosecond encoding, matching
// how a hand-edited config expresses it (see Config.UnmarshalJSON).
type configTemplateData struct {
	TargetRepo       string        `json:"targetRepo"`
	Breakdown        BreakdownMode `json:"breakdown"`
	RenderMode       RenderMode    `json:"renderMode"`
	RemoteRepo       string        `json:"remoteRepo"`
	CloneProtocol    CloneProtocol `json:"cloneProtocol"`
	ScheduleInterval string        `json:"scheduleInterval"`
}

// TemplateFields bundles the values a wizard collects before a starter
// config exists yet — a struct rather than positional args, since
// TargetRepo and RemoteRepo are same-shaped strings a positional mixup
// would silently swap.
type TemplateFields struct {
	// TargetRepo is the path to the target repo's local clone, resolved by
	// the caller (e.g. the wizard's clone step) — WriteTemplate itself
	// never sets it.
	TargetRepo string
	// RemoteRepo is the git remote URL the wizard would clone TargetRepo
	// from, or "" if TargetRepo already exists locally.
	RemoteRepo string
	// CloneProtocol is the wizard's chosen clone URL scheme. Zero ("")
	// resolves to CloneProtocolHTTPS so the scaffolded file always holds a
	// valid enum value — see configTemplateData's doc comment on why an
	// explicit-but-invalid key would corrupt reload otherwise.
	CloneProtocol CloneProtocol
	// ScheduleInterval is the wizard's chosen scheduled-run cadence. Zero
	// resolves to DefaultScheduleInterval, for the same reason.
	ScheduleInterval time.Duration
}

// WriteTemplate scaffolds a starter config file at path from fields,
// creating parent directories as needed. It refuses to overwrite an
// existing file — same atomic O_CREATE|O_EXCL convention as
// internal/cli/lock.go's writeLockFile. String fields are JSON-encoded via
// json.MarshalIndent rather than spliced into a raw string template, so an
// arbitrary local path or URL (a backslash on Windows, an embedded quote)
// round-trips safely instead of corrupting the JSON.
func WriteTemplate(path string, fields TemplateFields) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(configTemplateData{
		TargetRepo:       fields.TargetRepo,
		Breakdown:        BreakdownPerModel,
		RenderMode:       RenderModeSVG,
		RemoteRepo:       fields.RemoteRepo,
		CloneProtocol:    cmp.Or(fields.CloneProtocol, CloneProtocolHTTPS),
		ScheduleInterval: cmp.Or(fields.ScheduleInterval, DefaultScheduleInterval).String(),
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
