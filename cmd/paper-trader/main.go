// Command paper-trader is the live "would-have-bet" engine.
//
// It is the online counterpart to cmd/edge-scan: same edge kernel, same
// climate-shrunk-naive default model, but driven by the live Kalshi WS
// orderbook stream + Open-Meteo ensemble polls instead of recorded
// journals. When a triggered (ticker, side) clears the threshold, the
// decision is appended to a JSONL ledger under data/papertrade/. No
// real orders are placed; cmd/settle (forthcoming) reconciles ledger
// outcomes against Kalshi settlement events and NOAA CLI obs.
//
// Architecture mirrors cmd/tape: one goroutine per data source, all
// coordinated through an errgroup. Shared state — per-ticker books,
// per-location polls — is guarded by a single RWMutex; the work each
// callback does is O(1) per event so contention isn't a concern at
// realistic event rates (a few hundred messages/second).
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
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/clock"
	"github.com/wh0o0pz/isobar/internal/config"
	"github.com/wh0o0pz/isobar/internal/env"
	"github.com/wh0o0pz/isobar/internal/kalshi"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/obs"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/papertrade"
	"github.com/wh0o0pz/isobar/internal/replay"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to YAML config (kalshi + openmeteo sections only; trade params are flags)")
	climDir := flag.String("climate", "", "obs CSVs dir for climate-shrunk baseline (required for climate_shrunk model; falls back to naive when empty)")
	minEdge := flag.Float64("min-edge", 3.0, "minimum fee-aware edge cents to trigger a bet")
	maxSpread := flag.Int("max-spread", 5, "maximum book spread cents to trade through")
	maxPollAge := flag.Duration("max-poll-age", 4*time.Hour, "ignore signals when latest poll is older than this")
	minLeadHours := flag.Float64("min-lead-hours", 2.0, "minimum hours from decision to settlement")
	modelFlag := flag.String("model", "climate_shrunk", "probability path: naive | climate_shrunk | mos_calibrated")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("isobar paper-trader %s (%s) built %s\n", version, commit, buildTime)
		return
	}

	if _, err := env.LoadNearest(""); err != nil {
		fmt.Fprintln(os.Stderr, "warn: load .env:", err)
	}

	logger := obs.Init(os.Getenv("ISOBAR_LOG_LEVEL"))
	logger.Info("isobar paper-trader starting",
		"version", version, "commit", commit, "buildTime", buildTime,
		"config", *cfgPath, "min_edge", *minEdge, "max_spread", *maxSpread,
		"model", *modelFlag)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	model, err := parseModel(*modelFlag)
	if err != nil {
		logger.Error("model flag", "err", err)
		os.Exit(2)
	}

	var clim *climate.Lookup
	if *climDir != "" {
		clim = climate.New()
		if err := clim.Load(*climDir, market.AllStationCodes()); err != nil {
			logger.Error("load climate", "dir", *climDir, "err", err)
			os.Exit(1)
		}
		logger.Info("climate lookup loaded", "dir", *climDir)
	} else if model == papertrade.ModelClimateShrunk {
		logger.Warn("climate_shrunk model selected but no --climate provided; will fall back to naive per-bracket")
	}

	led, err := papertrade.OpenLedger(papertrade.LedgerOptions{
		Dir:              filepath.Join(cfg.DataDir, "papertrade"),
		MaxFileBytes:     cfg.Journal.MaxFileBytes,
		MaxFileAge:       cfg.Journal.MaxFileAge,
		FlushEvery:       cfg.Journal.FlushEvery,
		QueueSize:        cfg.Journal.QueueSize,
		CompressOnRotate: cfg.Journal.CompressOnRotate,
	}, logger)
	if err != nil {
		logger.Error("open ledger", "err", err)
		os.Exit(1)
	}
	defer func() { _ = led.Close() }()

	tr := newTrader(logger, led, clim, papertrade.Trigger{
		MinEdgeCents:   *minEdge,
		MaxSpreadCents: *maxSpread,
		MaxPollAge:     *maxPollAge,
		MinLeadHours:   *minLeadHours,
		Model:          model,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := tr.run(ctx, cfg); err != nil {
		logger.Error("paper-trader exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("paper-trader exited cleanly", "bets_recorded", led.Count())
}

// trader owns the live state. Books and polls are mutated from two
// goroutines (kalshi-ws and openmeteo); every read or write goes
// through tr.mu. Decisions never block I/O — they just hold the lock
// long enough to snapshot the state, then evaluate outside the lock.
type trader struct {
	logger *slog.Logger
	ledger *papertrade.Ledger
	clim   *climate.Lookup
	trig   papertrade.Trigger

	mu        sync.RWMutex
	books     map[string]*replay.Book
	walls     map[string]int64
	polls     map[string]*openmeteo.EnsemblePoll // by location ("NY" etc.)
	pollWalls map[string]int64

	// lastMid + lastSpread suppress redundant evaluations. A ticker is
	// only re-considered when its mid or spread changes — the typical
	// WS frame is a level-update at 95¢ that doesn't move the top of
	// book at all, and would otherwise burn CPU on identical decisions.
	lastMu     sync.Mutex
	lastMid    map[string]int
	lastSpread map[string]int

	// seriesByLoc maps a location tag ("NY") to the configured Kalshi
	// series prefixes that resolve to it. Used to find which tickers
	// to re-evaluate on a fresh poll for a given location.
	seriesByLoc map[string][]string
}

func newTrader(logger *slog.Logger, led *papertrade.Ledger, clim *climate.Lookup, trig papertrade.Trigger) *trader {
	return &trader{
		logger:      logger.With("component", "paper-trader"),
		ledger:      led,
		clim:        clim,
		trig:        trig,
		books:       map[string]*replay.Book{},
		walls:       map[string]int64{},
		polls:       map[string]*openmeteo.EnsemblePoll{},
		pollWalls:   map[string]int64{},
		lastMid:     map[string]int{},
		lastSpread:  map[string]int{},
		seriesByLoc: map[string][]string{},
	}
}

func (tr *trader) run(ctx context.Context, cfg config.Config) error {
	g, ctx := errgroup.WithContext(ctx)

	if cfg.Kalshi.Enabled {
		g.Go(func() error { return tr.runKalshi(ctx, cfg) })
	}
	if cfg.OpenMeteo.Enabled {
		// Build the location → series map upfront so onPoll() can find
		// every ticker subscribed at that location without re-deriving
		// the mapping per event.
		for _, loc := range cfg.OpenMeteo.Locations {
			tr.seriesByLoc[loc.Name] = append(tr.seriesByLoc[loc.Name], loc.Series...)
		}
		g.Go(func() error { return tr.runOpenMeteo(ctx, cfg) })
	}
	if !cfg.Kalshi.Enabled && !cfg.OpenMeteo.Enabled {
		return errors.New("paper-trader: both kalshi and openmeteo are disabled in config; nothing to do")
	}
	return g.Wait()
}

// runKalshi subscribes to the Kalshi WS feed and applies frames into
// the in-memory books map. Whenever a frame moves the mid or spread
// for a ticker, the trader evaluates that ticker's trigger.
func (tr *trader) runKalshi(ctx context.Context, cfg config.Config) error {
	pemBytes, source, err := loadKalshiPEM(cfg.Kalshi)
	if err != nil {
		return err
	}
	signer, err := kalshi.NewSigner(cfg.Kalshi.APIKeyID, pemBytes)
	if err != nil {
		return fmt.Errorf("kalshi: new signer (%s): %w", source, err)
	}
	tr.logger.Info("kalshi: credentials loaded", "key_id", redactKeyID(cfg.Kalshi.APIKeyID), "pem_source", source)

	rest := kalshi.NewClient(cfg.Kalshi.APIBase, signer)
	tickers, err := rest.DiscoverOpenTickers(ctx, cfg.Kalshi.Series, tr.logger)
	if err != nil {
		return fmt.Errorf("kalshi: discover tickers: %w", err)
	}
	tr.logger.Info("kalshi: discovered markets", "n", len(tickers), "series", cfg.Kalshi.Series)
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
		Logger:  tr.logger,
		OnFrame: tr.onWSFrame,
	})

	if cfg.Kalshi.RediscoverInterval > 0 {
		go runRediscovery(ctx, tr.logger, rest, sub, cfg.Kalshi.Series, cfg.Kalshi.RediscoverInterval)
	}
	return sub.Run(ctx)
}

// onWSFrame is the per-frame Kalshi WS callback. It applies the frame
// into the books map and re-evaluates the affected ticker.
func (tr *trader) onWSFrame(raw []byte) {
	var f replay.KalshiFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return
	}
	ticker := f.MarketTicker()
	if ticker == "" {
		return
	}

	now := clock.Now().Wall

	tr.mu.Lock()
	env := replay.Envelope{Wall: now}
	replay.ApplyFrameToBooks(tr.books, tr.walls, env, f)
	book := tr.books[ticker]
	tr.mu.Unlock()

	if book == nil {
		return
	}
	tr.evaluate(ticker, book, now)
}

