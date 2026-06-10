package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/bayesian"
	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/replay"
)

// ScanOptions configures Scan.
type ScanOptions struct {
	// DataDir is the root containing kalshi/ and openmeteo/ subtrees.
	DataDir string
	// StartDate is inclusive, EndDate exclusive (UTC midnights, the
	// dates as found under DataDir/<source>/<YYYY-MM-DD>/).
	StartDate, EndDate time.Time
	// MaxPollAge is the longest staleness allowed between a trade and
	// its paired Open-Meteo poll. 30 min is the natural choice (poll
	// cadence); 1h is the upper bound at which an ensemble member's
	// daily aggregate is still essentially the same number.
	MaxPollAge time.Duration

	// Truth, if non-nil, enables realized-PnL backtest mode. Trades whose
	// resolution date is missing from Truth are skipped (counted as
	// "no_truth_available") rather than scored against forecast-only
	// p_fair — this is how we avoid "imaginary bets that close idk when".
	Truth *TruthIndex

	// EdgeThresholdCents is the minimum naive edge (cents, post-fee)
	// required for our hypothetical strategy to take a trade. Default
	// is 5 — matches Phase 0.5 fee-aware floor in STRATEGY.md.
	EdgeThresholdCents int

	// MOS, if non-nil, switches p_fair from the naive ensemble-member-count
	// estimator to a calibrated MOS estimator: ensemble mean feeds the
	// fitted (a + b·f, σ) per (station, season, variable) bin, and p_fair
	// is integrated under that normal across the bracket Resolution.
	// Trades that hit a missing bin fall back to naive p_fair so the
	// backtest never silently disappears them.
	MOS *mos.Coefficients

	// Climate, if non-nil, enables the climate-shrunk-naive baseline:
	// p_climate_shrunk = w*climate + (1-w)*p_naive. This is reported
	// alongside p_fair, not used as the trading model.
	Climate *climate.Lookup

	// BayesianKernel, if non-nil together with HourlyObs, switches
	// p_fair from the MOS prior to a precision-weighted Gaussian
	// posterior that folds in the running extremum at trade time
	// (per ADR-0010). The update applies only on trades where MOS
	// itself applied (we need a Gaussian prior to update). When
	// either field is nil, the Bayesian path is silently skipped.
	BayesianKernel *bayesian.Kernel

	// HourlyObs is the station-keyed index of historical hourly ASOS
	// observations. Built once at scan startup via
	// bayesian.LoadHourlyIndex(dir). Required for BayesianKernel to
	// produce an Observation.
	HourlyObs *bayesian.HourlyIndex

	// EntryFilter, if non-empty, names a built-in entry predicate that
	// further restricts hypothetical-trade entry beyond the edge-cents
	// threshold. Currently supported:
	//
	//   "h4" — exclude KXHIGHNY; require either (a) bracket series is
	//          KXLOWT* (low-temp bracket) or (b) trade price ≤ 9¢ or
	//          ≥ 91¢ (extreme-OTM band). Derived from the v3 backtest
	//          subset-mining + leave-one-day-out CV (Sharpe 0.68,
	//          3/5 positive days). See reports/phase0.5/2026-05-19-v4.
	//
	// Unset (or unrecognised string) = no filter beyond the edge threshold.
	// An unrecognised filter name logs a one-shot warning to slog at
	// scan start so a typo doesn't silently disable filtering.
	EntryFilter string

	// FeeMode selects which Kalshi fee schedule the scanner uses when
	// computing per-trade fees and edges. Default (zero value) is
	// FeeTaker, which matches the legacy taker-only backtest behaviour.
	// FeeMaker is the maker-rebate-equivalent schedule (1/4 of taker),
	// useful as an upper-bound diagnostic on "what if we executed
	// maker-only?". The backtest does not currently simulate maker fill
	// probability; FeeMaker treats every taker print in the tape as if
	// our resting order had been filled at that price. See
	// docs/BACKTEST.md "Pitfalls" for the model limits.
	FeeMode FeeMode
}

