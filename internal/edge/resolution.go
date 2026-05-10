package edge

import "math"

// Resolution is the temperature range (°F) under which a bracket's
// market resolves YES, given the integer daily TMAX (or TMIN, for
// KXLOWT* series) reported on the NWS CLI bulletin.
//
// Lo is inclusive, Hi is exclusive: [Lo, Hi). Lo = -math.Inf(-1) means
// open-ended below ("≤ Hi - 1" effectively); Hi = math.Inf(+1) means
// open-ended above.
type Resolution struct {
	Lo float64 // inclusive lower bound, may be -Inf
	Hi float64 // exclusive upper bound, may be +Inf
}

// Contains reports whether an integer temperature observation falls in
// the bracket. Use Float observations directly with [Lo, Hi).
func (r Resolution) Contains(tempF float64) bool {
	return tempF >= r.Lo && tempF < r.Hi
}

// Resolve maps a Spec to its YES-condition temperature range.
//
// Bracket convention (FIXME — verify against a Kalshi market detail
// page before trusting downstream):
//
//   - B<K> with K a half-integer (K.5): 2°F bracket centred on K.
//     B56.5 → [55.5, 57.5)°F, covering integer high ∈ {56, 57}.
//   - B<K> with K an integer: 1°F bracket [K, K+1)°F.
//     B48 → [48, 49)°F, integer high == 48.
//   - T<K>: one-sided threshold. v0 assumption: "high-side", i.e.
//     resolve YES iff temp ≥ K. T61 → [61, +Inf).
//     The "low-side" T markets (e.g. KXHIGHNY-T56 = "high < 56") are
//     handled by ResolveWithSiblings, which uses neighbouring B
//     bracket midpoints to detect the direction.
//
// The first-order sanity check on this convention: for a market with
// brackets {B56.5, B58.5, B60.5, B62.5, T56-or-T64}, market prices
// across all brackets should sum to ~100¢ (mutually exclusive and
// exhaustive). If they don't, the convention is wrong and the scanner
// should warn.
func (s Spec) Resolve() Resolution {
	switch s.Kind {
	case BracketBetween:
		// Half-integer key → 2°F bracket; integer key → 1°F bracket.
		if isHalfInteger(s.Key) {
			return Resolution{Lo: s.Key - 1, Hi: s.Key + 1}
		}
		return Resolution{Lo: s.Key, Hi: s.Key + 1}
	case BracketThreshold:
		// Default: high-side threshold. ResolveWithSiblings can flip it.
		return Resolution{Lo: s.Key, Hi: math.Inf(+1)}
	}
	return Resolution{Lo: math.NaN(), Hi: math.NaN()}
}

// ResolveWithSiblings returns the Resolution for spec using the sibling
// bracket Specs to disambiguate T-bracket direction. The caller may pass
// any list of Specs; this function defensively filters to siblings in
// the same series and on the same valid_date so that a caller mistakenly
// passing tickers from a different market can't mis-resolve the bracket.
//
// If no qualifying siblings remain (or all are also T), falls back to
// the high-side default and the caller should treat the result with
// FIXME-level scepticism.
//
// Disambiguation rule: a T-bracket is "low-side" iff its key is ≤ the
// lowest sibling B-bracket midpoint. Otherwise it is "high-side"
// (default).
func ResolveWithSiblings(spec Spec, siblings []Spec) Resolution {
	if spec.Kind != BracketThreshold {
		return spec.Resolve()
	}
	// Find min B-bracket key among siblings in the same series and
	// valid_date, excluding spec itself by ticker identity.
	minB := math.Inf(+1)
	for _, sib := range siblings {
		if sib.Kind != BracketBetween {
			continue
		}
		if sib.Series != spec.Series || !sib.ValidDate.Equal(spec.ValidDate) {
			continue
		}
		if sib.Ticker == spec.Ticker {
			continue
		}
		if sib.Key < minB {
			minB = sib.Key
		}
	}
	if math.IsInf(minB, +1) {
		// No B-bracket siblings — keep default.
		return spec.Resolve()
	}
	if spec.Key <= minB {
		// Low-side threshold: high < spec.Key.
		return Resolution{Lo: math.Inf(-1), Hi: spec.Key}
	}
	// High-side: high ≥ spec.Key.
	return Resolution{Lo: spec.Key, Hi: math.Inf(+1)}
}

// isHalfInteger reports whether x is K.5 for some integer K.
func isHalfInteger(x float64) bool {
	frac := x - math.Floor(x)
	return math.Abs(frac-0.5) < 1e-9
}
