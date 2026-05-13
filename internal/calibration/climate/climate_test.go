package climate

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeObsCSV(t *testing.T, path string, rows [][]string) {
	t.Helper()
	f, err := os.Create(path) //nolint:gosec // test fixture writing to t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("valid_date,tmax_f,tmin_f,issued_at,bulletin_id\n"); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := f.WriteString(r[0] + "," + r[1] + "," + r[2] + ",2026-01-01T00:00:00Z,test\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLookupLoadAndBracketProb(t *testing.T) {
	dir := t.TempDir()
	// Synthetic KNYC: in mid-April every year over 5 years, TMAX ranges
	// 60-70°F. Test bracket "above 65" → expect ~50% climate probability.
	rows := [][]string{}
	for year := 2021; year <= 2025; year++ {
		for d := 10; d <= 20; d++ {
			ts := time.Date(year, 4, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
			tmax := 60 + (d - 10) // 60..70
			rows = append(rows, []string{ts, formatInt(tmax), formatInt(tmax - 20)})
		}
	}
	writeObsCSV(t, filepath.Join(dir, "KNYC.csv"), rows)

	l := New()
	if err := l.Load(dir, []string{"KNYC"}); err != nil {
		t.Fatalf("Load: %v", err)
	}

	target, _ := time.Parse("2006-01-02", "2026-04-15")
	// "above 65" should be ~50% (values 65-70 = 6/11 days per year ≈ 0.545)
	p := l.BracketProb("KNYC", "tmax", target, 65, true)
	if math.IsNaN(p) || p < 0.4 || p > 0.65 {
		t.Errorf("P(tmax >= 65) = %v, want ≈0.5 in 0.4-0.65 range", p)
	}

	// "above 75" (above all observed) should be 0
	p = l.BracketProb("KNYC", "tmax", target, 75, true)
	if p != 0 {
		t.Errorf("P(tmax >= 75) = %v, want 0 (none observed above)", p)
	}

	// "below 60" (below all observed) should be ~0 (note: 60 is included in obs)
	p = l.BracketProb("KNYC", "tmax", target, 59, false)
	if p != 0 {
		t.Errorf("P(tmax <= 59) = %v, want 0", p)
	}
}

func TestRangeProb(t *testing.T) {
	dir := t.TempDir()
	rows := [][]string{}
	for year := 2021; year <= 2025; year++ {
		for d := 10; d <= 20; d++ {
			ts := time.Date(year, 4, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
			tmax := 60 + (d - 10) // 60..70
			rows = append(rows, []string{ts, formatInt(tmax), formatInt(tmax - 20)})
		}
	}
	writeObsCSV(t, filepath.Join(dir, "KNYC.csv"), rows)

	l := New()
	if err := l.Load(dir, []string{"KNYC"}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	target, _ := time.Parse("2006-01-02", "2026-04-15")

	if got, want := l.RangeProb("KNYC", "tmax", target, 61.5, 63.5), 2.0/11.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("RangeProb [61.5,63.5) = %v, want %v", got, want)
	}
	if got, want := l.RangeProb("KNYC", "tmax", target, math.Inf(-1), 62), 2.0/11.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("RangeProb [-Inf,62) = %v, want %v", got, want)
	}
	if got, want := l.RangeProb("KNYC", "tmax", target, 70, math.Inf(+1)), 1.0/11.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("RangeProb [70,+Inf) = %v, want %v", got, want)
	}
	if got := l.RangeProb("KNYC", "tmax", target, 70, 60); !math.IsNaN(got) {
		t.Errorf("invalid range = %v, want NaN", got)
	}
}

