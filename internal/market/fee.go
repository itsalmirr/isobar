package market

import "math"

// FeeCents returns the Kalshi fee in cents for one side of a single
// contract at the given price in cents (1–99). Formula: ceil(7 * P * (1-P))
// where P = priceCents/100.
func FeeCents(priceCents int) int {
	p := float64(priceCents) / 100.0
	return int(math.Ceil(7.0 * p * (1.0 - p)))
}

// RoundTripFeeCents returns the total fee for buying and selling one
// contract at the given price.
func RoundTripFeeCents(priceCents int) int {
	return 2 * FeeCents(priceCents)
}
