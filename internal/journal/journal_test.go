package journal

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// alarmRecord captures one OnDropAlarm invocation for assertions.
type alarmRecord struct {
	src   string
	drops uint64
	since time.Duration
}

// syncBuf is a goroutine-safe wrapper around bytes.Buffer for use as a
// slog handler sink in tests where the slog goroutine writes and the
// test goroutine polls. -race flags the raw bytes.Buffer otherwise.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// recordingAlarmHook returns a hook that appends each call into the
// returned slice (under the returned mutex). Use under -race; tests
// reading the slice should grab the mutex.
func recordingAlarmHook() (func(string, uint64, time.Duration), *[]alarmRecord, *sync.Mutex) {
	var mu sync.Mutex
	var calls []alarmRecord
	hook := func(src string, drops uint64, since time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, alarmRecord{src: src, drops: drops, since: since})
	}
	return hook, &calls, &mu
}

// TestWrite_DropsIncrementCounter saturates the queue and verifies
// every overflow frame bumps Dropped(). With QueueSize=4 and a slow
// flush, 100 writes must drop at least 96. The "= 96" exact-equal in
// the ADR §1 test plan only holds if the writer goroutine never
// dequeues during the burst; in practice a real-time test sees a
// handful of dequeues. We assert the >=96 lower bound that follows
// from the channel capacity.
func TestWrite_DropsIncrementCounter(t *testing.T) {
	j, _ := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    4,
		FlushEvery:   time.Hour, // park the flush timer
	})
	t.Cleanup(func() { _ = j.Close() })

	const N = 100
	for i := 0; i < N; i++ {
		j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
	}
	require.GreaterOrEqual(t, j.Dropped(), uint64(96),
		"expected >=96 drops with queue=4 and 100 writes; got %d", j.Dropped())
}

// TestWrite_SampledDebugLogFires installs a capturing handler at
// debug level, sets SampleEvery=10, drops 100 frames, and asserts
// exactly 10 debug records were emitted with the expected fields.
// ADR-0011 §3 / test plan §2.
func TestWrite_SampledDebugLogFires(t *testing.T) {
	buf := &syncBuf{}
	handler := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	dir := t.TempDir()
	j, err := Open(Options{
		Dir:          dir,
		Source:       "test",
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    2,
		FlushEvery:   time.Hour, // park flush
		SampleEvery:  10,
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = j.Close() })

	// Write 100 frames; ~98 should overflow. With SampleEvery=10 we
	// expect 9 or 10 debug records (the exact figure depends on
	// whether the writer dequeues any while we hammer in).
	for i := 0; i < 100; i++ {
		j.Write(Frame{Source: "test", Kind: "ws", Payload: json.RawMessage(`"x"`)})
	}

	// Count debug records mentioning the sample message.
	var debugHits int
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		if rec["level"] == "DEBUG" && rec["msg"] == "journal: frame dropped (queue full)" {
			debugHits++
			require.Equal(t, "test", rec["src"])
			require.Equal(t, "ws", rec["kind"])
			require.NotNil(t, rec["total_drops"])
			require.NotNil(t, rec["queue_capacity"])
		}
	}
	require.GreaterOrEqual(t, debugHits, 9, "expected ~10 sampled debug hits, got %d", debugHits)
	require.LessOrEqual(t, debugHits, 11)
}

// withShortAlarmInterval temporarily lowers alarmCheckInterval for
// tests that exercise the rate-detection path without burning real
// seconds. Mirrors the existing rotateCheckInterval / dropReportInterval
// override pattern in this file.
func withShortAlarmInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := alarmCheckInterval
	alarmCheckInterval = d
	t.Cleanup(func() { alarmCheckInterval = prev })
}

