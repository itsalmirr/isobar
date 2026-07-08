package clockcheck

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// TestAnalyzerOnPassing exercises every allowed shape — the analyzer
// must emit zero diagnostics on this package.
func TestAnalyzerOnPassing(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "passing")
}

// TestAnalyzerOnFailing checks each of the three banned identifiers
// (time.Now / time.Since / time.Until) yields a diagnostic. // want
// comments inside testdata/src/failing/failing.go encode the expected
// messages.
func TestAnalyzerOnFailing(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "failing")
}

// TestCommentMatchesNolint covers the comment-parser table directly so
// future changes to the //nolint shape (e.g. adopting golangci's
// stricter syntax) keep the obvious cases green.
func TestCommentMatchesNolint(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"//nolint:clockcheck", true},
		{"//nolint:clockcheck // because foo", true},
		{"//nolint:gosec,clockcheck", true},
		{"//nolint:clockcheck,gosec", true},
		{"//nolint", true},
		{"//nolint // because", true},
		{"//nolint:gosec", false},
		{"// just a comment", false},
		{"//nolint:gosec,errcheck", false},
		{"// nolint:clockcheck", false}, // missing leading "//" structure (we trimmed already)
	}
	for _, tc := range cases {
		got := commentMatchesNolint(tc.in)
		if got != tc.want {
			t.Errorf("commentMatchesNolint(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestIsCmdMain documents the path shape: only files at cmd/<binary>/main.go
// match, and only one segment between cmd/ and main.go.
func TestIsCmdMain(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/repo/cmd/tape/main.go", true},
		{"/repo/cmd/edge-scan/main.go", true},
		{"/abs/path/cmd/x/main.go", true},
		{"/repo/cmd/main.go", false},                // no binary segment
		{"/repo/cmd/nested/sub/main.go", false},     // two segments
		{"/repo/internal/something/main.go", false}, // no /cmd/ prefix
		{"/repo/cmd/tape/other.go", false},          // not main.go
		{"", false},
	}
	for _, tc := range cases {
		if got := isCmdMain(tc.path); got != tc.want {
			t.Errorf("isCmdMain(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
