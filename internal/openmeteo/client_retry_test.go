package openmeteo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// failNTimes returns an http.Handler that responds with `failStatus` for
// the first `n` requests and then 200 with `okBody` thereafter. The hit
// counter is exposed via the returned pointer.
func failNTimes(n int, failStatus int, okBody string) (http.Handler, *atomic.Int64) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		k := hits.Add(1)
		if int(k) <= n {
			w.WriteHeader(failStatus)
			_, _ = w.Write([]byte(`{"err":"transient"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody))
	})
	return h, &hits
}

// shortRetryPoller builds a Poller with a small retry envelope so tests
// stay fast. Initial=10ms, max=40ms, attempts=4 → worst-case ~70ms dwell.
func shortRetryPoller(t *testing.T, baseURL string, locs []Location) *Poller {
	t.Helper()
	p, err := NewPoller(Options{
		BaseURL:             baseURL,
		Model:               "gfs_seamless",
		ForecastDays:        3,
		PollInterval:        time.Minute,
		Locations:           locs,
		Logger:              discardLogger(),
		RetryInitialBackoff: 10 * time.Millisecond,
		RetryMaxBackoff:     40 * time.Millisecond,
		RetryMaxAttempts:    4,
	})
	require.NoError(t, err)
	return p
}

func TestPoller_RetriesOn5xxStorm(t *testing.T) {
	cases := []struct {
		name      string
		failN     int
		wantOK    bool
		wantHits  int64
		minHitsLB int64 // lower bound for hits when we fail (==MaxAttempts)
	}{
		{"3 fails then 200 → success on 4th", 3, true, 4, 4},
		{"4 fails → give up", 4, false, 4, 4},
		{"5 fails → give up (never reaches 5th call)", 5, false, 4, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, hits := failNTimes(tc.failN, http.StatusInternalServerError, `{"ok":true}`)
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)

			p := shortRetryPoller(t, srv.URL, []Location{{Name: "NY", Lat: 40, Lon: -73}})

			body, err := p.fetchWithRetry(context.Background(), Location{Name: "NY", Lat: 40, Lon: -73})
			if tc.wantOK {
				require.NoError(t, err)
				require.Equal(t, `{"ok":true}`, string(body))
			} else {
				require.Error(t, err)
				require.Nil(t, body)
			}
			require.Equal(t, tc.wantHits, hits.Load(), "server hit count")
		})
	}
}

func TestPoller_RetriesOn429(t *testing.T) {
	h, hits := failNTimes(1, http.StatusTooManyRequests, `{"ok":true}`)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	p := shortRetryPoller(t, srv.URL, []Location{{Name: "NY", Lat: 40, Lon: -73}})
	body, err := p.fetchWithRetry(context.Background(), Location{Name: "NY", Lat: 40, Lon: -73})
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, string(body))
	require.Equal(t, int64(2), hits.Load(), "one 429 then 200")
}

func TestPoller_DoesNotRetry4xxOther(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"err":"bad request"}`))
	}))
	t.Cleanup(srv.Close)

	p := shortRetryPoller(t, srv.URL, []Location{{Name: "NY", Lat: 40, Lon: -73}})

	start := time.Now()
	body, err := p.fetchWithRetry(context.Background(), Location{Name: "NY", Lat: 40, Lon: -73})
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Nil(t, body)
	require.Contains(t, err.Error(), "400")
	require.Equal(t, int64(1), hits.Load(), "should not retry 4xx other than 429")
	// Should bail immediately — no backoff sleep was scheduled.
	require.Less(t, elapsed, 50*time.Millisecond, "non-retryable should not sleep")
}

func TestPoller_HonorsCtxCancellation(t *testing.T) {
	// Server holds the response until either the timeout fires or its ctx
	// dies. The client ctx will cancel after 100ms; the http.Client.Do
	// call will return a ctx error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
			w.WriteHeader(http.StatusInternalServerError)
		case <-r.Context().Done():
			// client cancelled — just return, the transport will report it
			return
		}
	}))
	t.Cleanup(srv.Close)

	p := shortRetryPoller(t, srv.URL, []Location{{Name: "NY", Lat: 40, Lon: -73}})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	body, err := p.fetchWithRetry(ctx, Location{Name: "NY", Lat: 40, Lon: -73})
	elapsed := time.Since(start)

	require.Error(t, err, "expected ctx error")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, body)
	require.Less(t, elapsed, 200*time.Millisecond,
		"should bail within ~100ms ctx deadline, took %v", elapsed)
}

