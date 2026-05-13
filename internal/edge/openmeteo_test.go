package edge

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// synthPoll builds a minimal-but-realistic Open-Meteo ensemble JSON:
// 72 hourly samples (3 days), one control series + two member series.
// Temperatures are deterministic so test math is exact.
func synthPoll(t *testing.T) []byte {
	t.Helper()
	t0, _ := time.Parse("2006-01-02T15:04", "2026-04-29T00:00")
	times := make([]string, 72)
	for i := 0; i < 72; i++ {
		times[i] = t0.Add(time.Duration(i) * time.Hour).Format("2006-01-02T15:04")
	}

	control := make([]float64, 72)
	mem01 := make([]float64, 72)
	mem02 := make([]float64, 72)
	for i := range control {
		control[i] = float64(i)
		mem01[i] = float64(i) + 1
		mem02[i] = float64(i) + 2
	}

	body := map[string]interface{}{
		"latitude":  40.78,
		"longitude": -73.97,
		"hourly": map[string]interface{}{
			"time":                    times,
			"temperature_2m":          control,
			"temperature_2m_member01": mem01,
			"temperature_2m_member02": mem02,
			"precipitation":           make([]float64, 72),
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("synthPoll marshal: %v", err)
	}
	return raw
}

func TestParsePoll_Synthetic(t *testing.T) {
	p, err := ParsePoll(synthPoll(t))
	if err != nil {
		t.Fatalf("ParsePoll: %v", err)
	}
	if p.MemberCount() != 3 {
		t.Errorf("MemberCount = %d, want 3", p.MemberCount())
	}
	if got := strings.Join(p.MemberIDs(), ","); got != "00,01,02" {
		t.Errorf("MemberIDs = %s, want 00,01,02", got)
	}
	if len(p.HourlyTimes) != 72 {
		t.Errorf("HourlyTimes len = %d, want 72", len(p.HourlyTimes))
	}
	if p.HourlyTimes[0].UTC().Format(time.RFC3339) != "2026-04-29T00:00:00Z" {
		t.Errorf("first time = %v, want 2026-04-29T00:00:00Z", p.HourlyTimes[0])
	}
}

func TestParsePoll_RejectsMissingHourly(t *testing.T) {
	_, err := ParsePoll([]byte(`{"latitude":40,"longitude":-73}`))
	if err == nil {
		t.Fatal("want error for missing hourly")
	}
}

func TestParsePoll_RejectsEmptyMembers(t *testing.T) {
	_, err := ParsePoll([]byte(`{"hourly":{"time":["2026-04-29T00:00"],"precipitation":[0]}}`))
	if err == nil || !strings.Contains(err.Error(), "no temperature_2m members") {
		t.Fatalf("err = %v, want 'no temperature_2m members'", err)
	}
}

func TestDailyExtremesF_ControlHourLine(t *testing.T) {
	// Civil date 2026-04-29 in America/New_York (EDT, UTC-4) → window
	// [Apr 29 04:00Z, Apr 30 04:00Z). Hour indices 4..27 inclusive..exclusive,
	// so control values 4..27. Max=27°C=80.6°F, Min=4°C=39.2°F.
	p, err := ParsePoll(synthPoll(t))
	if err != nil {
		t.Fatalf("ParsePoll: %v", err)
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York unavailable: %v", err)
	}
	civil := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	out, err := p.DailyExtremesF(civil, loc)
	if err != nil {
		t.Fatalf("DailyExtremesF: %v", err)
	}
	got := out["00"]
	if !approxF(got.TMaxF, 80.6) || !approxF(got.TMinF, 39.2) {
		t.Errorf("control = (max=%.2f, min=%.2f), want (80.6, 39.2)", got.TMaxF, got.TMinF)
	}
	if got.N != 24 {
		t.Errorf("control N = %d, want 24", got.N)
	}
	got01 := out["01"]
	if !approxF(got01.TMaxF, 82.4) || !approxF(got01.TMinF, 41.0) {
		t.Errorf("mem01 = (max=%.2f, min=%.2f), want (82.4, 41.0)", got01.TMaxF, got01.TMinF)
	}
}

func TestDailyExtremesF_InsufficientCoverage(t *testing.T) {
	p, _ := ParsePoll(synthPoll(t))
	loc, _ := time.LoadLocation("America/New_York")
	_, err := p.DailyExtremesF(time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC), loc)
	if !errors.Is(err, ErrInsufficientCoverage) {
		t.Fatalf("err = %v, want ErrInsufficientCoverage", err)
	}
}

