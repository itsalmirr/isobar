package replay

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKalshiFrameMarketTicker(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			"orderbook_delta with ticker",
			`{"type":"orderbook_delta","sid":1,"seq":42,"msg":{"market_ticker":"KXHIGHNY-26APR22-T64"}}`,
			"KXHIGHNY-26APR22-T64",
		},
		{
			"orderbook_snapshot with ticker",
			`{"type":"orderbook_snapshot","sid":1,"seq":1,"msg":{"market_ticker":"KXLOWTMIA-26APR22-T66"}}`,
			"KXLOWTMIA-26APR22-T66",
		},
		{
			"subscribed envelope has no market",
			`{"type":"subscribed","id":1,"msg":{"channel":"orderbook_delta","sid":1}}`,
			"",
		},
		{
			"error frame has no market",
			`{"type":"error","id":1,"msg":{"code":8,"msg":"Unknown channel name"}}`,
			"",
		},
		{
			"no msg at all",
			`{"type":"ping"}`,
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f KalshiFrame
			require.NoError(t, json.Unmarshal([]byte(tc.payload), &f))
			require.Equal(t, tc.want, f.MarketTicker())
		})
	}
}

func TestSeriesPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"KXHIGHNY-26APR22-T64", "KXHIGHNY"},
		{"KXLOWTNYC-26APR22-B48", "KXLOWTNYC"},
		{"KXHIGHMIA-26APR22-B81.5", "KXHIGHMIA"},
		{"KXBTCD-26APR1814-T66099.99", "KXBTCD"},
		{"KXHIGHCHI", "KXHIGHCHI"}, // no suffix at all
		{"-only-suffix", ""},       // leading non-letter
		{"", ""},                   // empty
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, SeriesPrefix(tc.in))
		})
	}
}
