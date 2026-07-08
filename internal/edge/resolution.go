package edge

import (
	"log"
	"math"
	"sync"
	"sync/atomic"
)

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

// resolveUnknownBranchTotal counts the number of times Spec.Resolve()
// was invoked on a Spec whose Kind is neither BracketBetween nor
// BracketThreshold (i.e. BracketUnknown or any future-added kind that
// hasn't been wired into the switch). All ParseTicker code paths that
// would produce such a Spec already return a non-nil error, so the
// only way to reach this branch is via programmatic Spec construction
// — which is fine in tests but a bug in production. The counter is
// exported via ResolveUnknownBranchTotal() so future tooling
// (cmd/analyze, scrape jobs, metrics exporters) can read it.
var resolveUnknownBranchTotal atomic.Uint64

// resolveUnknownWarnOnce gates the first-hit warn log per process, so
// a misuse doesn't flood the log. Pointer-typed so tests can swap it
// out for a fresh Once between cases without copying lock state.
var resolveUnknownWarnOnce = &sync.Once{}

// resolveUnknownLogger is overridable in tests; default is package log.
var resolveUnknownLogger = log.Printf

// ResolveUnknownBranchTotal returns the running count of unresolved-kind
// Spec.Resolve() invocations. Zero in healthy operation. Safe to call
// concurrently. Intended consumers: cmd/analyze post-run summary,
// regression tests, future metrics exporters.
func ResolveUnknownBranchTotal() uint64 {
	return resolveUnknownBranchTotal.Load()
}

// Resolve maps a Spec to its YES-condition temperature range.
//
// Bracket convention (locked in; was previously marked FIXME — now
// closed, see commit history and tests for the evidence below):
//
//   - B<K> with K a half-integer (K.5): 2°F bracket centred on K.
//     B56.5 → [55.5, 57.5)°F, covering integer high ∈ {56, 57}.
//     Exercised by TestResolveBetween / "B56.5 covers integer high 56 and 57".
//   - B<K> with K an integer: 1°F bracket [K, K+1)°F.
//     B48 → [48, 49)°F, integer high == 48.
//     Exercised by TestResolveBetween / "B48 (integer key) covers exactly 48".
//   - T<K>: one-sided threshold. Default ("high-side"), YES iff temp ≥ K.
//     T61 → [61, +Inf).
//     The "low-side" T markets (e.g. KXHIGHNY-T56 = "high < 56") are
//     handled by ResolveWithSiblings, which uses neighbouring B
//     bracket midpoints to detect the direction.
//     Exercised by TestResolveWithSiblings_{LowSide,HighSide}Detected.
//
// Evidence the convention is correct:
//
//  1. The four B-bracket midpoints in a typical Kalshi weather market
//     are spaced 2°F apart (e.g. B56.5, B58.5, B60.5, B62.5), with one
//     T-bracket on each end — exactly tiling the integer range covered
//     by their union when each B<K.5> spans 2°F.
//  2. ResolveWithSiblings detects low-side T-brackets specifically by
//     comparing T-key against the *minimum* B-key, which only works
//     if B-brackets cover a contiguous range above the lowest sibling
//     — i.e. they tile, which requires the 2°F width assumed here.
//  3. Realized-PnL backtest mode in scan.go uses Resolution.Contains
//     against truth.TMaxF / truth.TMinF (NWS CLI integer extremes);
//     YesResolved being right under the existing convention is
//     observable empirically once truth data lands.
//
// First-order invariant for downstream auditors: for a market with
// brackets {B56.5, B58.5, B60.5, B62.5, T56, T64}, market prices across
// all brackets sum to ~100¢ (mutually exclusive and exhaustive). If a
// future Kalshi series violates this, the scanner's poll_index sibling
// pass will surface anomalous resolution coverage gaps — at which point
// re-open this convention.
//
// Unknown Kind handling: see resolveUnknownBranchTotal. The default
// branch is defensive — no production caller can reach it because
// ParseTicker only produces Spec values with Kind ∈ {BracketBetween,
// BracketThreshold}. Programmatic constructions (e.g. tests) that hit
// it get a NaN-bounded Resolution (Contains() always returns false),
// the counter increments, and a one-time warn
// log fires per process so any future regression is observable.
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
	// Unreachable from any production caller (ParseTicker never returns a
	// BracketUnknown-kinded Spec on a non-error path, and the scan loop
	// in scan.go checks parseErr before calling Resolve). Counted +
	// logged so a future regression that breaks this invariant is loud.
	resolveUnknownBranchTotal.Add(1)
	resolveUnknownWarnOnce.Do(func() {
		resolveUnknownLogger("edge: Spec.Resolve() called on unknown Kind=%d (ticker=%q); returning NaN bounds. "+
			"This is a programmer error — every production-reachable Spec comes from ParseTicker, "+
			"which only produces BracketBetween or BracketThreshold.",
			s.Kind, s.Ticker)
	})
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
	// Find the minimum B-bracket key among siblings (excluding spec itself).
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
