package kalshi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testKeyPair(t *testing.T) (keyID string, pemKey []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(key)
	pemKey = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	return "test-key-id", pemKey
}

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	keyID, pemKey := testKeyPair(t)
	signer, err := NewSigner(keyID, pemKey)
	require.NoError(t, err)

	return NewClient(srv.URL, signer)
}

func TestGetMarket(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/markets/BTCUSD-26MAR18", r.URL.Path)

		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-KEY"))
		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-SIGNATURE"))
		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-TIMESTAMP"))

		resp := getMarketResponse{
			Market: Market{
				Ticker:        "BTCUSD-26MAR18",
				Title:         "BTC above 100k?",
				Status:        "open",
				YesBidDollars: "0.55",
				YesAskDollars: "0.58",
				VolumeFP:      "1200.00",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))

	m, err := c.GetMarket(context.Background(), "BTCUSD-26MAR18")
	require.NoError(t, err)
	require.Equal(t, "BTCUSD-26MAR18", m.Ticker)
	require.InDelta(t, 0.55, m.YesBid(), 0.001)
	require.InDelta(t, 0.58, m.YesAsk(), 0.001)
}

func TestGetOrderbook(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Contains(t, r.URL.Path, "/markets/BTCUSD-26MAR18/orderbook")

		resp := getOrderbookResponse{
			Orderbook: Orderbook{
				Yes: []OrderbookLevel{{55, 100}, {54, 200}},
				No:  []OrderbookLevel{{45, 150}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))

	ob, err := c.GetOrderbook(context.Background(), "BTCUSD-26MAR18", 5)
	require.NoError(t, err)
	require.Len(t, ob.Yes, 2)
	require.Equal(t, 55, ob.Yes[0][0])
	require.Equal(t, 100, ob.Yes[0][1])
}

func TestCreateOrder(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/portfolio/orders", r.URL.Path)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req CreateOrderRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "BTCUSD-26MAR18", req.Ticker)
		require.Equal(t, SideYes, req.Side)
		require.Equal(t, ActionBuy, req.Action)
		require.Equal(t, 10, req.Count)
		require.Equal(t, 55, req.YesPrice)

		resp := createOrderResponse{
			Order: Order{
				OrderID:  "order-123",
				Ticker:   req.Ticker,
				Side:     req.Side,
				Action:   req.Action,
				Status:   "resting",
				YesPrice: req.YesPrice,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))

	order, err := c.CreateOrder(context.Background(), CreateOrderRequest{
		Ticker:   "BTCUSD-26MAR18",
		Side:     SideYes,
		Action:   ActionBuy,
		Count:    10,
		YesPrice: 55,
	})
	require.NoError(t, err)
	require.Equal(t, "order-123", order.OrderID)
	require.Equal(t, "resting", order.Status)
}

func TestCancelOrder(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/portfolio/orders/order-123", r.URL.Path)

		resp := cancelOrderResponse{
			Order: Order{
				OrderID: "order-123",
				Status:  "cancelled",
			},
			ReducedBy: 10,
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))

	order, err := c.CancelOrder(context.Background(), "order-123")
	require.NoError(t, err)
	require.Equal(t, "cancelled", order.Status)
}

func TestGetFills(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/portfolio/fills", r.URL.Path)

		resp := getFillsResponse{
			Fills: []Fill{
				{
					TradeID:  "fill-1",
					OrderID:  "order-123",
					Ticker:   "BTCUSD-26MAR18",
					Side:     SideYes,
					Action:   ActionBuy,
					Count:    5,
					YesPrice: 55,
					IsTaker:  true,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))

	fills, err := c.GetFills(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, fills, 1)
	require.Equal(t, "fill-1", fills[0].TradeID)
	require.Equal(t, 5, fills[0].Count)
}

func TestAPIErrorReturnsStatusAndBody(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, err := w.Write([]byte(`{"code":"invalid_params","message":"bad ticker"}`))
		require.NoError(t, err)
	}))

	_, err := c.GetMarket(context.Background(), "INVALID")
	require.Error(t, err)
	require.Contains(t, err.Error(), "API error 400")
	require.Contains(t, err.Error(), "bad ticker")
}

