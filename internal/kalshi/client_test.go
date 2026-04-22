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
	c := &Client{minDelay: 1000 * time.Millisecond}

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
