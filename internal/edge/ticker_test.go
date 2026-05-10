package edge

import (
	"errors"
	"testing"
	"time"
)

func TestParseTicker(t *testing.T) {
	mustDate := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}

	tests := []struct {
		name   string
		ticker string
		want   Spec
		errIs  error
	}{
		{
			name:   "high NY between bracket with .5 key",
			ticker: "KXHIGHNY-26APR29-B62.5",
			want: Spec{
				Ticker:    "KXHIGHNY-26APR29-B62.5",
				Series:    "KXHIGHNY",
				ValidDate: mustDate(2026, time.April, 29),
				Kind:      BracketBetween,
				Key:       62.5,
			},
		},
		{
			name:   "high CHI threshold integer key",
			ticker: "KXHIGHCHI-26APR29-T61",
			want: Spec{
				Ticker:    "KXHIGHCHI-26APR29-T61",
				Series:    "KXHIGHCHI",
				ValidDate: mustDate(2026, time.April, 29),
				Kind:      BracketThreshold,
				Key:       61,
			},
		},
		{
			name:   "low NYC between integer key",
			ticker: "KXLOWTNYC-26MAY01-B48",
			want: Spec{
				Ticker:    "KXLOWTNYC-26MAY01-B48",
				Series:    "KXLOWTNYC",
				ValidDate: mustDate(2026, time.May, 1),
				Kind:      BracketBetween,
				Key:       48,
			},
		},
		{
			name:   "high MIA threshold high-temp",
			ticker: "KXHIGHMIA-26APR29-T88",
			want: Spec{
				Ticker:    "KXHIGHMIA-26APR29-T88",
				Series:    "KXHIGHMIA",
				ValidDate: mustDate(2026, time.April, 29),
				Kind:      BracketThreshold,
				Key:       88,
			},
		},
		{
			name:   "high LAX between with .5",
			ticker: "KXHIGHLAX-26APR29-B72.5",
			want: Spec{
				Ticker:    "KXHIGHLAX-26APR29-B72.5",
				Series:    "KXHIGHLAX",
				ValidDate: mustDate(2026, time.April, 29),
				Kind:      BracketBetween,
				Key:       72.5,
			},
		},
		{name: "wrong segment count", ticker: "KXHIGHNY-26APR29", errIs: ErrNotWeatherTicker},
		{name: "unknown series", ticker: "KXBOGUS-26APR29-B62.5", errIs: ErrNotWeatherTicker},
		{name: "non-weather BTC ticker", ticker: "KXBTCD-25NOV01-T100000", errIs: ErrNotWeatherTicker},
		{name: "bad month", ticker: "KXHIGHNY-26ZZZ29-B62.5", errIs: ErrNotWeatherTicker},
		{name: "bad day", ticker: "KXHIGHNY-26APR99-B62.5", errIs: ErrNotWeatherTicker},
		{name: "unknown bracket prefix", ticker: "KXHIGHNY-26APR29-X62.5", errIs: ErrNotWeatherTicker},
		{name: "non-numeric bracket key", ticker: "KXHIGHNY-26APR29-Bxx", errIs: ErrNotWeatherTicker},
		{name: "empty bracket suffix", ticker: "KXHIGHNY-26APR29-", errIs: ErrNotWeatherTicker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTicker(tt.ticker)
			if tt.errIs != nil {
				if !errors.Is(err, tt.errIs) {
					t.Fatalf("err = %v, want errors.Is(%v)", err, tt.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Spec mismatch:\n got: %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

func TestSpecStation(t *testing.T) {
	tests := []struct{ series, want string }{
		{"KXHIGHNY", "KNYC"},
		{"KXLOWTNYC", "KNYC"},
		{"KXHIGHMIA", "KMIA"},
		{"KXLOWTMIA", "KMIA"},
		{"KXHIGHLAX", "KLAX"},
		{"KXLOWTLAX", "KLAX"},
		{"KXHIGHCHI", "KMDW"},
		{"KXLOWTCHI", "KMDW"},
		{"KXBOGUS", ""},
	}
	for _, tt := range tests {
		t.Run(tt.series, func(t *testing.T) {
			got := Spec{Series: tt.series}.Station()
			if got != tt.want {
				t.Errorf("Station() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpecIsHigh(t *testing.T) {
	if !(Spec{Series: "KXHIGHNY"}.IsHigh()) {
		t.Error("KXHIGHNY should be high")
	}
	if (Spec{Series: "KXLOWTNYC"}.IsHigh()) {
		t.Error("KXLOWTNYC should not be high")
	}
}

func TestBracketKindString(t *testing.T) {
	if BracketBetween.String() != "B" || BracketThreshold.String() != "T" || BracketUnknown.String() != "?" {
		t.Errorf("BracketKind.String() unexpected: B=%q T=%q U=%q",
			BracketBetween, BracketThreshold, BracketUnknown)
	}
}
