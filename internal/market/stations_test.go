package market

import "testing"

func TestAllStationsCanonicalOrder(t *testing.T) {
	got := AllStationCodes()
	want := []string{"KNYC", "KMIA", "KLAX", "KMDW"}
	if len(got) != len(want) {
		t.Fatalf("AllStationCodes length: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AllStationCodes[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStationByCode(t *testing.T) {
	s, ok := StationByCode("KMIA")
	if !ok {
		t.Fatal("KMIA should be present")
	}
	if s.Location != "MIA" {
		t.Errorf("KMIA station = %+v", s)
	}
	if _, ok := StationByCode("KFOO"); ok {
		t.Error("unknown code should return ok=false")
	}
}

// TestStationFieldsAreInternallyConsistent guards against typos in the
// registry: every station's Code must round-trip back through
// StationByCode to the same Code.
func TestStationFieldsAreInternallyConsistent(t *testing.T) {
	for _, s := range AllStations() {
		got, ok := StationByCode(s.Code)
		if !ok || got.Code != s.Code {
			t.Errorf("%s: Code does not round-trip", s.Code)
		}
	}
}

func TestKMDWNotKORD(t *testing.T) {
	// ADR-0007 specifies KMDW (Midway) for Chicago, not KORD (O'Hare).
	// This test is an explicit guard against accidentally reverting.
	chi, ok := StationByCode("KMDW")
	if !ok {
		t.Fatal("KMDW must be present in registry per ADR-0007")
	}
	if chi.Code != "KMDW" {
		t.Errorf("Chicago station = %s, must be KMDW per ADR-0007", chi.Code)
	}
	// Midway coordinates: roughly 41.78, -87.76. O'Hare is 41.97, -87.91.
	// Reject if coords drift toward KORD.
	if chi.Lat > 41.9 {
		t.Errorf("Chicago lat = %v, looks like KORD (41.97). Should be KMDW (41.78)", chi.Lat)
	}
}

// TestStationCoordsAreCanonical pins each station's Lat/Lon to the
// NWS-canonical values documented in audit/chi-blast-radius.md (for
// KMDW) and the corresponding api.weather.gov/stations/<CODE> records
// for the other three. If you must change a value here, also update
// the audit memo and run a full backtest re-keyed off the new coords.
// Tolerance is 0.0001° (≈11 m) — much tighter than the validator
// tolerance, because this test owns the registry contents.
func TestStationCoordsAreCanonical(t *testing.T) {
	const eps = 0.0001
	canon := []struct {
		code     string
		lat, lon float64
	}{
		// KMDW values pulled from audit/chi-blast-radius.md (NWS:
		// 41.78417, -87.75528 → rounded to 4dp = 41.7842, -87.7553).
		{"KMDW", 41.7842, -87.7553},
		{"KNYC", 40.7794, -73.9692}, // Central Park
		{"KMIA", 25.7959, -80.2870}, // Miami International
		{"KLAX", 33.9425, -118.4081},
	}
	for _, want := range canon {
		s, ok := StationByCode(want.code)
		if !ok {
			t.Errorf("%s not present in registry", want.code)
			continue
		}
		if dLat := s.Lat - want.lat; dLat > eps || dLat < -eps {
			t.Errorf("%s.Lat = %v, want %v (Δ=%v > %v)", want.code, s.Lat, want.lat, dLat, eps)
		}
		if dLon := s.Lon - want.lon; dLon > eps || dLon < -eps {
			t.Errorf("%s.Lon = %v, want %v (Δ=%v > %v)", want.code, s.Lon, want.lon, dLon, eps)
		}
	}
}

// TestAllStationsCanonicalReturnsCopy guards against callers mutating
// the registry through a returned slice. AllStationsCanonical must
// return a deep copy of the per-element Station structs (or at least a
// fresh slice header — Station has no pointer fields) so that
// downstream mutation does not corrupt the singleton registry.
func TestAllStationsCanonicalReturnsCopy(t *testing.T) {
	a := AllStationsCanonical()
	if len(a) == 0 {
		t.Fatal("AllStationsCanonical returned empty slice")
	}
	original := a[0].Lat
	a[0].Lat = 999.0
	b := AllStationsCanonical()
	if b[0].Lat != original {
		t.Errorf("mutation through returned slice leaked: b[0].Lat = %v, want %v", b[0].Lat, original)
	}
}
