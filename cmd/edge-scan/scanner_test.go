package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/replay"
)

func TestNaiveProbability(t *testing.T) {
	// Members ranging 60..90 evenly spaced; 31 members.
	var members [openmeteo.NumMembers]float64
	for i := range members {
		members[i] = 60.0 + float64(i)*30.0/30.0 // 60, 61, ..., 90
	}

	tests := []struct {
		name      string
		bt        market.BracketType
		threshold float64
		wantP     float64
		wantCount int
	}{
		{"all-above-60", market.BracketAbove, 60, 1.0, 31},
		{"all-below-90", market.BracketBelow, 90, 1.0, 31},
		{"none-above-100", market.BracketAbove, 100, 0.0, 0},
		{"none-below-50", market.BracketBelow, 50, 0.0, 0},
		{"half-above-75", market.BracketAbove, 75, 16.0 / 31.0, 16}, // 75..90 = 16 members
		{"half-below-75", market.BracketBelow, 75, 16.0 / 31.0, 16}, // 60..75 = 16 members
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, c := naiveProbability(members, tt.bt, tt.threshold)
			if math.Abs(p-tt.wantP) > 1e-6 {
				t.Errorf("p = %v, want %v", p, tt.wantP)
			}
			if c != tt.wantCount {
				t.Errorf("count = %d, want %d", c, tt.wantCount)
			}
		})
	}
}

func TestNaiveProbabilityHandlesNaN(t *testing.T) {
	var members [openmeteo.NumMembers]float64
	for i := range members {
		members[i] = math.NaN()
	}
	p, c := naiveProbability(members, market.BracketAbove, 50)
	if p != 0 || c != 0 {
		t.Errorf("all-NaN: p=%v c=%d, want 0,0", p, c)
	}
}

func TestEnsembleRange(t *testing.T) {
	var members [openmeteo.NumMembers]float64
	for i := range members {
		members[i] = float64(i)
	}
	lo, hi := ensembleRange(members)
	if lo != 0 || hi != 30 {
		t.Errorf("range = (%v, %v), want (0, 30)", lo, hi)
	}
}

func TestEnsembleRangeAllInvalid(t *testing.T) {
	var members [openmeteo.NumMembers]float64
	for i := range members {
		members[i] = math.NaN()
	}
	lo, hi := ensembleRange(members)
	if !math.IsInf(lo, 1) || !math.IsInf(hi, -1) {
		t.Errorf("all-NaN range = (%v, %v), want (+Inf, -Inf)", lo, hi)
	}
}

func TestNearestPoll(t *testing.T) {
	mk := func(loc string, wall int64) *pollEntry {
		return &pollEntry{poll: &openmeteo.EnsemblePoll{Wall: wall, Location: loc}, loc: loc, wall: wall}
	}
	polls := []*pollEntry{
		mk("NY", 1000),
		mk("NY", 2000),
		mk("NY", 3000),
		mk("LAX", 1500),
	}

	// Closest to 1900 in NY is 2000.
	p, gap := nearestPoll(polls, "NY", 1900)
	if p == nil || p.Wall != 2000 {
		t.Errorf("closest to 1900: got wall=%v, want 2000", p)
	}
	if gap != 100 {
		t.Errorf("gap = %v, want 100", gap)
	}

	// LAX: only one poll.
	p, gap = nearestPoll(polls, "LAX", 1500)
	if p == nil || p.Wall != 1500 || gap != 0 {
		t.Errorf("LAX exact: got wall=%v gap=%v", p, gap)
	}

	// No matching location.
	p, _ = nearestPoll(polls, "MIA", 1500)
	if p != nil {
		t.Errorf("MIA: expected nil, got %v", p)
	}

	// Empty input.
	p, _ = nearestPoll(nil, "NY", 0)
	if p != nil {
		t.Errorf("empty: expected nil")
	}
}

// constMembers returns a [31]float64 where every member is v.
func constMembers(v float64) [openmeteo.NumMembers]float64 {
	var m [openmeteo.NumMembers]float64
	for i := range m {
		m[i] = v
	}
	return m
}

func TestEdgeFromExtremes_BracketAboveStrongYes(t *testing.T) {
	// All 31 members forecast 80°F. Bracket is "high ≥ 70". Naive P = 1.
	// Market mid = 30¢. Round-trip fee at 30¢ = 2 * ceil(7*0.3*0.7) = 2*ceil(1.47) = 4¢.
	// YES edge = 100 - 30 - 4 = +66. NO edge = 30 - 100 - 4 = -74.
	tk, _ := market.ParseTicker("KXHIGHNY-26APR22-T70")
	row := edgeFromExtremes(tk, 30, 2, constMembers(80), 60*time.Second)
	if row.NaiveP != 1.0 {
		t.Errorf("naive_p = %v, want 1.0", row.NaiveP)
	}
	if row.FeeRoundTripCents != 4 {
		t.Errorf("fee = %d, want 4", row.FeeRoundTripCents)
	}
	if row.YesEdgeCents != 66 {
		t.Errorf("yes_edge = %v, want 66", row.YesEdgeCents)
	}
	if row.NoEdgeCents != -74 {
		t.Errorf("no_edge = %v, want -74", row.NoEdgeCents)
	}
	if row.MembersInBracket != 31 {
		t.Errorf("members = %d, want 31", row.MembersInBracket)
	}
}