// runOpenMeteo polls Open-Meteo on a fixed interval. Each poll updates
// the latest-poll cache for its location and re-evaluates every
// subscribed ticker at that location.
func (tr *trader) runOpenMeteo(ctx context.Context, cfg config.Config) error {
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
		Logger:       tr.logger,
		OnFetch:      tr.onPoll,
	})
	if err != nil {
		return err
	}
	return poller.Run(ctx)
}

// onPoll is the per-fetch Open-Meteo callback. It parses the response,
// caches the latest poll for the location, and re-evaluates all known
// tickers at that location — a fresh poll changes the model
// probability, which changes the edge, which may newly satisfy the
// trigger threshold.
func (tr *trader) onPoll(loc openmeteo.Location, raw []byte) {
	poll, err := openmeteo.ParseEnsemblePoll(raw)
	if err != nil {
		tr.logger.Warn("parse poll failed", "loc", loc.Name, "err", err)
		return
	}
	now := clock.Now().Wall
	poll.Wall = now
	poll.Location = loc.Name

	tr.mu.Lock()
	tr.polls[loc.Name] = poll
	tr.pollWalls[loc.Name] = now
	// Snapshot the tickers to evaluate without holding the lock during
	// the per-ticker decide loop.
	tickers := make([]string, 0, len(tr.books))
	for ticker := range tr.books {
		// Filter to tickers whose location resolves to this poll's
		// location. ParseTicker is fast enough at this cardinality
		// (a few hundred markets max) that we don't pre-index.
		tk, parseErr := market.ParseTicker(ticker)
		if parseErr != nil {
			continue
		}
		if tk.Location() != loc.Name {
			continue
		}
		tickers = append(tickers, ticker)
	}
	tr.mu.Unlock()

	tr.logger.Debug("poll received", "loc", loc.Name, "tickers_to_evaluate", len(tickers))
	for _, ticker := range tickers {
		tr.mu.RLock()
		book := tr.books[ticker]
		tr.mu.RUnlock()
		if book == nil {
			continue
		}
		tr.evaluate(ticker, book, now)
	}
}

