// Command settle reconciles paper-trader bets against Kalshi market
// settlements and writes Outcome records back to the ledger.
//
// It is a one-shot command: scan the ledger for unresolved bets whose
// settlement instant is in the past, GET each ticker from Kalshi REST,
// derive a Resolution + paper P&L from the market's settled state, and
// append an Outcome JSONL line under
// data/papertrade/<bracket-date>/outcomes-<unix>.jsonl.
//
// Idempotency: bets already represented in any outcomes-*.jsonl[.gz]
// under the same date subdir are skipped, so re-running settle the
// same day is a no-op once the day's bets resolve.
//
// Run nightly via cron / systemd timer, e.g. shortly after 09:00 UTC
// to catch all daily-bracket settlements (NY settles ~04:00 UTC, LAX
// ~07:00 UTC).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wh0o0pz/isobar/internal/clock"
	"github.com/wh0o0pz/isobar/internal/config"
	"github.com/wh0o0pz/isobar/internal/env"
	"github.com/wh0o0pz/isobar/internal/kalshi"
	"github.com/wh0o0pz/isobar/internal/obs"
	"github.com/wh0o0pz/isobar/internal/papertrade"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to YAML config (kalshi creds + data_dir)")
	lookback := flag.Duration("lookback", 7*24*time.Hour, "scan ledger files this far back from now")
	graceMin := flag.Duration("settlement-grace", 30*time.Minute, "wait this long after a bet's scheduled settlement before querying Kalshi")
	dryRun := flag.Bool("dry-run", false, "log what would be written without appending to outcome files")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Printf("isobar settle %s (%s) built %s\n", version, commit, buildTime)
		return
	}

	if _, err := env.LoadNearest(""); err != nil {
		fmt.Fprintln(os.Stderr, "warn: load .env:", err)
	}

	logger := obs.Init(os.Getenv("ISOBAR_LOG_LEVEL"))
	logger.Info("isobar settle starting",
		"version", version, "commit", commit, "buildTime", buildTime,
		"config", *cfgPath, "lookback", *lookback,
		"settlement_grace", *graceMin, "dry_run", *dryRun)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	pemBytes, source, err := loadKalshiPEM(cfg.Kalshi)
	if err != nil {
		logger.Error("kalshi creds", "err", err)
		os.Exit(1)
	}
	signer, err := kalshi.NewSigner(cfg.Kalshi.APIKeyID, pemBytes)
	if err != nil {
		logger.Error("kalshi signer", "src", source, "err", err)
		os.Exit(1)
	}
	logger.Info("kalshi creds loaded", "key_id", redactKeyID(cfg.Kalshi.APIKeyID), "src", source)

	rest := kalshi.NewClient(cfg.Kalshi.APIBase, signer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stats, err := run(ctx, logger, rest, cfg.DataDir, *lookback, *graceMin, *dryRun)
	if err != nil {
		logger.Error("settle exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("settle complete",
		"bets_scanned", stats.scanned,
		"already_resolved", stats.alreadyResolved,
		"too_early", stats.tooEarly,
		"kalshi_pending", stats.kalshiPending,
		"resolved_yes", stats.resolvedYes,
		"resolved_no", stats.resolvedNo,
		"resolved_void", stats.resolvedVoid,
		"errors", stats.errors,
		"net_pnl_cents", stats.netPnLCents,
	)
}

type runStats struct {
	scanned         int
	alreadyResolved int
	tooEarly        int
	kalshiPending   int
	resolvedYes     int
	resolvedNo      int
	resolvedVoid    int
	errors          int
	netPnLCents     int
}

// run is the settle pipeline: build the already-resolved set, walk the
// ledger, query Kalshi for each unresolved+matured bet, append Outcome
// rows. Returns aggregated stats for top-level logging.
func run(ctx context.Context, logger *slog.Logger, rest *kalshi.Client, dataDir string, lookback, grace time.Duration, dryRun bool) (runStats, error) {
	now := time.Now()
	since := now.Add(-lookback)
	ledgerDir := filepath.Join(dataDir, "papertrade")

	// 1) Build the set of bet IDs we've already resolved so a re-run
	//    is idempotent.
	resolved := map[string]struct{}{}
	if err := papertrade.IterOutcomes(ledgerDir, since, func(o papertrade.Outcome) error {
		resolved[o.BetID] = struct{}{}
		return nil
	}); err != nil {
		return runStats{}, fmt.Errorf("scan outcomes: %w", err)
	}
	logger.Info("idempotency state", "already_resolved_ids", len(resolved))

	// 2) Walk every Bet in the lookback window and decide what to do.
	stats := runStats{}
	if err := papertrade.IterBets(ledgerDir, since, func(b papertrade.Bet) error {
		stats.scanned++
		if _, ok := resolved[b.ID]; ok {
			stats.alreadyResolved++
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		// Skip bets whose settlement is still in the future (with a
		// grace window so we don't query Kalshi while a market is
		// still in the closing-auction state).
		settleAt := time.Unix(0, b.SettlementUnixNano)
		if settleAt.Add(grace).After(now) {
			stats.tooEarly++
			return nil
		}

		market, err := rest.GetMarket(ctx, b.Ticker)
		if err != nil {
			logger.Warn("get market failed", "ticker", b.Ticker, "err", err)
			stats.errors++
			return nil // continue walking other bets
		}

		res := papertrade.ClassifyResolution(market.Status, market.Result)
		if res == papertrade.ResolutionUnsettled {
			logger.Debug("market still pending", "ticker", b.Ticker, "status", market.Status, "result", market.Result)
			stats.kalshiPending++
			return nil
		}

		won, pnl := papertrade.PnLFromBet(b, res)
		stats.netPnLCents += pnl
		switch res {
		case papertrade.ResolutionYes:
			stats.resolvedYes++
		case papertrade.ResolutionNo:
			stats.resolvedNo++
		case papertrade.ResolutionVoid:
			stats.resolvedVoid++
		}

		out := papertrade.Outcome{
			SchemaVersion: papertrade.OutcomeSchemaVersion,
			BetID:         b.ID,
			SettledAt:     clock.Now().Wall,
			Ticker:        b.Ticker,
			KalshiStatus:  market.Status,
			KalshiResult:  market.Result,
			Resolution:    res,
			BetSide:       b.Side,
			BetMidCents:   b.MidCents,
			BetEdgeCents:  b.EdgeCents,
			BetFeeCents:   b.FeeRoundTripCents,
			Won:           won,
			PnLCents:      pnl,
		}

		if dryRun {
			logger.Info("would record outcome",
				"bet_id", out.BetID, "ticker", out.Ticker,
				"resolution", out.Resolution, "won", out.Won, "pnl_cents", out.PnLCents)
			return nil
		}

		path := outcomePath(ledgerDir, b.BracketDate, now)
		if _, err := papertrade.AppendOutcome(path, out); err != nil {
			logger.Warn("append outcome failed", "bet_id", out.BetID, "err", err)
			stats.errors++
			return nil
		}
		logger.Info("recorded outcome",
			"bet_id", out.BetID, "ticker", out.Ticker,
			"resolution", out.Resolution, "won", out.Won, "pnl_cents", out.PnLCents)
		return nil
	}); err != nil {
		return stats, fmt.Errorf("scan bets: %w", err)
	}
	return stats, nil
}

// outcomePath builds the per-run outcome file path under the bracket-
// date subdir (matching the bet ledger's layout). One file per settle
// run keeps re-runs from overwriting prior output, and the unix
// timestamp suffix makes file ordering chronological.
func outcomePath(ledgerDir, bracketDate string, now time.Time) string {
	if bracketDate == "" {
		bracketDate = now.UTC().Format("2006-01-02")
	}
	return filepath.Join(ledgerDir, bracketDate,
		fmt.Sprintf("outcomes-%d.jsonl", now.UnixNano()))
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
