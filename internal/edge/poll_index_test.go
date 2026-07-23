package edge

import (
	"encoding/json"
	"testing"
)

// TestTradeMsgPriceDollars pins the three wire shapes priceDollars must
// keep decoding: v2-docs int cents, recorded-WS dollar strings, and the
// legacy bare price_dollars string. The e2e Scan tests only exercise the
// happy path; this table is the contract for the fallback order and the
// (0,1)-exclusive validity band.
func TestTradeMsgPriceDollars(t *testing.T) {
	tests := []struct {
		name string
		msg  tradeMsg
		want float64
	}{
		// v2 int-cents shape.
		{"yes side int price", tradeMsg{YesPrice: 40, TakerSide: "yes"}, 0.40},
		{"no side taker still yields YES price", tradeMsg{NoPrice: 30, TakerSide: "no"}, 0.70},
		{"yes side reconstructed from no", tradeMsg{NoPrice: 30, TakerSide: "yes"}, 0.70},
		{"no side taker keeps yes_price", tradeMsg{YesPrice: 40, TakerSide: "no"}, 0.40},
		{"legacy side field fallback", tradeMsg{YesPrice: 25, Side: "yes"}, 0.25},
		{"int price missing side", tradeMsg{YesPrice: 40}, -1},
		{"int price out of band", tradeMsg{YesPrice: 100, TakerSide: "yes"}, -1},

		// Recorded-WS dollar-string shape.
		{"yes dollars string", tradeMsg{YesPriceDollars: "0.4"}, 0.40},
		{"no dollars string complements", tradeMsg{NoPriceDollars: "0.3"}, 0.70},
		{"yes dollars preferred over no", tradeMsg{YesPriceDollars: "0.4", NoPriceDollars: "0.3"}, 0.40},
		{"dollars zero rejected", tradeMsg{YesPriceDollars: "0"}, -1},
		{"dollars one rejected", tradeMsg{YesPriceDollars: "1"}, -1},
		{"dollars junk rejected", tradeMsg{YesPriceDollars: "x"}, -1},

		// Legacy bare price_dollars raw-JSON string.
		{"legacy price_dollars", tradeMsg{PriceDollars: json.RawMessage(`"0.55"`)}, 0.55},
		{"legacy price_dollars non-string", tradeMsg{PriceDollars: json.RawMessage(`0.55`)}, -1},

		// Int shape wins over strings when both present.
		{"int shape preferred", tradeMsg{YesPrice: 40, TakerSide: "yes", YesPriceDollars: "0.9"}, 0.40},

		{"nothing derivable", tradeMsg{}, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.msg.priceDollars()
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("priceDollars() = %v, want %v", got, tc.want)
			}
		})
	}
}
