package obs

import (
	"log/slog"
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
	require.True(t, l.Handler().Enabled(nil, slog.LevelInfo))
	require.False(t, l.Handler().Enabled(nil, slog.LevelDebug))
}

func TestInitHonorsLevel(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	l := Init("error")
	require.False(t, l.Handler().Enabled(nil, slog.LevelWarn))
	require.True(t, l.Handler().Enabled(nil, slog.LevelError))
}