func TestEdgeFromExtremes_BracketBelowStrongNo(t *testing.T) {
	// All members forecast 80°F. Bracket "high ≤ 70". Naive P = 0.
	// Market mid = 60¢. Round-trip fee at 60¢ = 2 * ceil(7*0.6*0.4) = 2*ceil(1.68) = 4¢.
	// YES edge = 0 - 60 - 4 = -64. NO edge = 60 - 0 - 4 = +56.
	tk, _ := market.ParseTicker("KXHIGHNY-26APR22-B70")
	row := edgeFromExtremes(tk, 60, 1, constMembers(80), 60*time.Second)
	if row.NaiveP != 0 {
		t.Errorf("naive_p = %v, want 0", row.NaiveP)
	}
	if row.YesEdgeCents != -64 {
		t.Errorf("yes_edge = %v, want -64", row.YesEdgeCents)
	}
	if row.NoEdgeCents != 56 {
		t.Errorf("no_edge = %v, want 56", row.NoEdgeCents)
	}
}

func TestEdgeFromExtremes_FeeSymmetry(t *testing.T) {
	// At any price: yes_edge + no_edge = -2 * round_trip_fee
	// (This is the parabolic-fee identity that makes the market a zero-sum
	// minus fees, regardless of probability disagreement.)
	tk, _ := market.ParseTicker("KXHIGHNY-26APR22-T70")
	for mid := 5; mid <= 95; mid += 10 {
		row := edgeFromExtremes(tk, mid, 1, constMembers(80), 0)
		sum := row.YesEdgeCents + row.NoEdgeCents
		want := -2.0 * float64(row.FeeRoundTripCents)
		if math.Abs(sum-want) > 1e-9 {
			t.Errorf("mid=%d: yes_edge + no_edge = %v, want %v", mid, sum, want)
		}
	}
}

func TestComputeEdge_SkipsBadTicker(t *testing.T) {
	book := mockBook(45, 55)
	if _, ok := computeEdge("NOT-A-TICKER", book, 0, nil, time.Hour); ok {
		t.Error("expected ok=false for unparseable ticker")
	}
}

func TestComputeEdge_SkipsOneSidedBook(t *testing.T) {
	book := replay.NewBook()
	// Only yes side populated → BestNoBid()=0, MidYesCents()=0, ask=100.
	book.Yes[45] = 100
	if _, ok := computeEdge("KXHIGHNY-26APR22-T70", book, 0, nil, time.Hour); ok {
		t.Error("expected ok=false for one-sided book")
	}
}

func TestComputeEdge_SkipsWhenNoPollsForLocation(t *testing.T) {
	book := mockBook(45, 55)
	polls := []*pollEntry{} // empty
	if _, ok := computeEdge("KXHIGHNY-26APR22-T70", book, 0, polls, time.Hour); ok {
		t.Error("expected ok=false when no polls available")
	}
}

// mockBook builds a Book with the given top-of-book yes-bid and no-bid prices
// (in cents). Quantity is arbitrary.
func mockBook(yesBid, noBid int) *replay.Book {
	b := replay.NewBook()
	b.Yes[yesBid] = 100
	b.No[noBid] = 100
	return b
}

func TestEdgeCSVHeaderAndRow(t *testing.T) {
	hdr := edgeCSVHeader()
	if got := len(hdr); got != 17 {
		t.Errorf("header has %d cols, want 17", got)
	}

	tk, _ := market.ParseTicker("KXHIGHNY-26APR22-T70")
	row := edgeFromExtremes(tk, 30, 2, constMembers(80), 60*time.Second)

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(hdr); err != nil {
		t.Fatal(err)
	}
	if err := writeEdgeRow(w, "2026-04-22", row); err != nil {
		t.Fatal(err)
	}
	w.Flush()

	got := buf.String()
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), got)
	}
	if !strings.Contains(lines[1], "KXHIGHNY-26APR22-T70") {
		t.Errorf("row missing ticker: %q", lines[1])
	}
	if !strings.Contains(lines[1], "1.0000") {
		t.Errorf("row missing naive_p=1.0000: %q", lines[1])
	}
	if !strings.Contains(lines[1], ",66.00,") {
		t.Errorf("row missing yes_edge=66.00: %q", lines[1])
	}
}

func TestComputeEdge_HappyPathIntegration(t *testing.T) {
	// Build a synthetic ensemble poll with one full forecast day where the
	// hourly temp climbs 10°C → 25°C. UTC, NY-offset (-4h).
	hours := 48
	times := make([]string, hours)
	temps := make([]float64, hours)
	for i := 0; i < hours; i++ {
		times[i] = jsonTime(2026, 4, 22, i)
		temps[i] = 10.0 + float64(i%24)*15.0/23.0 // 10..25°C cycle each day
	}
	poll := &openmeteo.EnsemblePoll{
		Wall:     1000,
		Location: "NY",
		Times:    times,
	}
	for m := range poll.Members {
		poll.Members[m] = append([]float64{}, temps...)
	}
	pe := &pollEntry{poll: poll, loc: "NY", wall: 1000}

	// Daily max 25°C ≈ 77°F. Bracket "high ≥ 70" → all 31 members above.
	tk, _ := market.ParseTicker("KXHIGHNY-26APR22-T70")
	book := mockBook(45, 55) // mid=45, spread=0
	row, ok := computeEdge(tk.Raw, book, 1000, []*pollEntry{pe}, time.Hour)
	if !ok {
		t.Fatal("computeEdge returned ok=false")
	}
	if row.NaiveP != 1.0 {
		t.Errorf("naive_p = %v, want 1.0 (all members forecast >70°F)", row.NaiveP)
	}
	if row.MembersInBracket != 31 {
		t.Errorf("members = %d, want 31", row.MembersInBracket)
	}
}

// jsonTime formats a UTC timestamp as "YYYY-MM-DDTHH:00".
func jsonTime(y, mo, d, hourOffset int) string {
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC).Add(time.Duration(hourOffset) * time.Hour)
	return t.Format("2006-01-02T15:04")
}

// silence "unused" complaint if json import lands here in future edits
var _ = json.Marshal
