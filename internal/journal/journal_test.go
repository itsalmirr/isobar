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

// TestOpenRejectsEmptyDirAndSource exercises Open's up-front validation
// — the happy-path tests always pass valid options so these branches
// stayed uncovered.
func TestOpenRejectsEmptyDirAndSource(t *testing.T) {
	_, err := Open(Options{Dir: "", Source: "x"}, nil)
	require.ErrorContains(t, err, "required")
	_, err = Open(Options{Dir: "x", Source: ""}, nil)
	require.ErrorContains(t, err, "required")
}

// TestOpenAcceptsNilLoggerAndDefaultsQueue verifies the two default-fill
// branches in Open: nil logger falls back to slog.Default, and a
// non-positive QueueSize gets bumped to 8192.
func TestOpenAcceptsNilLoggerAndDefaultsQueue(t *testing.T) {
	dir := t.TempDir()
	// QueueSize omitted → defaults to 8192; logger nil → slog.Default.
	j, err := Open(Options{
		Dir:          dir,
		Source:       "test",
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
	}, nil)
	require.NoError(t, err)
	j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"ok"`)})
	require.NoError(t, j.Close())
	files := walkExt(t, dir, ".jsonl")
	require.Len(t, files, 1)
}

// TestOpenInitialOpenFailsEntersDrainMode covers the path where the
// very first file-open fails inside run(). We trigger it by pointing
// Dir at a regular file — MkdirAll inside newPath will fail because a
// component in the path is a non-directory.
func TestOpenInitialOpenFailsEntersDrainMode(t *testing.T) {
	parent := t.TempDir()
	// Create a FILE where the journal expects to create a directory.
	junk := filepath.Join(parent, "not-a-dir")
	require.NoError(t, os.WriteFile(junk, []byte("nope"), 0o600))

	j, err := Open(Options{
		Dir:          junk, // MkdirAll on junk/2026-… will fail; junk is a file.
		Source:       "test",
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    4,
	}, nil)
	require.NoError(t, err, "Open returns; run() fails asynchronously")

	// Writing must not block (drain-mode goroutine is still reading j.closed).
	j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
	require.NoError(t, j.Close())
}

// TestGzipFileMissingSource exercises the `os.Open(src)` error branch
// in gzipFile directly (it's package-private, hence same-package test).
func TestGzipFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	require.Error(t, gzipFile(filepath.Join(dir, "does-not-exist.jsonl")))
}

// TestGzipFileCompressesRealFile covers the happy path of gzipFile.
// TestJournalRotatesOnSize hits it via the rotate callback; an
// explicit test keeps coverage stable if the rotate wiring changes.
func TestGzipFileCompressesRealFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jsonl")
	require.NoError(t, os.WriteFile(src, []byte("hello world\n"), 0o600))
	require.NoError(t, gzipFile(src))

	gzPath := src + ".gz"
	gzBytes, err := os.ReadFile(gzPath) //nolint:gosec // test-local path under t.TempDir()
	require.NoError(t, err)
	require.Greater(t, len(gzBytes), 0)

	f, err := os.Open(gzPath) //nolint:gosec // test-local path under t.TempDir()
	require.NoError(t, err)
	defer f.Close()
	gr, err := gzip.NewReader(f)
	require.NoError(t, err)
	data, err := io.ReadAll(gr)
	require.NoError(t, err)
	require.Equal(t, "hello world\n", string(data))
}

// TestNewPathCreatesNestedDir covers newPath's directory-creation
// branch. If the parent dir already exists, MkdirAll is a no-op; this
// test ensures it creates missing intermediates.
func TestNewPathCreatesNestedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	// dir doesn't exist yet.
	path, err := newPath(dir, "kalshi")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(path, dir))
	require.True(t, strings.HasSuffix(path, ".jsonl"))

	// Confirm the dated subdirectory was created.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "date-stamped subdir created")
	require.True(t, entries[0].IsDir())
}

// TestGzipFileFailsWhenDestExistsAsDirectory exercises the
// os.OpenFile(src+".gz") error branch — we pre-create a directory
// with the same name the gz file would take, so the create-or-truncate
// open fails.
func TestGzipFileFailsWhenDestExistsAsDirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jsonl")
	require.NoError(t, os.WriteFile(src, []byte("hello\n"), 0o600))
	// Block the .gz output path with a directory.
	require.NoError(t, os.MkdirAll(src+".gz", 0o750))
	err := gzipFile(src)
	require.Error(t, err)
}

// TestJournalAgeBasedRotation drives the run-loop's rotateTimer path.
// We shrink rotateCheckInterval so the test completes in <1s and set
// MaxFileAge to 10ms so the first tick rotates the current file.
func TestJournalAgeBasedRotation(t *testing.T) {
	prev := rotateCheckInterval
	rotateCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() { rotateCheckInterval = prev })

	j, dir := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   10 * time.Millisecond,
		QueueSize:    16,
	})

	// Write one frame, wait for a rotation tick, write another.
	j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"before"`)})
	time.Sleep(80 * time.Millisecond) // > rotateCheckInterval + MaxFileAge
	j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"after"`)})
	require.NoError(t, j.Close())

	files := walkExt(t, dir, ".jsonl")
	require.GreaterOrEqual(t, len(files), 2, "age-based rotation should produce >=2 files")
}

// TestJournalDropReportLogsWhenQueueFills drives the drop-report
// ticker branch (count > 0 triggers a warn log). We shrink
// dropReportInterval, overflow the queue, and wait for at least one
// tick to fire with a non-zero count.
func TestJournalDropReportLogsWhenQueueFills(t *testing.T) {
	prev := dropReportInterval
	dropReportInterval = 30 * time.Millisecond
	t.Cleanup(func() { dropReportInterval = prev })

	j, _ := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    2,
		FlushEvery:   time.Second, // don't flush, keep writer busy
	})
	for i := 0; i < 10_000; i++ {
		j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
	}
	// Give the drop-report ticker at least one fire.
	time.Sleep(80 * time.Millisecond)
	require.NoError(t, j.Close())
	require.Positive(t, j.Dropped())
}

// TestJournalDrainOnCloseWithPendingWrites ensures Close drains
// in-flight frames rather than discarding them.
func TestJournalDrainOnCloseWithPendingWrites(t *testing.T) {
	// Large queue, slow flush — frames sit in the channel when Close runs.
	j, dir := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    1000,
		FlushEvery:   5 * time.Second, // much longer than the test
	})
	const N = 500
	for i := 0; i < N; i++ {
		j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
	}
	require.NoError(t, j.Close())

	data, err := os.ReadFile(walkExt(t, dir, ".jsonl")[0])
	require.NoError(t, err)
	lines := strings.Count(string(data), "\n")
	require.Equal(t, N, lines, "all enqueued frames must be written on close")
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
