// Command tape is the Phase 0 market-data recorder.
//
// It runs one goroutine per enabled source (Kalshi WS, Open-Meteo REST),
// each writing raw frames into a per-source append-only JSONL journal
// under DataDir. SIGINT/SIGTERM trigger a graceful shutdown that drains
// queues and fsyncs current files.
//
// BTC-hourly (Coinbase) was removed in W1 — see docs/research/SYNTHESIS.md.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/wh0o0pz/isobar/internal/clock"
	"github.com/wh0o0pz/isobar/internal/config"
	"github.com/wh0o0pz/isobar/internal/env"
	"github.com/wh0o0pz/isobar/internal/journal"
	"github.com/wh0o0pz/isobar/internal/kalshi"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/obs"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
)

// Build info; populated via -ldflags by the Makefile.
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to YAML config")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("isobar tape %s (%s) built %s\n", version, commit, buildTime)
		return
	}

	if _, err := env.LoadNearest(""); err != nil {
		// Insecure-mode errors are fatal: refusing to boot with a
		// world/group-readable .env is the whole point of the check.
		// Other read errors (corrupt file, EISDIR) are also fatal here
		// since the operator opted in to a .env by placing one in the
		// walk path. The earlier behaviour of warning-and-continuing
		// silently masked credential-loading failures.
		fmt.Fprintln(os.Stderr, "fatal: load .env:", err)
		os.Exit(1)
	}

	logger := obs.Init(os.Getenv("ISOBAR_LOG_LEVEL"))
	logger.Info("isobar tape starting",
		"version", version, "commit", commit, "buildTime", buildTime, "config", *cfgPath)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	// SHA-256 the raw config bytes so the boot frame can stamp a stable
	// fingerprint of the operator's deployed configuration. We re-read
	// the file rather than threading it through config.Load to keep the
	// config package's API unchanged. Empty path (test runs) → "".
	configSHA := computeConfigSHA(*cfgPath, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, logger, cfg, configSHA); err != nil {
		logger.Error("tape exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("tape exited cleanly")
}

// computeConfigSHA returns the lowercase hex SHA-256 of the bytes at
// path. If path is empty or the read fails, returns "" and logs at
// warn — the boot frame still ships but with an empty fingerprint, so
// downstream cutover detection just becomes "different from previous
// boot frame" (which still works as a segmentation signal).
func computeConfigSHA(path string, logger *slog.Logger) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied config flag, not user input; already read by config.Load
	if err != nil {
		logger.Warn("boot frame: could not re-read config for SHA-256", "path", path, "err", err)
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func run(ctx context.Context, logger *slog.Logger, cfg config.Config, configSHA string) error {
	g, ctx := errgroup.WithContext(ctx)

	// Single per-process alarm sink shared across both source journals.
	// It owns the lazily-opened <DataDir>/alarms/<date>.jsonl file and
	// the JournalDropsTotal counter. Closed via defer here so the file
	// is fsynced + closed even on errgroup-error shutdown, matching
	// the existing defer pattern for journals inside startKalshi /
	// startOpenMeteo.
	alarms := newAlarmSink(cfg.DataDir, logger)
	defer func() {
		if err := alarms.Close(); err != nil {
			logger.Error("tape-alarm: close failed", "err", err)
		}
	}()

	if cfg.Kalshi.Enabled {
		g.Go(func() error { return startKalshi(ctx, logger, cfg, configSHA, alarms) })
	}
	if cfg.OpenMeteo.Enabled {
		g.Go(func() error { return startOpenMeteo(ctx, logger, cfg, configSHA, alarms) })
	}
	return g.Wait()
}

// bootSchemaVersion identifies the on-disk shape of the boot frame's
// payload. Bump when fields are renamed/removed (additions are
// backward-compatible). Downstream tools (edge-scan, analyze) match
// on this string before trusting field names.
//
// v2: stations entries carry only {Code, Location, Lat, Lon, TZ} —
// the unread City/UTCOffsetHours/HighSeries/LowSeries/CLIPil fields
// were deleted from market.Station in the Phase-2 reduction.
const bootSchemaVersion = "isobar-tape.boot/v2"

// bootPayload is the JSON shape carried in the boot frame's Payload
// field. Field order/names are part of the public tape format.
type bootPayload struct {
	ConfigSHA256  string           `json:"config_sha256"`
	BuildCommit   string           `json:"build_commit"`
	BuildVersion  string           `json:"build_version,omitempty"`
	BuildTime     string           `json:"build_time,omitempty"`
	Stations      []market.Station `json:"stations"`
	SchemaVersion string           `json:"schema_version"`
}

// writeFrame stamps and enqueues one journal frame. Every frame tape
// emits (boot marker, WS payload, REST poll) goes through here so the
// Stamp source stays clock.Now uniformly (ADR-0012).
func writeFrame(jrn *journal.Journal, source, kind string, payload []byte) {
	jrn.Write(journal.Frame{
		Stamp:   clock.Now(),
		Source:  source,
		Kind:    kind,
		Payload: json.RawMessage(payload),
	})
}

// writeBootFrame emits a single cutover-marker frame at journal open.
// The frame's src is "isobar-tape" (not the per-source journal name)
// so downstream readers can identify it across both kalshi/ and
// openmeteo/ streams with one predicate. Kind is "boot".
func writeBootFrame(jrn *journal.Journal, configSHA string) {
	payload := bootPayload{
		ConfigSHA256:  configSHA,
		BuildCommit:   commit,
		BuildVersion:  version,
		BuildTime:     buildTime,
		Stations:      market.AllStationsCanonical(),
		SchemaVersion: bootSchemaVersion,
	}
	b, err := json.Marshal(&payload)
	if err != nil {
		// json.Marshal on a flat struct of strings + market.Station
		// (no pointers, no channels) will not fail in practice. Log
		// at warn rather than swallow silently.
		slog.Warn("boot frame: marshal failed; skipping", "err", err)
		return
	}
	writeFrame(jrn, "isobar-tape", "boot", b)
}

func startKalshi(ctx context.Context, logger *slog.Logger, cfg config.Config, configSHA string, alarms *alarmSink) error {
	pemBytes, source, err := kalshi.LoadPEM(cfg.Kalshi.PrivateKey, cfg.Kalshi.PrivateKeyPath)
	if err != nil {
		return err
	}
	signer, err := kalshi.NewSigner(cfg.Kalshi.APIKeyID, pemBytes)
	if err != nil {
		return fmt.Errorf("kalshi: new signer (%s): %w", source, err)
	}
	logger.Info("kalshi: credentials loaded", "key_id", redactKeyID(cfg.Kalshi.APIKeyID), "pem_source", source)

	jrn, err := openJournal(cfg, "kalshi", logger, alarms)
	if err != nil {
		return err
	}
	defer func() { _ = jrn.Close() }()
	writeBootFrame(jrn, configSHA)

	rest := kalshi.NewClient(cfg.Kalshi.APIBase, signer)
	tickers, err := rest.DiscoverOpenTickers(ctx, cfg.Kalshi.Series, logger)
	if err != nil {
		return fmt.Errorf("kalshi: discover tickers: %w", err)
	}
	logger.Info("kalshi: discovered markets", "n", len(tickers), "series", cfg.Kalshi.Series)
	if len(tickers) == 0 {
		return errors.New("kalshi: no open markets matched configured series")
	}

	sub := kalshi.NewSubscriber(kalshi.SubscriberOptions{
		WSURL:    cfg.Kalshi.WSURL,
		Signer:   signer,
		Channels: cfg.Kalshi.Channels,
		Tickers:  tickers,
		Reconnect: kalshi.ReconnectOptions{
			InitialBackoff: cfg.Kalshi.Reconnect.InitialBackoff,
			MaxBackoff:     cfg.Kalshi.Reconnect.MaxBackoff,
			MaxAttempts:    cfg.Kalshi.Reconnect.MaxAttempts,
		},
		Logger:  logger,
		OnFrame: makeWSFrameHandler(jrn, "kalshi"),
		// Self-heal on every reconnect: re-resolve the open-market set so
		// a watchdog-forced reconnect can never resubscribe a fully
		// settled list (2026-05 outage — docs/OPERATIONS.md postmortem).
		// The 4h rediscovery loop below still handles adding next-day
		// markets to connections that stay healthy for hours.
		RefreshTickers: func(ctx context.Context) ([]string, error) {
			return rest.DiscoverOpenTickers(ctx, cfg.Kalshi.Series, logger)
		},
	})

	// Scope the rediscovery loop and the subscriber to a child group whose
	// lifetime ends before the deferred jrn.Close above. runRediscovery may
	// grow to emit journal frames (a "set changed" marker is the obvious next
	// feature), and a Write from a goroutine outliving jrn.Close would race
	// the journal's teardown. Returning gk.Wait() keeps the deferred Close
	// ordered strictly after both children exit; sub.Run takes gctx so a
	// rediscovery error cross-cancels the subscriber instead of deadlocking
	// the Wait on shutdown.
	gk, gctx := errgroup.WithContext(ctx)
	if cfg.Kalshi.RediscoverInterval > 0 {
		gk.Go(func() error {
			return runRediscovery(gctx, logger, rest, sub, cfg.Kalshi.Series, cfg.Kalshi.RediscoverInterval)
		})
	}
	gk.Go(func() error { return sub.Run(gctx) })
	return gk.Wait()
}

// runRediscovery periodically re-runs DiscoverOpenTickers and reloads the
// subscriber if the open-ticker set changed. This prevents the recorder
// from going dead after configured series' brackets settle (the next-day
// markets exist in Kalshi's catalog but were not in the initial
// subscription).
//
// Lifecycle: returns nil on ctx cancellation (graceful shutdown). A
// panic inside the loop is recovered and converted into an error so
// the parent errgroup kills the whole process rather than leaving an
// orphaned goroutine with the tape running blind.
func runRediscovery(ctx context.Context, logger *slog.Logger, rest *kalshi.Client, sub *kalshi.Subscriber, series []string, interval time.Duration) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			// Use the default logger as a fallback: if the supplied
			// logger is what panicked (e.g. nil deref), calling it
			// again here would re-panic past the recover boundary.
			lg := logger
			if lg == nil {
				lg = slog.Default()
			}
			lg.Error("kalshi: rediscovery panicked — failing the errgroup", "recover", r)
			retErr = fmt.Errorf("kalshi: rediscovery panic: %v", r)
		}
	}()

	t := time.NewTicker(interval)
	defer t.Stop()
	logger.Info("kalshi: rediscovery started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			fresh, err := rest.DiscoverOpenTickers(ctx, series, logger)
			if err != nil {
				logger.Warn("kalshi: rediscovery failed", "err", err)
				continue
			}
			current := sub.Tickers()
			if sameTickerSet(current, fresh) {
				logger.Debug("kalshi: rediscovery — no change", "n", len(current))
				continue
			}
			added := diffTickers(fresh, current)
			removed := diffTickers(current, fresh)
			logger.Info("kalshi: rediscovery — set changed",
				"before", len(current), "after", len(fresh),
				"added", len(added), "removed", len(removed),
				"added_sample", sample(added, 3))
			sub.SetTickers(fresh)
			sub.Reload()
		}
	}
}

