package replay

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBookApplySnapshotInitializesLevels(t *testing.T) {
	b := NewBook()
	msg := json.RawMessage(`{
		"market_ticker":"KXHIGHNY-26APR22-T64",
		"market_id":"m-1",
		"yes_dollars_fp":[["0.4500","100.00"],["0.4200","50.50"]],
		"no_dollars_fp":[["0.5200","75.00"],["0.5500","12.00"]]
	}`)
	require.NoError(t, b.ApplySnapshot(msg))
	require.Equal(t, 100.0, b.Yes[45])
	require.Equal(t, 50.5, b.Yes[42])
	require.Equal(t, 75.0, b.No[52])
	require.Equal(t, 12.0, b.No[55])
	// Top-of-book
	require.Equal(t, 45, b.BestYesBid())
	require.Equal(t, 55, b.BestNoBid())
	require.Equal(t, 45, b.BestYesAsk()) // 100 - 55
	require.Equal(t, 45, b.MidYesCents()) // (45 + 45) / 2
	require.Equal(t, 0, b.SpreadYesCents())
}

func TestBookSnapshotWipesPriorState(t *testing.T) {
	b := NewBook()
	b.Yes[10] = 999
	b.No[20] = 999
	msg := json.RawMessage(`{
		"market_ticker":"X",
		"yes_dollars_fp":[["0.3000","5.00"]],
		"no_dollars_fp":[["0.7000","5.00"]]
	}`)
	require.NoError(t, b.ApplySnapshot(msg))
	require.Len(t, b.Yes, 1)
	require.Len(t, b.No, 1)
	_, hadOld := b.Yes[10]
	require.False(t, hadOld)
}

func TestBookApplyDeltaAddsAndRemoves(t *testing.T) {
	b := NewBook()
	require.NoError(t, b.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.4500","100.00"]],
		"no_dollars_fp":[["0.5200","75.00"]]
	}`)))

	// Add 10 at yes 45 → 110
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{
		"price_dollars":"0.4500","delta_fp":"10.00","side":"yes"
	}`)))
	require.Equal(t, 110.0, b.Yes[45])

	// New level at yes 46
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{
		"price_dollars":"0.4600","delta_fp":"5.00","side":"yes"
	}`)))
	require.Equal(t, 5.0, b.Yes[46])
	require.Equal(t, 46, b.BestYesBid())

	// Negative delta that zeroes a level → removal
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{
		"price_dollars":"0.4600","delta_fp":"-5.00","side":"yes"
	}`)))
	_, present := b.Yes[46]
	require.False(t, present)
	require.Equal(t, 45, b.BestYesBid())

	// Negative delta that overshoots also removes (defensive)
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{
		"price_dollars":"0.5200","delta_fp":"-999.00","side":"no"
	}`)))
	_, present = b.No[52]
	require.False(t, present)
	require.Equal(t, 0, b.BestNoBid())
}

func TestBookDeltaUnknownSideReturnsError(t *testing.T) {
	b := NewBook()
	err := b.ApplyDelta(json.RawMessage(`{"price_dollars":"0.50","delta_fp":"1.00","side":"maybe"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown side")
}

func TestBookDeltaMalformedPriceOrQty(t *testing.T) {
	b := NewBook()
	require.Error(t, b.ApplyDelta(json.RawMessage(`{"price_dollars":"xx","delta_fp":"1.00","side":"yes"}`)))
	require.Error(t, b.ApplyDelta(json.RawMessage(`{"price_dollars":"0.50","delta_fp":"yy","side":"yes"}`)))
}

func TestBookTopOfBookOnEmptySide(t *testing.T) {
	b := NewBook()
	require.Equal(t, 0, b.BestYesBid())
	require.Equal(t, 0, b.BestNoBid())
	require.Equal(t, 100, b.BestYesAsk(), "empty no side → yes can go to 100")
	require.Equal(t, 0, b.MidYesCents(), "one-sided market has undefined mid; report 0")
	require.Equal(t, 100, b.SpreadYesCents())
}

func TestBookMidAndSpreadOnTwoSided(t *testing.T) {
	b := NewBook()
	// Yes bid 40, No bid 55 → Yes ask = 45, spread = 5, mid = 42
	require.NoError(t, b.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.4000","1"]],
		"no_dollars_fp":[["0.5500","1"]]
	}`)))
	require.Equal(t, 40, b.BestYesBid())
	require.Equal(t, 45, b.BestYesAsk())
	require.Equal(t, 42, b.MidYesCents())
	require.Equal(t, 5, b.SpreadYesCents())
}

func TestPriceDollarsToCents(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{"0.0000", 0, false},
		{"0.0100", 1, false},
		{"0.5500", 55, false},
		{"0.9900", 99, false},
		{"1.0000", 100, false},
		{"1.5000", 0, true}, // out of range
		{"-0.10", 0, true},  // negative
		{"abc", 0, true},    // non-numeric
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := priceDollarsToCents(tc.in)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBookRoundTripSnapshotThenDeltas(t *testing.T) {
	// Realistic sequence: snapshot establishes book, then a series
	// of deltas move the top of book. Verifies mid + best bid/ask
	// stay consistent throughout.
	b := NewBook()
	require.NoError(t, b.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.3000","10"],["0.3500","20"],["0.4000","30"]],
		"no_dollars_fp":[["0.5500","10"],["0.5800","20"],["0.6200","30"]]
	}`)))
	require.Equal(t, 40, b.BestYesBid())
	require.Equal(t, 62, b.BestNoBid())
	// Add volume at current top → same top, deeper
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{"price_dollars":"0.4000","delta_fp":"5","side":"yes"}`)))
	require.Equal(t, 35.0, b.Yes[40])
	require.Equal(t, 40, b.BestYesBid())
	// Remove entire top level → best drops to 35
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{"price_dollars":"0.4000","delta_fp":"-35","side":"yes"}`)))
	require.Equal(t, 35, b.BestYesBid())
}
