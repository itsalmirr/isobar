// Package universe discovers the tradeable Kalshi catalog and classifies
// event groups by structural topology (mutually-exclusive sets, threshold
// ladders, complement pairs) so detectors can target internal-consistency
// mispricings without any per-domain model (ADR-0014, proposal 09 §3-4).
//
// The package deliberately does NOT import internal/kalshi: discovery uses
// only the public unauthenticated REST endpoints, and keeping the import
// graph disjoint lets this package build and test while the WS client is
// under repair. Wiring into cmd/ composes the two at the binary level.
package universe

import "time"

// Market is the slim, universe-level view of one Kalshi market: identity,
// strike topology, and top-of-book. Prices are in cents (float64 because
// Kalshi reports fractional fills and sub-cent fp sizes; dollar strings
// like "0.0200" parse losslessly at 4dp).
type Market struct {
	Ticker      string
	EventTicker string
	Status      string // "active", "closed", ...
	MarketType  string // "binary" is the only type detectors handle today

	// Strike topology. StrikeType is Kalshi's own label ("less",
	// "between", "greater"); when absent it is inferred from which
	// bounds are set. A one-sided market has exactly one bound.
	StrikeType  string
	FloorStrike *float64
	CapStrike   *float64

	YesBidCents float64
	YesAskCents float64
	NoBidCents  float64
	NoAskCents  float64

	Volume24h    float64
	OpenInterest float64
	CloseTime    time.Time
}

// OneSided reports whether the market is a single-bound threshold
// (a ladder rung), and if so which direction: "less" (cap only) or
// "greater" (floor only).
func (m *Market) OneSided() (dir string, ok bool) {
	switch {
	case m.FloorStrike != nil && m.CapStrike == nil:
		return "greater", true
	case m.FloorStrike == nil && m.CapStrike != nil:
		return "less", true
	default:
		return "", false
	}
}

// Event is one Kalshi event with its nested markets.
type Event struct {
	EventTicker  string
	SeriesTicker string
	Category     string
	// CollateralReturnType is Kalshi's collateral-netting mode. "MECNET"
	// (mutually-exclusive collateral netting) is the exchange's own
	// declaration that at most one market in the event resolves YES —
	// the precondition the NO-basket detector relies on. We treat it as
	// authoritative rather than inferring exclusivity from strikes.
	CollateralReturnType string
	Markets              []Market
}

// MutuallyExclusive reports whether the exchange declares at most one
// market in this event can resolve YES.
func (e *Event) MutuallyExclusive() bool { return e.CollateralReturnType == "MECNET" }

// GroupKind classifies an event's structural topology.
type GroupKind int

const (
	// KindUnknown — no detector-relevant structure identified.
	KindUnknown GroupKind = iota
	// KindComplement — exactly two mutually exclusive binaries.
	KindComplement
	// KindMutexSet — three or more mutually exclusive binaries
	// (bracket family). NO-basket arithmetic is valid on any mutex
	// set; YES-basket additionally requires Exhaustive.
	KindMutexSet
	// KindLadder — two or more one-sided thresholds in the same
	// direction (monotone family). Not mutually exclusive.
	KindLadder
)

func (k GroupKind) String() string {
	switch k {
	case KindComplement:
		return "complement"
	case KindMutexSet:
		return "mutex_set"
	case KindLadder:
		return "ladder"
	default:
		return "unknown"
	}
}

// Group is a classified event.
type Group struct {
	Event Event
	Kind  GroupKind
	// Exhaustive means the mutex set's strikes tile the whole real line
	// (a "less" bottom rung, contiguous betweens, a "greater" top rung),
	// so exactly one market resolves YES. Only meaningful for
	// KindMutexSet/KindComplement; conservatively false on any doubt —
	// a YES-basket placed on a non-exhaustive set can lose every leg.
	Exhaustive bool
	// Reason records the classification rationale (or what disqualified
	// the group) so measurement reports are auditable.
	Reason string
}

// SettlementSource names where a series' ground truth comes from.
type SettlementSource struct {
	Name string
	URL  string
}

// SeriesMeta is the per-series metadata detectors and the fee model need.
// FeeType/FeeMultiplier come from the exchange itself (ADR-0014: fee
// parameters are data, not constants — the flat schedule in internal/edge
// is wrong across categories).
type SeriesMeta struct {
	Ticker            string
	Category          string
	FeeType           string // "quadratic" observed on weather series
	FeeMultiplier     float64
	ContractTermsURL  string
	SettlementSources []SettlementSource
}
