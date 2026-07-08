package edge

import "testing"

// TestTradeMsg_PriceDollars locks in the contract documented on
// priceDollars(): it always returns the executed YES-price, whichever
// wire shape carried the trade and whichever side the taker was on.
func TestTradeMsg_PriceDollars(t *testing.T) {
	tests := []struct {
		name string
		msg  tradeMsg
		want float64
	}{
		{
			name: "int shape, yes-side taker",
			msg:  tradeMsg{YesPrice: 45, NoPrice: 55, Side: "yes"},
			want: 0.45,
		},
		{
			// Regression: the int path used to return the raw NO price
			// (0.55 here) for no-side takers, inverting the YES price.
			name: "int shape, no-side taker still yields the YES price",
			msg:  tradeMsg{YesPrice: 45, NoPrice: 55, Side: "no"},
			want: 0.45,
		},
		{
			name: "int shape, yes-side with only no_price present",
			msg:  tradeMsg{NoPrice: 55, Side: "yes"},
			want: 0.45,
		},
		{
			name: "int shape, no-side with only no_price present",
			msg:  tradeMsg{NoPrice: 55, Side: "no"},
			want: 0.45,
		},
		{
			name: "int shape, unknown side is rejected",
			msg:  tradeMsg{YesPrice: 45, NoPrice: 55},
			want: -1,
		},
		{
			name: "recorded WS shape, yes_price_dollars",
			msg:  tradeMsg{YesPriceDollars: "0.3800", TakerSide: "yes"},
			want: 0.38,
		},
		{
			name: "recorded WS shape, no_price_dollars converts to YES price",
			msg:  tradeMsg{NoPriceDollars: "0.6200", TakerSide: "no"},
			want: 0.38,
		},
		{
			name: "taker_side wins over side",
			msg:  tradeMsg{YesPrice: 45, NoPrice: 55, Side: "bogus", TakerSide: "no"},
			want: 0.45,
		},
		{
			name: "no usable fields",
			msg:  tradeMsg{Side: "yes"},
			want: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.msg.priceDollars()
			if diff := got - tt.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("priceDollars() = %v, want %v", got, tt.want)
			}
		})
	}
}