// EdgeRow is one trade-event record in the output CSV.
type EdgeRow struct {
	TSMs            int64
	Ticker          string
	Series          string
	ValidDate       time.Time
	BracketKind     string
	BracketKey      float64
	Side            string
	TradePriceCents int
	PFairNaive      float64
	PFairMOS        float64 // 0 if no MOS coeffs, bin missing, or model-set mismatch
	PFair           float64 // the p_fair actually used for edge math (MOS if applied, naive otherwise)
	MOSApplied      bool

	// SkippedReason is a per-row tag explaining why MOS was NOT applied
	// to this trade. Possible values:
	//   ""                              MOS was applied (MOSApplied=true)
	//   "no_coeffs"                     ScanOptions.MOS was nil
	//   "no_bin"                        no fitted bin for (station, season, variable)
	//   "model_set_mismatch:<obs>vs<fit>"  poll's models ≠ coefficients.FitModelSet
	SkippedReason string

	ClimateP         float64 // NaN if no climate lookup or no matching observations
	PFairClimate     float64 // climate-shrunk-naive p_fair; NaN if ClimateP is NaN
	ClimateApplied   bool
	EdgeClimateCents int

	// Bayesian posterior update of the MOS prior (ADR-0010). Populated
	// only when ScanOptions.BayesianKernel + HourlyObs are set AND
	// the MOS prior itself was valid for this trade.
	PFairBayes        float64 // 0 when not applied
	BayesApplied      bool
	BayesSkipReason   string  // "no_kernel" | "no_obs_for_station" | "cold_start" | "h_remain_zero" | "prior_only" | ""
	BayesRunningTempF float64 // R(t) at trade time; 0 when not applied
	BayesHRemain      int     // integer hours_remaining at trade time; 0 when not applied
	BayesObsWeight    float64 // posterior's ObsWeight (1 = observation dominated, 0 = prior dominated)

	NMembers      int
	NMembersInBkt int
	PollAgeS      int
	FeeCents      int
	EdgeCents     int

	// Backtest fields — populated only when ScanOptions.Truth is set.
	HasTruth    bool   // true iff truth was looked up successfully
	ActualTempF int    // observed TMAX (KXHIGH*) or TMIN (KXLOWT*) on valid_date
	YesResolved bool   // bracket would have paid YES
	OurSide     string // "yes", "no", or "" (skipped — no edge band)
	OurPnLCents int    // realized PnL after entry fee (0 if OurSide=="")
}

// Result is the output of Scan: per-trade rows plus aggregate counters.
type Result struct {
	TotalFrames   int
	TradeFrames   int
	WeatherTrades int
	PairedTrades  int
	Rows          []EdgeRow
	SkippedReason map[string]int

	// MembersSkipped counts ensemble members dropped across all paired
	// trades for failing the MinHourlySamplesPerMember coverage check.
	// A non-zero value here means at least one member was sparse on at
	// least one poll, but the row was still produced from the surviving
	// member subset (previous behaviour was to drop the whole trade).
	MembersSkipped int

	// Backtest aggregates — only populated when ScanOptions.Truth is set.
	BacktestEnabled bool
	TradesEntered   int // rows where OurSide != ""
	TradesWon       int // rows where OurPnLCents > 0
	TotalPnLCents   int // sum of OurPnLCents

	// MOSEnabled is true iff opts.MOS was non-nil. MOSApplied is the
	// count of paired trades that successfully hit a fitted bin (i.e.
	// p_fair came from MOS, not naive fallback).
	MOSEnabled bool
	MOSApplied int

	// ClimateEnabled is true iff opts.Climate was non-nil. ClimateApplied
	// counts rows where a historical climate probability was available.
	ClimateEnabled bool
	ClimateApplied int

	// BayesEnabled is true iff opts.BayesianKernel + HourlyObs were set.
	// BayesApplied counts rows where the posterior update actually fired
	// (MOS was applied, hourly obs were available, and Update produced
	// the bayes-gauss branch rather than prior-only).
	BayesEnabled bool
	BayesApplied int

	// FeeMode records which Kalshi fee schedule produced the FeeCents
	// + EdgeCents columns on every row. Echoed so downstream reports
	// don't have to guess.
	FeeMode FeeMode

	// EntryFilterName echoes opts.EntryFilter so the markdown summary
	// can report which filter ran. Empty string means no filter.
	EntryFilterName string
}

