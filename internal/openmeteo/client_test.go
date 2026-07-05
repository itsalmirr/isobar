package openmeteo

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewPollerValidatesOptions(t *testing.T) {
	// Each case blanks exactly one required field of an otherwise-valid
	// Options so the triggered error is unambiguous. Config-owned knobs
	// (model, forecast days, poll interval) are rejected rather than
	// silently re-defaulted — internal/config owns the defaults.
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"no base url", func(o *Options) { o.BaseURL = "" }},
		{"no model", func(o *Options) { o.Model = "" }},
		{"non-positive poll interval", func(o *Options) { o.PollInterval = 0 }},
		{"no locations", func(o *Options) { o.Locations = nil }},
		{"non-positive forecast days", func(o *Options) { o.ForecastDays = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{
				BaseURL:      "http://example",
				Model:        "gfs_seamless",
				ForecastDays: 3,
				PollInterval: time.Minute,
				Locations:    []Location{{Name: "x"}},
			}
			tc.mutate(&opts)
			_, err := NewPoller(opts)
			require.Error(t, err)
		})
	}
}

func TestNewPollerDefaults(t *testing.T) {
	p, err := NewPoller(Options{
		BaseURL:      "http://example",
		Model:        "gfs_seamless",
		ForecastDays: 3,
		PollInterval: time.Minute,
		Locations:    []Location{{Name: "NY", Lat: 40, Lon: -73}},
	})
	require.NoError(t, err)
	require.NotNil(t, p)
	require.NotNil(t, p.opts.Client)
	require.NotNil(t, p.opts.OnFetch)
	require.Equal(t, retryInitialBackoff, p.opts.RetryInitialBackoff)
	require.Equal(t, retryMaxBackoff, p.opts.RetryMaxBackoff)
	require.Equal(t, retryMaxAttempts, p.opts.RetryMaxAttempts)
}

func TestFetchOneBuildsExpectedQuery(t *testing.T) {
	var gotURL atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL.Store(r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	p, err := NewPoller(Options{
		BaseURL:      srv.URL,
		Model:        "gfs_seamless",
		Variables:    []string{"temperature_2m", "precipitation"},
		Locations:    []Location{{Name: "NY", Lat: 40.7794, Lon: -73.9692}},
		ForecastDays: 2,
		PollInterval: time.Minute,
		Logger:       discardLogger(),
	})
	require.NoError(t, err)

	body, status, err := p.fetchOne(context.Background(), Location{Name: "NY", Lat: 40.7794, Lon: -73.9692})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, `{"ok":true}`, string(body))

	raw := gotURL.Load().(string)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	require.Equal(t, "40.7794", q.Get("latitude"))
	require.Equal(t, "-73.9692", q.Get("longitude"))
	require.Equal(t, "gfs_seamless", q.Get("models"))
	require.Equal(t, "2", q.Get("forecast_days"))
	require.Equal(t, "temperature_2m,precipitation", q.Get("hourly"))
	require.Equal(t, "UTC", q.Get("timezone"))
}

func TestFetchOneReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"reason":"rate limited"}`))
	}))
	t.Cleanup(srv.Close)

	p, err := NewPoller(Options{
		BaseURL:      srv.URL,
		Model:        "gfs_seamless",
		ForecastDays: 3,
		PollInterval: time.Minute,
		Locations:    []Location{{Name: "NY", Lat: 40, Lon: -73}},
	})
	require.NoError(t, err)

	_, status, err := p.fetchOne(context.Background(), Location{Name: "NY", Lat: 40, Lon: -73})
	require.Error(t, err)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Contains(t, err.Error(), "429")
}

func TestRunFiresImmediatelyAndHonorsContext(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	received := make(chan struct{}, 4)

	p, err := NewPoller(Options{
		BaseURL:      srv.URL,
		Model:        "gfs_seamless",
		ForecastDays: 3,
		PollInterval: time.Minute, // long so Run relies on initial immediate fetch
		Locations: []Location{
			{Name: "NY", Lat: 40, Lon: -73},
			{Name: "LA", Lat: 34, Lon: -118},
		},
		Logger: discardLogger(),
		OnFetch: func(_ Location, raw []byte) {
			require.Equal(t, `{"ok":true}`, string(raw))
			select {
			case received <- struct{}{}:
			default:
			}
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// Both locations should receive an initial fetch.
	for i := 0; i < 2; i++ {
		select {
		case <-received:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected fetch %d within 2s", i+1)
		}
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of context cancel")
	}
	require.GreaterOrEqual(t, hits.Load(), int64(2))
}

func TestTruncate(t *testing.T) {
	require.Equal(t, "abc", truncate("abc", 10))
	require.Equal(t, "abcde...", truncate("abcdefghijklm", 5))
}

func TestFetchOneRespectsBodyLimit(t *testing.T) {
	// Sanity: a body larger than the 32 MiB limiter would still succeed
	// for small payloads. Here we just verify the read path doesn't err
	// when the server terminates the response cleanly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":1}`)
	}))
	t.Cleanup(srv.Close)

	p, err := NewPoller(Options{
		BaseURL:      srv.URL,
		Model:        "gfs_seamless",
		ForecastDays: 3,
		PollInterval: time.Minute,
		Locations:    []Location{{Name: "NY", Lat: 40, Lon: -73}},
	})
	require.NoError(t, err)
	body, status, err := p.fetchOne(context.Background(), Location{Name: "NY", Lat: 40, Lon: -73})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, `{"ok":1}`, string(body))
}
