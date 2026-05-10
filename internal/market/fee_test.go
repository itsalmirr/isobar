package market

import "testing"

func TestFeeCents(t *testing.T) {
	tests := []struct {
		price int
		want  int
	}{
		{50, 2}, // ceil(7 * 0.5 * 0.5) = ceil(1.75) = 2
		{10, 1}, // ceil(7 * 0.1 * 0.9) = ceil(0.63) = 1
		{90, 1}, // symmetric with 10
		{1, 1},  // ceil(7 * 0.01 * 0.99) = ceil(0.0693) = 1
		{99, 1}, // symmetric with 1
		{25, 2}, // ceil(7 * 0.25 * 0.75) = ceil(1.3125) = 2
		{75, 2}, // symmetric with 25
	}
	for _, tt := range tests {
		got := FeeCents(tt.price)
		if got != tt.want {
			t.Errorf("FeeCents(%d) = %d, want %d", tt.price, got, tt.want)
		}
	}
}

func TestRoundTripFeeCents(t *testing.T) {
	if got := RoundTripFeeCents(50); got != 4 {
		t.Errorf("RoundTripFeeCents(50) = %d, want 4", got)
	}
}