// TestDailyExtremesF_PartialMemberSkip exercises the per-member coverage
// rule: a poll where one of three members has <12 in-window samples must
// surface the other two (not abort the whole poll). The "skipped" count
// is surfaced via DailyExtremesFWithSkip.
//
// Build the poll directly (bypass JSON parsing — encoding/json refuses
// NaN) where member 01 has only 6 valid in-window samples while 00 and
// 02 are fully populated. Earlier behaviour: ErrInsufficientCoverage for
// the whole poll. New behaviour: members 00 and 02 returned, skipped=1.
func TestDailyExtremesF_PartialMemberSkip(t *testing.T) {
	t0, _ := time.Parse("2006-01-02T15:04", "2026-04-29T00:00")
	times := make([]time.Time, 72)
	for i := range times {
		times[i] = t0.Add(time.Duration(i) * time.Hour)
	}
	control := make([]float64, 72)
	mem01 := make([]float64, 72) // intentionally sparse
	mem02 := make([]float64, 72)
	for i := 0; i < 72; i++ {
		control[i] = float64(i)
		mem02[i] = float64(i) + 2
		mem01[i] = math.NaN()
	}
	// Only 6 valid samples for member 01 inside the in-window range
	// (hours 4..27 UTC for EDT civil 2026-04-29) — below the 12-sample floor.
	for _, ok := range []int{4, 5, 6, 7, 8, 9} {
		mem01[ok] = float64(ok) + 1
	}

	p := &Poll{
		Latitude:    40.78,
		Longitude:   -73.97,
		HourlyTimes: times,
		Members: map[string][]float64{
			"00": control,
			"01": mem01,
			"02": mem02,
		},
	}
	loc, _ := time.LoadLocation("America/New_York")
	civil := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	out, skipped, err := p.DailyExtremesFWithSkip(civil, loc)
	if err != nil {
		t.Fatalf("DailyExtremesFWithSkip: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (member 01 below %d-sample floor)",
			skipped, MinHourlySamplesPerMember)
	}
	if len(out) != 2 {
		t.Errorf("len(out) = %d, want 2 (surviving members 00 and 02)", len(out))
	}
	if _, ok := out["01"]; ok {
		t.Errorf("member 01 must have been dropped for partial coverage")
	}
	if _, ok := out["00"]; !ok {
		t.Errorf("control member 00 missing — should survive")
	}
	if _, ok := out["02"]; !ok {
		t.Errorf("member 02 missing — should survive")
	}
}

// TestDailyExtremesF_AllMembersSparseStillErrors asserts that the "skip
// sparse member" change preserves the original failure mode when every
// member is sparse — i.e. the poll really is unusable.
func TestDailyExtremesF_AllMembersSparseStillErrors(t *testing.T) {
	p, _ := ParsePoll(synthPoll(t))
	loc, _ := time.LoadLocation("America/New_York")
	// Apr 26 is entirely before the synthetic poll's window — every
	// member has 0 in-window samples → every member dropped → error.
	_, skipped, err := p.DailyExtremesFWithSkip(
		time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC), loc)
	if !errors.Is(err, ErrInsufficientCoverage) {
		t.Fatalf("err = %v, want ErrInsufficientCoverage", err)
	}
	if skipped != p.MemberCount() {
		t.Errorf("skipped = %d, want %d (all members)", skipped, p.MemberCount())
	}
}

func TestMatchTemperatureMember(t *testing.T) {
	cases := map[string]struct {
		id string
		ok bool
	}{
		"temperature_2m":          {"00", true},
		"temperature_2m_member01": {"01", true},
		"temperature_2m_member30": {"30", true},
		"temperature_2m_max":      {"", false},
		"precipitation":           {"", false},
		"temperature_2m_member1":  {"", false},
		"temperature_2m_memberAB": {"", false},

		// Multi-model: ?models=A,B suffixes every series with the model tag.
		"temperature_2m_ncep_gefs_seamless":             {"ncep_gefs_seamless:00", true},
		"temperature_2m_member01_ncep_gefs_seamless":    {"ncep_gefs_seamless:01", true},
		"temperature_2m_member30_ncep_gefs_seamless":    {"ncep_gefs_seamless:30", true},
		"temperature_2m_ecmwf_ifs025_ensemble":          {"ecmwf_ifs025_ensemble:00", true},
		"temperature_2m_member50_ecmwf_ifs025_ensemble": {"ecmwf_ifs025_ensemble:50", true},
		// Aggregators stay rejected even with model suffix.
		"temperature_2m_max_member01_ecmwf_ifs025_ensemble": {"", false},
		"temperature_2m_max_ecmwf_ifs025_ensemble":          {"", false},
	}
	for in, want := range cases {
		gotID, gotOK := matchTemperatureMember(in)
		if gotID != want.id || gotOK != want.ok {
			t.Errorf("matchTemperatureMember(%q) = (%q,%v), want (%q,%v)",
				in, gotID, gotOK, want.id, want.ok)
		}
	}
}

func approxF(a, b float64) bool { return math.Abs(a-b) < 0.05 }
