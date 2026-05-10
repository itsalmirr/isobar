package edge

import "math"

// KalshiFeeBPS is Kalshi's standard event-contract trading fee, in
// basis points of notional, expressed as the multiplier in the
// schedule: fee_per_contract_cents = ceil(0.07 · 1 · P · (1−P) · 100)
// for a single contract at price P (dollars). The 0.07 figure is the
// public-published rate as of 2026; if Kalshi changes it, change here.
const KalshiFeeBPS = 0.07

// FeePerContractCents returns the per-side, per-contract fee in cents
// that Kalshi charges at fill price priceDollars (0..1).
//
// Schedule (per Kalshi's published fee page):
//
//	fee_cents = ceil(N · 0.07 · price · (1 - price) · 100)
//
// for N contracts. This function assumes N=1; multiply by quantity
// outside if needed. Note that the fee is *parabolic* — peaks at price
// = 0.50 and is zero at the boundaries, which is exactly why the Phase
// 0.5 entry rule from STRATEGY.md uses a price-aware edge floor.
//
// We deliberately model this with no rounding internally and ceil at
// the end so that micro-priced trades (e.g., 1¢ taker fills) match
// what Kalshi actually billed.
func FeePerContractCents(priceDollars float64) int {
	if priceDollars < 0 {
		priceDollars = 0
	}
	if priceDollars > 1 {
		priceDollars = 1
	}
	raw := KalshiFeeBPS * priceDollars * (1 - priceDollars) * 100
	return int(math.Ceil(raw))
}

// RoundTripFeeCents returns the open + close fee assuming both legs
// fill at the same price. Phase 0 strategy is hold-to-expiry; the
// closing leg is a settlement, which Kalshi *does not* charge a fee
// for. So the round-trip fee is just the entry fee.
//
// We expose this as a separate function so the scanner can swap to a
// genuine round-trip model (e.g. for cancel-and-quote strategies)
// without changing call sites.
func RoundTripFeeCents(priceDollars float64) int {
	return FeePerContractCents(priceDollars)
}

// EdgeAfterFeeCents is the naive edge in cents per contract:
//
//	edge = |p_fair_cents − price_cents| − round_trip_fee_cents
//
// Positive value means there's expected positive value after fees at
// this trade price under the model. p_fair and price are rounded to
// integer cents first (Kalshi quotes in 1¢ increments anyway), which
// also avoids IEEE-float artefacts like |0.60 − 0.50| = 9.9999¢.
func EdgeAfterFeeCents(pFair, priceDollars float64) int {
	pFairCents := int(math.Round(pFair * 100))
	priceCents := int(math.Round(priceDollars * 100))
	mispricing := pFairCents - priceCents
	if mispricing < 0 {
		mispricing = -mispricing
	}
	return mispricing - RoundTripFeeCents(priceDollars)
}
