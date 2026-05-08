package main

import (
	"testing"

	"github.com/wh0o0pz/isobar/internal/papertrade"
)

func TestClassifyResolution(t *testing.T) {
	cases := []struct {
		status string
		result string
		want   papertrade.Resolution
	}{
		{"settled", "yes", papertrade.ResolutionYes},
		{"settled", "no", papertrade.ResolutionNo},
		{"finalized", "yes", papertrade.ResolutionYes},
		{"expired", "no", papertrade.ResolutionNo},
		{"settled", "", papertrade.ResolutionVoid},
		// Mixed-case + whitespace tolerance.
		{"SETTLED", " Yes ", papertrade.ResolutionYes},
		// Open / pending markets.
		{"open", "", papertrade.ResolutionUnsettled},
		{"active", "", papertrade.ResolutionUnsettled},
		{"", "", papertrade.ResolutionUnsettled},
		// Unknown status — conservatively pending so we retry.
		{"weird-state", "yes", papertrade.ResolutionUnsettled},
		// Settled with unknown result string → void as defensive default.
		{"settled", "maybe", papertrade.ResolutionVoid},
	}
	for _, c := range cases {
		got := classifyResolution(c.status, c.result)
		if got != c.want {
			t.Errorf("classifyResolution(%q, %q) = %q, want %q",
				c.status, c.result, got, c.want)
		}
	}
}
