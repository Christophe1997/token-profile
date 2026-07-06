package render

import "testing"

// TestFormatTokens_UsesMillionAndBillionUnits covers the shortened-number
// display (R8): totals at or above one million render as "X.YM"/"X.YB"
// rather than a long comma-separated digit string, since the trend graph and
// breakdown lines both go through formatTokens and cache tokens (now
// included in Tokens) routinely push daily totals into the tens or hundreds
// of millions.
func TestFormatTokens_UsesMillionAndBillionUnits(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{"zero", 0, "0"},
		{"below thousand", 999, "999"},
		{"comma thousands", 12_345, "12,345"},
		{"just below million stays comma-formatted", 999_999, "999,999"},
		{"exact million", 1_000_000, "1M"},
		{"million with fraction", 1_230_000, "1.2M"},
		{"million rounds to whole", 1_999_999, "2M"},
		{"tens of millions", 12_345_678, "12.3M"},
		{"just below billion stays million-formatted", 999_000_000, "999M"},
		{"exact billion", 1_000_000_000, "1B"},
		{"billion with fraction", 2_500_000_000, "2.5B"},
		{"negative million", -1_500_000, "-1.5M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTokens(tt.n); got != tt.want {
				t.Errorf("formatTokens(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}
