// Command token-profile renders a GitHub profile dashboard card from local
// AI-coding-session usage data. This entrypoint wires up the root command
// and attaches its subcommands; the actual logic behind each subcommand
// lives in internal/cli.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Christophe1997/token-profile/internal/cli"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
