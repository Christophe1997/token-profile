// Command token-profile renders a GitHub profile dashboard card from local
// AI-coding-session usage data. This entrypoint currently only wires up the
// root command; `run` and `init` subcommands are added in later units.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
	return &cobra.Command{
		Use:     "token-profile",
		Short:   "Render a GitHub profile dashboard card from local AI coding usage data",
		Version: version,
	}
}