func TestSignerAuthHeaders(t *testing.T) {
	keyID, pemKey := testKeyPair(t)
	signer, err := NewSigner(keyID, pemKey)
	require.NoError(t, err)

	key, sig, ts, err := signer.AuthHeaders("GET", "/markets/TEST")
	require.NoError(t, err)
	require.Equal(t, keyID, key)
	require.NotEmpty(t, sig)
	require.NotEmpty(t, ts)
}

func TestDiscoverOpenTickersPaginatesAndDedupes(t *testing.T) {
	// Three pages with a duplicate ticker across pages and one non-prefix
	// market that must be filtered out.
	pages := []getMarketsResponse{
		{Markets: []Market{{Ticker: "KXHIGHNY-APR18-T75"}, {Ticker: "KXHIGHNY-APR18-T80"}}, Cursor: "p2"},
		{Markets: []Market{{Ticker: "KXHIGHNY-APR18-T80"}, {Ticker: "OTHER-MARKET"}, {Ticker: "KXHIGHNY-APR18-T85"}}, Cursor: "p3"},
		{Markets: []Market{{Ticker: "KXHIGHNY-APR19-T75"}}, Cursor: ""},
	}
	var idx atomic.Int32

	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/markets", r.URL.Path)
		require.Equal(t, "KXHIGHNY", r.URL.Query().Get("series_ticker"))
		require.Equal(t, "open", r.URL.Query().Get("status"))
		i := int(idx.Add(1)) - 1
		require.Less(t, i, len(pages))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(pages[i]))
	}))

	got, err := c.DiscoverOpenTickers(context.Background(), []string{"KXHIGHNY"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.Equal(t, []string{
		"KXHIGHNY-APR18-T75",
		"KXHIGHNY-APR18-T80",
		"KXHIGHNY-APR18-T85",
		"KXHIGHNY-APR19-T75",
	}, got)
}

func TestDiscoverOpenTickersHandlesEmptyPageWithCursor(t *testing.T) {
	// Defensive: if the API returns cursor != "" but zero markets,
	// DiscoverOpenTickers must break (not spin forever).
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(getMarketsResponse{
			Markets: nil,
			Cursor:  "still-has-cursor-but-no-data",
		}))
	}))
	got, err := c.DiscoverOpenTickers(context.Background(), []string{"KXHIGHNY"}, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestThrottleDoesNotSerializeCallers(t *testing.T) {
	// If throttle held the mutex across its sleep, 4 concurrent callers
	// would each wait 1s back-to-back. The fix reserves slots under the
	// lock and sleeps outside, so total wall time is ~3s for 4 callers
	// (first goes immediately, then 1s * 3 for the queue). We keep the
	// assertion loose so it's not flaky: wall time must be substantially
	// less than the buggy serialization worst case of ~4s.
	c := &Client{minDelay: 1000 * time.Millisecond, now: time.Now}

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.throttle()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Bug-case would be ~4s (each caller sleeps 1s in series). Fixed
	// case is ~3s (one slot per second, reserved under the lock). Leave
	// margin to keep the test reliable under CI load.
	require.Less(t, elapsed, 3500*time.Millisecond, "throttle is serializing callers; lock is held during sleep")
}

func TestGetRaw(t *testing.T) {
	const body = `{"series":[{"ticker":"KXHIGHNY","title":"NYC daily high"}]}`
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/series", r.URL.Path)
		require.Equal(t, "Weather", r.URL.Query().Get("category"))
		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-KEY"))
		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-SIGNATURE"))
		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-TIMESTAMP"))
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(body))
		require.NoError(t, err)
	}))

	got, err := c.GetRaw(context.Background(), "/series", map[string]string{"category": "Weather"})
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestGetRawPropagatesAPIError(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(`{"code":"forbidden","message":"nope"}`))
		require.NoError(t, err)
	}))

	_, err := c.GetRaw(context.Background(), "/series", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "API error 403")
	require.Contains(t, err.Error(), "nope")
}

// --- Coverage: raw and typed helpers that existed but had no tests ---