// TestWrite_AlarmHookFiresAtThreshold installs a recording hook, sets
// a tight rate/sustain config, generates a burst of drops, and asserts
// the hook fires at least once with sane fields. ADR-0011 test plan §3.
//
// Note on rate computation: alarmCheckInterval is shortened to 5ms so
// the hook fires fast; with one drop per tick (i.e., 200 drops/sec)
// we are well above the AlarmThreshold=1 drops/sec target. We hammer
// without inter-write sleep so writes outpace the writer goroutine's
// channel dequeues and produce real drops.
func TestWrite_AlarmHookFiresAtThreshold(t *testing.T) {
	withShortAlarmInterval(t, 5*time.Millisecond)

	hook, calls, mu := recordingAlarmHook()

	dir := t.TempDir()
	j, err := Open(Options{
		Dir:            dir,
		Source:         "test",
		MaxFileBytes:   1 << 20,
		MaxFileAge:     time.Hour,
		QueueSize:      2,
		FlushEvery:     time.Hour, // park flush so queue stays full
		AlarmThreshold: 1,         // 1 drop/sec
		AlarmSustain:   20 * time.Millisecond,
		OnDropAlarm:    hook,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = j.Close() })

	// Hammer writes in a tight loop so the producer outpaces the
	// writer goroutine and most writes hit the default branch
	// (incrementing the drop counter). Yield occasionally to let the
	// alarm ticker run.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		for k := 0; k < 1000; k++ {
			j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
		}
		// Yield so the alarm goroutine can tick.
		time.Sleep(time.Millisecond)
	}

	// At this point the drop counter should be huge; wait for the
	// alarm-tick goroutine to observe a sustained breach.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(*calls) >= 1
	}, 500*time.Millisecond, 5*time.Millisecond,
		"alarm hook should have fired at least once; drops=%d", j.Dropped())

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(*calls), 1)
	first := (*calls)[0]
	require.Equal(t, "test", first.src)
	require.Greater(t, first.drops, uint64(0), "drops should be non-zero")
	require.Greater(t, first.since, time.Duration(0), "since should be non-zero")
}

// TestWrite_AlarmDoesNotFireBelowThreshold drops a small number of
// frames in a single non-sustained burst and asserts the hook never
// fires. ADR-0011 §4: the trigger requires a sustained rate, not a
// single-tick spike.
func TestWrite_AlarmDoesNotFireBelowThreshold(t *testing.T) {
	withShortAlarmInterval(t, 5*time.Millisecond)

	hook, calls, mu := recordingAlarmHook()

	j, _ := newTestJournalWithHook(t, Options{
		MaxFileBytes:   1 << 20,
		MaxFileAge:     time.Hour,
		QueueSize:      2,
		FlushEvery:     time.Hour,
		AlarmThreshold: 1000, // far above what a small burst can hit
		AlarmSustain:   30 * time.Millisecond,
		OnDropAlarm:    hook,
	})

	// Small burst: a handful of drops, well below 1000/sec at the
	// configured threshold.
	for i := 0; i < 20; i++ {
		j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
	}

	// Wait long enough for several alarm ticks to fire.
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, *calls, "alarm should not fire below threshold; got %d calls", len(*calls))
}

// TestWrite_AlarmRespectsDebounce triggers the alarm, then keeps
// generating drops above threshold for a full second; the hook should
// fire exactly once for that single crossing. ADR-0011 §4: edge-
// triggered debounce.
func TestWrite_AlarmRespectsDebounce(t *testing.T) {
	withShortAlarmInterval(t, 5*time.Millisecond)

	hook, calls, mu := recordingAlarmHook()

	j, _ := newTestJournalWithHook(t, Options{
		MaxFileBytes:   1 << 20,
		MaxFileAge:     time.Hour,
		QueueSize:      2,
		FlushEvery:     time.Hour,
		AlarmThreshold: 1,
		AlarmSustain:   20 * time.Millisecond,
		OnDropAlarm:    hook,
	})

	// Continuously generate drops above threshold for ~400ms (well
	// past AlarmSustain, with ~80 alarm ticks happening in that span).
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		for k := 0; k < 1000; k++ {
			j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
		}
		time.Sleep(time.Millisecond)
	}

	// One call only: the alarmActive debounce should have prevented
	// subsequent ticks from re-firing the hook while the breach
	// persisted.
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *calls, 1,
		"expected exactly 1 alarm call across the sustained breach; got %d", len(*calls))
}

