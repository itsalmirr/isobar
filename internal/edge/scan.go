package edge

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/wh0o0pz/isobar/internal/calibration/bayesian"
	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/calibration/mos"
	"github.com/wh0o0pz/isobar/internal/market"
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
	// BayesSkipReason values:
	//   ""                update applied (BayesApplied=true)
	//   "no_prior"        MOS prior missing for this trade (nothing to update)
	//   "wrong_civil_day" trade time is not on the bracket's resolution
	//                     civil day (station-local) — e.g. a next-day
	//                     bracket traded today has no same-day R(t) yet
	//   "cold_start"      no hourly obs in the civil-day window up to trade time
	//   "h_remain_zero"   settlement boundary reached
	//   "prior-only"      kernel bin missing (bayesian.Update fell back to prior)
	PFairBayes        float64 // 0 when not applied
	BayesApplied      bool
	BayesSkipReason   string
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

	// EdgeThresholdCents echoes the (defaulted) entry threshold that
	// gated backtest entries, so reports can label edge-band sections
	// with the value that actually ran instead of assuming the default.
	EdgeThresholdCents int
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
		log.Printf("edge: WARNING — unknown EntryFilter %q; falling through to no-op admit (no filtering applied). Known names: %s.",
			opts.EntryFilter, knownEntryFilterNames())
	}

	res := &Result{
		SkippedReason:      map[string]int{},
		BacktestEnabled:    opts.Truth != nil,
		MOSEnabled:         opts.MOS != nil,
		ClimateEnabled:     opts.Climate != nil,
		BayesEnabled:       opts.BayesianKernel != nil && opts.HourlyObs != nil,
		FeeMode:            opts.FeeMode,
		EntryFilterName:    opts.EntryFilter,
		EdgeThresholdCents: opts.EdgeThresholdCents,
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

	var fitModelSet []string
	if opts.MOS != nil {
		fitModelSet = mos.CanonicalModelSet(opts.MOS.FitModelSet)
		if len(fitModelSet) == 0 {
			// Defensive: an in-memory Coefficients{} built without
			// LoadCoefficients may have no FitModelSet. Log a single
			// warning and proceed; the per-trade guard will skip every
			// MOS apply via the model_set_mismatch branch.
			log.Printf("edge: MOSCoefficients.FitModelSet is empty; MOS will not be applied to any row")
		}
	}

	s := &scanner{
		opts:        opts,
		res:         res,
		idx:         idx,
		siblings:    siblings,
		fitModelSet: fitModelSet,
		mmLogged:    map[string]struct{}{},
	}
	err = replay.ForEachFrame(kalshiFiles, s.processFrame)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// Stable sort by ts for deterministic CSV output.
	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].TSMs < res.Rows[j].TSMs })
	return res, nil
}

// scanner threads the per-run state through the frame walk. ForEachFrame
// is strictly sequential, so none of this needs synchronization.
type scanner struct {
	opts        ScanOptions
	res         *Result
	idx         *pollIndex
	siblings    map[string][]Spec
	fitModelSet []string
	// mmLogged throttles "observed vs fit" model-set mismatch warnings
	// to once per distinct pair, so a single misconfigured run doesn't
	// flood the logs with one line per trade.
	mmLogged map[string]struct{}
}

// processFrame decodes one journal frame, filters it down to a weather
// trade, and hands it to processTrade. Malformed frames and non-weather
// tickers are counted and skipped — expected tape noise, never fatal.
func (s *scanner) processFrame(env replay.Envelope, _ int, _ bool) error {
	s.res.TotalFrames++
	var kf replay.KalshiFrame
	if decodeErr := sonic.Unmarshal(env.Payload, &kf); decodeErr != nil {
		s.res.SkippedReason["kalshi_frame_decode_error"]++
		return nil //nolint:nilerr // malformed frames are counted and skipped, not fatal to a scan
	}
	if kf.Type != "trade" {
		return nil
	}
	s.res.TradeFrames++

	var msg tradeMsg
	if decodeErr := sonic.Unmarshal(kf.Msg, &msg); decodeErr != nil {
		s.res.SkippedReason["trade_decode_error"]++
		return nil //nolint:nilerr // malformed trade messages are counted and skipped
	}
	spec, parseErr := ParseTicker(msg.MarketTicker)
	if parseErr != nil {
		s.res.SkippedReason["non_weather_ticker"]++
		return nil //nolint:nilerr // non-weather tickers are expected noise in the tape
	}
	s.res.WeatherTrades++
	return s.processTrade(spec, msg)
}

