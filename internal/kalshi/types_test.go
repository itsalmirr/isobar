package kalshi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarketAccessors(t *testing.T) {
	m := Market{
		YesBidDollars:    "0.42",
		YesAskDollars:    "0.45",
		NoBidDollars:     "0.55",
		NoAskDollars:     "0.58",
		LastPriceDollars: "0.50",
		VolumeFP:         "1250.00",
		Volume24hFP:      "800.00",
		OpenInterestFP:   "300.00",
	}
	require.InDelta(t, 0.42, m.YesBid(), 1e-9)
	require.InDelta(t, 0.45, m.YesAsk(), 1e-9)
	require.InDelta(t, 0.55, m.NoBid(), 1e-9)
	require.InDelta(t, 0.58, m.NoAsk(), 1e-9)
	require.InDelta(t, 0.50, m.LastPrice(), 1e-9)
	require.Equal(t, 1250, m.Volume())
	require.Equal(t, 800, m.Volume24H())
	require.Equal(t, 300, m.OpenInterest())
}

func TestMarketAccessorsHandleEmpty(t *testing.T) {
	m := Market{}
	require.Equal(t, 0.0, m.YesBid())
	require.Equal(t, 0, m.Volume())
}

func TestOrderYesPriceCentsPrefersFP(t *testing.T) {
	o := Order{YesPrice: 40, YesPriceDollars: "0.55"}
	require.Equal(t, 55, o.YesPriceCents())
	// Falls back to legacy int when FP absent.
	o2 := Order{YesPrice: 40}
	require.Equal(t, 40, o2.YesPriceCents())
}

func TestOrderTotalFillCount(t *testing.T) {
	// Prefer FillCountFP.
	o := Order{MakerFillCount: 1, TakerFillCount: 2, FillCountFP: "10"}
	require.Equal(t, 10, o.TotalFillCount())

	// Otherwise maker+taker FP.
	o = Order{MakerFillCountFP: "3", TakerFillCountFP: "4"}
	require.Equal(t, 7, o.TotalFillCount())

	// Otherwise legacy ints.
	o = Order{MakerFillCount: 2, TakerFillCount: 5}
	require.Equal(t, 7, o.TotalFillCount())
}

func TestOrderRemainingQty(t *testing.T) {
	o := Order{RemainingCount: 3, RemainingCountFP: "9"}
	require.Equal(t, 9, o.RemainingQty())
	o2 := Order{RemainingCount: 3}
	require.Equal(t, 3, o2.RemainingQty())
}

func TestFillAccessors(t *testing.T) {
	f := Fill{YesPrice: 30, YesPriceDollars: "0.45", Count: 2, CountFP: "7"}
	require.Equal(t, 45, f.YesPriceCents())
	require.Equal(t, 7, f.Qty())

	// Fallback paths.
	f2 := Fill{YesPrice: 30, Count: 2}
	require.Equal(t, 30, f2.YesPriceCents())
	require.Equal(t, 2, f2.Qty())
}

func TestFPLevelsToIntDropsInvalid(t *testing.T) {
	levels := []OrderbookLevelFP{
		{"0.55", "100"},    // valid → [55, 100]
		{"0.00", "999"},    // edge: 0 cents excluded
		{"1.00", "50"},     // 100 cents excluded
		{"not-a-num", "5"}, // parse fail
		{"0.25", "not"},    // qty parse fail
		{"0.33", "12.5"},   // qty float → 12
	}
	got := fpLevelsToInt(levels)
	require.Len(t, got, 2)
	require.Equal(t, OrderbookLevel{55, 100}, got[0])
	require.Equal(t, OrderbookLevel{33, 12}, got[1])
}

func TestParseHelpersHandleEmpty(t *testing.T) {
	require.Equal(t, 0.0, parseFloat(""))
	require.Equal(t, 0, parseInt(""))
	require.Equal(t, 0, parseFP(""))
	require.Equal(t, 0, parseFPCents(""))
}

func TestParseFPRoundsHalfUp(t *testing.T) {
	// 0.4 rounds to 0 under naive int-cast; parseFP uses +0.5 semantics so
	// values near integers don't surprise callers expecting banker's rules.
	require.Equal(t, 1, parseFP("0.5"))
	require.Equal(t, 10, parseFP("10.4"))
	require.Equal(t, 11, parseFP("10.5"))
}

func TestParseFPCentsRoundsHalfUp(t *testing.T) {
	require.Equal(t, 55, parseFPCents("0.55"))
	require.Equal(t, 33, parseFPCents("0.334"))
	require.Equal(t, 34, parseFPCents("0.335"))
}
