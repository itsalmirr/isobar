package edge

import "math"

// KalshiTakerFeeBPS is Kalshi's standard taker fee multiplier:
//
//	fee_cents = ceil(N · 0.07 · price · (1 − price) · 100)
//
// This applies when our order crosses the spread (lifts an ask /
// hits a bid). The taker fee is the default the backtest uses, since
// the recorded trade prints in the tape were all taker fills from our
// observation point.
//
// Source: Kalshi published fee schedule, 2026.
const (
	KalshiTakerFeeBPS = 0.07

	// KalshiMakerFeeBPS is Kalshi's maker fee multiplier when our
	// resting limit order gets taken by a counterparty. Strictly 1/4
	// of the taker fee per the published schedule.
	//
	//	maker_fee_cents = ceil(N · 0.0175 · price · (1 − price) · 100)
	//
	// Maker-only execution is documented in STRATEGY.md as Phase 1+
	// doctrine. The backtest does not simulate fill probability under
	// a maker model (that requires a queue-position simulator — Phase
	// 2 ROADMAP item); when `--maker-fees` is passed to edge-scan,
	// the existing trade prints in the tape are scored *as if* we had
	// filled them as maker (which is an upper bound on real maker
	// PnL — see docs/BACKTEST.md "Pitfalls").
	KalshiMakerFeeBPS = 0.0175
)

// FeeMode selects which fee schedule the scanner applies. Default is
// Taker (the legacy behaviour); --maker-fees on edge-scan switches it
// to Maker for the entire scan.
type FeeMode int

const (
	FeeTaker FeeMode = 0
	FeeMaker FeeMode = 1
)

func (m FeeMode) BPS() float64 {
	if m == FeeMaker {
		return KalshiMakerFeeBPS
	}
	return KalshiTakerFeeBPS
}

func (m FeeMode) String() string {
	if m == FeeMaker {
		return "maker"
	}
	return "taker"
}

// FeePerContractCentsMode returns the per-side, per-contract fee in
// cents that Kalshi charges at fill price priceDollars (0..1) under the
// given fee mode. mode=FeeMaker uses KalshiMakerFeeBPS (1/4 of taker).
//
// Schedule (per Kalshi's published fee page):
//
//	fee_cents = ceil(N · 0.07 · price · (1 − price) · 100)
//
// for N contracts. This function assumes N=1; multiply by quantity
// outside if needed. Note that the fee is *parabolic* — peaks at price
// = 0.50 and is zero at the boundaries, which is exactly why the Phase
// 0.5 entry rule from STRATEGY.md uses a price-aware edge floor.
//
// We deliberately model this with no rounding internally and ceil at
// the end so that micro-priced trades (e.g., 1¢ taker fills) match
// what Kalshi actually billed.
func FeePerContractCentsMode(priceDollars float64, mode FeeMode) int {
	if priceDollars < 0 {
		priceDollars = 0
	}
	if priceDollars > 1 {
		priceDollars = 1
	}
	raw := mode.BPS() * priceDollars * (1 - priceDollars) * 100
	return int(math.Ceil(raw))
}

// RoundTripFeeCentsMode returns the open + close fee assuming both legs
// fill at the same price, under a chosen fee mode. Phase 0 strategy is
// hold-to-expiry; the closing leg is a settlement, which Kalshi *does
// not* charge a fee for. So the round-trip fee is just the entry fee.
//
// We expose this as a separate function so the scanner can swap to a
// genuine round-trip model (e.g. for cancel-and-quote strategies)
// without changing call sites.
func RoundTripFeeCentsMode(priceDollars float64, mode FeeMode) int {
	return FeePerContractCentsMode(priceDollars, mode)
}

// EdgeAfterFeeCentsMode is the naive edge in cents per contract, under
// a chosen fee mode:
//
//	edge = |p_fair_cents − price_cents| − round_trip_fee_cents
//
// Positive value means there's expected positive value after fees at
// this trade price under the model. p_fair and price are rounded to
// integer cents first (Kalshi quotes in 1¢ increments anyway), which
// also avoids IEEE-float artefacts like |0.60 − 0.50| = 9.9999¢.
func EdgeAfterFeeCentsMode(pFair, priceDollars float64, mode FeeMode) int {
	pFairCents := int(math.Round(pFair * 100))
	priceCents := int(math.Round(priceDollars * 100))
	mispricing := pFairCents - priceCents
	if mispricing < 0 {
		mispricing = -mispricing
	}
	return mispricing - RoundTripFeeCentsMode(priceDollars, mode)
}
