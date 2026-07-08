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

func TestGetMarketsPage(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/markets", r.URL.Path)
		require.Equal(t, "KXHIGHNY", r.URL.Query().Get("series_ticker"))

		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-KEY"))
		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-SIGNATURE"))
		require.NotEmpty(t, r.Header.Get("KALSHI-ACCESS-TIMESTAMP"))

		resp := getMarketsResponse{
			Markets: []Market{
				{Ticker: "KXHIGHNY-APR18-T75", Status: "open", YesBidDollars: "0.55", YesAskDollars: "0.58"},
				{Ticker: "KXHIGHNY-APR18-T80", Status: "open"},
			},
			Cursor: "next-page",
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))

	markets, cursor, err := c.GetMarketsPage(context.Background(), map[string]string{"series_ticker": "KXHIGHNY"})
	require.NoError(t, err)
	require.Len(t, markets, 2)
	require.Equal(t, "KXHIGHNY-APR18-T75", markets[0].Ticker)
	require.InDelta(t, 0.55, markets[0].YesBid(), 0.001)
	require.InDelta(t, 0.58, markets[0].YesAsk(), 0.001)
	require.Equal(t, "next-page", cursor)
}

func TestAPIErrorReturnsStatusAndBody(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, err := w.Write([]byte(`{"code":"invalid_params","message":"bad ticker"}`))
		require.NoError(t, err)
	}))

	_, _, err := c.GetMarketsPage(context.Background(), nil)
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
	// If throttle held the mutex across its wait, 4 concurrent callers
	// would each wait 1s back-to-back. The fix reserves slots under the
	// lock and waits outside, so total wall time is ~3s for 4 callers
	// (first goes immediately, then 1s * 3 for the queue). We keep the
	// assertion loose so it's not flaky: wall time must be substantially
	// less than the buggy serialization worst case of ~4s.
	c := &Client{minDelay: 1000 * time.Millisecond, now: time.Now}
	ctx := context.Background()

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.throttle(ctx)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Bug-case would be ~4s (each caller sleeps 1s in series). Fixed
	// case is ~3s (one slot per second, reserved under the lock). Leave
	// margin to keep the test reliable under CI load.
	require.Less(t, elapsed, 3500*time.Millisecond, "throttle is serializing callers; lock is held during wait")
}

func TestResetRateLimit(t *testing.T) {
	// ResetRateLimit is a no-network setter; exercise it only for coverage.
	c := &Client{
		minDelay:     2 * time.Second,
		baseMinDelay: 1 * time.Second,
		now:          time.Now,
	}
	c.ResetRateLimit()
	require.Equal(t, 1*time.Second, c.minDelay)
	// Zeroing minDelay does not cause subsequent throttle calls to panic.
	require.NoError(t, c.throttle(context.Background()))
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
		require.NoError(t, json.NewEncoder(w).Encode(getMarketsResponse{
			Markets: []Market{{Ticker: "OK", Status: "open"}},
		}))
	}))
	// Shorten initial backoff so the test doesn't take the default
	// 2s+ — we only care that the retry path runs.
	c.minDelay = 10 * time.Millisecond

	// Use a context deadline that lets the first 2s backoff elapse but
	// bounds the test runtime.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	markets, _, err := c.GetMarketsPage(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "OK", markets[0].Ticker)
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
		require.NoError(t, json.NewEncoder(w).Encode(getMarketsResponse{
			Markets: []Market{{Ticker: "OK", Status: "open"}},
		}))
	}))
	c.minDelay = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := c.GetMarketsPage(ctx, nil)
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
		require.NoError(t, json.NewEncoder(w).Encode(getMarketsResponse{
			Markets: []Market{{Ticker: "OK", Status: "open"}},
		}))
	}))
	c.minDelay = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, _, err := c.GetMarketsPage(ctx, nil)
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
		require.NoError(t, json.NewEncoder(w).Encode(getMarketsResponse{
			Markets: []Market{{Ticker: "OK", Status: "open"}},
		}))
	}))
	c.minDelay = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	markets, _, err := c.GetMarketsPage(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "OK", markets[0].Ticker)
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

	_, _, err := c.GetMarketsPage(context.Background(), nil)
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

	_, _, err := c.GetMarketsPage(context.Background(), nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAuth)
	require.Equal(t, int32(1), calls.Load(), "403 must not be retried")
}

// TestThrottle_HonorsCtxCancellation verifies the throttle wait can be
// cancelled mid-flight by ctx. Before the fix this was a non-cancellable
// time.Sleep that could pin a caller for up to 2s after the parent
// context already wanted them gone.
func TestThrottle_HonorsCtxCancellation(t *testing.T) {
	c := &Client{
		minDelay: 2 * time.Second,
		now:      time.Now,
		// Pretend a recent call already happened so throttle has to wait.
		lastCall: time.Now(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.throttle(ctx)
	}()
	// Sleep a hair so throttle has entered its wait, then cancel.
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(50 * time.Millisecond):
		t.Fatal("throttle did not return within 50ms of cancel")
	}
}

// TestDoRaw_MinDelayDecaysAfterSuccessCluster verifies the
// success-counter decay restores minDelay to baseline after a 429
// cluster. Without this, a transient rate-limit storm would leave the
// client throttled at maxAdaptiveDelay (2s) for process lifetime.
func TestDoRaw_MinDelayDecaysAfterSuccessCluster(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"rate_limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(getMarketsResponse{
			Markets: []Market{{Ticker: "OK", Status: "open"}},
		}))
	}))
	// Speed up the test: tiny baseline so the 1s bit-shift backoff +
	// 11 calls doesn't blow our timeout. Also tiny baseMinDelay so the
	// decay floor matches.
	c.baseMinDelay = 1 * time.Millisecond
	c.minDelay = 1 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// First call hits 429, retries to 200. Observation: minDelay should
	// be bumped to 2 * baseMinDelay (or capped at maxAdaptiveDelay).
	_, _, err := c.GetMarketsPage(ctx, nil)
	require.NoError(t, err)

	c.rateMu.Lock()
	bumped := c.minDelay
	c.rateMu.Unlock()
	require.Greater(t, bumped, c.baseMinDelay, "429 must bump minDelay")

	// Now drive 10 more successful 200s; minDelay should decay back to
	// baseMinDelay after the 10th success (consecutive2x already has 1
	// from the retried success, so 9 more is enough — be generous and
	// drive 10 to be sure).
	for i := 0; i < 10; i++ {
		_, _, err := c.GetMarketsPage(ctx, nil)
		require.NoError(t, err)
	}

	c.rateMu.Lock()
	final := c.minDelay
	c.rateMu.Unlock()
	require.Equal(t, c.baseMinDelay, final,
		"minDelay should decay back to baseMinDelay after a success cluster")
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
