package replay_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wh0o0pz/isobar/internal/clock"
	"github.com/wh0o0pz/isobar/internal/journal"
	"github.com/wh0o0pz/isobar/internal/replay"
)

func TestVerify_JournalToReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	j, err := journal.Open(journal.Options{
		Dir:          dir,
		Source:       "kalshi",
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    64,
		OnDropAlarm:  journal.NoopAlarmHook,
	}, logger)
	require.NoError(t, err)

	type want struct {
		wall, mono int64
		source     string
		kind       string
		payload    json.RawMessage
	}
	frames := []want{
		{100, 1, "kalshi", "ws", json.RawMessage(`{"type":"subscribed"}`)},
		{200, 2, "kalshi", "ws", json.RawMessage(`{"type":"orderbook_delta","x":[1,2,3]}`)},
		{300, 3, "openmeteo", "http", json.RawMessage(`{"temp":21.4}`)},
	}
	for _, f := range frames {
		j.Write(journal.Frame{
			Stamp:   clock.Stamp{Wall: f.wall, Mono: f.mono},
			Source:  f.source,
			Kind:    f.kind,
			Payload: f.payload,
		})
	}
	require.NoError(t, j.Close())

	// Locate the produced .jsonl
	var produced string
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && filepath.Ext(p) == ".jsonl" {
			produced = p
		}
		return nil
	}))
	require.NotEmpty(t, produced)

	// Also produce a gzip copy to exercise the .gz reader path.
	raw, err := os.ReadFile(produced) //nolint:gosec // test-only: path discovered under t.TempDir()
	require.NoError(t, err)
	gzPath := produced + ".gz"
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err = gw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	require.NoError(t, os.WriteFile(gzPath, buf.Bytes(), 0o600))

	for _, path := range []string{produced, gzPath} {
		var got []replay.Envelope
		require.NoError(t, replay.ForEachFrame([]string{path}, func(env replay.Envelope, _ int, _ bool) error {
			got = append(got, env)
			return nil
		}))
		require.Len(t, got, len(frames), "path=%s", path)
		for i, f := range frames {
			require.Equal(t, f.wall, got[i].Wall)
			require.Equal(t, f.mono, got[i].Mono)
			require.Equal(t, f.source, got[i].Source)
			require.Equal(t, f.kind, got[i].Kind)
			require.Equal(t, journal.EnvelopeSchemaVersion, got[i].Version)
			require.JSONEq(t, string(f.payload), string(got[i].Payload))
			require.True(t, bytes.Equal(f.payload, got[i].Payload),
				"payload bytes must pass through untouched: want %s got %s", f.payload, got[i].Payload)
		}
	}
}
