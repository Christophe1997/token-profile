package snapshot

import (
	"cmp"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
// that machine's complete, current history (Write always fully replaces
// it), so a machine re-running never inflates its own totals here — only
// distinct machines' rows are additive.
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