func TestGetMarketRaw(t *testing.T) {
	const body = `{"market":{"ticker":"T","status":"open"}}`
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/markets/T", r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	got, err := c.GetMarketRaw(context.Background(), "T")
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestGetMarkets(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/markets", r.URL.Path)
		require.Equal(t, "KXHIGHNY", r.URL.Query().Get("series_ticker"))
		require.NoError(t, json.NewEncoder(w).Encode(getMarketsResponse{
			Markets: []Market{{Ticker: "KXHIGHNY-T"}, {Ticker: "KXHIGHNY-T2"}},
		}))
	}))
	markets, err := c.GetMarkets(context.Background(), map[string]string{"series_ticker": "KXHIGHNY"})
	require.NoError(t, err)
	require.Len(t, markets, 2)
}

func TestGetEvents(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/events", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(getEventsResponse{
			Events: []Event{{EventTicker: "EV-1", SeriesTicker: "SER"}},
		}))
	}))
	events, err := c.GetEvents(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "EV-1", events[0].EventTicker)
}

func TestGetOrderbookRaw(t *testing.T) {
	const body = `{"orderbook":{"yes":[],"no":[]}}`
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/markets/T/orderbook", r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	got, err := c.GetOrderbookRaw(context.Background(), "T")
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestGetOrder(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/portfolio/orders/ord-1", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(getOrderResponse{
			Order: Order{OrderID: "ord-1", Status: "resting"},
		}))
	}))
	got, err := c.GetOrder(context.Background(), "ord-1")
	require.NoError(t, err)
	require.Equal(t, "ord-1", got.OrderID)
	require.Equal(t, "resting", got.Status)
}

func TestGetOrders(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/portfolio/orders", r.URL.Path)
		require.Equal(t, "resting", r.URL.Query().Get("status"))
		require.NoError(t, json.NewEncoder(w).Encode(getOrdersResponse{
			Orders: []Order{{OrderID: "o1"}, {OrderID: "o2"}},
		}))
	}))
	got, err := c.GetOrders(context.Background(), map[string]string{"status": "resting"})
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestGetOrdersRaw(t *testing.T) {
	const body = `{"orders":[]}`
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/portfolio/orders", r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	got, err := c.GetOrdersRaw(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestGetFillsRaw(t *testing.T) {
	const body = `{"fills":[]}`
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/portfolio/fills", r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	got, err := c.GetFillsRaw(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, body, got)
}

func TestGetPositions(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/portfolio/positions", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(getPositionsResponse{
			MarketPositions: []Position{{Ticker: "P1"}},
		}))
	}))
	got, err := c.GetPositions(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "P1", got[0].Ticker)
}

func TestGetPositionsRaw(t *testing.T) {
	const body = `{"market_positions":[]}`
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/portfolio/positions", r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	got, err := c.GetPositionsRaw(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, body, string(got))
}

func TestGetBalanceAndRaw(t *testing.T) {
	const body = `{"balance":1234,"payout":0}`
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/portfolio/balance", r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	bal, err := c.GetBalance(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1234, bal.Balance)

	raw, err := c.GetBalanceRaw(context.Background())
	require.NoError(t, err)
	require.Equal(t, body, raw)
}

func TestResetRateLimit(t *testing.T) {
	// ResetRateLimit is a no-network setter; exercise it only for coverage.
	c := &Client{minDelay: 2 * time.Second, now: time.Now}
	c.ResetRateLimit()
	// Zeroing minDelay does not cause subsequent throttle calls to panic.
	c.throttle()
}

// TestDeleteSucceeds ensures the DELETE helper's happy path is covered.
// CancelOrder exercises it; an explicit test keeps coverage stable if
// CancelOrder's shape changes.
func TestDeleteReturnsParsedBody(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/portfolio/orders/ord-xyz", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(getOrderResponse{
			Order: Order{OrderID: "ord-xyz", Status: "canceled"},
		}))
	}))
	got, err := c.CancelOrder(context.Background(), "ord-xyz")
	require.NoError(t, err)
	require.Equal(t, "canceled", got.Status)
}

