package readme

import (
	"errors"
	"strings"
	"testing"
)

func TestInject_HappyPath_ReplacesOnlyContentBetweenMarkers(t *testing.T) {
	const before = `# My Profile

Some intro text.

<!-- token-profile:start -->
old content line 1
old content line 2
<!-- token-profile:end -->

Footer text.
`
	const want = `# My Profile

Some intro text.

<!-- token-profile:start -->
new line 1
new line 2
<!-- token-profile:end -->

Footer text.
`

	got, err := Inject([]byte(before), "new line 1\nnew line 2")
	if err != nil {
		t.Fatalf("Inject() error = %v, want nil", err)
	}
	if string(got) != want {
		t.Errorf("Inject() = %q, want %q", got, want)
	}
}

func TestInject_MissingMarkers_ReturnsActionableError(t *testing.T) {
	tests := []struct {
		name   string
		readme string
	}{
		{"no markers at all", "# My Profile\n\nNo markers here.\n"},
		{"missing end marker", "# My Profile\n\n<!-- token-profile:start -->\nold\n"},
		{"missing start marker", "# My Profile\n\nold\n<!-- token-profile:end -->\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Inject([]byte(tt.readme), "new content")
			if err == nil {
				t.Fatal("Inject() error = nil, want an error about missing markers")
			}
			if !errors.Is(err, ErrMarkersMissing) {
				t.Errorf("Inject() error = %v, want it to wrap ErrMarkersMissing", err)
			}
			if !strings.Contains(err.Error(), "init") {
				t.Errorf("Inject() error = %q, want it to mention the init command", err.Error())
			}
		})
	}
}
