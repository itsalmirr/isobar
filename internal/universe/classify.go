package universe

import (
	"fmt"
	"sort"
)

// strikeEpsilon tolerates float jitter when comparing strike bounds.
const strikeEpsilon = 1e-9

// maxTileGap is the largest floor−cap step still treated as contiguous
// tiling. Integer-quantized variables (whole-degree temperatures) tile as
// "≤76" then "≥77" — a gap of exactly 1 that covers every achievable
// outcome. A genuinely continuous variable with the same gap would leave
// (76,77) uncovered, so Exhaustive carries that assumption in its Reason
// and the YES-basket detector must re-verify against the series' rules
// text before going live (proposal 09 §8 settlement-rule checklist).
const maxTileGap = 1.0

// Classify assigns a structural topology to one event. It only considers
// active binary markets; anything else is excluded from the group's legs
// and noted in Reason.
func Classify(e Event) Group {
	g := Group{Event: e, Kind: KindUnknown}

	var legs []Market
	skipped := 0
	for _, m := range e.Markets {
		if m.Status == "active" && m.MarketType == "binary" {
			legs = append(legs, m)
		} else {
			skipped++
		}
	}
	if len(legs) < 2 {
		g.Reason = fmt.Sprintf("only %d active binary markets (%d skipped)", len(legs), skipped)
		return g
	}

	if dir, ok := allOneSided(legs); ok {
		g.Kind = KindLadder
		g.Reason = fmt.Sprintf("%d one-sided %q strikes (%d skipped)", len(legs), dir, skipped)
		return g
	}

	if e.MutuallyExclusive() {
		if len(legs) == 2 {
			g.Kind = KindComplement
		} else {
			g.Kind = KindMutexSet
		}
		g.Exhaustive, g.Reason = tilesRealLine(legs)
		g.Reason = fmt.Sprintf("MECNET, %d legs (%d skipped): %s", len(legs), skipped, g.Reason)
		return g
	}

	g.Reason = fmt.Sprintf("no structure: collateral=%q, %d legs not one-sided uniform (%d skipped)",
		e.CollateralReturnType, len(legs), skipped)
	return g
}

// allOneSided reports whether every leg is a single-bound threshold in the
// same direction.
func allOneSided(legs []Market) (string, bool) {
	dir := ""
	for i := range legs {
		d, ok := legs[i].OneSided()
		if !ok {
			return "", false
		}
		if dir == "" {
			dir = d
		} else if dir != d {
			return "", false
		}
	}
	return dir, true
}

// tilesRealLine checks whether a mutex set's strikes cover the whole line:
// exactly one bottom rung (cap only), contiguous two-sided brackets, and
// exactly one top rung (floor only). Conservative: any doubt → false.
func tilesRealLine(legs []Market) (bool, string) {
	var bottom, top int
	var betweens []Market
	for i := range legs {
		switch dir, ok := legs[i].OneSided(); {
		case ok && dir == "less":
			bottom++
		case ok && dir == "greater":
			top++
		case legs[i].FloorStrike != nil && legs[i].CapStrike != nil:
			betweens = append(betweens, legs[i])
		default:
			return false, fmt.Sprintf("leg %s has no strikes", legs[i].Ticker)
		}
	}
	if bottom != 1 || top != 1 {
		return false, fmt.Sprintf("open ends: %d bottom, %d top (need exactly 1 each)", bottom, top)
	}

	sort.Slice(betweens, func(i, j int) bool { return *betweens[i].FloorStrike < *betweens[j].FloorStrike })

	// Chain: bottom cap → between floors/caps → top floor.
	var bottomCap, topFloor float64
	for i := range legs {
		if dir, ok := legs[i].OneSided(); ok {
			if dir == "less" {
				bottomCap = *legs[i].CapStrike
			} else {
				topFloor = *legs[i].FloorStrike
			}
		}
	}
	prev := bottomCap
	for i := range betweens {
		if !contiguous(prev, *betweens[i].FloorStrike) {
			return false, fmt.Sprintf("gap between cap %v and floor %v", prev, *betweens[i].FloorStrike)
		}
		if *betweens[i].CapStrike < *betweens[i].FloorStrike {
			return false, fmt.Sprintf("inverted bracket %s", betweens[i].Ticker)
		}
		prev = *betweens[i].CapStrike
	}
	if !contiguous(prev, topFloor) {
		return false, fmt.Sprintf("gap between cap %v and top floor %v", prev, topFloor)
	}
	return true, fmt.Sprintf("tiled %v→%v across %d brackets (gap≤%v convention)", bottomCap, topFloor, len(betweens), maxTileGap)
}

// contiguous accepts floor == cap (continuous convention) through
// floor == cap + maxTileGap (integer-quantized convention).
func contiguous(cap, floor float64) bool {
	d := floor - cap
	return d >= -strikeEpsilon && d <= maxTileGap+strikeEpsilon
}

// ClassifyAll maps Classify over a discovery batch, dropping nothing:
// Unknown groups are kept so measurement reports can count what the
// classifier could not place (no silent truncation).
func ClassifyAll(events []Event) []Group {
	out := make([]Group, 0, len(events))
	for _, e := range events {
		out = append(out, Classify(e))
	}
	return out
}
