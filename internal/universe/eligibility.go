package universe

import "sort"

// Gate is the fail-closed NY-eligibility allowlist (ADR-0014; RISK.md
// compliance posture). A series passes only if the operator explicitly
// confirmed Kalshi serves it to NY retail and added it to the allowlist.
// Discovery never auto-enables anything: the public catalog carries no
// per-state eligibility signal, so the absence of evidence must read as
// ineligible.
type Gate struct {
	allowed map[string]struct{}
}

// NewGate builds a gate from operator-confirmed series tickers.
func NewGate(confirmedSeries []string) *Gate {
	g := &Gate{allowed: make(map[string]struct{}, len(confirmedSeries))}
	for _, s := range confirmedSeries {
		if s != "" {
			g.allowed[s] = struct{}{}
		}
	}
	return g
}

// Allowed reports whether a series is operator-confirmed.
func (g *Gate) Allowed(seriesTicker string) bool {
	_, ok := g.allowed[seriesTicker]
	return ok
}

// Dropped records one group excluded by the gate, with the series that
// caused the exclusion (for the measurement report's no-silent-caps rule).
type Dropped struct {
	EventTicker  string
	SeriesTicker string
	Reason       string
}

// Filter splits groups into gate-passing and dropped. Order is preserved
// for kept; dropped is sorted by series for stable report output.
func (g *Gate) Filter(groups []Group) (kept []Group, dropped []Dropped) {
	for _, grp := range groups {
		if g.Allowed(grp.Event.SeriesTicker) {
			kept = append(kept, grp)
			continue
		}
		dropped = append(dropped, Dropped{
			EventTicker:  grp.Event.EventTicker,
			SeriesTicker: grp.Event.SeriesTicker,
			Reason:       "series not operator-confirmed NY-eligible",
		})
	}
	sort.Slice(dropped, func(i, j int) bool {
		if dropped[i].SeriesTicker != dropped[j].SeriesTicker {
			return dropped[i].SeriesTicker < dropped[j].SeriesTicker
		}
		return dropped[i].EventTicker < dropped[j].EventTicker
	})
	return kept, dropped
}