// sameTickerSet returns true iff a and b contain the same elements (order
// irrelevant). O(n+m).
func sameTickerSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, x := range b {
		if _, ok := set[x]; !ok {
			return false
		}
	}
	return true
}

// diffTickers returns elements of a not in b, sorted.
func diffTickers(a, b []string) []string {
	bset := make(map[string]struct{}, len(b))
	for _, x := range b {
		bset[x] = struct{}{}
	}
	var out []string
	for _, x := range a {
		if _, ok := bset[x]; !ok {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func sample(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func startOpenMeteo(ctx context.Context, logger *slog.Logger, cfg config.Config, configSHA string, alarms *alarmSink) error {
	jrn, err := openJournal(cfg, "openmeteo", logger, alarms)
	if err != nil {
		return err
	}
	defer func() { _ = jrn.Close() }()
	writeBootFrame(jrn, configSHA)

	locs := make([]openmeteo.Location, 0, len(cfg.OpenMeteo.Locations))
	for _, l := range cfg.OpenMeteo.Locations {
		locs = append(locs, openmeteo.Location{Name: l.Name, Lat: l.Lat, Lon: l.Lon})
	}

	poller, err := openmeteo.NewPoller(openmeteo.Options{
		BaseURL:      cfg.OpenMeteo.BaseURL,
		Model:        cfg.OpenMeteo.Model,
		Variables:    cfg.OpenMeteo.Variables,
		Locations:    locs,
		ForecastDays: cfg.OpenMeteo.ForecastDays,
		PollInterval: cfg.OpenMeteo.PollInterval,
		Logger:       logger,
		OnFetch: func(loc openmeteo.Location, raw []byte) {
			writeFrame(jrn, "openmeteo", "rest:"+loc.Name, raw)
		},
	})
	if err != nil {
		return err
	}
	return poller.Run(ctx)
}

// alarmSustainForTest lets the cmd/tape regression test exercise the
// alarm-crossing path without paying journal.Options' 60-second
// default sustain window. Production runs leave this at zero, which
// causes journal.Open to apply its default. Set ONLY from tests.
var alarmSustainForTest time.Duration

// alarmThresholdForTest mirrors alarmSustainForTest for the per-second
// drops/sec threshold. Production leaves it zero (journal applies the
// default of 1 drop/sec); tests can lower or raise it as needed.
var alarmThresholdForTest uint64

// openJournal creates a per-source journal under cfg.DataDir/<source>
// and wires its OnDropAlarm hook to the process-wide alarms sink.
//
// The hook closes over a *journal.Journal so it can read live
// QueueDepth/QueueCapacity at fire time (the structured log and the
// alarms file both carry the saturation context). Note that the
// returned hook is bound BEFORE the *journal.Journal is allocated;
// the closure captures the local variable, which is then assigned
// from Open's return. Open invokes the hook only from its writer
// goroutine's alarm tick, which cannot fire before AlarmSustain (60s
// default) has elapsed — there's no race in practice.
//
// ADR-0011 §A1: journal.Open returns ErrAlarmHookRequired if the
// hook is nil, so this function is the only place a hook can be
// silently omitted from the production code path. Tests
// (cmd/tape/main_test.go) include a tripwire that asserts every
// journal constructed here has OnDropAlarm bound.
func openJournal(cfg config.Config, source string, logger *slog.Logger, alarms *alarmSink) (*journal.Journal, error) {
	var jrn *journal.Journal
	hook := alarms.hookFor(
		source,
		func() int {
			if jrn == nil {
				return 0
			}
			return jrn.QueueDepth()
		},
		func() int {
			if jrn == nil {
				return 0
			}
			return jrn.QueueCapacity()
		},
	)
	j, err := journal.Open(journal.Options{
		Dir:              filepath.Join(cfg.DataDir, source),
		Source:           source,
		MaxFileBytes:     cfg.Journal.MaxFileBytes,
		MaxFileAge:       cfg.Journal.MaxFileAge,
		FlushEvery:       cfg.Journal.FlushEvery,
		QueueSize:        cfg.Journal.QueueSize,
		CompressOnRotate: cfg.Journal.CompressOnRotate,
		OnDropAlarm:      hook,
		AlarmSustain:     alarmSustainForTest,   // 0 in prod → journal default
		AlarmThreshold:   alarmThresholdForTest, // 0 in prod → journal default
	}, logger)
	if err != nil {
		return nil, err
	}
	jrn = j
	return j, nil
}

// makeWSFrameHandler returns an OnFrame callback that wraps a raw payload
// in a journal Frame with the current timestamp pair and the given source.
func makeWSFrameHandler(jrn *journal.Journal, source string) func([]byte) {
	return func(raw []byte) {
		writeFrame(jrn, source, "ws", raw)
	}
}

// redactKeyID shows only the first 8 chars of a UUID-ish ID, for safe logging.
func redactKeyID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}
