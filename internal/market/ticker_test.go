package market

import (
	"testing"
	"time"
)

func TestParseTicker(t *testing.T) {
	tests := []struct {
		raw     string
		series  string
		date    time.Time
		bt      BracketType
		tempF   float64
		tv      TempVar
		loc     string
		wantErr bool
	}{
		{
			raw:    "KXHIGHNY-26APR22-T64",
			series: "KXHIGHNY", date: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
			bt: BracketAbove, tempF: 64, tv: DailyHigh, loc: "NY",
		},
		{
			raw:    "KXHIGHLAX-26APR22-B70.5",
			series: "KXHIGHLAX", date: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
			bt: BracketBelow, tempF: 70.5, tv: DailyHigh, loc: "LAX",
		},
		{
			raw:    "KXLOWTCHI-26APR22-B50.5",
			series: "KXLOWTCHI", date: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
			bt: BracketBelow, tempF: 50.5, tv: DailyLow, loc: "CHI",
		},
		{
			raw:    "KXLOWTNYC-26APR22-B45.5",
			series: "KXLOWTNYC", date: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
			bt: BracketBelow, tempF: 45.5, tv: DailyLow, loc: "NY",
		},
		{
			raw:    "KXHIGHCHI-26APR22-T76",
			series: "KXHIGHCHI", date: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
			bt: BracketAbove, tempF: 76, tv: DailyHigh, loc: "CHI",
		},
		{
			raw:    "KXHIGHMIA-26JAN05-T80",
			series: "KXHIGHMIA", date: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
			bt: BracketAbove, tempF: 80, tv: DailyHigh, loc: "MIA",
		},
		{raw: "BADTICKER", wantErr: true},
		{raw: "KXFOO-26APR22-T64", wantErr: true},
		{raw: "KXHIGHNY-26XXX22-T64", wantErr: true},
		{raw: "KXHIGHNY-26APR22-X64", wantErr: true},
	}
	for _, tt := range tests {
		tk, err := ParseTicker(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseTicker(%q) wanted error, got nil", tt.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTicker(%q) unexpected error: %v", tt.raw, err)
			continue
		}
		if tk.Series != tt.series {
			t.Errorf("%s: series = %q, want %q", tt.raw, tk.Series, tt.series)
		}
		if !tk.Date.Equal(tt.date) {
			t.Errorf("%s: date = %v, want %v", tt.raw, tk.Date, tt.date)
		}
		if tk.Type != tt.bt {
			t.Errorf("%s: type = %v, want %v", tt.raw, tk.Type, tt.bt)
		}
		if tk.TempF != tt.tempF {
			t.Errorf("%s: tempF = %v, want %v", tt.raw, tk.TempF, tt.tempF)
		}
		if tk.TempVar != tt.tv {
			t.Errorf("%s: tempVar = %v, want %v", tt.raw, tk.TempVar, tt.tv)
		}
		if tk.Location() != tt.loc {
			t.Errorf("%s: location = %q, want %q", tt.raw, tk.Location(), tt.loc)
		}
	}
}

func TestUTCOffset(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{"KXHIGHNY-26APR22-T64", -4},  // EDT
		{"KXHIGHMIA-26APR22-T80", -4}, // EDT
		{"KXHIGHCHI-26APR22-T70", -5}, // CDT
		{"KXHIGHLAX-26APR22-T70", -7}, // PDT
		{"KXLOWTNYC-26APR22-B40", -4},
		{"KXLOWTCHI-26APR22-B45", -5},
	}
	for _, tt := range tests {
		tk, err := ParseTicker(tt.raw)
		if err != nil {
			t.Fatalf("ParseTicker(%q): %v", tt.raw, err)
		}
		if got := tk.UTCOffset(); got != tt.want {
			t.Errorf("%s: UTCOffset = %d, want %d", tt.raw, got, tt.want)
		}
	}
}

func TestBracketTypeString(t *testing.T) {
	if got := BracketAbove.String(); got != "above" {
		t.Errorf("BracketAbove.String() = %q, want %q", got, "above")
	}
	if got := BracketBelow.String(); got != "below" {
		t.Errorf("BracketBelow.String() = %q, want %q", got, "below")
	}
}

func TestParseTickerEdgeCases(t *testing.T) {
	cases := []string{
		"",                      // empty
		"KXHIGHNY",              // only series
		"KXHIGHNY-26APR22",      // missing bracket
		"KXHIGHNY-X-T64",        // bad date
		"KXHIGHNY-26APR22-",     // empty bracket
		"KXHIGHNY-26APR22-T",    // missing temp
		"KXHIGHNY-26APR22-Tabc", // non-numeric temp
		"KXHIGHNY-2APR22-T64",   // short year
		"KXHIGHNY-26APR-T64",    // missing day
	}
	for _, raw := range cases {
		if _, err := ParseTicker(raw); err == nil {
			t.Errorf("ParseTicker(%q) should have failed", raw)
		}
	}
}
