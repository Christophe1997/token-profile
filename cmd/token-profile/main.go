// Command token-profile renders a GitHub profile dashboard card from local
// AI-coding-session usage data. This entrypoint wires up the root command
// and attaches its subcommands; the actual logic behind each subcommand
// lives in internal/cli.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Christophe1997/token-profile/internal/cli"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// runTimeout bounds a single invocation of token-profile's root command,
// propagated to every subcommand's cmd.Context(). Without it, an
// unresponsive agentsview or git subprocess (a hung network call, a git
// credential prompt with no TTY to answer it) would hang the process
// forever. 5 minutes is generous for the real work involved — resolving
// local usage, merging snapshots, and gitops.Publish's own bounded
// fetch/rebase/push retry loop — while still keeping a genuinely stuck run
// bounded to a fraction of a scheduled run's own interval (hourly at the
// tightest, per Init's scaffolded scheduling entry), rather than
// indefinitely.
const runTimeout = 5 * time.Minute

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	if err := execute(ctx, newRootCmd()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// execute runs cmd with ctx as its execution context — via
// cmd.ExecuteContext, not the bare cmd.Execute(), so ctx (and its
// deadline/cancellation) reaches every subcommand's own cmd.Context().
// Factored out from main so a test can supply its own context (e.g. one
// with an already-expired deadline) and confirm it actually propagates,
// without needing to simulate a real multi-minute hang.
func execute(ctx context.Context, cmd *cobra.Command) error {
	return cmd.ExecuteContext(ctx)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "token-profile",
		Short:   "Render a GitHub profile dashboard card from local AI coding usage data",
		Version: version,
	}
	root.AddCommand(cli.NewRunCmd())
	root.AddCommand(cli.NewInitCmd())
	return root
}
