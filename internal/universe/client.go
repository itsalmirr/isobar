package universe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wh0o0pz/isobar/internal/clock"
)

// DefaultBaseURL is Kalshi's public (unauthenticated) trade API. Discovery
// reads /series and /events only — both verified to serve without auth.
const DefaultBaseURL = "https://api.elections.kalshi.com/trade-api/v2"

// defaultMinInterval keeps discovery far inside the documented 20 reads/sec
// basic-tier ceiling, leaving headroom for the recorder's REST traffic
// which shares the same source IP in production.
const defaultMinInterval = 250 * time.Millisecond

// HTTPDoer is the http.Client seam (tests inject httptest transports).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client fetches and parses the public catalog endpoints with a simple
// serialized throttle. Safe for concurrent use; concurrent callers are
// throttled collectively, not per-goroutine.
type Client struct {
	base string
	http HTTPDoer
	clk  clock.Clock

	minInterval time.Duration
	sleep       func(ctx context.Context, d time.Duration) error

	mu   sync.Mutex
	last time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base (tests point at httptest servers).
func WithBaseURL(base string) Option { return func(c *Client) { c.base = base } }

// WithHTTP overrides the HTTP doer.
func WithHTTP(d HTTPDoer) Option { return func(c *Client) { c.http = d } }

// WithMinInterval overrides the inter-request throttle (tests set 0).
func WithMinInterval(d time.Duration) Option { return func(c *Client) { c.minInterval = d } }

// NewClient builds a discovery client. clk must be non-nil (ADR-0012:
// wall time is injected, never read ambiently).
func NewClient(clk clock.Clock, opts ...Option) *Client {
	c := &Client{
		base:        DefaultBaseURL,
		http:        &http.Client{Timeout: 30 * time.Second},
		clk:         clk,
		minInterval: defaultMinInterval,
		sleep:       ctxSleep,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ctxSleep waits d or until ctx is done, whichever comes first.
func ctxSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// throttle enforces minInterval between request starts.
func (c *Client) throttle(ctx context.Context) error {
	c.mu.Lock()
	wait := c.minInterval - c.clk.Now().Sub(c.last)
	if wait < 0 {
		wait = 0
	}
	c.last = c.clk.Now().Add(wait)
	c.mu.Unlock()
	if wait == 0 {
		return nil
	}
	return c.sleep(ctx, wait)
}

func (c *Client) getJSON(ctx context.Context, path string, params url.Values, dst any) error {
	if err := c.throttle(ctx); err != nil {
		return err
	}
	u := c.base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("universe: GET %s: status %d: %.200s", path, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, dst)
}

// --- wire types (raw API shapes; exported types live in types.go) ---

type wireMarket struct {
	Ticker      string   `json:"ticker"`
	EventTicker string   `json:"event_ticker"`
	Status      string   `json:"status"`
	MarketType  string   `json:"market_type"`
	StrikeType  string   `json:"strike_type"`
	FloorStrike *float64 `json:"floor_strike"`
	CapStrike   *float64 `json:"cap_strike"`
	YesBid      string   `json:"yes_bid_dollars"`
	YesAsk      string   `json:"yes_ask_dollars"`
	NoBid       string   `json:"no_bid_dollars"`
	NoAsk       string   `json:"no_ask_dollars"`
	Volume24h   string   `json:"volume_24h_fp"`
	OpenInt     string   `json:"open_interest_fp"`
	CloseTime   string   `json:"close_time"`
}

type wireEvent struct {
	EventTicker          string       `json:"event_ticker"`
	SeriesTicker         string       `json:"series_ticker"`
	Category             string       `json:"category"`
	CollateralReturnType string       `json:"collateral_return_type"`
	Markets              []wireMarket `json:"markets"`
}

type wireEventsPage struct {
	Cursor string      `json:"cursor"`
	Events []wireEvent `json:"events"`
}

type wireSeriesResp struct {
	Series struct {
		Ticker            string  `json:"ticker"`
		Category          string  `json:"category"`
		FeeType           string  `json:"fee_type"`
		FeeMultiplier     float64 `json:"fee_multiplier"`
		ContractTermsURL  string  `json:"contract_terms_url"`
		SettlementSources []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"settlement_sources"`
	} `json:"series"`
}

// dollarsToCents parses Kalshi's dollar-string price fields ("0.0200")
// into cents. Empty strings parse as 0 (absent quote).
func dollarsToCents(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	d, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("universe: bad dollar amount %q: %w", s, err)
	}
	return d * 100, nil
}

func parseFP(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func (w *wireMarket) toMarket() (Market, error) {
	m := Market{
		Ticker:       w.Ticker,
		EventTicker:  w.EventTicker,
		Status:       w.Status,
		MarketType:   w.MarketType,
		StrikeType:   w.StrikeType,
		FloorStrike:  w.FloorStrike,
		CapStrike:    w.CapStrike,
		Volume24h:    parseFP(w.Volume24h),
		OpenInterest: parseFP(w.OpenInt),
	}
	var err error
	if m.YesBidCents, err = dollarsToCents(w.YesBid); err != nil {
		return m, err
	}
	if m.YesAskCents, err = dollarsToCents(w.YesAsk); err != nil {
		return m, err
	}
	if m.NoBidCents, err = dollarsToCents(w.NoBid); err != nil {
		return m, err
	}
	if m.NoAskCents, err = dollarsToCents(w.NoAsk); err != nil {
		return m, err
	}
	if w.CloseTime != "" {
		if m.CloseTime, err = time.Parse(time.RFC3339, w.CloseTime); err != nil {
			return m, fmt.Errorf("universe: market %s: bad close_time: %w", w.Ticker, err)
		}
	}
	return m, nil
}

// seriesFromEventTicker derives the series prefix from an event ticker
// (KXHIGHNY-26JUN10 → KXHIGHNY) for payloads that omit series_ticker.
func seriesFromEventTicker(et string) string {
	if i := strings.IndexByte(et, '-'); i > 0 {
		return et[:i]
	}
	return et
}

// Events fetches all events for one series (paginated) with nested
// markets. status filters server-side ("open" for live discovery; empty
// for everything).
func (c *Client) Events(ctx context.Context, seriesTicker, status string) ([]Event, error) {
	var out []Event
	cursor := ""
	for {
		params := url.Values{
			"series_ticker":       {seriesTicker},
			"with_nested_markets": {"true"},
			"limit":               {"200"},
		}
		if status != "" {
			params.Set("status", status)
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var page wireEventsPage
		if err := c.getJSON(ctx, "/events", params, &page); err != nil {
			return nil, err
		}
		for _, we := range page.Events {
			ev := Event{
				EventTicker:          we.EventTicker,
				SeriesTicker:         we.SeriesTicker,
				Category:             we.Category,
				CollateralReturnType: we.CollateralReturnType,
			}
			if ev.SeriesTicker == "" {
				ev.SeriesTicker = seriesFromEventTicker(we.EventTicker)
			}
			for i := range we.Markets {
				m, err := we.Markets[i].toMarket()
				if err != nil {
					return nil, fmt.Errorf("event %s: %w", we.EventTicker, err)
				}
				ev.Markets = append(ev.Markets, m)
			}
			out = append(out, ev)
		}
		if page.Cursor == "" {
			return out, nil
		}
		cursor = page.Cursor
	}
}

// Series fetches per-series metadata, including the exchange's own fee
// parameters (fee_type, fee_multiplier).
func (c *Client) Series(ctx context.Context, ticker string) (SeriesMeta, error) {
	var resp wireSeriesResp
	if err := c.getJSON(ctx, "/series/"+url.PathEscape(ticker), nil, &resp); err != nil {
		return SeriesMeta{}, err
	}
	meta := SeriesMeta{
		Ticker:           resp.Series.Ticker,
		Category:         resp.Series.Category,
		FeeType:          resp.Series.FeeType,
		FeeMultiplier:    resp.Series.FeeMultiplier,
		ContractTermsURL: resp.Series.ContractTermsURL,
	}
	if meta.Ticker == "" {
		meta.Ticker = ticker
	}
	for _, s := range resp.Series.SettlementSources {
		meta.SettlementSources = append(meta.SettlementSources, SettlementSource{Name: s.Name, URL: s.URL})
	}
	return meta, nil
}
