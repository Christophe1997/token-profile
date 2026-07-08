package main

import (
	"context"
	"runtime/debug"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestResolveVersion covers the gap between GoReleaser's ldflags-injected
// version (which `go build`/`go install pkg@latest` never set, always
// leaving the "dev" sentinel) and what `go install pkg@version` actually
// embeds: the resolved module version in runtime/debug.BuildInfo. Without
// this fallback, every `go install`-installed binary reports "dev"
// regardless of which tag was actually installed.
func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name         string
		buildVersion string
		info         *debug.BuildInfo
		ok           bool
		want         string
	}{
		{
			name:         "ldflags override wins over build info",
			buildVersion: "v1.0.1",
			info:         &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}},
			ok:           true,
			want:         "v1.0.1",
		},
		{
			name:         "go install pkg@version falls back to the resolved module version",
			buildVersion: "dev",
			info:         &debug.BuildInfo{Main: debug.Module{Version: "v1.0.1"}},
			ok:           true,
			want:         "v1.0.1",
		},
		{
			name:         "a local go build from source stays dev, not (devel)",
			buildVersion: "dev",
			info:         &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			ok:           true,
			want:         "dev",
		},
		{
			name:         "build info unavailable stays dev",
			buildVersion: "dev",
			info:         nil,
			ok:           false,
			want:         "dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.buildVersion, tt.info, tt.ok); got != tt.want {
				t.Errorf("resolveVersion(%q, %+v, %v) = %q, want %q", tt.buildVersion, tt.info, tt.ok, got, tt.want)
			}
		})
	}
}

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
