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

// DailyRow is one flattened (day, model) usage observation, derived from a
// `daily[]` entry's `modelBreakdowns[]` in `agentsview usage daily --json
// --breakdown` (real schema, see internal/agentsview/testdata — the API has
// no flat per-row shape like this; parseUsageDaily flattens it from the
// nested rawDailyEntry/rawBreakdown decode types).
//
// Agent attribution: Agent is always the FetchOptions.Agent the call was
// made with, never read from the response's own `agentBreakdowns[]`. An
// *unfiltered* call's modelBreakdowns aggregate a model's usage across every
// agent, so a single modelBreakdown entry can't be attributed to one agent
// from the JSON alone. Resolve, however, only ever calls FetchUsageDaily
// once per known agent (--agent set), so for every row this code actually
// produces in production, FetchOptions.Agent is exact and unambiguous.
//
// Token definition: Tokens is inputTokens+outputTokens+cacheCreationTokens+
// cacheReadTokens — every token dimension the API reports. Cost already
// reflects cache read/write pricing, so excluding cache tokens from Tokens
// would decouple the headline "tokens used" number from the cost billed
// against it.
type DailyRow struct {
	Date   string
	Agent  string
	Model  string
	Tokens int64
	Cost   float64
}

// Totals aggregates Tokens/Cost using the same definitions as DailyRow
// (every token dimension included). It doubles as the decoded shape of the
// API's top-level `totals` object (via rawTotals) and as the return type of
// Dataset's aggregation methods (resolve.go), so the two are directly
// comparable.
type Totals struct {
	Tokens int64
	Cost   float64
}

// UsageDaily is the decoded, flattened response of one `agentsview usage
// daily --json --breakdown` invocation.
type UsageDaily struct {
	Daily  []DailyRow
	Totals Totals
}

// FetchUsageDaily runs `agentsview usage daily --json --breakdown --offline`
// (plus any --agent/--since from opts) and decodes its output.
func (c *Client) FetchUsageDaily(ctx context.Context, opts FetchOptions) (*UsageDaily, error) {
	bin := cmp.Or(c.BinaryName, defaultBinaryName)

	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("looking up %q: %w", bin, ErrNotInstalled)
	}

	// --timezone UTC (KTD5): bucket days in UTC rather than agentsview's
	// default of the local system timezone, so dates are stable across
	// machines in different timezones.
	args := []string{"usage", "daily", "--json", "--breakdown", "--offline", "--timezone", "UTC"}
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

	return parseUsageDaily(stdout.Bytes(), opts.Agent)
}

// sessionListResponse is the decoded response of `agentsview session list
// --json` (real schema confirmed against a captured response, see
// internal/agentsview/testdata/real_session_list_trimmed.json — note
// next_cursor is snake_case, unlike usage daily's camelCase fields). Only
// the fields ListActiveAgents needs are decoded; the rest of the payload's
// many session fields are ignored.
type sessionListResponse struct {
	Sessions   []sessionSummary `json:"sessions,omitzero"`
	NextCursor string           `json:"next_cursor,omitzero"`
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

// rawUsageDaily is the literal decode shape of `agentsview usage daily
// --json --breakdown`'s real response (verified against captured fixtures,
// see internal/agentsview/testdata/real_usage_daily_claude.json). Only the
// fields parseUsageDaily's flattening needs are declared; per agentsview's
// own additive-schema guidance, every other field (projectBreakdowns,
// agentBreakdowns, modelsUsed, sessionCounts, cacheSavings, etc.) is
// ignored — encoding/json does this by default, so no extra code is needed.
type rawUsageDaily struct {
	Daily  []rawDailyEntry `json:"daily"`
	Totals rawTotals       `json:"totals"`
}

// rawDailyEntry is one entry of the real `daily[]` array — one per
// calendar day in the requested window.
type rawDailyEntry struct {
	Date            string         `json:"date"`
	ModelBreakdowns []rawBreakdown `json:"modelBreakdowns"`
}

// rawBreakdown is one entry of a rawDailyEntry's `modelBreakdowns[]` — one
// per model used that day (scoped to the requested --agent, if any).
type rawBreakdown struct {
	ModelName           string  `json:"modelName"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheCreationTokens int64   `json:"cacheCreationTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	Cost                float64 `json:"cost"`
}

// rawTotals is the real top-level `totals` object.
type rawTotals struct {
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheCreationTokens int64   `json:"cacheCreationTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	TotalCost           float64 `json:"totalCost"`
}

// tokens sums every token dimension the API reports (see DailyRow's doc
// comment for why cache tokens are included).
func (t rawTotals) tokens() int64 {
	return t.InputTokens + t.OutputTokens + t.CacheCreationTokens + t.CacheReadTokens
}

func (b rawBreakdown) tokens() int64 {
	return b.InputTokens + b.OutputTokens + b.CacheCreationTokens + b.CacheReadTokens
}

// parseUsageDaily decodes agentsview's `usage daily --json --breakdown`
// output and flattens each day's modelBreakdowns into one DailyRow per
// (day, model), attributing every row to agent (see DailyRow's doc comment
// for why attribution comes from the caller rather than the response).
func parseUsageDaily(data []byte, agent string) (*UsageDaily, error) {
	var raw rawUsageDaily
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decoding agentsview usage daily output: %w", err)
	}

	out := &UsageDaily{
		Totals: Totals{
			Tokens: raw.Totals.tokens(),
			Cost:   raw.Totals.TotalCost,
		},
	}
	for _, day := range raw.Daily {
		for _, mb := range day.ModelBreakdowns {
			out.Daily = append(out.Daily, DailyRow{
				Date:   day.Date,
				Agent:  agent,
				Model:  mb.ModelName,
				Tokens: mb.tokens(),
				Cost:   mb.Cost,
			})
		}
	}
	return out, nil
}