// Scan walks the data tree, pairs each Kalshi trade event with the
// most-recent Open-Meteo poll for its station, and emits a Result.
//
// Open-Meteo polls are loaded into memory first (size budget: a few
// hundred MB worst case for years of data, easily fits). Kalshi trades
// are streamed.
func Scan(opts ScanOptions) (*Result, error) {
	if opts.DataDir == "" {
		return nil, errors.New("edge: ScanOptions.DataDir required")
	}
	if opts.MaxPollAge <= 0 {
		opts.MaxPollAge = time.Hour
	}
	if opts.EdgeThresholdCents == 0 {
		opts.EdgeThresholdCents = 5
	}
	if opts.EndDate.Before(opts.StartDate) {
		return nil, errors.New("edge: EndDate before StartDate")
	}

	idx, err := loadPolls(filepath.Join(opts.DataDir, "openmeteo"), opts.StartDate, opts.EndDate)
	if err != nil {
		return nil, fmt.Errorf("load openmeteo: %w", err)
	}

	// Surface an unknown EntryFilter loudly via stdlib log. We
	// deliberately don't error out — the entryFilterAdmits fallback is
	// "admit everything", which keeps the scan running. The warning
	// makes a typo'd flag impossible to miss in operator logs.
	if !isKnownEntryFilter(opts.EntryFilter) {
		log.Printf("edge: WARNING — unknown EntryFilter %q; falling through to no-op admit (no filtering applied). Known names: \"\", \"none\", \"h4\".",
			opts.EntryFilter)
	}

	res := &Result{
		SkippedReason:   map[string]int{},
		BacktestEnabled: opts.Truth != nil,
		MOSEnabled:      opts.MOS != nil,
		ClimateEnabled:  opts.Climate != nil,
		BayesEnabled:    opts.BayesianKernel != nil && opts.HourlyObs != nil,
		FeeMode:         opts.FeeMode,
		EntryFilterName: opts.EntryFilter,
	}

	kalshiFiles, err := dailyFiles(filepath.Join(opts.DataDir, "kalshi"), opts.StartDate, opts.EndDate)
	if err != nil {
		return nil, fmt.Errorf("discover kalshi files: %w", err)
	}

	// First pass: discover every unique parsed Spec in the scan window so
	// we can group them by (series, valid_date) and resolve T-brackets
	// with sibling-aware direction inference. Without this pre-pass, a
	// "low-side" T market (e.g. KXLOWTNYC-T56 in a market whose B-brackets
	// start at B58.5) silently resolves as high-side and p_fair is
	// computed against the wrong threshold.
	siblings, err := collectSiblings(kalshiFiles)
	if err != nil {
		return nil, fmt.Errorf("collect siblings: %w", err)
	}

	// Once-per-distinct-pair throttle for "observed vs fit" model-set
	// mismatch warnings, so a single misconfigured run doesn't flood the
	// logs with one line per trade.
	var mmLogOnce sync.Map // key = "obs|fit" string → struct{}
	var fitModelSet []string
	if opts.MOS != nil {
		fitModelSet = mos.CanonicalModelSet(opts.MOS.FitModelSet)
		if len(fitModelSet) == 0 {
			// Defensive: an in-memory Coefficients{} built without
			// LoadCoefficients may have no FitModelSet. Log a single
			// warning and proceed; the per-trade guard below will skip
			// every MOS apply via the model_set_mismatch branch.
			log.Printf("edge: MOSCoefficients.FitModelSet is empty; MOS will not be applied to any row")
		}
	}

	err = replay.ForEachFrame(kalshiFiles, func(env replay.Envelope, _ int, _ bool) error {
		res.TotalFrames++
		var kf replay.KalshiFrame
		if decodeErr := json.Unmarshal(env.Payload, &kf); decodeErr != nil {
			res.SkippedReason["kalshi_frame_decode_error"]++
			return nil //nolint:nilerr // malformed frames are counted and skipped, not fatal to a scan
		}
		if kf.Type != "trade" {
			return nil
		}
		res.TradeFrames++

		var msg tradeMsg
		if decodeErr := json.Unmarshal(kf.Msg, &msg); decodeErr != nil {
			res.SkippedReason["trade_decode_error"]++
			return nil //nolint:nilerr // malformed trade messages are counted and skipped
		}
		spec, parseErr := ParseTicker(msg.MarketTicker)
		if parseErr != nil {
			res.SkippedReason["non_weather_ticker"]++
			return nil //nolint:nilerr // non-weather tickers are expected noise in the tape
		}
		res.WeatherTrades++

		// Find most-recent poll for this station.
		station := spec.Station()
		tradeTime := time.Unix(0, msg.TSMs*int64(time.Millisecond)).UTC()
		poll, pollTime, ok := idx.latest(station, tradeTime)
		if !ok {
			res.SkippedReason["no_poll_for_station"]++
			return nil
		}
		age := tradeTime.Sub(pollTime)
		if age > opts.MaxPollAge {
			res.SkippedReason["poll_too_old"]++
			return nil
		}

		tz, tzErr := stationLocation(station)
		if tzErr != nil {
			res.SkippedReason["unknown_station_tz"]++
			return nil //nolint:nilerr // unknown stations are counted and skipped
		}
		extremes, skipped, extremesErr := poll.DailyExtremesFWithSkip(spec.ValidDate, tz)
		if extremesErr != nil {
			if errors.Is(extremesErr, ErrInsufficientCoverage) {
				res.SkippedReason["insufficient_poll_coverage"]++
				return nil //nolint:nilerr // partial polls are counted and skipped
			}
			return fmt.Errorf("daily extremes: %w", extremesErr)
		}
		// Diagnostic: how many ensemble members were dropped for partial
		// coverage on this trade-poll pairing. Aggregate counter only —
		// not a per-row field, since the row's NMembers already reflects
		// the surviving subset.
		res.MembersSkipped += skipped

		// Resolution. T-brackets default to high-side (≥ K). The first-pass
		// scan over Kalshi files (above) groups every observed Spec by
		// (series, valid_date) so ResolveWithSiblings can flip a T-bracket
		// to low-side when its key sits below the lowest sibling B-bracket
		// midpoint. The bracket convention was previously marked FIXME
		// and is now closed (see resolution.go and its tests).
		sibKey := siblingKey(spec.Series, spec.ValidDate)
		resolution := ResolveWithSiblings(spec, siblings[sibKey])

		// Naive p_fair = fraction of members whose daily extreme falls in
		// the bracket. KXHIGH* uses TMAX, KXLOWT* uses TMIN. We also sum
		// the per-member extreme to compute the ensemble mean that feeds
		// the MOS calibrator below.
		nIn := 0
		var sumT float64
		for _, md := range extremes {
			t := md.TMaxF
			if !spec.IsHigh() {
				t = md.TMinF
			}
			sumT += t
			if resolution.Contains(t) {
				nIn++
			}
		}
		nMembers := len(extremes)
		pFairNaive := float64(nIn) / float64(nMembers)

		// variable is reused by both the MOS Predict() and the
		// Climate.RangeProb() paths below.
		variable := "tmax"
		if !spec.IsHigh() {
			variable = "tmin"
		}

		// MOS-calibrated p_fair, if coefficients were provided. We feed
		// the ensemble mean into the fitted (μ=a+b·f, σ) and integrate
		// the normal across the bracket [Lo, Hi). Skip paths:
		//   - no coefficients loaded                  → "no_coeffs"
		//   - model-set mismatch (observed vs fit)    → "model_set_mismatch:<obs>vs<fit>"
		//   - no fitted bin for (station, season, variable) → "no_bin"
		// All fall back to naive p_fair so the backtest never silently
		// disappears them.
		pFair := pFairNaive
		var (
			pFairMOS      float64
			mosApplied    bool
			mosSkipReason string
			// MOS prior (μ, σ); preserved across the switch so the
			// Bayesian path below can fold in the running extremum
			// without re-running Predict.
			mosMu, mosSigma float64
		)
		switch {
		case opts.MOS == nil:
			mosSkipReason = "no_coeffs"
		case len(fitModelSet) == 0:
			// Coefficients with empty FitModelSet — treat as a fatal
			// mismatch for every poll. Logged once at the top of Scan.
			obsModelSet := poll.ModelSet
			mosSkipReason = fmt.Sprintf("model_set_mismatch:%svs",
				strings.Join(obsModelSet, "+"))
			res.SkippedReason[mosSkipReason]++
		default:
			obsModelSet := poll.ModelSet
			if !mos.ModelSetEqual(obsModelSet, fitModelSet) {
				key := strings.Join(obsModelSet, "+") + "|" + strings.Join(fitModelSet, "+")
				if _, loaded := mmLogOnce.LoadOrStore(key, struct{}{}); !loaded {
					log.Printf("edge: MOS model-set mismatch — observed=%v fit=%v; falling back to naive p_fair",
						obsModelSet, fitModelSet)
				}
				mosSkipReason = fmt.Sprintf("model_set_mismatch:%svs%s",
					strings.Join(obsModelSet, "+"), strings.Join(fitModelSet, "+"))
				res.SkippedReason[mosSkipReason]++
			} else {
				mean := sumT / float64(nMembers)
				mu, sigma, ok := opts.MOS.Predict(station, spec.ValidDate, variable, mean)
				if ok {
					pFairMOS = mos.ProbBelow(resolution.Hi, mu, sigma) -
						mos.ProbBelow(resolution.Lo, mu, sigma)
					if pFairMOS < 0 {
						pFairMOS = 0
					}
					if pFairMOS > 1 {
						pFairMOS = 1
					}
					pFair = pFairMOS
					mosApplied = true
					mosMu, mosSigma = mu, sigma
				} else {
					mosSkipReason = "no_bin"
				}
			}
		}

		// Bayesian posterior update (ADR-0010): fold the running
		// extremum at trade time into the MOS prior. Only applies
		// when MOS itself was applied; otherwise we have no Gaussian
		// prior to update.
		var (
			pFairBayes        float64
			bayesApplied      bool
			bayesSkipReason   string
			bayesRunningTempF float64
			bayesHRemain      int
			bayesObsWeight    float64
		)
		if opts.BayesianKernel != nil && opts.HourlyObs != nil {
			switch {
			case !mosApplied:
				bayesSkipReason = "no_prior"
			default:
				tradeTimeUTC := time.UnixMilli(msg.TSMs).UTC()
				r, hRemain, ok := opts.HourlyObs.RunningExtremum(station, variable, tradeTimeUTC)
				switch {
				case !ok:
					bayesSkipReason = "cold_start"
				case hRemain == 0:
					// At settlement boundary the kernel's update path
					// returns obs-only; for the backtest we treat it as
					// "settled, no useful posterior update beyond R(t)".
					bayesSkipReason = "h_remain_zero"
				default:
					prior := bayesian.Prior{Mu: mosMu, Sigma: mosSigma}
					obsRec := bayesian.Observation{
						Station:         station,
						Variable:        variable,
						RunningExtremum: r,
						HoursRemaining:  hRemain,
					}
					post := bayesian.Update(prior, obsRec, opts.BayesianKernel)
					if post.Method != "bayes-gauss" {
						bayesSkipReason = post.Method // "prior-only" if bin missing
					} else {
						pBayes := mos.ProbBelow(resolution.Hi, post.Mu, post.Sigma) -
							mos.ProbBelow(resolution.Lo, post.Mu, post.Sigma)
						if pBayes < 0 {
							pBayes = 0
						}
						if pBayes > 1 {
							pBayes = 1
						}
						pFairBayes = pBayes
						pFair = pBayes
						bayesApplied = true
						bayesRunningTempF = r
						bayesHRemain = hRemain
						bayesObsWeight = post.ObsWeight
					}
				}
			}
		}

		priceDollars := msg.priceDollars()
		if priceDollars < 0 {
			res.SkippedReason["bad_trade_price"]++
			return nil
		}

		// Climate-shrunk-naive baseline. Reported alongside p_fair as a
		// diagnostic column; not used as the trading model.
		climateP := math.NaN()
		pFairClimate := math.NaN()
		edgeClimateCents := 0
		climateApplied := false
		if opts.Climate != nil {
			climateP = opts.Climate.RangeProb(station, variable, spec.ValidDate, resolution.Lo, resolution.Hi)
			if !math.IsNaN(climateP) {
				pFairClimate = climate.ShrinkP(pFairNaive, climateP, climate.DefaultShrinkage)
				edgeClimateCents = EdgeAfterFeeCentsMode(pFairClimate, priceDollars, opts.FeeMode)
				climateApplied = true
			}
		}

		row := EdgeRow{
			TSMs:              msg.TSMs,
			Ticker:            spec.Ticker,
			Series:            spec.Series,
			ValidDate:         spec.ValidDate,
			BracketKind:       spec.Kind.String(),
			BracketKey:        spec.Key,
			Side:              msg.side(),
			TradePriceCents:   int(priceDollars*100 + 0.5),
			PFairNaive:        pFairNaive,
			PFairMOS:          pFairMOS,
			PFair:             pFair,
			MOSApplied:        mosApplied,
			SkippedReason:     mosSkipReason,
			ClimateP:          climateP,
			PFairClimate:      pFairClimate,
			ClimateApplied:    climateApplied,
			EdgeClimateCents:  edgeClimateCents,
			PFairBayes:        pFairBayes,
			BayesApplied:      bayesApplied,
			BayesSkipReason:   bayesSkipReason,
			BayesRunningTempF: bayesRunningTempF,
			BayesHRemain:      bayesHRemain,
			BayesObsWeight:    bayesObsWeight,
			NMembers:          nMembers,
			NMembersInBkt:     nIn,
			PollAgeS:          int(age.Seconds()),
			FeeCents:          FeePerContractCentsMode(priceDollars, opts.FeeMode),
			EdgeCents:         EdgeAfterFeeCentsMode(pFair, priceDollars, opts.FeeMode),
		}

		// Realized-PnL backtest path. If no truth available we skip with a
		// reason — this is the user's "no imaginary bets" rule: never score
		// a trade whose outcome we can't look up.
		if opts.Truth != nil {
			truth, ok := opts.Truth.Get(station, spec.ValidDate)
			if !ok {
				res.SkippedReason["no_truth_available"]++
				return nil
			}
			row.HasTruth = true
			actual := truth.TMaxF
			if !spec.IsHigh() {
				actual = truth.TMinF
			}
			row.ActualTempF = actual
			row.YesResolved = resolution.Contains(float64(actual))

			// Decide hypothetical action: take the side with the larger
			// post-fee edge, if it clears the threshold AND the named
			// entry filter (if any) admits this trade.
			noPriceDollars := 1 - priceDollars
			fee := FeePerContractCentsMode(priceDollars, opts.FeeMode)
			edgeYesCents := int(pFair*100+0.5) - row.TradePriceCents - fee
			edgeNoCents := int((1-pFair)*100+0.5) - int(noPriceDollars*100+0.5) - fee

			filterAdmits := entryFilterAdmits(opts.EntryFilter, spec.Series, row.TradePriceCents)

			switch {
			case filterAdmits && edgeYesCents >= opts.EdgeThresholdCents && edgeYesCents >= edgeNoCents:
				row.OurSide = "yes"
				if row.YesResolved {
					row.OurPnLCents = 100 - row.TradePriceCents - fee
				} else {
					row.OurPnLCents = -row.TradePriceCents - fee
				}
			case filterAdmits && edgeNoCents >= opts.EdgeThresholdCents:
				row.OurSide = "no"
				noPriceCents := 100 - row.TradePriceCents
				if !row.YesResolved {
					row.OurPnLCents = 100 - noPriceCents - fee
				} else {
					row.OurPnLCents = -noPriceCents - fee
				}
			default:
				// no edge OR filter excluded — skip the trade (PnL = 0, OurSide = "")
			}

			if row.OurSide != "" {
				res.TradesEntered++
				if row.OurPnLCents > 0 {
					res.TradesWon++
				}
				res.TotalPnLCents += row.OurPnLCents
			}
		}

		res.Rows = append(res.Rows, row)
		res.PairedTrades++
		if row.MOSApplied {
			res.MOSApplied++
		}
		if row.ClimateApplied {
			res.ClimateApplied++
		}
		if row.BayesApplied {
			res.BayesApplied++
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// Stable sort by ts for deterministic CSV output.
	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].TSMs < res.Rows[j].TSMs })
	return res, nil
}

