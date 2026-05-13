package market

import "testing"

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
