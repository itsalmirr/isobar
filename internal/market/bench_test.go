package market

import "testing"

// BenchmarkParseTicker measures the cost of decoding a Kalshi weather
// ticker. Called once per emitted edge-scan row, so n × per-scan.
func BenchmarkParseTicker(b *testing.B) {
	raw := "KXHIGHNY-26APR22-T64"
	b.ResetTimer()
	for range b.N {
		_, _ = ParseTicker(raw)
	}
}

// BenchmarkFeeCents measures the trivial fee calculation. Called once
// per edge-scan row.
func BenchmarkFeeCents(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		_ = FeeCents(45)
	}
}

// BenchmarkRoundTripFeeCents — same plus the doubling.
func BenchmarkRoundTripFeeCents(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		_ = RoundTripFeeCents(45)
	}
}

// BenchmarkStationBySeries measures the linear scan of the registry.
// Called twice per row (for MOS lookup and for climate lookup); with 4
// stations the scan is trivial. If the registry grows past ~16 stations
// switch to a hash map.
func BenchmarkStationBySeries(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		_, _ = StationBySeries("KXHIGHNY")
	}
}

// BenchmarkStationByLocation — same shape, different key type.
func BenchmarkStationByLocation(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		_, _ = StationByLocation("NY")
	}
}
