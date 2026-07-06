package main

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestExecute_PropagatesContextToSubcommand covers the testable half of
// Fix 2's context-timeout requirement: main() must use ExecuteContext
// (which threads a caller-supplied context through to every subcommand's
// cmd.Context()), not the bare Execute() (which always yields an
// un-cancellable context.Background() inside RunE, letting a stuck
// agentsview or git subprocess hang forever). Exercising a real multi-
// minute hang isn't practical in a unit test, so this instead proves the
// wiring that makes main's timeout actually take effect: a context whose
// deadline has already passed must be observably expired from inside a
// subcommand's RunE.
func TestExecute_PropagatesContextToSubcommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done() // force the deadline to have already passed before executing

	var observedErr error
	cmd := &cobra.Command{
		Use: "probe",
		RunE: func(cmd *cobra.Command, args []string) error {
			observedErr = cmd.Context().Err()
			return nil
		},
	}

	if err := execute(ctx, cmd); err != nil {
		t.Fatalf("execute() error = %v, want nil", err)
	}
	if observedErr == nil {
		t.Error("subcommand's cmd.Context().Err() = nil, want the already-expired deadline visible inside RunE (proves ExecuteContext, not Execute, is wired up)")
	}
}
