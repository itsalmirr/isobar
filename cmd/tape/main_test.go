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