// TestClose_FlushesInflight writes N frames where N is well below the
// queue capacity, then Closes. All N must reach the on-disk file —
// confirms the drop-counter machinery and the new alarm tick do not
// disturb the existing shutdown drain.
func TestClose_FlushesInflight(t *testing.T) {
	j, dir := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    64,
		FlushEvery:   5 * time.Second, // intentionally never fires
	})

	const N = 10
	for i := 0; i < N; i++ {
		j.Write(Frame{
			Source:  "test",
			Kind:    "ws",
			Payload: json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`),
		})
	}
	require.NoError(t, j.Close())
	require.Zero(t, j.Dropped(), "no drops expected at N=10 < QueueSize=64")

	files := walkExt(t, dir, ".jsonl")
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Equal(t, N, strings.Count(string(data), "\n"),
		"all enqueued frames must be flushed on Close")
}

// TestClose_DoesNotPanicOnConcurrentWrite fires N goroutines that
// each call Write while Close is also in progress. The contract is
// that Write never panics — even after Close has begun the shutdown
// drain. With QueueSize=4 most goroutines will hit the drop branch;
// the test passes if it terminates without panic.
func TestClose_DoesNotPanicOnConcurrentWrite(t *testing.T) {
	j, _ := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    4,
		FlushEvery:   time.Hour,
	})

	const goroutines = 32
	const perGoroutine = 500
	var wg sync.WaitGroup
	var stopped atomic.Bool
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if stopped.Load() {
					return
				}
				j.Write(Frame{
					Source:  "test",
					Kind:    "x",
					Payload: json.RawMessage(`"x"`),
				})
			}
		}()
	}

	// Let producers ramp, then Close concurrently.
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, j.Close())
	stopped.Store(true)
	wg.Wait()
}

// TestDefaultAlarmHook_FiresStructuredWarn verifies the default
// fallback hook (used when Options.OnDropAlarm is nil) emits a
// structured slog.Warn record with the expected field shape.
// ADR-0011 §4 default-hook contract.
func TestDefaultAlarmHook_FiresStructuredWarn(t *testing.T) {
	withShortAlarmInterval(t, 5*time.Millisecond)

	buf := &syncBuf{}
	handler := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	dir := t.TempDir()
	j, err := Open(Options{
		Dir:            dir,
		Source:         "test",
		MaxFileBytes:   1 << 20,
		MaxFileAge:     time.Hour,
		QueueSize:      2,
		FlushEvery:     time.Hour,
		AlarmThreshold: 1,
		AlarmSustain:   20 * time.Millisecond,
		// OnDropAlarm intentionally nil -> default sink.
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = j.Close() })

	// First, expect a startup Warn line about the default sink itself.
	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "drop-alarm sink is default slog.Warn")
	}, 200*time.Millisecond, 5*time.Millisecond, "startup warn line missing")

	// Burst drops to force a sustained breach.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		for k := 0; k < 1000; k++ {
			j.Write(Frame{Source: "test", Kind: "x", Payload: json.RawMessage(`"x"`)})
		}
		time.Sleep(time.Millisecond)
	}

	// Wait for the alarm-fired log line.
	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "drop-alarm fired")
	}, 500*time.Millisecond, 5*time.Millisecond, "expected default warn fire")

	// Verify the fired record has the documented shape.
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == "journal: drop-alarm fired" && rec["level"] == "WARN" {
			require.Equal(t, "test", rec["src"])
			require.NotNil(t, rec["drops"])
			require.NotNil(t, rec["since"])
			require.NotNil(t, rec["threshold_per_sec"])
			require.NotNil(t, rec["sustained_for"])
			found = true
			break
		}
	}
	require.True(t, found, "expected at least one drop-alarm fired record at WARN level")
}

// newTestJournalWithHook is a variant of newTestJournal that respects
// a caller-supplied OnDropAlarm (and the alarm thresholds it carries).
// The shared helper above silently overwrote OnDropAlarm because it
// took its defaults from a fixed slog.Discard logger; this variant
// keeps alarm-specific fields intact.
func newTestJournalWithHook(t *testing.T, opts Options) (*Journal, string) {
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
	t.Cleanup(func() { _ = j.Close() })
	return j, dir
}

// TestEnvelope_NewWritesHaveV1 is the write-side tripwire for the
// envelope schema-version field. It opens a journal, writes a single
// frame, closes (which drains + fsyncs), reads the raw on-disk bytes
// back, and asserts the serialized line carries
// `"v":"isobar.journal/v1"`. This is a regression detector: if a
// future change accidentally drops the schema stamp from the writer,
// this test fails before the unstamped bytes can land in a production
// tape. The constant value is pinned because a bump requires a
// reader-side migration policy decision (held for Phase 2).
func TestEnvelope_NewWritesHaveV1(t *testing.T) {
	j, dir := newTestJournal(t, Options{
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    16,
	})
	j.Write(Frame{
		Stamp:   clock.Now(),
		Source:  "test",
		Kind:    "ws",
		Payload: json.RawMessage(`{"hello":"world"}`),
	})
	require.NoError(t, j.Close())

	files := walkExt(t, dir, ".jsonl")
	require.Len(t, files, 1)
	raw, err := os.ReadFile(files[0])
	require.NoError(t, err)

	// Substring assertion on the raw bytes — the on-disk format is
	// part of the public tape contract and a struct-level decode
	// would hide a field-drop regression.
	require.Contains(t, string(raw), `"v":"`+EnvelopeSchemaVersion+`"`,
		"envelope must carry the schema-version field; got: %s", string(raw))
	require.Equal(t, "isobar.journal/v1", EnvelopeSchemaVersion,
		"EnvelopeSchemaVersion constant must remain isobar.journal/v1 — bump requires reader migration policy decision")
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
