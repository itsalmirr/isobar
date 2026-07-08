package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wh0o0pz/isobar/internal/config"
	"github.com/wh0o0pz/isobar/internal/journal"
	"github.com/wh0o0pz/isobar/internal/kalshi"
)

// TestWriteBootFrameShape verifies the boot frame that lands in a
// journal: src must be "isobar-tape", kind must be "boot", and the
// payload must decode into a bootPayload whose schema_version is
// exactly the published constant. Downstream tools key off these
// fields, so the test is a public-contract guard.
func TestWriteBootFrameShape(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	j, err := journal.Open(journal.Options{
		Dir:          dir,
		Source:       "kalshi",
		MaxFileBytes: 1 << 20,
		MaxFileAge:   time.Hour,
		QueueSize:    16,
		FlushEvery:   10 * time.Millisecond,
		OnDropAlarm:  journal.NoopAlarmHook,
	}, logger)
	require.NoError(t, err)

	const sha = "deadbeefcafef00d"
	writeBootFrame(j, sha)
	require.NoError(t, j.Close())

	// Read the single line back.
	envs := readEnvelopes(t, dir)
	require.Len(t, envs, 1, "expected exactly one boot frame")

	env := envs[0]
	require.Equal(t, "isobar-tape", env["src"], "src must mark this as a cutover frame")
	require.Equal(t, "boot", env["k"], "kind must be 'boot'")

	rawPayload, ok := env["p"]
	require.True(t, ok, "envelope missing payload")

	// Re-marshal map[string]any -> bytes -> bootPayload to get typed access.
	payloadBytes, err := json.Marshal(rawPayload)
	require.NoError(t, err)

	var bp bootPayload
	require.NoError(t, json.Unmarshal(payloadBytes, &bp))

	require.Equal(t, sha, bp.ConfigSHA256)
	require.Equal(t, bootSchemaVersion, bp.SchemaVersion)
	require.NotEmpty(t, bp.Stations, "stations must be populated from canonical registry")

	// Spot-check Chicago — its presence at canonical KMDW coords is
	// the regression the audit/chi-blast-radius.md memo highlights.
	var foundCHI bool
	for _, s := range bp.Stations {
		if s.Location == "CHI" {
			foundCHI = true
			require.InDelta(t, 41.7842, s.Lat, 0.0001, "CHI lat must be canonical KMDW")
			require.InDelta(t, -87.7553, s.Lon, 0.0001, "CHI lon must be canonical KMDW")
		}
	}
	require.True(t, foundCHI, "boot frame missing CHI station entry")
}

// TestWriteBootFrameWritesToBothStreams confirms the contract that
// each journal opened by tape gets its own boot frame: one in
// kalshi/, one in openmeteo/. Without this, downstream code that
// partitions a single stream can't anchor on the boot marker.
func TestWriteBootFrameWritesToBothStreams(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, src := range []string{"kalshi", "openmeteo"} {
		t.Run(src, func(t *testing.T) {
			dir := t.TempDir()
			j, err := journal.Open(journal.Options{
				Dir:          dir,
				Source:       src,
				MaxFileBytes: 1 << 20,
				MaxFileAge:   time.Hour,
				QueueSize:    8,
				FlushEvery:   10 * time.Millisecond,
				OnDropAlarm:  journal.NoopAlarmHook,
			}, logger)
			require.NoError(t, err)
			writeBootFrame(j, "sha-"+src)
			require.NoError(t, j.Close())

			envs := readEnvelopes(t, dir)
			require.Len(t, envs, 1)
			require.Equal(t, "boot", envs[0]["k"])
		})
	}
}

// TestComputeConfigSHADeterministic verifies the hash function is
// stable for identical bytes and that the empty-path branch returns
// "" rather than panicking — that branch is hit by ephemeral tests
// where Load("") was the operator's intent.
func TestComputeConfigSHADeterministic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	body := []byte("data_dir: ./d\n")
	require.NoError(t, os.WriteFile(path, body, 0o600))

	a := computeConfigSHA(path, logger)
	b := computeConfigSHA(path, logger)
	require.Equal(t, a, b)
	require.Len(t, a, 64, "sha-256 hex must be 64 chars")

	require.Empty(t, computeConfigSHA("", logger), "empty path → empty SHA")
	require.Empty(t,
		computeConfigSHA(filepath.Join(dir, "missing"), logger),
		"missing file logs warn and returns empty",
	)
}

// readEnvelopes opens the only journal file under dir and decodes
// every NDJSON envelope. Test-only helper; mirrors the on-disk shape
// declared by internal/journal.envelope.
func readEnvelopes(t *testing.T, dir string) []map[string]any {
	t.Helper()

	var jsonlPath string
	require.NoError(t, filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".jsonl") {
			jsonlPath = p
		}
		return nil
	}))
	require.NotEmpty(t, jsonlPath, "no .jsonl file produced under %s", dir)

	f, err := os.Open(jsonlPath) //nolint:gosec // test-only, path produced by journal package
	require.NoError(t, err)
	defer f.Close()

	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var env map[string]any
		require.NoError(t, json.Unmarshal(line, &env), "decode line: %s", string(line))
		out = append(out, env)
	}
	require.NoError(t, sc.Err())
	return out
}

