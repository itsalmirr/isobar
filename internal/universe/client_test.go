package universe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/clock"
)

// Trimmed copies of real payloads captured 2026-06-09 from the live
// public API (curl, unauthenticated) — field names must not drift.
const (
	eventsPage1 = `{"cursor":"abc123","events":[{"event_ticker":"KXHIGHNY-26JUN10","category":"Climate and Weather","collateral_return_type":"MECNET","markets":[{"ticker":"KXHIGHNY-26JUN10-T75","event_ticker":"KXHIGHNY-26JUN10","status":"active","market_type":"binary","strike_type":"less","cap_strike":75,"yes_bid_dollars":"0.0000","yes_ask_dollars":"0.0200","no_bid_dollars":"0.9800","no_ask_dollars":"1.0000","volume_24h_fp":"1807.46","open_interest_fp":"1541.59","close_time":"2026-06-11T04:59:00Z"}]}]}`
	eventsPage2 = `{"cursor":"","events":[{"event_ticker":"KXHIGHNY-26JUN11","category":"Climate and Weather","collateral_return_type":"MECNET","markets":[{"ticker":"KXHIGHNY-26JUN11-B75.5","event_ticker":"KXHIGHNY-26JUN11","status":"active","market_type":"binary","strike_type":"between","floor_strike":75,"cap_strike":76,"yes_bid_dollars":"0.1400","yes_ask_dollars":"0.1600","no_bid_dollars":"0.8400","no_ask_dollars":"0.8600","volume_24h_fp":"0","open_interest_fp":"0","close_time":"2026-06-12T04:59:00Z"}]}]}`
	seriesBody  = `{"series":{"ticker":"KXHIGHNY","category":"Climate and Weather","fee_type":"quadratic","fee_multiplier":1,"contract_terms_url":"https://kalshi-public-docs.s3.amazonaws.com/contract_terms/NHIGH.pdf","settlement_sources":[{"name":"NWS Climatological Report","url":"https://forecast.weather.gov"}]}}`
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(
		clock.Fake(time.Date(2026, 6, 9, 20, 0, 0, 0, time.UTC)),
		WithBaseURL(srv.URL),
		WithHTTP(srv.Client()),
		WithMinInterval(0),
	)
}

func TestEventsPaginatesAndParses(t *testing.T) {
	var gotQueries []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			t.Errorf("path = %q, want /events", r.URL.Path)
		}
		q := r.URL.Query()
		gotQueries = append(gotQueries, q.Get("cursor"))
		if q.Get("series_ticker") != "KXHIGHNY" || q.Get("with_nested_markets") != "true" {
			t.Errorf("missing required params in %v", q)
		}
		if q.Get("cursor") == "" {
			_, _ = w.Write([]byte(eventsPage1))
		} else {
			_, _ = w.Write([]byte(eventsPage2))
		}
	})

	events, err := c.Events(context.Background(), "KXHIGHNY", "open")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2 (pagination)", len(events))
	}
	if len(gotQueries) != 2 || gotQueries[1] != "abc123" {
		t.Fatalf("cursor sequence = %v, want [\"\" abc123]", gotQueries)
	}

	ev := events[0]
	if ev.SeriesTicker != "KXHIGHNY" {
		t.Errorf("SeriesTicker = %q (derived from event ticker), want KXHIGHNY", ev.SeriesTicker)
	}
	if !ev.MutuallyExclusive() {
		t.Error("MECNET event not flagged mutually exclusive")
	}
	m := ev.Markets[0]
	if m.YesAskCents != 2.0 || m.NoBidCents != 98.0 {
		t.Errorf("price parse: yes_ask=%v no_bid=%v, want 2 and 98 cents", m.YesAskCents, m.NoBidCents)
	}
	if m.CapStrike == nil || *m.CapStrike != 75 || m.FloorStrike != nil {
		t.Errorf("strike parse: floor=%v cap=%v, want one-sided cap 75", m.FloorStrike, m.CapStrike)
	}
	if m.CloseTime.IsZero() {
		t.Error("close_time not parsed")
	}

	m2 := events[1].Markets[0]
	if m2.FloorStrike == nil || m2.CapStrike == nil {
		t.Fatalf("between market lost a bound: floor=%v cap=%v", m2.FloorStrike, m2.CapStrike)
	}
}

func TestSeriesParsesFeeParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/series/KXHIGHNY" {
			t.Errorf("path = %q, want /series/KXHIGHNY", r.URL.Path)
		}
		_, _ = w.Write([]byte(seriesBody))
	})

	meta, err := c.Series(context.Background(), "KXHIGHNY")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if meta.FeeType != "quadratic" || meta.FeeMultiplier != 1 {
		t.Errorf("fee params = %q ×%v, want quadratic ×1", meta.FeeType, meta.FeeMultiplier)
	}
	if meta.Category != "Climate and Weather" {
		t.Errorf("Category = %q", meta.Category)
	}
	if len(meta.SettlementSources) != 1 || meta.SettlementSources[0].Name != "NWS Climatological Report" {
		t.Errorf("settlement sources = %+v", meta.SettlementSources)
	}
}

// TestEventsStopsOnEmptyPageWithCursor guards the pagination loop
// against an API quirk the kalshi REST client already defends against:
// a page with zero events but a non-empty cursor. Without the guard,
// Events would re-request the same cursor forever.
func TestEventsStopsOnEmptyPageWithCursor(t *testing.T) {
	var hits int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(eventsPage1)) // cursor "abc123", 1 event
			return
		}
		// Every subsequent page: empty events, cursor still set.
		_, _ = w.Write([]byte(`{"cursor":"abc123","events":[]}`))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := c.Events(ctx, "KXHIGHNY", "open")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (one real page, one empty page, then stop)", hits)
	}
}

func TestEventsNon200IsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusTooManyRequests)
	})
	if _, err := c.Events(context.Background(), "KXHIGHNY", ""); err == nil {
		t.Fatal("want error on 429, got nil")
	}
}

func TestThrottleSpacesRequests(t *testing.T) {
	clk := clock.Fake(time.Date(2026, 6, 9, 20, 0, 0, 0, time.UTC))
	var slept []time.Duration

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(seriesBody))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(clk, WithBaseURL(srv.URL), WithHTTP(srv.Client()), WithMinInterval(250*time.Millisecond))
	c.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	ctx := context.Background()
	if _, err := c.Series(ctx, "KXHIGHNY"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Series(ctx, "KXHIGHNY"); err != nil {
		t.Fatal(err)
	}
	// Fake clock never advances, so the second call must wait the full
	// interval; the first (cold) call must not sleep at all.
	if len(slept) != 1 || slept[0] != 250*time.Millisecond {
		t.Fatalf("slept = %v, want exactly [250ms]", slept)
	}
}