// processTrade runs the pipeline for one weather trade: pair it to the
// freshest poll, resolve the bracket, layer the probability models
// (naive → MOS → Bayes, plus the climate diagnostic), and score + record
// the row. Unpairable trades increment a SkippedReason counter and drop.
func (s *scanner) processTrade(spec Spec, msg tradeMsg) error {
	station := spec.Station()
	tradeTime := time.Unix(0, msg.TSMs*int64(time.Millisecond)).UTC()
	poll, pollTime, ok := s.idx.latest(station, tradeTime)
	if !ok {
		s.res.SkippedReason["no_poll_for_station"]++
		return nil
	}
	age := tradeTime.Sub(pollTime)
	if age > s.opts.MaxPollAge {
		s.res.SkippedReason["poll_too_old"]++
		return nil
	}

	tz, tzErr := market.StationTZ(station)
	if tzErr != nil {
		s.res.SkippedReason["unknown_station_tz"]++
		return nil //nolint:nilerr // unknown stations are counted and skipped
	}
	extremes, skipped, extremesErr := poll.DailyExtremesFWithSkip(spec.ValidDate, tz)
	if extremesErr != nil {
		if errors.Is(extremesErr, ErrInsufficientCoverage) {
			s.res.SkippedReason["insufficient_poll_coverage"]++
			return nil //nolint:nilerr // partial polls are counted and skipped
		}
		return fmt.Errorf("daily extremes: %w", extremesErr)
	}
	// Diagnostic: how many ensemble members were dropped for partial
	// coverage on this trade-poll pairing. Aggregate counter only —
	// not a per-row field, since the row's NMembers already reflects
	// the surviving subset.
	s.res.MembersSkipped += skipped

	// Resolution. T-brackets default to high-side (≥ K). collectSiblings
	// grouped every observed Spec by (series, valid_date) so
	// ResolveWithSiblings can flip a T-bracket to low-side when its key
	// sits below the lowest sibling B-bracket midpoint.
	resolution := ResolveWithSiblings(spec, s.siblings[siblingKey(spec.Series, spec.ValidDate)])

	pFairNaive, nIn, sumT := naiveEnsembleStats(spec.IsHigh(), extremes, resolution)
	nMembers := len(extremes)
	variable := spec.Variable()

	// MOS-calibrated p_fair, if coefficients were provided. applyMOS
	// integrates the fitted normal across the bracket and, on any skip
	// path, leaves p_fair on the naive estimate. It also surfaces the
	// (μ, σ) prior the Bayesian path below folds the running extremum
	// into, plus the SkippedReason key to count (empty when none).
	m := applyMOS(mosInputs{
		coeffs:      s.opts.MOS,
		poll:        poll,
		station:     station,
		validDate:   spec.ValidDate,
		variable:    variable,
		resolution:  resolution,
		sumT:        sumT,
		nMembers:    nMembers,
		fitModelSet: s.fitModelSet,
		logged:      s.mmLogged,
	})
	pFair := pFairNaive
	if m.applied {
		pFair = m.pFairMOS
	}
	if m.countReason != "" {
		s.res.SkippedReason[m.countReason]++
	}

	// Bayesian posterior update (ADR-0010): fold the running extremum
	// at trade time into the MOS prior. Only applies when MOS itself
	// applied (we need a Gaussian prior to update); the (μ, σ) come
	// straight from applyMOS above.
	b := applyBayes(bayesInputs{
		kernel:     s.opts.BayesianKernel,
		obs:        s.opts.HourlyObs,
		station:    station,
		variable:   variable,
		tsMs:       msg.TSMs,
		resolution: resolution,
		mosApplied: m.applied,
		priorMu:    m.mu,
		priorSigma: m.sigma,
	})
	if b.applied {
		pFair = b.pFairBayes
	}

	priceDollars := msg.priceDollars()
	if priceDollars < 0 {
		s.res.SkippedReason["bad_trade_price"]++
		return nil
	}

	// Climate-shrunk-naive baseline. Reported alongside p_fair as a
	// diagnostic column; not used as the trading model.
	c := applyClimate(climateInputs{
		lookup:     s.opts.Climate,
		station:    station,
		variable:   variable,
		validDate:  spec.ValidDate,
		resolution: resolution,
		pFairNaive: pFairNaive,
		price:      priceDollars,
		feeMode:    s.opts.FeeMode,
	})

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
		PFairMOS:          m.pFairMOS,
		PFair:             pFair,
		MOSApplied:        m.applied,
		SkippedReason:     m.skipReason,
		ClimateP:          c.climateP,
		PFairClimate:      c.pFairClimate,
		ClimateApplied:    c.applied,
		EdgeClimateCents:  c.edgeClimateCents,
		PFairBayes:        b.pFairBayes,
		BayesApplied:      b.applied,
		BayesSkipReason:   b.skipReason,
		BayesRunningTempF: b.runningTempF,
		BayesHRemain:      b.hRemain,
		BayesObsWeight:    b.obsWeight,
		NMembers:          nMembers,
		NMembersInBkt:     nIn,
		PollAgeS:          int(age.Seconds()),
		FeeCents:          FeePerContractCentsMode(priceDollars, s.opts.FeeMode),
		EdgeCents:         EdgeAfterFeeCentsMode(pFair, priceDollars, s.opts.FeeMode),
	}

	// Realized-PnL backtest path. If no truth available we skip with a
	// reason — this is the user's "no imaginary bets" rule: never score
	// a trade whose outcome we can't look up.
	if s.opts.Truth != nil {
		bt, ok := scoreBacktestRow(backtestInputs{
			truth:         s.opts.Truth,
			station:       station,
			validDate:     spec.ValidDate,
			series:        spec.Series,
			isHigh:        spec.IsHigh(),
			resolution:    resolution,
			pFair:         pFair,
			price:         priceDollars,
			tradePrice:    row.TradePriceCents,
			feeMode:       s.opts.FeeMode,
			entryFilter:   s.opts.EntryFilter,
			edgeThreshold: s.opts.EdgeThresholdCents,
		})
		if !ok {
			s.res.SkippedReason["no_truth_available"]++
			return nil
		}
		row.HasTruth = true
		row.ActualTempF = bt.actualTempF
		row.YesResolved = bt.yesResolved
		row.OurSide = bt.ourSide
		row.OurPnLCents = bt.ourPnLCents

		if row.OurSide != "" {
			s.res.TradesEntered++
			if row.OurPnLCents > 0 {
				s.res.TradesWon++
			}
			s.res.TotalPnLCents += row.OurPnLCents
		}
	}

	s.res.Rows = append(s.res.Rows, row)
	s.res.PairedTrades++
	if row.MOSApplied {
		s.res.MOSApplied++
	}
	if row.ClimateApplied {
		s.res.ClimateApplied++
	}
	if row.BayesApplied {
		s.res.BayesApplied++
	}
	return nil
}