// TestRunRediscoveryHonorsCtxCancel asserts the bounded-shutdown
// contract: when the parent context is cancelled, runRediscovery
// returns nil within 5 seconds. The 5s bound is the same one the
// errgroup pattern in run() relies on — without it, a stale
// rediscovery goroutine could keep firing API calls after the rest
// of the process has stopped.
func TestRunRediscoveryHonorsCtxCancel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Real rest + sub objects. We never reach the t.C branch (interval
	// is hours, ctx is cancelled immediately), so the nil-ish signer
	// inside these never gets dereferenced.
	rest := kalshiNewClientForTest()
	sub := kalshiNewSubscriberForTest(logger)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runRediscovery(ctx, logger, rest, sub, []string{"KXHIGHNY"}, 1*time.Hour)
	}()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "runRediscovery must return nil on graceful cancel")
	case <-time.After(5 * time.Second):
		t.Fatal("runRediscovery did not return within 5s of ctx cancel")
	}
}

// TestRunRediscoveryRecoversPanic exercises the defer/recover path
// by injecting a panic at the very first statement of the function.
// runRediscovery's first line is `logger.Info(...)`; passing a nil
// *slog.Logger guarantees a nil-deref panic before the loop starts.
// The recover block must catch it and return a non-nil error so the
// parent errgroup terminates rather than orphans the goroutine.
//
// Note: this is the only realistic injection point without mocking
// the kalshi client. The recover catches any panic regardless of
// origin, so the test still validates the production-relevant case.
func TestRunRediscoveryRecoversPanic(t *testing.T) {
	rest := kalshiNewClientForTest()
	sub := kalshiNewSubscriberForTest(slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		// nil logger → logger.Info panics on first call.
		done <- runRediscovery(ctx, nil, rest, sub, []string{"KXHIGHNY"}, 1*time.Hour)
	}()

	select {
	case err := <-done:
		require.Error(t, err, "panic must convert to error")
		require.Contains(t, err.Error(), "panic")
	case <-time.After(5 * time.Second):
		t.Fatal("runRediscovery did not return within 5s of panic")
	}
}

// TestProdJournalHasAlarmHookWired is the static tripwire for A1
// (ADR-0011). It exercises the production journal-construction path
// (openJournal) with a representative config fixture and asserts the
// returned journal succeeded — which is only possible if openJournal
// passed a non-nil OnDropAlarm to journal.Open (Open returns
// ErrAlarmHookRequired otherwise).
//
// This is the regression detector: if anyone ever drops the hook
// wiring from openJournal (or rebuilds it with a typo), this test
// fails on the build, before the unwired binary can ship.
func TestProdJournalHasAlarmHookWired(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		DataDir: t.TempDir(),
		Journal: config.Journal{
			MaxFileBytes:     1 << 20,
			MaxFileAge:       time.Hour,
			FlushEvery:       250 * time.Millisecond,
			QueueSize:        16,
			CompressOnRotate: false,
		},
	}
	alarms := newAlarmSink(cfg.DataDir, logger)
	t.Cleanup(func() { _ = alarms.Close() })

	for _, src := range []string{"kalshi", "openmeteo"} {
		t.Run(src, func(t *testing.T) {
			j, err := openJournal(cfg, src, logger, alarms)
			require.NoError(t, err,
				"openJournal must construct successfully with a wired hook; "+
					"if it returns ErrAlarmHookRequired here, the alarm hook "+
					"has been silently dropped from cmd/tape's wiring")
			t.Cleanup(func() { _ = j.Close() })
		})
	}
}