// evaluate is the trigger-evaluation kernel. Skips redundant work via
// last-mid/last-spread tracking, then calls papertrade.Decide and, on
// a positive trigger, records the bet.
func (tr *trader) evaluate(ticker string, book *replay.Book, now int64) {
	mid := book.MidYesCents()
	spread := book.SpreadYesCents()

	tr.lastMu.Lock()
	prevMid, prevMidOK := tr.lastMid[ticker]
	prevSpread, prevSpreadOK := tr.lastSpread[ticker]
	unchanged := prevMidOK && prevSpreadOK && prevMid == mid && prevSpread == spread
	tr.lastMid[ticker] = mid
	tr.lastSpread[ticker] = spread
	tr.lastMu.Unlock()
	if unchanged {
		return
	}

	tk, err := market.ParseTicker(ticker)
	if err != nil {
		return
	}
	loc := tk.Location()
	if loc == "" {
		return
	}

	tr.mu.RLock()
	poll := tr.polls[loc]
	pollWall := tr.pollWalls[loc]
	tr.mu.RUnlock()

	bet, _, ok := papertrade.Decide(papertrade.Snapshot{
		Ticker:    ticker,
		Book:      book,
		Poll:      poll,
		PollWall:  pollWall,
		DecidedAt: now,
	}, tr.trig, tr.clim)
	if !ok {
		return
	}

	wrote, err := tr.ledger.Record(bet)
	if err != nil {
		tr.logger.Warn("ledger record failed", "ticker", ticker, "err", err)
		return
	}
	if !wrote {
		return // dedup hit, expected on subsequent same-key triggers
	}
	tr.logger.Info("paper bet recorded",
		"ticker", bet.Ticker,
		"side", bet.Side,
		"model", bet.Model,
		"model_p", roundN(bet.ModelP, 4),
		"mid_cents", bet.MidCents,
		"edge_cents", roundN(bet.EdgeCents, 2),
		"lead_hours", roundN(bet.LeadHours, 1),
		"settlement_utc", time.Unix(0, bet.SettlementUnixNano).UTC().Format(time.RFC3339),
	)
}

func parseModel(s string) (papertrade.Model, error) {
	switch s {
	case string(papertrade.ModelNaive):
		return papertrade.ModelNaive, nil
	case string(papertrade.ModelClimateShrunk):
		return papertrade.ModelClimateShrunk, nil
	case string(papertrade.ModelMOSCalibrated):
		return papertrade.ModelMOSCalibrated, nil
	default:
		return "", fmt.Errorf("unknown model %q (want naive | climate_shrunk | mos_calibrated)", s)
	}
}

func roundN(v float64, digits int) float64 {
	mul := 1.0
	for i := 0; i < digits; i++ {
		mul *= 10
	}
	return float64(int64(v*mul+0.5*signBias(v))) / mul
}

func signBias(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// --- helpers shared with cmd/tape (duplicated rather than imported to keep
// cmd/tape's surface stable; tiny enough that DRY would cost more than it
// saves) ----------------------------------------------------------------

func runRediscovery(ctx context.Context, logger *slog.Logger, rest *kalshi.Client, sub *kalshi.Subscriber, series []string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	logger.Info("kalshi: rediscovery started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fresh, err := rest.DiscoverOpenTickers(ctx, series, logger)
			if err != nil {
				logger.Warn("kalshi: rediscovery failed", "err", err)
				continue
			}
			current := sub.Tickers()
			if sameTickerSet(current, fresh) {
				continue
			}
			logger.Info("kalshi: rediscovery — set changed",
				"before", len(current), "after", len(fresh))
			sub.SetTickers(fresh)
			sub.Reload()
		}
	}
}

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

func redactKeyID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}