// TestLoadFailsLoudlyOnMissingColumn proves the audit's P2.5 finding is
// fixed: a CSV without a required column must error, not silently read
// the wrong field. Pre-fix this would have read column 0 (returning the
// zero-value index from a `map[string]int` lookup) and corrupted output.
func TestLoadFailsLoudlyOnMissingColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "KNYC.csv")
	// Write a CSV with valid_date but missing tmax_f.
	f, err := os.Create(path) //nolint:gosec // test fixture writing to t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := f.WriteString("valid_date,wrong_col,tmin_f\n2024-04-15,99,40\n"); werr != nil {
		t.Fatal(werr)
	}
	_ = f.Close()

	l := New()
	if err := l.Load(dir, []string{"KNYC"}); err == nil {
		t.Fatal("expected error on missing tmax_f column, got nil")
	} else {
		if !strings.Contains(err.Error(), "missing required columns") {
			t.Errorf("error should name missing columns; got %v", err)
		}
		if !strings.Contains(err.Error(), "tmax_f") {
			t.Errorf("error should name the specific missing column tmax_f; got %v", err)
		}
	}
}

func TestBracketProbUnknownStationReturnsNaN(t *testing.T) {
	l := New()
	target, _ := time.Parse("2006-01-02", "2025-04-15")
	if got := l.BracketProb("KFOO", "tmax", target, 70, true); !math.IsNaN(got) {
		t.Errorf("unknown station: got %v, want NaN", got)
	}
}

func TestBracketProbSeasonWindow(t *testing.T) {
	// Put observations only in winter; query in summer; expect NaN
	// because no obs in season window.
	dir := t.TempDir()
	rows := [][]string{}
	for d := 1; d <= 20; d++ {
		ts := time.Date(2024, 1, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		rows = append(rows, []string{ts, "40", "30"})
	}
	writeObsCSV(t, filepath.Join(dir, "KNYC.csv"), rows)
	l := New()
	if err := l.Load(dir, []string{"KNYC"}); err != nil {
		t.Fatal(err)
	}

	// Summer query — winter obs are >120 days away on circular calendar
	target, _ := time.Parse("2006-01-02", "2025-07-15")
	p := l.BracketProb("KNYC", "tmax", target, 50, false)
	if !math.IsNaN(p) {
		t.Errorf("summer query, winter-only obs: got %v, want NaN (out of season window)", p)
	}

	// Winter query — should hit all 20 obs
	target, _ = time.Parse("2006-01-02", "2025-01-10")
	p = l.BracketProb("KNYC", "tmax", target, 50, false)
	if math.IsNaN(p) || p != 1.0 {
		t.Errorf("winter query: P(tmax <= 50) = %v, want 1.0 (all 20 obs were 40)", p)
	}
}

func TestShrinkP(t *testing.T) {
	// Standard case: 0.5*0.5 + 0.5*0.9 = 0.7
	if got := ShrinkP(0.9, 0.5, 0.5); math.Abs(got-0.7) > 1e-9 {
		t.Errorf("ShrinkP(0.9, 0.5, 0.5) = %v, want 0.7", got)
	}
	// Default 0.2 weight: 0.2*0.5 + 0.8*1.0 = 0.9
	if got := ShrinkP(1.0, 0.5, 0.2); math.Abs(got-0.9) > 1e-9 {
		t.Errorf("ShrinkP(1.0, 0.5, 0.2) = %v, want 0.9", got)
	}
	// NaN climate → falls back to naive
	if got := ShrinkP(0.7, math.NaN(), 0.2); got != 0.7 {
		t.Errorf("ShrinkP(0.7, NaN, 0.2) = %v, want 0.7", got)
	}
}

func TestCircDist(t *testing.T) {
	// Same day
	if got := circDist(180, 180); got != 0 {
		t.Errorf("circDist(180, 180) = %d, want 0", got)
	}
	// 5 days apart
	if got := circDist(180, 175); got != 5 {
		t.Errorf("circDist(180, 175) = %d, want 5", got)
	}
	// Wrap-around: Jan 1 (1) and Dec 31 (366) should be 1 day apart in circular calendar
	if got := circDist(1, 365); got != 2 {
		t.Errorf("circDist(1, 365) = %d, want 2", got)
	}
}

// TestDayOfYear_Feb29InLeapYear pins Feb 29 (leap years only) to DOY 60
// and asserts it does NOT collide with the same year's March 1 (DOY 61).
// Pre-fix both returned 60, silently aliasing leap-year Feb 29 obs into
// the Mar 1 bucket; this test would fail under that behavior.
func TestDayOfYear_Feb29InLeapYear(t *testing.T) {
	feb29 := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)
	mar1Leap := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	if got := dayOfYear(feb29); got != 60 {
		t.Errorf("dayOfYear(2024-02-29) = %d, want 60", got)
	}
	if got := dayOfYear(mar1Leap); got != 61 {
		t.Errorf("dayOfYear(2024-03-01) = %d, want 61", got)
	}
	if dayOfYear(feb29) == dayOfYear(mar1Leap) {
		t.Errorf("Feb 29 and Mar 1 (leap year) must be distinguishable, both got %d", dayOfYear(feb29))
	}
}