// TestProdJournalAlarmHookEmitsCounterLogAndFile is the dynamic
// regression test for A1. It forces enough drops through a journal
// built by openJournal to cross the alarm threshold, then asserts the
// three sinks the hook wires:
//
//  1. JournalDropsTotal() bumps by at least N (the process-wide
//     counter, future Prometheus surface).
//  2. The alarms file exists at <DataDir>/alarms/<YYYY-MM-DD>.jsonl.
//  3. The file contains at least one well-formed alarm record with
//     the documented fields (ts, src, dropped, queue_depth,
//     since_last_drop_ms).
//
// Without the A1 hook wiring this test fails at JournalDropsTotal()
// (still zero) AND at the file-exists assertion. With it, all three
// pass. The test overrides journal.alarmCheckInterval and
// alarmSustainForTest so the breach window collapses to ms.
func TestProdJournalAlarmHookEmitsCounterLogAndFile(t *testing.T) {
	prev := journal.SetAlarmCheckIntervalForTest(5 * time.Millisecond)
	t.Cleanup(func() { journal.SetAlarmCheckIntervalForTest(prev) })

	// Reduce AlarmSustain so the crossing-edge fires within the test
	// run instead of waiting the 60s production default.
	prevSustain := alarmSustainForTest
	prevThreshold := alarmThresholdForTest
	alarmSustainForTest = 20 * time.Millisecond
	alarmThresholdForTest = 1 // 1 drop/sec is plenty for the burst below
	t.Cleanup(func() {
		alarmSustainForTest = prevSustain
		alarmThresholdForTest = prevThreshold
	})

	// Snapshot the process counter so we can measure the delta this
	// test contributes; other parallel tests in the same binary may
	// have left non-zero state behind.
	before := JournalDropsTotal()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dataDir := t.TempDir()
	cfg := config.Config{
		DataDir: dataDir,
		Journal: config.Journal{
			MaxFileBytes: 1 << 20,
			MaxFileAge:   time.Hour,
			FlushEvery:   time.Hour, // park the flush so the queue stays full
			QueueSize:    2,         // tiny — guaranteed overflow
		},
	}
	alarms := newAlarmSink(dataDir, logger)
	t.Cleanup(func() { _ = alarms.Close() })

	j, err := openJournal(cfg, "kalshi", logger, alarms)
	require.NoError(t, err)
	t.Cleanup(func() { _ = j.Close() })

	// Hammer drops in a tight loop, yielding occasionally so the
	// alarm-check tick goroutine actually runs. Mirrors the pattern
	// from internal/journal/journal_test.go's alarm tests.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		for k := 0; k < 1000; k++ {
			j.Write(journal.Frame{Source: "kalshi", Kind: "x", Payload: []byte(`"x"`)})
		}
		time.Sleep(time.Millisecond)
	}

	// (1) Process counter — bump means the hook fired at least once.
	require.Eventually(t, func() bool {
		return JournalDropsTotal() > before
	}, 500*time.Millisecond, 5*time.Millisecond,
		"JournalDropsTotal must bump after sustained drops; before=%d now=%d drops=%d",
		before, JournalDropsTotal(), j.Dropped())

	// (2) Alarms file exists at the documented path.
	day := time.Now().UTC().Format("2006-01-02")
	alarmFile := filepath.Join(dataDir, "alarms", day+".jsonl")
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(alarmFile)
		return statErr == nil
	}, 500*time.Millisecond, 5*time.Millisecond,
		"alarms file should exist at %s after sustained drops", alarmFile)

	// (3) File has at least one well-formed record with the spec's
	// fields. May have more than one if debounce permits a clear+
	// re-cross during the burst — we assert at-least, not equals.
	data, err := os.ReadFile(alarmFile) //nolint:gosec // test-local path under t.TempDir()
	require.NoError(t, err)
	require.NotEmpty(t, data, "alarms file is empty after sustained drops")

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.GreaterOrEqual(t, len(lines), 1, "expected at least one alarm record")
	for _, line := range lines {
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec),
			"alarm record must be valid JSON: %s", line)
		require.Contains(t, rec, "ts", "alarm record missing ts: %s", line)
		require.Contains(t, rec, "src", "alarm record missing src: %s", line)
		require.Equal(t, "kalshi", rec["src"])
		require.Contains(t, rec, "dropped", "alarm record missing dropped: %s", line)
		require.Contains(t, rec, "queue_depth", "alarm record missing queue_depth: %s", line)
		require.Contains(t, rec, "since_last_drop_ms", "alarm record missing since_last_drop_ms: %s", line)
		// ts must parse as RFC3339Nano so log shippers can normalize.
		tsStr, ok := rec["ts"].(string)
		require.True(t, ok, "ts must be a string")
		_, err := time.Parse(time.RFC3339Nano, tsStr)
		require.NoError(t, err, "ts must be RFC3339Nano: %s", tsStr)
	}
}

// kalshiNewClientForTest builds a kalshi.Client with a nil signer.
// runRediscovery only dereferences the signer inside DiscoverOpenTickers,
// which we never reach in the bounded-shutdown test (ctx cancels first).
func kalshiNewClientForTest() *kalshi.Client {
	return kalshi.NewClient("http://example.invalid", nil)
}

// kalshiNewSubscriberForTest mirrors the above: a subscriber whose
// Run is never called, used only to satisfy runRediscovery's typed
// parameter list.
func kalshiNewSubscriberForTest(logger *slog.Logger) *kalshi.Subscriber {
	return kalshi.NewSubscriber(kalshi.SubscriberOptions{
		WSURL:    "wss://example.invalid",
		Channels: []string{"orderbook_delta"},
		Logger:   logger,
	})
}
