package snapshot

import (
	"cmp"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// mergeKey identifies one (date, agent, model) bucket that multiple
// machines' snapshots can independently contribute to.
type mergeKey struct {
	Date, Agent, Model string
}

// MergedDataset is the union of every machine's snapshot under a target
// repo, pre-summed by (date, agent, model) across machine boundaries: each
// Row already reflects every machine's combined contribution to that
// bucket, ready for direct consumption (e.g. streak/summary computation)
// without the caller needing to re-derive per-machine totals.
type MergedDataset struct {
	Rows []Row
}

// Merge reads every snapshot file under targetRepo's snapshots directory
// and unions their rows, summing different machines' contributions to the
// same (date, agent, model) bucket. Each machine's own file already holds
// that machine's complete accumulated history, deduplicated by key (Write
// merges rather than replaces — see mergeRowsByKey), so a machine
// re-running never inflates its own totals here — only distinct machines'
// rows are additive.
//
// A snapshot file that fails to parse (corrupted or a partial write) is
// skipped with a logged warning rather than aborting the whole merge, so
// one machine's bad file can't take down every other machine's data.
//
// A missing snapshots directory (no machine has ever run against this
// target repo) is not an error: it yields an empty MergedDataset.
func Merge(targetRepo string) (MergedDataset, error) {
	dir := snapshotsDir(targetRepo)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return MergedDataset{}, nil
		}
		return MergedDataset{}, fmt.Errorf("reading snapshots directory %s: %w", dir, err)
	}

	totals := make(map[mergeKey]Row)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		rows, err := readSnapshotFile(path)
		if err != nil {
			log.Printf("snapshot: skipping unreadable/corrupted snapshot %s: %v", path, err)
			continue
		}

		for _, r := range rows {
			k := mergeKey{Date: r.Date, Agent: r.Agent, Model: r.Model}
			t := totals[k]
			t.Date, t.Agent, t.Model = r.Date, r.Agent, r.Model
			t.Tokens += r.Tokens
			t.Cost += r.Cost
			totals[k] = t
		}
	}

	rows := slices.Collect(maps.Values(totals))
	slices.SortFunc(rows, func(a, b Row) int {
		return cmp.Or(
			cmp.Compare(a.Date, b.Date),
			cmp.Compare(a.Agent, b.Agent),
			cmp.Compare(a.Model, b.Model),
		)
	})
	return MergedDataset{Rows: rows}, nil
}

// mergeRowsByKey unions existing and fresh by (date, agent, model),
// preferring fresh's value whenever both share a key: fresh always reflects
// the latest resolve for whatever days it covers, so it wins on overlap
// (re-running the same day never double-counts). Rows found only in
// existing — days that have since rolled out of the resolve window — are
// preserved rather than dropped, so a machine's snapshot accumulates
// history across runs instead of rolling off with the trailing window. The
// result is sorted by (date, agent, model) for a stable, human-diffable
// on-disk order.
func mergeRowsByKey(existing, fresh []Row) []Row {
	merged := make(map[mergeKey]Row, len(existing)+len(fresh))
	for _, r := range existing {
		merged[mergeKey{Date: r.Date, Agent: r.Agent, Model: r.Model}] = r
	}
	for _, r := range fresh {
		merged[mergeKey{Date: r.Date, Agent: r.Agent, Model: r.Model}] = r
	}
	rows := slices.Collect(maps.Values(merged))
	slices.SortFunc(rows, func(a, b Row) int {
		return cmp.Or(
			cmp.Compare(a.Date, b.Date),
			cmp.Compare(a.Agent, b.Agent),
			cmp.Compare(a.Model, b.Model),
		)
	})
	return rows
}

// FilterSince returns the subset of ds.Rows dated on or after since
// (inclusive), preserving order — the window-scoping step cli/run.go
// applies to an accumulated (potentially multi-window) MergedDataset before
// handing it to render.Render, so trend/breakdown reflect only the current
// window rather than a machine's full accumulated history.
func FilterSince(ds MergedDataset, since time.Time) MergedDataset {
	cutoff := since.UTC().Format(time.DateOnly)
	var out MergedDataset
	for _, r := range ds.Rows {
		if r.Date >= cutoff {
			out.Rows = append(out.Rows, r)
		}
	}
	return out
}
