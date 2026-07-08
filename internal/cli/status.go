package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/Christophe1997/token-profile/internal/runhistory"
)

// StatusDeps bundles the read-only dependencies Status needs. Unlike
// Run/Cleanup, status has no target-repo config to load — both the
// schedule label and the history path are machine-global (KTD2), so
// StatusDeps carries no config.Config field.
type StatusDeps struct {
	// Schedule is passed to CheckScheduleState as-is. Only Label (plus the
	// GOOS/Launchctl/Crontab test overrides) is actually read for this
	// read-only check.
	Schedule ScheduleDeps
	// HistoryPath is where Status reads recorded run outcomes from.
	HistoryPath string
	// Stdout receives the rendered report. Nil is valid and silently
	// discards the report, mirroring RunDeps.Stdout's own nil-is-a-no-op
	// convention.
	Stdout io.Writer
}

// Status reports the schedule's live registration state and the recorded
// run history, most-recent first, to deps.Stdout. It always returns nil: a
// failed schedule check or an unreadable history file are reported as data
// rather than as a command failure (KTD5), mirroring cleanup's own
// treatment of ScheduleCheckFailed. A nil deps.Stdout silently discards the
// report rather than panicking, mirroring RunDeps.Stdout's own
// nil-is-a-no-op convention.
func Status(ctx context.Context, deps StatusDeps) error {
	if deps.Stdout == nil {
		return nil
	}

	state, err := CheckScheduleState(ctx, deps.Schedule)
	if err != nil {
		fmt.Fprintf(deps.Stdout, "schedule: %s (%v)\n", state, err)
	} else {
		fmt.Fprintf(deps.Stdout, "schedule: %s\n", state)
	}

	records, err := runhistory.Read(deps.HistoryPath)
	if err != nil {
		fmt.Fprintf(deps.Stdout, "history unavailable: %v\n", err)
		return nil
	}
	if len(records) == 0 {
		fmt.Fprintln(deps.Stdout, "no runs recorded yet")
		return nil
	}
	for _, rec := range slices.Backward(records) {
		printRecord(deps.Stdout, rec)
	}
	return nil
}

// printRecord writes one history record as a single line: its RFC3339
// timestamp, then "ok" or "failed: <error text>".
func printRecord(w io.Writer, rec runhistory.Record) {
	ts := rec.Timestamp.Format(time.RFC3339)
	if rec.Success {
		fmt.Fprintf(w, "%s  ok\n", ts)
		return
	}
	fmt.Fprintf(w, "%s  failed: %s\n", ts, rec.Error)
}

// NewStatusCmd builds the `token-profile status` cobra command: a thin
// wrapper delegating to Status, mirroring NewRunCmd/NewCleanupCmd's own
// shape. Unlike those commands, it takes no --config flag — both halves of
// its report resolve from machine-global state (KTD2), with no target repo
// in play.
func NewStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether the schedule is registered and show recent run history",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Status(cmd.Context(), StatusDeps{
				Schedule:    ScheduleDeps{Label: launchdLabel},
				HistoryPath: defaultHistoryPath(),
				Stdout:      cmd.OutOrStdout(),
			})
		},
	}
	return cmd
}
