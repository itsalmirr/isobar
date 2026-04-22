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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/wh0o0pz/isobar/internal/clock"
	"github.com/wh0o0pz/isobar/internal/config"
	"github.com/wh0o0pz/isobar/internal/env"
	"github.com/wh0o0pz/isobar/internal/journal"
	"github.com/wh0o0pz/isobar/internal/kalshi"
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
		fmt.Fprintln(os.Stderr, "warn: load .env:", err)
	}

	logger := obs.Init(os.Getenv("ISOBAR_LOG_LEVEL"))
	logger.Info("isobar tape starting",
		"version", version, "commit", commit, "buildTime", buildTime, "config", *cfgPath)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, logger, cfg); err != nil {
		logger.Error("tape exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("tape exited cleanly")
}

func run(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	g, ctx := errgroup.WithContext(ctx)

	if cfg.Kalshi.Enabled {
		g.Go(func() error { return startKalshi(ctx, logger, cfg) })
	}
	if cfg.OpenMeteo.Enabled {
		g.Go(func() error { return startOpenMeteo(ctx, logger, cfg) })
	}
	return g.Wait()
}

func startKalshi(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	pemBytes, source, err := loadKalshiPEM(cfg.Kalshi)
	if err != nil {
		return err
	}
	signer, err := kalshi.NewSigner(cfg.Kalshi.APIKeyID, pemBytes)
	if err != nil {
		return fmt.Errorf("kalshi: new signer (%s): %w", source, err)
	}
	logger.Info("kalshi: credentials loaded", "key_id", redactKeyID(cfg.Kalshi.APIKeyID), "pem_source", source)

	jrn, err := openJournal(cfg, "kalshi", logger)
	if err != nil {
		return err
	}
	defer func() { _ = jrn.Close() }()

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
	})
	return sub.Run(ctx)
}

func startOpenMeteo(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	jrn, err := openJournal(cfg, "openmeteo", logger)
	if err != nil {
		return err
	}
	defer func() { _ = jrn.Close() }()

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
			jrn.Write(journal.Frame{
				Stamp:   clock.Now(),
				Source:  "openmeteo",
				Kind:    "rest:" + loc.Name,
				Payload: json.RawMessage(raw),
			})
		},
	})
	if err != nil {
		return err
	}
	return poller.Run(ctx)
}

// openJournal creates a per-source journal under cfg.DataDir/<source>.
func openJournal(cfg config.Config, source string, logger *slog.Logger) (*journal.Journal, error) {
	return journal.Open(journal.Options{
		Dir:              filepath.Join(cfg.DataDir, source),
		Source:           source,
		MaxFileBytes:     cfg.Journal.MaxFileBytes,
		MaxFileAge:       cfg.Journal.MaxFileAge,
		FlushEvery:       cfg.Journal.FlushEvery,
		QueueSize:        cfg.Journal.QueueSize,
		CompressOnRotate: cfg.Journal.CompressOnRotate,
	}, logger)
}

// makeWSFrameHandler returns an OnFrame callback that wraps a raw payload
// in a journal Frame with the current timestamp pair and the given source.
func makeWSFrameHandler(jrn *journal.Journal, source string) func([]byte) {
	return func(raw []byte) {
		jrn.Write(journal.Frame{
			Stamp:   clock.Now(),
			Source:  source,
			Kind:    "ws",
			Payload: json.RawMessage(raw),
		})
	}
}

// loadKalshiPEM returns the PEM bytes for the Kalshi signer, preferring
// the inline value if present. Returns ("", "", err) only if neither is set
// or the file read fails. The second return identifies the source for logs.
func loadKalshiPEM(k config.Kalshi) ([]byte, string, error) {
	if k.PrivateKey != "" {
		return []byte(k.PrivateKey), "inline (KALSHI_PRIVATE_KEY)", nil
	}
	if k.PrivateKeyPath == "" {
		return nil, "", errors.New("kalshi: no PEM source configured")
	}
	b, err := os.ReadFile(k.PrivateKeyPath)
	if err != nil {
		return nil, "", fmt.Errorf("kalshi: read private key %s: %w", k.PrivateKeyPath, err)
	}
	return b, "file (" + k.PrivateKeyPath + ")", nil
}

// redactKeyID shows only the first 8 chars of a UUID-ish ID, for safe logging.
func redactKeyID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}
