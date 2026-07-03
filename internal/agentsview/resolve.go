package agentsview

import (
	"cmp"
	"context"
	"fmt"
)

// Row is one (date, agent, model) usage observation in a resolved Dataset.
type Row struct {
	Date   string
	Agent  string
	Model  string
	Tokens int64
	Cost   float64
}

// Dataset is the unified per-(date, agent, model) usage dataset produced by
// Client.Resolve. Per-model, per-tool, and combined views are all derivable
// from it by summing Rows along different axes (KTD3).
type Dataset struct {
	Rows []Row
}

// ByModel sums Rows across all agents and dates, grouped by model.
func (d Dataset) ByModel() map[string]Totals {
	return groupTotals(d.Rows, func(r Row) string { return r.Model })
}

// ByTool sums Rows across all models and dates, grouped by agent.
func (d Dataset) ByTool() map[string]Totals {
	return groupTotals(d.Rows, func(r Row) string { return r.Agent })
}

// Total sums Rows across the entire dataset.
func (d Dataset) Total() Totals {
	var t Totals
	for _, r := range d.Rows {
		t.Tokens += r.Tokens
		t.Cost += r.Cost
	}
	return t
}

func groupTotals(rows []Row, key func(Row) string) map[string]Totals {
	out := make(map[string]Totals)
	for _, r := range rows {
		k := key(r)
		t := out[k]
		t.Tokens += r.Tokens
		t.Cost += r.Cost
		out[k] = t
	}
	return out
}

// ResolveOptions configures a Resolve run.
type ResolveOptions struct {
	// Since, if set, is propagated verbatim to every per-agent
	// FetchUsageDaily call (see FetchOptions.Since).
	Since string
}

// Resolve enumerates active agents via c.ListActiveAgents and calls
// c.FetchUsageDaily once per agent (KTD3, KTD12), combining every agent's
// rows into a single Dataset keyed by (date, agent, model). Agents with no
// usage in the window contribute no rows.
func (c *Client) Resolve(ctx context.Context, opts ResolveOptions) (*Dataset, error) {
	agents, err := c.ListActiveAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing active agents: %w", err)
	}

	var ds Dataset
	for _, agent := range agents {
		usage, err := c.FetchUsageDaily(ctx, FetchOptions{Agent: agent, Since: opts.Since})
		if err != nil {
			return nil, fmt.Errorf("fetching usage for agent %q: %w", agent, err)
		}
		for _, row := range usage.Daily {
			if row.Tokens == 0 && row.Cost == 0 {
				continue // zero-usage rows are noise, not signal — omit rather than zero-fill
			}
			ds.Rows = append(ds.Rows, Row{
				Date:   row.Date,
				Agent:  cmp.Or(row.Agent, agent),
				Model:  row.Model,
				Tokens: row.Tokens,
				Cost:   row.Cost,
			})
		}
	}
	return &ds, nil
}
