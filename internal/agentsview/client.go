// Package agentsview shells out to the agentsview CLI (https://github.com/kenn-io/agentsview)
// to read local AI-coding-session usage data.
package agentsview

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// defaultBinaryName is the agentsview executable name looked up on PATH when
// Client.BinaryName is unset.
const defaultBinaryName = "agentsview"

// ErrNotInstalled indicates the agentsview binary could not be found on PATH.
var ErrNotInstalled = errors.New(
	"agentsview not installed: install it from https://github.com/kenn-io/agentsview#installation and ensure it is on your PATH",
)

// Client shells out to the agentsview CLI.
type Client struct {
	// BinaryName overrides the executable looked up on PATH. Defaults to
	// "agentsview" when empty; primarily useful for tests.
	BinaryName string
}

// ExitError wraps a non-zero agentsview exit, preserving its stderr output
// so callers can surface (or programmatically inspect) what went wrong.
type ExitError struct {
	Err    error
	Stderr string
}

func (e *ExitError) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		return fmt.Sprintf("agentsview usage daily: %v", e.Err)
	}
	return fmt.Sprintf("agentsview usage daily: %v: %s", e.Err, stderr)
}

func (e *ExitError) Unwrap() error { return e.Err }

// FetchOptions configures a single `agentsview usage daily` invocation.
type FetchOptions struct {
	// Agent, if set, filters usage to a single agent via --agent.
	Agent string
	// Since, if set, is passed verbatim as --since <date>. Empty omits the
	// flag, deferring to agentsview's own default trailing window (30 days).
	Since string
}

// DailyRow is one row of the `daily[]` array from
// `agentsview usage daily --json --breakdown`.
type DailyRow struct {
	Date   string  `json:"date"`
	Agent  string  `json:"agent,omitzero"`
	Model  string  `json:"model,omitzero"`
	Tokens int64   `json:"tokens,omitzero"`
	Cost   float64 `json:"cost,omitzero"`
}

// Totals is the `totals` object from `agentsview usage daily --json --breakdown`.
type Totals struct {
	Tokens int64   `json:"tokens,omitzero"`
	Cost   float64 `json:"cost,omitzero"`
}

// UsageDaily is the decoded response of `agentsview usage daily --json --breakdown`.
type UsageDaily struct {
	Daily  []DailyRow `json:"daily,omitzero"`
	Totals Totals     `json:"totals,omitzero"`
}

// FetchUsageDaily runs `agentsview usage daily --json --breakdown --offline`
// (plus any --agent/--since from opts) and decodes its output.
func (c *Client) FetchUsageDaily(ctx context.Context, opts FetchOptions) (*UsageDaily, error) {
	bin := cmp.Or(c.BinaryName, defaultBinaryName)

	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("looking up %q: %w", bin, ErrNotInstalled)
	}

	args := []string{"usage", "daily", "--json", "--breakdown", "--offline"}
	if opts.Agent != "" {
		args = append(args, "--agent", opts.Agent)
	}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	}

	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, &ExitError{Err: err, Stderr: stderr.String()}
	}

	return parseUsageDaily(stdout.Bytes())
}

// parseUsageDaily decodes agentsview's `usage daily --json --breakdown` output.
// Per agentsview's own additive-schema guidance, unrecognized fields are
// ignored — encoding/json does this by default, so no extra code is needed.
func parseUsageDaily(data []byte) (*UsageDaily, error) {
	var out UsageDaily
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding agentsview usage daily output: %w", err)
	}
	return &out, nil
}
