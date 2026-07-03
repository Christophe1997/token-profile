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
	"maps"
	"os/exec"
	"slices"
	"strings"
)

// defaultBinaryName is the agentsview executable name looked up on PATH when
// Client.BinaryName is unset.
const defaultBinaryName = "agentsview"

// maxSessionListPages bounds `session list` pagination as a defensive guard
// against looping forever if agentsview ever echoes back a non-empty cursor
// indefinitely.
const maxSessionListPages = 10_000

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
	// Cmd labels which agentsview subcommand failed, e.g. "session list".
	// Defaults to "usage daily" when empty, for existing call sites.
	Cmd    string
	Err    error
	Stderr string
}

func (e *ExitError) Error() string {
	label := cmp.Or(e.Cmd, "usage daily")
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		return fmt.Sprintf("agentsview %s: %v", label, e.Err)
	}
	return fmt.Sprintf("agentsview %s: %v: %s", label, e.Err, stderr)
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

// sessionListResponse is the decoded response of `agentsview session list
// --json`. ASSUMPTION: this JSON shape isn't vendored in this repo (unlike
// `usage daily`, agentsview's session-list schema isn't documented here), so
// it's a permissive best-effort decode: each session carries at least an
// `agent` field, and pagination continues via NextCursor until it's empty.
type sessionListResponse struct {
	Sessions   []sessionSummary `json:"sessions,omitzero"`
	NextCursor string           `json:"nextCursor,omitzero"`
}

type sessionSummary struct {
	Agent string `json:"agent,omitzero"`
}

// ListActiveAgents pages through `agentsview session list --json` and
// returns the distinct, sorted set of agent names across all sessions.
// There is no dedicated "list agents" command (KTD3), so this is derived
// from session data instead.
func (c *Client) ListActiveAgents(ctx context.Context) ([]string, error) {
	bin := cmp.Or(c.BinaryName, defaultBinaryName)

	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("looking up %q: %w", bin, ErrNotInstalled)
	}

	agents := make(map[string]struct{})
	cursor := ""
	for range maxSessionListPages {
		resp, err := fetchSessionListPage(ctx, path, cursor)
		if err != nil {
			return nil, err
		}
		for _, s := range resp.Sessions {
			if s.Agent != "" {
				agents[s.Agent] = struct{}{}
			}
		}

		if resp.NextCursor == "" {
			return slices.Sorted(maps.Keys(agents)), nil
		}
		cursor = resp.NextCursor
	}

	return nil, fmt.Errorf("session list: exceeded %d pages without an empty cursor", maxSessionListPages)
}

// fetchSessionListPage runs `agentsview session list --json [--cursor
// cursor]` and decodes a single page of its response.
func fetchSessionListPage(ctx context.Context, path, cursor string) (*sessionListResponse, error) {
	args := []string{"session", "list", "--json"}
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}

	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, &ExitError{Cmd: "session list", Err: err, Stderr: stderr.String()}
	}

	var resp sessionListResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("decoding agentsview session list output: %w", err)
	}
	return &resp, nil
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
