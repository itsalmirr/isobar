package climate

import (
	"math"
	"os"
	"path/filepath"
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

func formatInt(n int) string {
	if n < 0 {
		return "-" + formatInt(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return formatInt(n/10) + string(rune('0'+n%10))
}