func TestPoller_FailureCounter(t *testing.T) {
	// Single Poller, two locations. NY always 500 (will exhaust retries);
	// LA always 200. The Poller has one BaseURL for all locations, so we
	// dispatch by latitude inside the handler.
	var nyHits, laHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lat := r.URL.Query().Get("latitude")
		if lat == "40.0000" {
			nyHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		laHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":"LA"}`))
	}))
	t.Cleanup(srv.Close)

	var (
		laRaw atomic.Value // []byte
		nyRaw atomic.Value
	)
	p, err := NewPoller(Options{
		BaseURL:      srv.URL,
		Model:        "gfs_seamless",
		ForecastDays: 3,
		PollInterval: time.Minute,
		Locations: []Location{
			{Name: "NY", Lat: 40, Lon: -73},
			{Name: "LA", Lat: 34, Lon: -118},
		},
		Logger:              discardLogger(),
		RetryInitialBackoff: 5 * time.Millisecond,
		RetryMaxBackoff:     20 * time.Millisecond,
		RetryMaxAttempts:    3,
		OnFetch: func(loc Location, raw []byte) {
			// Copy bytes — the test's atomic.Value just stores a reference.
			cp := make([]byte, len(raw))
			copy(cp, raw)
			if loc.Name == "LA" {
				laRaw.Store(cp)
			} else {
				nyRaw.Store(cp)
			}
		},
	})
	require.NoError(t, err)

	p.fetchAll(context.Background())

	require.Equal(t, uint64(1), p.FetchFailuresTotal(),
		"NY exhausted retries → counter==1")

	// NY should have been hit MaxAttempts==3 times.
	require.Equal(t, int64(3), nyHits.Load(), "NY hits = MaxAttempts")
	// LA should have been hit once and succeeded.
	require.Equal(t, int64(1), laHits.Load(), "LA hits = 1")

	// LA's OnFetch must have been invoked.
	got := laRaw.Load()
	require.NotNil(t, got, "LA OnFetch should have fired")
	require.Equal(t, `{"ok":"LA"}`, string(got.([]byte)))
	// NY's OnFetch must NOT have been invoked.
	require.Nil(t, nyRaw.Load(), "NY OnFetch should not fire on retry exhaustion")
}

// TestPoller_ClientTimeoutIsRetried guards the distinction between a
// parent-context cancellation (clean shutdown) and an http.Client.Timeout
// transient. Both surface as errors.Is(err, context.DeadlineExceeded), but
// only the latter — where the parent ctx is still live — must be retried and
// counted as a fetch failure rather than aborting the whole tick.
//
// The regression this pins: a single slow location was misclassified as
// shutdown, so it was never retried AND every remaining location in the tick
// was silently dropped with no bump to FetchFailuresTotal. Here NY hangs
// past the client Timeout while LA is healthy; we assert NY is retried the
// full envelope, LA is still fetched, and the failure counter increments.
func TestPoller_ClientTimeoutIsRetried(t *testing.T) {
	var nyHits, laHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lat := r.URL.Query().Get("latitude")
		if lat == "40.0000" {
			nyHits.Add(1)
			// Hang past the client Timeout, then bail when the request ctx
			// dies so the goroutine doesn't leak past srv.Close.
			select {
			case <-time.After(10 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		laHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":"LA"}`))
	}))
	t.Cleanup(srv.Close)

	var laRaw atomic.Value // []byte
	p, err := NewPoller(Options{
		BaseURL:      srv.URL,
		Model:        "gfs_seamless",
		ForecastDays: 3,
		PollInterval: time.Minute,
		Locations: []Location{
			{Name: "NY", Lat: 40, Lon: -73},
			{Name: "LA", Lat: 34, Lon: -118},
		},
		Logger: discardLogger(),
		// Client-side Timeout well below the server's 10s hang so Do returns
		// a DeadlineExceeded transient while the parent ctx stays live.
		Client:              &http.Client{Timeout: 40 * time.Millisecond},
		RetryInitialBackoff: 5 * time.Millisecond,
		RetryMaxBackoff:     20 * time.Millisecond,
		RetryMaxAttempts:    3,
		OnFetch: func(loc Location, raw []byte) {
			if loc.Name == "LA" {
				cp := make([]byte, len(raw))
				copy(cp, raw)
				laRaw.Store(cp)
			}
		},
	})
	require.NoError(t, err)

	p.fetchAll(context.Background())

	require.Equal(t, int64(3), nyHits.Load(),
		"client timeout must be retried the full envelope, not misclassified as shutdown")
	require.Equal(t, uint64(1), p.FetchFailuresTotal(),
		"exhausted client timeout must count as a fetch failure")

	require.Equal(t, int64(1), laHits.Load(),
		"a slow location must not abandon the remaining locations in the tick")
	got := laRaw.Load()
	require.NotNil(t, got, "LA OnFetch should have fired despite NY timing out")
	require.Equal(t, `{"ok":"LA"}`, string(got.([]byte)))
}