// TestDayOfYear_Mar1InLeapYear pins Mar 1 in a leap year to DOY 61.
// Goes with TestDayOfYear_Mar1InNonLeapYear to demonstrate the year-aware
// (ISO-style) shift around the leap day.
func TestDayOfYear_Mar1InLeapYear(t *testing.T) {
	mar1Leap := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	if got := dayOfYear(mar1Leap); got != 61 {
		t.Errorf("dayOfYear(2024-03-01) = %d, want 61", got)
	}
}

// TestDayOfYear_Mar1InNonLeapYear pins Mar 1 in a non-leap year to DOY 60.
// This is the ISO day-of-year for any year without a Feb 29; the storage
// path uses a leap-year-canonical mapping (Mar 1 → 61) but queries against
// non-leap years return 60, and the ±SeasonWindowDays window covers the
// 1-slot offset.
func TestDayOfYear_Mar1InNonLeapYear(t *testing.T) {
	mar1NonLeap := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	if got := dayOfYear(mar1NonLeap); got != 60 {
		t.Errorf("dayOfYear(2025-03-01) = %d, want 60", got)
	}
	// Spot-check the century edge cases: 2000 is leap (div by 400),
	// 1900 is not (div by 100, not by 400).
	if got := dayOfYear(time.Date(2000, 3, 1, 0, 0, 0, 0, time.UTC)); got != 61 {
		t.Errorf("dayOfYear(2000-03-01) = %d, want 61 (year 2000 is leap)", got)
	}
	if got := dayOfYear(time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC)); got != 60 {
		t.Errorf("dayOfYear(1900-03-01) = %d, want 60 (year 1900 is NOT leap)", got)
	}
}

