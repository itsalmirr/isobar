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

func TestParseHelpersHandleEmpty(t *testing.T) {
	require.Equal(t, 0.0, parseFloat(""))
	require.Equal(t, 0, parseInt(""))
}
