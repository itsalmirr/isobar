package universe

import (
	"strings"
	"testing"
)

func fp(v float64) *float64 { return &v }

// bracketEvent builds a KXHIGH-style tiled mutex set:
// T75 (less, cap 75) | B75.5 [75,76] | B77.5 [77,78] | T79 (greater, floor 79)
// — the integer-quantized tiling observed live on KXHIGHNY-26JUN10.
func bracketEvent(collateral string) Event {
	return Event{
		EventTicker:          "KXHIGHNY-26JUN10",
		SeriesTicker:         "KXHIGHNY",
		Category:             "Climate and Weather",
		CollateralReturnType: collateral,
		Markets: []Market{
			{Ticker: "KXHIGHNY-26JUN10-T75", Status: "active", MarketType: "binary", StrikeType: "less", CapStrike: fp(75)},
			{Ticker: "KXHIGHNY-26JUN10-B75.5", Status: "active", MarketType: "binary", StrikeType: "between", FloorStrike: fp(75), CapStrike: fp(76)},
			{Ticker: "KXHIGHNY-26JUN10-B77.5", Status: "active", MarketType: "binary", StrikeType: "between", FloorStrike: fp(77), CapStrike: fp(78)},
			{Ticker: "KXHIGHNY-26JUN10-T79", Status: "active", MarketType: "binary", StrikeType: "greater", FloorStrike: fp(79)},
		},
	}
}

func TestClassifyMutexSetExhaustive(t *testing.T) {
	g := Classify(bracketEvent("MECNET"))
	if g.Kind != KindMutexSet {
		t.Fatalf("Kind = %v, want mutex_set (reason: %s)", g.Kind, g.Reason)
	}
	if !g.Exhaustive {
		t.Fatalf("Exhaustive = false, want true (reason: %s)", g.Reason)
	}
}

func TestClassifyMutexSetGapNotExhaustive(t *testing.T) {
	e := bracketEvent("MECNET")
	// Widen one bracket's floor to open a 2-unit hole: cap 76 → floor 78.
	e.Markets[2].FloorStrike = fp(78)
	g := Classify(e)
	if g.Kind != KindMutexSet {
		t.Fatalf("Kind = %v, want mutex_set", g.Kind)
	}
	if g.Exhaustive {
		t.Fatal("Exhaustive = true across a 2-unit strike gap; YES-basket on this set can lose every leg")
	}
	if !strings.Contains(g.Reason, "gap") {
		t.Errorf("Reason = %q, want gap explanation", g.Reason)
	}
}

func TestClassifyMissingTopRungNotExhaustive(t *testing.T) {
	e := bracketEvent("MECNET")
	e.Markets = e.Markets[:3] // drop the "greater" top rung
	g := Classify(e)
	if g.Exhaustive {
		t.Fatal("Exhaustive = true without a top rung")
	}
	if !strings.Contains(g.Reason, "open ends") {
		t.Errorf("Reason = %q, want open-ends explanation", g.Reason)
	}
}

func TestClassifyComplement(t *testing.T) {
	e := Event{
		EventTicker:          "KXGAME-X",
		CollateralReturnType: "MECNET",
		Markets: []Market{
			{Ticker: "A", Status: "active", MarketType: "binary"},
			{Ticker: "B", Status: "active", MarketType: "binary"},
		},
	}
	g := Classify(e)
	if g.Kind != KindComplement {
		t.Fatalf("Kind = %v, want complement (reason: %s)", g.Kind, g.Reason)
	}
	if g.Exhaustive {
		t.Fatal("Exhaustive = true for strikeless complement; must stay conservative")
	}
}

func TestClassifyLadder(t *testing.T) {
	e := Event{
		EventTicker: "KXBTCD-X",
		Markets: []Market{
			{Ticker: "L1", Status: "active", MarketType: "binary", FloorStrike: fp(100000)},
			{Ticker: "L2", Status: "active", MarketType: "binary", FloorStrike: fp(105000)},
			{Ticker: "L3", Status: "active", MarketType: "binary", FloorStrike: fp(110000)},
		},
	}
	g := Classify(e)
	if g.Kind != KindLadder {
		t.Fatalf("Kind = %v, want ladder (reason: %s)", g.Kind, g.Reason)
	}
}

func TestClassifyFiltersInactiveAndNonBinary(t *testing.T) {
	e := bracketEvent("MECNET")
	e.Markets[1].Status = "closed"
	e.Markets[2].MarketType = "scalar"
	g := Classify(e)
	// Two active binaries remain (T75, T79) → complement-sized mutex set,
	// but with a hole where the brackets were: must not be exhaustive.
	if g.Kind != KindComplement {
		t.Fatalf("Kind = %v, want complement after filtering (reason: %s)", g.Kind, g.Reason)
	}
	if g.Exhaustive {
		t.Fatal("Exhaustive = true after dropping interior brackets")
	}
}

func TestClassifyTooFewLegs(t *testing.T) {
	e := Event{
		EventTicker:          "KXONE-X",
		CollateralReturnType: "MECNET",
		Markets:              []Market{{Ticker: "A", Status: "active", MarketType: "binary"}},
	}
	if g := Classify(e); g.Kind != KindUnknown {
		t.Fatalf("Kind = %v, want unknown for single-leg event", g.Kind)
	}
}

func TestClassifyNonMutexTwoSidedIsUnknown(t *testing.T) {
	e := bracketEvent("") // same strikes, no MECNET declaration
	if g := Classify(e); g.Kind != KindUnknown {
		t.Fatalf("Kind = %v, want unknown without exchange mutex declaration (reason: %s)", g.Kind, g.Reason)
	}
}

func TestClassifyAllKeepsUnknown(t *testing.T) {
	groups := ClassifyAll([]Event{bracketEvent("MECNET"), {EventTicker: "EMPTY-X"}})
	if len(groups) != 2 {
		t.Fatalf("len = %d, want 2 (no silent truncation)", len(groups))
	}
	if groups[1].Kind != KindUnknown {
		t.Fatalf("empty event Kind = %v, want unknown", groups[1].Kind)
	}
}
