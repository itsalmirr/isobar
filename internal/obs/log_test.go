package obs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLevelKnown(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"  debug  ", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"bogus", slog.LevelInfo}, // unknown falls back to info
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, parseLevel(tc.in))
		})
	}
}

func TestInitInstallsDefaultLogger(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	l := Init("debug")
	require.NotNil(t, l)
	require.Same(t, l, slog.Default())
}

func TestInitReturnsNonNilForUnknownLevel(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	l := Init("nonsense")
	require.NotNil(t, l)
	// With level=info, debug should be filtered out but info allowed.
	require.True(t, l.Handler().Enabled(context.Background(), slog.LevelInfo))
	require.False(t, l.Handler().Enabled(context.Background(), slog.LevelDebug))
}

func TestInitHonorsLevel(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	l := Init("error")
	require.False(t, l.Handler().Enabled(context.Background(), slog.LevelWarn))
	require.True(t, l.Handler().Enabled(context.Background(), slog.LevelError))
}

// TestInitRenamesTimeKeyToTs exercises the ReplaceAttr callback — Init's
// one behavior that isn't a simple pass-through. We redirect os.Stderr
// temporarily, emit one log line, and assert the rendered JSON has
// `"ts":` (the renamed key) rather than slog's default `"time":`.
func TestInitRenamesTimeKeyToTs(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	l := Init("info")
	l.Info("hello", "foo", "bar")
	require.NoError(t, w.Close())

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	got := string(out)
	require.Contains(t, got, `"ts":`, "Init must rename the default time key to ts")
	require.NotContains(t, got, `"time":`, "raw time key must not leak through")
	require.Contains(t, got, `"msg":"hello"`)
	require.Contains(t, got, `"foo":"bar"`)
}