// TestPoller_BackoffEnvelope records server-hit timestamps and asserts the
// inter-hit delays approximate the configured backoff schedule (1x, 2x,
// 4x of InitialBackoff) within a tolerant jitter band. Uses a small
// initial backoff to keep test runtime under a second.
func TestPoller_BackoffEnvelope(t *testing.T) {
	const initial = 50 * time.Millisecond

	var (
		mu     sync.Mutex
		stamps []time.Time
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	p, err := NewPoller(Options{
		BaseURL:             srv.URL,
		Model:               "gfs_seamless",
		ForecastDays:        3,
		PollInterval:        time.Minute,
		Locations:           []Location{{Name: "NY", Lat: 40, Lon: -73}},
		Logger:              discardLogger(),
		RetryInitialBackoff: initial,
		RetryMaxBackoff:     30 * time.Second,
		RetryMaxAttempts:    4,
	})
	require.NoError(t, err)

	_, fetchErr := p.fetchWithRetry(context.Background(), Location{Name: "NY", Lat: 40, Lon: -73})
	require.Error(t, fetchErr)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 4, len(stamps), "MaxAttempts == 4 server hits")

	// Compute the three inter-attempt deltas.
	deltas := []time.Duration{
		stamps[1].Sub(stamps[0]),
		stamps[2].Sub(stamps[1]),
		stamps[3].Sub(stamps[2]),
	}
	// Expected schedule before jitter: 1x, 2x, 4x of initial. Jitter
	// adds +0..25%; small request handler latency adds a few ms on top.
	// Use a wide ±50% tolerance band per spec.
	expected := []time.Duration{initial, 2 * initial, 4 * initial}
	for i, d := range deltas {
		lower := time.Duration(float64(expected[i]) * 0.7) // -30% (handler latency can compress)
		upper := time.Duration(float64(expected[i]) * 1.6) // +60% (jitter + handler latency)
		require.GreaterOrEqualf(t, d, lower,
			"delta[%d]=%v below lower bound %v (expected ~%v)", i, d, lower, expected[i])
		require.LessOrEqualf(t, d, upper,
			"delta[%d]=%v above upper bound %v (expected ~%v)", i, d, upper, expected[i])
	}

	// Also assert approximate doubling: delta[1]/delta[0] ~ 2, delta[2]/delta[1] ~ 2.
	r1 := float64(deltas[1]) / float64(deltas[0])
	r2 := float64(deltas[2]) / float64(deltas[1])
	require.InDelta(t, 2.0, r1, 1.0, "delta[1]/delta[0] should approximate 2x, got %v", r1)
	require.InDelta(t, 2.0, r2, 1.0, "delta[2]/delta[1] should approximate 2x, got %v", r2)
}

// TestPoller_NetworkErrorRetries ensures transport-level failures (no
// HTTP response at all) are treated as retryable. Closing the listener
// before the request yields a connection-refused — exactly the "dial"
// shape the retry envelope is meant to cover.
func TestPoller_NetworkErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	addr := srv.URL
	srv.Close() // immediately close → any subsequent dial fails

	p, err := NewPoller(Options{
		BaseURL:             addr,
		Model:               "gfs_seamless",
		ForecastDays:        3,
		PollInterval:        time.Minute,
		Locations:           []Location{{Name: "NY", Lat: 40, Lon: -73}},
		Logger:              discardLogger(),
		RetryInitialBackoff: 5 * time.Millisecond,
		RetryMaxBackoff:     20 * time.Millisecond,
		RetryMaxAttempts:    3,
	})
	require.NoError(t, err)

	start := time.Now()
	body, fetchErr := p.fetchWithRetry(context.Background(), Location{Name: "NY", Lat: 40, Lon: -73})
	elapsed := time.Since(start)

	require.Error(t, fetchErr, "transport failure should bubble up after retries")
	require.Nil(t, body)
	// Should have spent at least 2 backoffs (~5+10=15ms minimum, allow a
	// generous floor for slow CI / dial timeout variance).
	require.GreaterOrEqual(t, elapsed, 10*time.Millisecond,
		"expected at least one retry sleep before giving up")
}