// pollModelSet returns the canonical sorted model set observed in the
// poll, derived from member-ID prefixes. Bare numeric IDs ("00".."30")
// are GFS (single-model default). Prefixed IDs like
// "ncep_gefs_seamless:NN" / "ecmwf_ifs025_ensemble:NN" canonicalize to
// the short tags mos.ModelGFS / mos.ModelECMWF respectively so they can
// be compared against [mos.Coefficients.FitModelSet] via
// [mos.ModelSetEqual].
//
// This signature takes the post-coverage-filter extremes map because
// historically the scan hot path passed that view. In the typical case
// (all members surviving the [MinHourlySamplesPerMember] check) it
// agrees with [pollModelSetFromMembers] computed from the full member
// set. The pathological case — every member of an ENTIRE model failing
// coverage on the same poll — would yield a smaller set here than the
// precomputed [Poll.ModelSet]; that case is rare enough that A11 caches
// the full-member view and surfaces such polls as model-set mismatches
// (which they functionally are). Retained for the cache-consistency
// regression test and as the canonical derivation reference.
func pollModelSet(extremes map[string]MemberDaily) []string {
	if len(extremes) == 0 {
		return nil
	}
	tags := make([]string, 0, 2)
	for id := range extremes {
		tags = append(tags, memberModelTag(id))
	}
	return mos.CanonicalModelSet(tags)
}