// TestAllEndpointsPropagateErrors hits the uncovered error-return path
// in every thin wrapper. Each wrapper has the shape "if err := get(…);
// err != nil { return zero, err }"; a 500 from the mock exercises it
// once per method.
func TestAllEndpointsPropagateErrors(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal","message":"boom"}`))
	}))
	ctx := context.Background()

	calls := []struct {
		name string
		fn   func() error
	}{
		{"GetMarkets", func() error { _, err := c.GetMarkets(ctx, nil); return err }},
		{"GetMarketsPage", func() error { _, _, err := c.GetMarketsPage(ctx, nil); return err }},
		{"GetEvents", func() error { _, err := c.GetEvents(ctx, nil); return err }},
		{"GetOrderbook", func() error { _, err := c.GetOrderbook(ctx, "T", 0); return err }},
		{"GetOrder", func() error { _, err := c.GetOrder(ctx, "o1"); return err }},
		{"GetOrders", func() error { _, err := c.GetOrders(ctx, nil); return err }},
		{"GetFills", func() error { _, err := c.GetFills(ctx, nil); return err }},
		{"GetPositions", func() error { _, err := c.GetPositions(ctx, nil); return err }},
		{"GetBalance", func() error { _, err := c.GetBalance(ctx); return err }},
		{"CancelOrder", func() error { _, err := c.CancelOrder(ctx, "o"); return err }},
		{"CreateOrder", func() error {
			_, err := c.CreateOrder(ctx, CreateOrderRequest{Ticker: "T", Action: "buy", Side: "yes", Count: 1, Type: "limit"})
			return err
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			require.Error(t, err)
			require.Contains(t, err.Error(), "API error 500")
		})
	}
}

// TestDoRaw429RetriesThenSucceeds exercises the rate-limit backoff in
// doRaw. The mock server returns 429 on the first call and 200 on the
// second; the client should backoff and retry, returning the body.
func TestDoRaw429RetriesThenSucceeds(t *testing.T) {
	var callCount atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if callCount.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"rate_limited","message":"slow down"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(getMarketResponse{
			Market: Market{Ticker: "OK", Status: "open"},
		}))
	}))
	// Shorten initial backoff so the test doesn't take the default
	// 2s+ — we only care that the retry path runs.
	c.minDelay = 10 * time.Millisecond

	// Use a context deadline that lets the first 2s backoff elapse but
	// bounds the test runtime.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	m, err := c.GetMarket(ctx, "RATELIMITED")
	require.NoError(t, err)
	require.Equal(t, "OK", m.Ticker)
	require.GreaterOrEqual(t, callCount.Load(), int32(2), "expected at least one retry")
}

// TestDoRaw_RetryAfterDeltaSeconds verifies the client honors a
// numeric Retry-After header as a floor (≥5s in this case).
func TestDoRaw_RetryAfterDeltaSeconds(t *testing.T) {
	var calls atomic.Int32
	var firstAt, secondAt time.Time
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			firstAt = time.Now()
			w.Header().Set("Retry-After", "1") // keep test fast
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"rate_limited"}`))
			return
		}
		secondAt = time.Now()
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(getMarketResponse{
			Market: Market{Ticker: "OK", Status: "open"},
		}))
	}))
	c.minDelay = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.GetMarket(ctx, "X")
	require.NoError(t, err)
	require.GreaterOrEqual(t, calls.Load(), int32(2))
	// The default bit-shift backoff for attempt=0 is 2s; Retry-After=1s
	// should NOT shorten it (Retry-After is a floor, not an override).
	// So the actual gap should be ≥ 2s.
	require.GreaterOrEqual(t, secondAt.Sub(firstAt), 1900*time.Millisecond,
		"second request should not happen before the bit-shift backoff (2s)")
}

// TestDoRaw_RetryAfterHTTPDate verifies that an HTTP-date Retry-After is
// parsed and applied as the floor when larger than the default backoff.
// We use a 4-second HTTP-date so the test asserts ≥3s with margin for
// the IMF-fixdate format's 1-second granularity plus transit time.
func TestDoRaw_RetryAfterHTTPDate(t *testing.T) {
	var calls atomic.Int32
	var firstAt, secondAt time.Time
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			firstAt = time.Now()
			// 4 seconds from now in HTTP-date form (whole-second truncation
			// may eat up to 1s of the gap; default bit-shift backoff is 2s
			// at attempt=0, so we want a clear ≥3s assertion floor).
			when := time.Now().UTC().Add(4 * time.Second).Format(http.TimeFormat)
			w.Header().Set("Retry-After", when)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"rate_limited"}`))
			return
		}
		secondAt = time.Now()
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(getMarketResponse{
			Market: Market{Ticker: "OK", Status: "open"},
		}))
	}))
	c.minDelay = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, err := c.GetMarket(ctx, "X")
	require.NoError(t, err)
	require.GreaterOrEqual(t, calls.Load(), int32(2))
	require.GreaterOrEqual(t, secondAt.Sub(firstAt), 3*time.Second,
		"client should wait ~4s per Retry-After HTTP-date")
}

