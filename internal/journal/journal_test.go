package journal

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wh0o0pz/isobar/internal/clock"
)

func newTestJournal(t *testing.T, opts Options) (*Journal, string) {
	t.Helper()
	dir := t.TempDir()
	opts.Dir = dir
	if opts.Source == "" {
		opts.Source = "test"
	}
	if opts.QueueSize == 0 {
		opts.QueueSize = 64
	}
	if opts.FlushEvery == 0 {
		opts.FlushEvery = 50 * time.Millisecond
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	j, err := Open(opts, logger)
	require.NoError(t, err)
	return j, dir
}

func TestJournalRoundtrip(t *testing.T) {
	const N = 500
	// Queue must fit the full burst — Write is non-blocking by contract;
	// dropping is a feature, not something the roundtrip test wants to exercise.
	j, dir := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20, // big enough — no rotation
		MaxFileAge:   time.Hour,
		QueueSize:    2 * N,
	})

	for i := 0; i < N; i++ {
		j.Write(Frame{
			Stamp:   clock.Now(),
			Source:  "test",
			Kind:    "ws",
			Payload: json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`),
		})
	}
	require.NoError(t, j.Close())

	files := walkExt(t, dir, ".jsonl")
	require.Len(t, files, 1)

	count := 0
	f, err := os.Open(files[0])
	require.NoError(t, err)
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var got map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &got))
		require.Equal(t, "test", got["src"])
		require.Equal(t, "ws", got["k"])
		count++
	}
	require.NoError(t, sc.Err())
	require.Equal(t, N, count)
	require.Zero(t, j.Dropped())
}

func TestJournalDropsOnFullQueue(t *testing.T) {
	// Tiny queue, slow flush — guarantees overflow under burst.
	j, _ := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    4,
		FlushEvery:   500 * time.Millisecond,
	})

	for i := 0; i < 10_000; i++ {
		j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
	}
	require.NoError(t, j.Close())
	require.Greater(t, j.Dropped(), uint64(0), "expected some frames dropped under burst")
}

func TestJournalRotatesOnSize(t *testing.T) {
	const N = 200
	j, dir := newTestJournal(t, Options{
		MaxFileBytes:     1 << 10, // 1 KiB — easily exceeded
		MaxFileAge:       time.Hour,
		CompressOnRotate: true,
		QueueSize:        2 * N, // fit the burst so all records survive
	})

	for i := 0; i < N; i++ {
		j.Write(Frame{
			Source:  "test",
			Kind:    "ws",
			Payload: json.RawMessage(`{"d":"` + strings.Repeat("x", 80) + `"}`),
		})
	}
	require.NoError(t, j.Close())

	plain := walkExt(t, dir, ".jsonl")
	zipped := walkExt(t, dir, ".jsonl.gz")
	require.GreaterOrEqual(t, len(plain)+len(zipped), 2,
		"expected at least 2 files after rotation; got plain=%d gz=%d", len(plain), len(zipped))

	for _, p := range zipped {
		f, err := os.Open(p) //nolint:gosec // test-local path under t.TempDir()
		require.NoError(t, err)
		gr, err := gzip.NewReader(f)
		require.NoError(t, err, "gzipped file %s should be readable", p)
		_, err = io.ReadAll(gr)
		require.NoError(t, err)
		_ = gr.Close()
		_ = f.Close()
	}
}

func TestJournalWaitObservesContext(t *testing.T) {
	j, _ := newTestJournal(t, Options{MaxFileBytes: 1 << 20, MaxFileAge: time.Hour})
	t.Cleanup(func() { _ = j.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	j.Wait(ctx)
	require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
}

func TestJournalPayloadCopyDetachesCallerBuffer(t *testing.T) {
	// Producer reuses a backing slice between writes (mimics WS read buffer).
	j, dir := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
	})

	scratch := make([]byte, 64)
	for i := 0; i < 50; i++ {
		// Reuse `scratch`, mutate per write.
		buf := append(scratch[:0], []byte(`{"i":`+strconv.Itoa(i)+`}`)...)
		j.Write(Frame{Source: "test", Kind: "ws", Payload: json.RawMessage(buf)})
		// Now mutate scratch to ensure the journal already detached.
		for k := range scratch {
			scratch[k] = 0xFF
		}
	}
	require.NoError(t, j.Close())

	files := walkExt(t, dir, ".jsonl")
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)

	// Each line should still be a valid JSON object with monotonic i values.
	want := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &got))
		p, ok := got["p"].(map[string]any)
		require.True(t, ok)
		i, ok := p["i"].(float64)
		require.True(t, ok)
		require.Equal(t, want, int(i))
		want++
	}
	require.Equal(t, 50, want)
}

func walkExt(t *testing.T, dir, suffix string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && strings.HasSuffix(p, suffix) {
			out = append(out, p)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}