// naiveEnsembleStats computes the naive p_fair — the fraction of ensemble
// members whose daily extreme falls in the bracket (TMAX for KXHIGH*,
// TMIN for KXLOWT*) — plus the member-extreme sum that feeds the MOS
// ensemble mean. Callers guarantee extremes is non-empty
// (DailyExtremesFWithSkip errors instead of returning an empty map).
func naiveEnsembleStats(isHigh bool, extremes map[string]MemberDaily, res Resolution) (pFair float64, nIn int, sumT float64) {
	for _, md := range extremes {
		t := md.TMaxF
		if !isHigh {
			t = md.TMinF
		}
		sumT += t
		if res.Contains(t) {
			nIn++
		}
	}
	return float64(nIn) / float64(len(extremes)), nIn, sumT
}

// bracketProb integrates a fitted normal (μ, σ) across the half-open
// bracket [lo, hi) and clamps the result into [0, 1]. Both the MOS and
// Bayesian p_fair paths price a bracket this way.
func bracketProb(lo, hi, mu, sigma float64) float64 {
	p := mos.ProbBelow(hi, mu, sigma) - mos.ProbBelow(lo, mu, sigma)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// mosInputs bundles the per-trade inputs applyMOS reads. It is passed by
// value; every field is a scalar, string, or pointer already live on the
// scan hot path, so no additional allocation occurs.
type mosInputs struct {
	coeffs      *mos.Coefficients
	poll        *Poll
	station     string
	validDate   time.Time
	variable    string
	resolution  Resolution
	sumT        float64
	nMembers    int
	fitModelSet []string
	// logged is the once-per-distinct-pair mismatch-warning throttle,
	// keyed "obs|fit". Plain map: the scan walk is single-threaded.
	logged map[string]struct{}
}

// mosResult is the by-value contribution of the MOS calibration step.
// pFairMOS is 0 unless applied; mu/sigma carry the fitted prior forward to
// the Bayesian update. countReason, when non-empty, is the Result
// SkippedReason key the caller must increment (the aggregate side-effect
// kept out of this otherwise-pure helper). skipReason is the per-row tag.
type mosResult struct {
	pFairMOS    float64
	applied     bool
	skipReason  string
	countReason string
	mu, sigma   float64
}

// applyMOS computes the MOS-calibrated p_fair contribution for one trade.
// On every skip path it leaves applied=false so the caller keeps the naive
// estimate; the model-set-mismatch paths also emit a once-per-distinct-pair
// warning via in.logged. See the mos-prior/model-set-guard discussion in
// docs/CALIBRATION.md.
func applyMOS(in mosInputs) mosResult {
	switch {
	case in.coeffs == nil:
		return mosResult{skipReason: "no_coeffs"}
	case len(in.fitModelSet) == 0:
		// Coefficients with empty FitModelSet — treat as a fatal mismatch
		// for every poll. Logged once at the top of Scan.
		obs := in.poll.ModelSet
		reason := fmt.Sprintf("model_set_mismatch:%svs", strings.Join(obs, "+"))
		return mosResult{skipReason: reason, countReason: reason}
	default:
		obs := in.poll.ModelSet
		if !mos.ModelSetEqual(obs, in.fitModelSet) {
			key := strings.Join(obs, "+") + "|" + strings.Join(in.fitModelSet, "+")
			if _, loaded := in.logged[key]; !loaded {
				in.logged[key] = struct{}{}
				log.Printf("edge: MOS model-set mismatch — observed=%v fit=%v; falling back to naive p_fair",
					obs, in.fitModelSet)
			}
			reason := fmt.Sprintf("model_set_mismatch:%svs%s",
				strings.Join(obs, "+"), strings.Join(in.fitModelSet, "+"))
			return mosResult{skipReason: reason, countReason: reason}
		}
		mean := in.sumT / float64(in.nMembers)
		mu, sigma, ok := in.coeffs.Predict(in.station, in.validDate, in.variable, mean)
		if !ok {
			return mosResult{skipReason: "no_bin"}
		}
		return mosResult{
			pFairMOS: bracketProb(in.resolution.Lo, in.resolution.Hi, mu, sigma),
			applied:  true,
			mu:       mu,
			sigma:    sigma,
		}
	}
}

// bayesInputs bundles the per-trade inputs applyBayes reads. priorMu and
// priorSigma are the MOS-fitted prior handed off from applyMOS; the update
// only runs when mosApplied is true.
type bayesInputs struct {
	kernel     *bayesian.Kernel
	obs        *bayesian.HourlyIndex
	station    string
	variable   string
	tsMs       int64
	resolution Resolution
	mosApplied bool
	priorMu    float64
	priorSigma float64
}

// bayesResult is the by-value contribution of the Bayesian posterior
// update. pFairBayes is 0 unless applied; the caller overwrites p_fair with
// it only when applied is true (last-writer-wins: naive → MOS → Bayes).
type bayesResult struct {
	pFairBayes   float64
	applied      bool
	skipReason   string
	runningTempF float64
	hRemain      int
	obsWeight    float64
}

// applyBayes folds the running extremum at trade time into the MOS prior
// (ADR-0010). It no-ops (returns applied=false, empty skipReason) when no
// kernel/obs index is configured, mirroring the "silently skipped" contract
// in ScanOptions.BayesianKernel's doc.
func applyBayes(in bayesInputs) bayesResult {
	if in.kernel == nil || in.obs == nil {
		return bayesResult{}
	}
	if !in.mosApplied {
		return bayesResult{skipReason: "no_prior"}
	}
	tradeTimeUTC := time.UnixMilli(in.tsMs).UTC()
	r, hRemain, ok := in.obs.RunningExtremum(in.station, in.variable, tradeTimeUTC)
	switch {
	case !ok:
		return bayesResult{skipReason: "cold_start"}
	case hRemain == 0:
		// At the settlement boundary the kernel's update path returns
		// obs-only; for the backtest we treat it as "settled, no useful
		// posterior update beyond R(t)".
		return bayesResult{skipReason: "h_remain_zero"}
	}
	prior := bayesian.Prior{Mu: in.priorMu, Sigma: in.priorSigma}
	obsRec := bayesian.Observation{
		Station:         in.station,
		Variable:        in.variable,
		RunningExtremum: r,
		HoursRemaining:  hRemain,
	}
	post := bayesian.Update(prior, obsRec, in.kernel)
	if post.Method != "bayes-gauss" {
		return bayesResult{skipReason: post.Method} // "prior-only" if bin missing
	}
	return bayesResult{
		pFairBayes:   bracketProb(in.resolution.Lo, in.resolution.Hi, post.Mu, post.Sigma),
		applied:      true,
		runningTempF: r,
		hRemain:      hRemain,
		obsWeight:    post.ObsWeight,
	}
}

		row := EdgeRow{
			TsMs:            msg.TsMs,
			Ticker:          spec.Ticker,
			Series:          spec.Series,
			ValidDate:       spec.ValidDate,
			BracketKind:     spec.Kind.String(),
			BracketKey:      spec.Key,
			Side:            msg.Side,
			TradePriceCents: execPriceCents,
			PFairNaive:      pFair,
			NMembers:        nMembers,
			NMembersInBkt:   nIn,
			PollAgeS:        int(age.Seconds()),
			FeeCents:        fee,
			// EdgeCents is the YES-perspective post-fee mispricing magnitude
			// — same number for either trade side because the underlying
			// market is the same.
			EdgeCents: EdgeAfterFeeCents(pFair, yesPriceDollars),
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

			// Decide hypothetical action: take whichever side has the larger
			// post-fee edge, if it clears the threshold. Both edges are
			// computed in the canonical YES-perspective frame, so this works
			// identically whether the recorded trade was yes-side or no-side.
			pFairYesCents := int(pFair*100 + 0.5)
			edgeYesCents := pFairYesCents - yesPriceCents - fee
			edgeNoCents := (100 - pFairYesCents) - noPriceCents - fee

			filterAdmits := entryFilterAdmits(opts.EntryFilter, spec.Series, row.TradePriceCents)

			switch {
			case filterAdmits && edgeYesCents >= opts.EdgeThresholdCents && edgeYesCents >= edgeNoCents:
				row.OurSide = "yes"
				if row.YesResolved {
					row.OurPnLCents = 100 - yesPriceCents - fee
				} else {
					row.OurPnLCents = -yesPriceCents - fee
				}
			case filterAdmits && edgeNoCents >= opts.EdgeThresholdCents:
				row.OurSide = "no"
				if !row.YesResolved {
					row.OurPnLCents = 100 - noPriceCents - fee
				} else {
					row.OurPnLCents = -noPriceCents - fee
				}
			default:
				// no edge OR filter excluded — skip the trade (PnL = 0, OurSide = "")
			}

// applyClimate computes the climate-shrunk-naive diagnostic p_fair. It is
// reported alongside p_fair, never used as the trading model.
func applyClimate(in climateInputs) climateResult {
	out := climateResult{climateP: math.NaN(), pFairClimate: math.NaN()}
	if in.lookup == nil {
		return out
	}
	out.climateP = in.lookup.RangeProb(in.station, in.variable, in.validDate, in.resolution.Lo, in.resolution.Hi)
	if math.IsNaN(out.climateP) {
		return out
	}
	out.pFairClimate = climate.ShrinkP(in.pFairNaive, out.climateP, climate.DefaultShrinkage)
	out.edgeClimateCents = EdgeAfterFeeCentsMode(out.pFairClimate, in.price, in.feeMode)
	out.applied = true
	return out
}

// backtestInputs bundles the inputs scoreBacktestRow reads.
type backtestInputs struct {
	truth         *TruthIndex
	station       string
	validDate     time.Time
	series        string
	isHigh        bool
	resolution    Resolution
	pFair         float64
	price         float64
	tradePrice    int
	feeMode       FeeMode
	entryFilter   string
	edgeThreshold int
}

// backtestResult is the realized-PnL decision for one trade. actualTempF /
// yesResolved describe the settled outcome; ourSide is "yes"/"no"/"" and
// ourPnLCents is the realized PnL after entry fee (0 when ourSide is "").
type backtestResult struct {
	actualTempF int
	yesResolved bool
	ourSide     string
	ourPnLCents int
}

// scoreBacktestRow decides the hypothetical action and its realized PnL,
// pricing the NO leg off a single canonical NO cost (100 − tradePrice) for
// both the entry-edge decision and settlement so the two never diverge by
// the sub-cent 1¢ rounding gap. Returns ok=false when truth is unavailable
// for (station, valid_date) — the caller counts that as "no_truth_available"
// and drops the trade (the "no imaginary bets" rule).
func scoreBacktestRow(in backtestInputs) (backtestResult, bool) {
	truth, ok := in.truth.Get(in.station, in.validDate)
	if !ok {
		return backtestResult{}, false
	}
	actual := truth.TMaxF
	if !in.isHigh {
		actual = truth.TMinF
	}
	out := backtestResult{
		actualTempF: actual,
		yesResolved: in.resolution.Contains(float64(actual)),
	}

	noPriceCents := 100 - in.tradePrice
	fee := FeePerContractCentsMode(in.price, in.feeMode)
	edgeYesCents := int(in.pFair*100+0.5) - in.tradePrice - fee
	edgeNoCents := int((1-in.pFair)*100+0.5) - noPriceCents - fee

	filterAdmits := entryFilterAdmits(in.entryFilter, in.series, in.tradePrice)

	switch {
	case filterAdmits && edgeYesCents >= in.edgeThreshold && edgeYesCents >= edgeNoCents:
		out.ourSide = "yes"
		if out.yesResolved {
			out.ourPnLCents = 100 - in.tradePrice - fee
		} else {
			out.ourPnLCents = -in.tradePrice - fee
		}
	case filterAdmits && edgeNoCents >= in.edgeThreshold:
		out.ourSide = "no"
		if !out.yesResolved {
			out.ourPnLCents = 100 - noPriceCents - fee
		} else {
			out.ourPnLCents = -noPriceCents - fee
		}
	default:
		// no edge OR filter excluded — skip the trade (PnL = 0, ourSide = "")
	}
	return out, true
}

// pollModelSetFromMembers returns the canonical sorted model set for a
// poll, derived from its member-ID prefixes. Bare numeric IDs
// ("00".."30") are GFS (single-model default). Prefixed IDs like
// "ncep_gefs_seamless:NN" / "ecmwf_ifs025_ensemble:NN" canonicalize to
// the short tags mos.ModelGFS / mos.ModelECMWF respectively so they can
// be compared against [mos.Coefficients.FitModelSet] via
// [mos.ModelSetEqual].
//
// [ParsePoll] calls this once per poll-load to precompute [Poll.ModelSet];
// the scan hot path then reads that field directly instead of recomputing
// the set per trade.
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
		if decodeErr := sonic.Unmarshal(env.Payload, &kf); decodeErr != nil {
			return nil //nolint:nilerr // malformed frames are silently skipped here; counted by the main scan loop
		}
		if kf.Type != "trade" {
			return nil
		}
		// The sibling map only needs each unique market_ticker's Spec, so
		// this pass decodes just the ticker (a single-field sub-shape of
		// Msg) rather than the full tradeMsg the main scan loop parses —
		// halving the decode cost of this whole-tape pre-pass. Direction
		// inference still requires the complete sibling set per bucket, so
		// this stays a separate pass ahead of the main scan (folding it in
		// would resolve T-brackets against a partially-built sibling map).
		ticker := kf.MarketTicker()
		if seenTicker[ticker] {
			return nil
		}
		seenTicker[ticker] = true
		spec, parseErr := ParseTicker(ticker)
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

// entryFilters is the registry of named entry predicates. A predicate
// returns true iff it admits an entry on (series, tradePriceCents).
// Registering a filter here is the single step to ship a new one —
// admits logic, the known-name check, and the boot-warning text all
// read this map.
//
// "h4" (Phase 0.5 v4): exclude KXHIGHNY entirely; require either a
// low-temp series (KXLOWT*) OR an extreme-OTM price band (≤ 9¢ or
// ≥ 91¢). See reports/phase0.5/2026-05-19-v4 for the subset-mining +
// LOO-CV evidence behind these conditions.
var entryFilters = map[string]func(series string, tradePriceCents int) bool{
	"":     admitAll,
	"none": admitAll,
	"h4": func(series string, tradePriceCents int) bool {
		if series == "KXHIGHNY" {
			return false
		}
		return strings.HasPrefix(series, "KXLOWT") ||
			tradePriceCents <= 9 || tradePriceCents >= 91
	},
}

func admitAll(string, int) bool { return true }

// entryFilterAdmits returns true iff the named filter admits an entry.
// Unknown filter names admit everything (no-op) so future flag additions
// never accidentally skip every trade — Scan() logs a one-shot warning
// at boot when that fallback will be taken (see isKnownEntryFilter).
func entryFilterAdmits(filter, series string, tradePriceCents int) bool {
	if f, ok := entryFilters[filter]; ok {
		return f(series, tradePriceCents)
	}
	return true
}

// isKnownEntryFilter is the predicate Scan() uses to decide whether to
// warn about an unknown filter name.
func isKnownEntryFilter(filter string) bool {
	_, ok := entryFilters[filter]
	return ok
}

// knownEntryFilterNames renders the registry keys for the boot warning,
// sorted for deterministic log output.
func knownEntryFilterNames() string {
	names := make([]string, 0, len(entryFilters))
	for name := range entryFilters {
		names = append(names, fmt.Sprintf("%q", name))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