// pollModelSetFromMembers is the [pollModelSet] equivalent that operates
// directly on [Poll.Members]. Used by [ParsePoll] to precompute
// [Poll.ModelSet] once per poll-load instead of per trade.
func pollModelSetFromMembers(members map[string][]float64) []string {
	if len(members) == 0 {
		return nil
	}
	tags := make([]string, 0, 2)
	for id := range members {
		tags = append(tags, memberModelTag(id))
	}
	return mos.CanonicalModelSet(tags)
}

// memberModelTag maps a parser-emitted member ID to the canonical short
// model name (mos.ModelGFS / mos.ModelECMWF / "<full-prefix>" for
// unknown). Bare numeric IDs are GFS; prefixed IDs split on ":" and the
// prefix is matched against known model-name patterns.
func memberModelTag(memberID string) string {
	colon := strings.Index(memberID, ":")
	if colon < 0 {
		// Bare "00".."30" → GFS (single-model default).
		return mos.ModelGFS
	}
	prefix := memberID[:colon]
	switch {
	case strings.HasPrefix(prefix, "ecmwf"):
		return mos.ModelECMWF
	case strings.HasPrefix(prefix, "ncep_gefs"), strings.HasPrefix(prefix, "gfs"):
		return mos.ModelGFS
	default:
		// Unknown model prefix — surface verbatim. The mismatch guard
		// will reject the poll against any coefficients.FitModelSet
		// that doesn't list this exact tag.
		return prefix
	}
}

