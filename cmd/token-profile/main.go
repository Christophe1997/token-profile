// Command token-profile renders a GitHub profile dashboard card from local
// AI-coding-session usage data. This entrypoint wires up the root command
// and attaches its subcommands; the actual logic behind each subcommand
// lives in internal/cli.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"

	"github.com/Christophe1997/token-profile/internal/cli"
)

// version is overridden at build time via -ldflags by GoReleaser's own
// build. It stays the "dev" sentinel for a plain `go build` or, notably,
// `go install .../token-profile@latest` — the install path this repo's own
// README documents — since neither passes ldflags; resolveVersion covers
// that gap.
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

// resolveVersion returns the effective version: buildVersion as-is once
// GoReleaser's ldflags have overridden the "dev" sentinel, otherwise the
// resolved module version debug.ReadBuildInfo reports for a binary fetched
// via `go install module@version` — falling back to buildVersion itself
// when even that isn't available (a local `go build`, which leaves
// info.Main.Version as the literal string "(devel)").
func resolveVersion(buildVersion string, info *debug.BuildInfo, ok bool) string {
	if buildVersion != "dev" {
		return buildVersion
	}
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return buildVersion
}

func newRootCmd() *cobra.Command {
	info, ok := debug.ReadBuildInfo()
	root := &cobra.Command{
		Use:     "token-profile",
		Short:   "Render a GitHub profile dashboard card from local AI coding usage data",
		Version: resolveVersion(version, info, ok),
	}
	root.AddCommand(cli.NewRunCmd())
	root.AddCommand(cli.NewInitCmd())
	root.AddCommand(cli.NewCleanupCmd())
	root.AddCommand(cli.NewStatusCmd())
	return root
}