// TestClimate_Feb29ObsNotAliasedToMar1 proves the silent-collision bug
// is fixed end-to-end: a Feb 29 observation and a Mar 1 observation are
// stored in distinct day-of-year slots, not merged into one.
//
// The ±SeasonWindowDays window means the BracketProb output for Feb 29
// and Mar 1 targets is similar (each window absorbs both obs), so the
// test inspects the post-compile per-day storage table directly — that
// is the surface where the aliasing previously occurred. Pre-fix, both
// obs landed in slot 60; post-fix, Feb 29 is in slot 60 and Mar 1 is
// in slot 61.
func TestClimate_Feb29ObsNotAliasedToMar1(t *testing.T) {
	dir := t.TempDir()
	rows := [][]string{
		{"2024-02-29", "99", "70"}, // distinguishable tmax for Feb 29
		{"2024-03-01", "50", "30"}, // distinguishable tmax for Mar 1
	}
	writeObsCSV(t, filepath.Join(dir, "KNYC.csv"), rows)
	l := New()
	if err := l.Load(dir, []string{"KNYC"}); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Sanity-check the raw obs table — both obs should be retained,
	// keyed by their month-day strings.
	tmaxObs := l.obs["KNYC"]["tmax"]
	if len(tmaxObs) != 2 {
		t.Fatalf("tmax obs count = %d, want 2; obs=%+v", len(tmaxObs), tmaxObs)
	}
	mdSet := map[string]float64{}
	for _, p := range tmaxObs {
		mdSet[p.monthDay] = p.value
	}
	if mdSet["02-29"] != 99 {
		t.Errorf("Feb 29 obs missing or wrong value: got %v, want 99", mdSet["02-29"])
	}
	if mdSet["03-01"] != 50 {
		t.Errorf("Mar 1 obs missing or wrong value: got %v, want 50", mdSet["03-01"])
	}

	// The keying used by compile must place these two obs in different
	// DOY buckets. This is the bug-discriminating assertion: pre-fix,
	// dayOfYearMD returned 60 for both; post-fix, Feb 29 → 60 and
	// Mar 1 → 61.
	feb29DOY := dayOfYearMD("02-29")
	mar1DOY := dayOfYearMD("03-01")
	if feb29DOY == mar1DOY {
		t.Fatalf("Feb 29 and Mar 1 share DOY %d — aliasing not fixed", feb29DOY)
	}
	if feb29DOY != 60 {
		t.Errorf("dayOfYearMD(02-29) = %d, want 60", feb29DOY)
	}
	if mar1DOY != 61 {
		t.Errorf("dayOfYearMD(03-01) = %d, want 61", mar1DOY)
	}

	// End-to-end via the BracketProb surface: a leap-year Feb 29 query
	// (DOY 60) and a leap-year Mar 1 query (DOY 61) both see both obs
	// via the ±30-day window, so each window is [50, 99]. The point of
	// the test is that both queries succeed (no NaN, both obs counted),
	// confirming the per-day table populates correctly for both slots.
	feb29Target, _ := time.Parse("2006-01-02", "2024-02-29")
	pFeb29 := l.BracketProb("KNYC", "tmax", feb29Target, 70, true)
	if math.IsNaN(pFeb29) {
		t.Errorf("Feb 29 BracketProb = NaN, slot 60 unpopulated")
	}
	if pFeb29 != 0.5 {
		t.Errorf("Feb 29 BracketProb(above 70) = %v, want 0.5 (1 of 2 obs ≥ 70)", pFeb29)
	}

	mar1Target, _ := time.Parse("2006-01-02", "2024-03-01")
	pMar1 := l.BracketProb("KNYC", "tmax", mar1Target, 70, true)
	if math.IsNaN(pMar1) {
		t.Errorf("Mar 1 BracketProb = NaN, slot 61 unpopulated")
	}
	if pMar1 != 0.5 {
		t.Errorf("Mar 1 BracketProb(above 70) = %v, want 0.5 (1 of 2 obs ≥ 70)", pMar1)
	}
}

// TestCircDist_LeapYearWrapAround pins circular distance behavior at the
// 366-day cycle boundary so any future refactor that drops back to a
// 365-day cycle (which would re-create the Feb 29 collision risk) fails
// loudly.
func TestCircDist_LeapYearWrapAround(t *testing.T) {
	// Day 366 (Dec 31 in a leap year) and day 1 (Jan 1) are adjacent.
	if got := circDist(366, 1); got != 1 {
		t.Errorf("circDist(366, 1) = %d, want 1 (adjacent across year boundary)", got)
	}
	// Day 366 to day 2 should be 2 days, not duplicating day 1.
	if got := circDist(366, 2); got != 2 {
		t.Errorf("circDist(366, 2) = %d, want 2", got)
	}
	// Reflexive at the boundary.
	if got := circDist(366, 366); got != 0 {
		t.Errorf("circDist(366, 366) = %d, want 0", got)
	}
	// Diametric opposite: day 1 to day 184 is the max distance (183).
	if got := circDist(1, 184); got != 183 {
		t.Errorf("circDist(1, 184) = %d, want 183", got)
	}
	// Feb 29 (60) and Mar 1 leap (61) are 1 day apart, not 0.
	if got := circDist(60, 61); got != 1 {
		t.Errorf("circDist(60, 61) = %d, want 1 (Feb 29 vs Mar 1 distinct)", got)
	}
}

func formatInt(n int) string {
	if n < 0 {
		return "-" + formatInt(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return formatInt(n/10) + string(rune('0'+n%10))
}