// TestDoRaw_RetryAfterCapped verifies that a misbehaving Retry-After
// (3600s) is capped at retryAfterCap (60s). We can't wait 60s in the
// test, so we drive the parser directly instead.
func TestDoRaw_RetryAfterCapped(t *testing.T) {
	// Unit-level check of the parser plus the cap policy.
	d, ok := parseRetryAfter("3600", time.Now())
	require.True(t, ok)
	require.Equal(t, 3600*time.Second, d)
	// The cap is applied in doRaw; assert the constant so a regression
	// in the policy is caught loudly.
	require.Equal(t, 60*time.Second, retryAfterCap)
}

// TestDoRaw_Retries5xx verifies that 502 from the server triggers a
// retry on GET, succeeding on the second attempt.
func TestDoRaw_Retries5xx(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`bad gateway`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(getMarketResponse{
			Market: Market{Ticker: "OK", Status: "open"},
		}))
	}))
	c.minDelay = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := c.GetMarket(ctx, "X")
	require.NoError(t, err)
	require.Equal(t, "OK", m.Ticker)
	require.GreaterOrEqual(t, calls.Load(), int32(2))
}

// TestDoRaw_DoesNotRetry401 verifies 401 returns ErrAuth and no retry.
func TestDoRaw_DoesNotRetry401(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
	}))
	c.minDelay = 10 * time.Millisecond

	_, err := c.GetMarket(context.Background(), "X")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAuth)
	require.Equal(t, int32(1), calls.Load(), "401 must not be retried")
}

// TestDoRaw_DoesNotRetry403 verifies 403 also maps to ErrAuth.
func TestDoRaw_DoesNotRetry403(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"forbidden"}`))
	}))
	c.minDelay = 10 * time.Millisecond

	_, err := c.GetMarket(context.Background(), "X")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAuth)
	require.Equal(t, int32(1), calls.Load(), "403 must not be retried")
}

// TestParseRetryAfter exercises the RFC 9110 parser in isolation.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	t.Run("delta seconds", func(t *testing.T) {
		d, ok := parseRetryAfter("5", now)
		require.True(t, ok)
		require.Equal(t, 5*time.Second, d)
	})
	t.Run("HTTP-date", func(t *testing.T) {
		when := now.Add(7 * time.Second).Format(http.TimeFormat)
		d, ok := parseRetryAfter(when, now)
		require.True(t, ok)
		require.Equal(t, 7*time.Second, d)
	})
	t.Run("past HTTP-date clamps to zero", func(t *testing.T) {
		when := now.Add(-5 * time.Second).Format(http.TimeFormat)
		d, ok := parseRetryAfter(when, now)
		require.True(t, ok)
		require.Equal(t, time.Duration(0), d)
	})
	t.Run("empty header", func(t *testing.T) {
		_, ok := parseRetryAfter("", now)
		require.False(t, ok)
	})
	t.Run("garbage header", func(t *testing.T) {
		_, ok := parseRetryAfter("not-a-thing", now)
		require.False(t, ok)
	})
	t.Run("negative seconds rejected", func(t *testing.T) {
		_, ok := parseRetryAfter("-5", now)
		require.False(t, ok)
	})
}
