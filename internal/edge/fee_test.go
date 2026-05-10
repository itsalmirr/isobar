package edge

import "testing"

func TestFeePerContractCents(t *testing.T) {
	// Kalshi schedule: ceil(0.07 · price · (1-price) · 100). Spot-checked
	// against a few representative prices.
	tests := []struct {
		price   float64
		wantFee int
	}{
		{0.00, 0}, // P(1-P)=0
		{1.00, 0},
		{0.50, 2},  // ceil(0.07·0.25·100) = ceil(1.75) = 2  — peak fee
		{0.10, 1},  // ceil(0.07·0.09·100) = ceil(0.63) = 1
		{0.90, 1},  // mirror of 0.10
		{0.25, 2},  // ceil(0.07·0.1875·100) = ceil(1.3125) = 2
		{0.75, 2},  // mirror of 0.25
		{0.05, 1},  // ceil(0.7·0.0475) = ceil(0.3325) = 1
		{0.95, 1},
		{0.40, 2},  // ceil(0.07·0.24·100) = ceil(1.68) = 2
	}
	for _, tt := range tests {
		got := FeePerContractCents(tt.price)
		if got != tt.wantFee {
			t.Errorf("FeePerContractCents(%.2f) = %d, want %d", tt.price, got, tt.wantFee)
		}
	}
}

func TestFeePerContractCents_ClampsOutOfRange(t *testing.T) {
	if got := FeePerContractCents(-0.5); got != 0 {
		t.Errorf("negative price clamp = %d, want 0", got)
	}
	if got := FeePerContractCents(1.5); got != 0 {
		t.Errorf("price>1 clamp = %d, want 0 (P=1, P(1-P)=0)", got)
	}
}

func TestEdgeAfterFeeCents(t *testing.T) {
	tests := []struct {
		name     string
		pFair    float64
		price    float64
		wantEdge int
	}{
		{
			name:     "10¢ mispricing at peak fee zone",
			pFair:    0.60, price: 0.50,
			// |60 - 50| = 10¢, fee at price 0.5 = 2¢ → edge = 8¢
			wantEdge: 8,
		},
		{
			name:     "negative edge: 1¢ mispricing eaten by fee",
			pFair:    0.50, price: 0.49,
			// |50-49| = 1¢, fee = ceil(0.07·0.49·0.51·100) = ceil(1.7493) = 2¢ → -1¢
			wantEdge: -1,
		},
		{
			name:     "extreme price low fee, big mispricing",
			pFair:    0.30, price: 0.05,
			// |30-5| = 25¢, fee = 1¢ → 24¢
			wantEdge: 24,
		},
		{
			name:     "no mispricing",
			pFair:    0.42, price: 0.42,
			// 0 - fee(0.42) = ceil(0.07·0.42·0.58·100) = ceil(1.7052) = 2 → -2
			wantEdge: -2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EdgeAfterFeeCents(tt.pFair, tt.price)
			if got != tt.wantEdge {
				t.Errorf("EdgeAfterFeeCents(%.2f, %.2f) = %d, want %d",
					tt.pFair, tt.price, got, tt.wantEdge)
			}
		})
	}
}