// siblingKey is the canonical bucket for "specs in the same market family
// on the same civil day", used to look up siblings when resolving
// T-bracket direction. Variable is implicit in series prefix (KXHIGH* vs
// KXLOWT*), so series+valid_date is sufficient.
func siblingKey(series string, validDate time.Time) string {
	return series + "|" + validDate.Format("2006-01-02")
}

// collectSiblings does a first pass over every Kalshi trade frame in the
// scan window, parses each unique market_ticker into a Spec, and groups
// the Specs by siblingKey. The returned map's slices each contain every
// Spec observed for that (series, valid_date) bucket — which is exactly
// the input ResolveWithSiblings expects.
//
// Errors only on file-level IO problems; malformed trade frames and
// non-weather tickers are silently skipped (the main scan loop counts
// those separately).
func collectSiblings(kalshiFiles []string) (map[string][]Spec, error) {
	seenTicker := map[string]bool{} // dedupe by raw ticker
	out := map[string][]Spec{}
	err := replay.ForEachFrame(kalshiFiles, func(env replay.Envelope, _ int, _ bool) error {
		var kf replay.KalshiFrame
		if decodeErr := json.Unmarshal(env.Payload, &kf); decodeErr != nil {
			return nil //nolint:nilerr // malformed frames are silently skipped here; counted by the main scan loop
		}
		if kf.Type != "trade" {
			return nil
		}
		var msg tradeMsg
		if decodeErr := json.Unmarshal(kf.Msg, &msg); decodeErr != nil {
			return nil //nolint:nilerr // malformed trade messages are silently skipped; counted by the main scan loop
		}
		if seenTicker[msg.MarketTicker] {
			return nil
		}
		seenTicker[msg.MarketTicker] = true
		spec, parseErr := ParseTicker(msg.MarketTicker)
		if parseErr != nil {
			// Non-weather or malformed ticker — skip silently here; the
			// main scan loop will count these per-trade.
			return nil //nolint:nilerr // non-weather tickers are expected; counted by the main scan loop
		}
		k := siblingKey(spec.Series, spec.ValidDate)
		out[k] = append(out[k], spec)
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return out, nil
}

// stationLocation returns the IANA timezone for each Kalshi-resolved
// weather station. NWS CLI civil days are local time at the station.
func stationLocation(station string) (*time.Location, error) {
	switch station {
	case "KNYC", "KMIA":
		return time.LoadLocation("America/New_York")
	case "KMDW":
		return time.LoadLocation("America/Chicago")
	case "KLAX":
		return time.LoadLocation("America/Los_Angeles")
	}
	return nil, fmt.Errorf("unknown station %q", station)
}

// entryFilterAdmits returns true iff the named filter admits an entry
// on (series, tradePriceCents). The empty filter ("") and unknown
// filter names admit everything (no-op), matching the v0-1 behaviour
// of "enter on any clearing edge".
//
// Filter "h4" (Phase 0.5 v4): exclude KXHIGHNY entirely; require
// either a low-temp series (KXLOWT*) OR an extreme-OTM price band
// (≤ 9¢ or ≥ 91¢). See reports/phase0.5/2026-05-19-v4 for the
// subset-mining + LOO-CV evidence behind these conditions.
func entryFilterAdmits(filter, series string, tradePriceCents int) bool {
	switch filter {
	case "", "none":
		return true
	case "h4":
		if series == "KXHIGHNY" {
			return false
		}
		if strings.HasPrefix(series, "KXLOWT") {
			return true
		}
		if tradePriceCents <= 9 || tradePriceCents >= 91 {
			return true
		}
		return false
	default:
		// Unknown filter name — silently fall back to "no filter" so
		// future flag additions never accidentally skip every trade.
		// Scan() logs a one-shot warning at boot when this branch will
		// be taken; operators are still expected to typo-check before
		// running 4-hour scans.
		return true
	}
}

// isKnownEntryFilter is the predicate Scan() uses to decide whether to
// warn about an unknown filter name. Kept next to entryFilterAdmits so
// the two stay in sync if a new filter ships.
func isKnownEntryFilter(filter string) bool {
	switch filter {
	case "", "none", "h4":
		return true
	default:
		return false
	}
}
