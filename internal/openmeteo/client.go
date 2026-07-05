// Package openmeteo polls the Open-Meteo ensemble forecast API.
//
// The free tier exposes the 31-member NOAA GFS ensemble — the data that
// lets us price Kalshi weather brackets against a probabilistic distribution
// instead of a single point forecast. Responses are emitted as raw JSON
// through OnFetch so the caller (recorder) can persist them unchanged.
//
// Reliability:
//
// Open-Meteo is a free public endpoint with occasional 5xx storms and rare
// 429s. To avoid silent multi-hour data gaps on transient outages, each
// per-location fetch runs inside a bounded exponential-backoff envelope:
//
//   - InitialBackoff = 1s, MaxBackoff = 30s, MaxAttempts = 4, multiplier 2.0
//   - Jitter = +0..25% via internal/retry.Jitter (the same shape kalshi-ws
//     uses for reconnect, so all our backoffs thunder-band uniformly)
//   - Retry on: HTTP 5xx, HTTP 429, and network errors (dial / timeout /
//     connection reset / EOF). NOT retried: HTTP 4xx other than 429
//     (those mean we sent a bad request — retrying is wasted budget)
//   - Honors ctx mid-retry: ctx cancellation returns immediately
//
// A full circuit-breaker is Phase 6 work; today we just take the easy
// 80%: bounded retries with structured per-attempt logging so operators
// can see exactly where in the envelope a fetch died.
package openmeteo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wh0o0pz/isobar/internal/retry"
)

// Retry envelope constants for the per-location fetch. Documented here
// so log readers and on-call ops can correlate observed dwell with config.
const (
	retryInitialBackoff = 1 * time.Second
	retryMaxBackoff     = 30 * time.Second
	retryMaxAttempts    = 4 // worst-case dwell ≈ 1+2+4s ≈ 7s before giving up on this tick
)

// Location identifies one geographic point to poll.
type Location struct {
	Name string
	Lat  float64
	Lon  float64
}

// Options configures the poller.
//
// BaseURL, Model, Locations, and positive ForecastDays/PollInterval are
// required — NewPoller rejects zero values instead of re-defaulting
// them. Production defaults (model, forecast days, the >=1m
// rate-limit floor on poll interval) live in internal/config
// (Defaults/Validate), the single source of truth.
type Options struct {
	BaseURL      string
	Model        string   // e.g., "gfs_seamless"
	Variables    []string // hourly variables, e.g., ["temperature_2m"]
	Locations    []Location
	ForecastDays int
	PollInterval time.Duration

	Logger *slog.Logger
	Client *http.Client
	// OnFetch is invoked for each successful fetch with the raw JSON
	// response body. The byte slice is freshly allocated per call.
	OnFetch func(loc Location, raw []byte)

	// RetryInitialBackoff / RetryMaxBackoff / RetryMaxAttempts override the
	// per-location fetch retry envelope. Zero values inherit the package
	// defaults (1s / 30s / 4). Exposed so tests can shrink the envelope.
	RetryInitialBackoff time.Duration
	RetryMaxBackoff     time.Duration
	RetryMaxAttempts    int
}

// Poller emits raw JSON ensemble responses on a fixed interval.
type Poller struct {
	opts   Options
	logger *slog.Logger

	// fetchFailuresTotal counts location-level retry exhaustions across
	// the poller's lifetime. Atomic so tests and future metrics exporters
	// can read it without taking a lock.
	fetchFailuresTotal atomic.Uint64

	// now is the wall-time source for per-attempt latency measurement.
	// Defaults to time.Now in NewPoller; tests override via SetNowForTest
	// when they need deterministic latency values. Per ADR-0012.
	now func() time.Time
}

// NewPoller validates options and returns a Poller. Call Run to start.
// Config-owned knobs (model, forecast days, poll interval) must arrive
// populated; the interval check here is only the mechanical floor that
// keeps time.NewTicker from panicking — the 1m rate-limit policy is
// enforced by config.Validate.
func NewPoller(opts Options) (*Poller, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("openmeteo: base_url required")
	}
	if opts.Model == "" {
		return nil, errors.New("openmeteo: model required")
	}
	if opts.PollInterval <= 0 {
		return nil, errors.New("openmeteo: poll_interval must be > 0")
	}
	if len(opts.Locations) == 0 {
		return nil, errors.New("openmeteo: at least one location required")
	}
	if opts.ForecastDays <= 0 {
		return nil, errors.New("openmeteo: forecast_days must be >= 1")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.OnFetch == nil {
		opts.OnFetch = func(Location, []byte) {}
	}
	if opts.RetryInitialBackoff <= 0 {
		opts.RetryInitialBackoff = retryInitialBackoff
	}
	if opts.RetryMaxBackoff <= 0 {
		opts.RetryMaxBackoff = retryMaxBackoff
	}
	if opts.RetryMaxAttempts <= 0 {
		opts.RetryMaxAttempts = retryMaxAttempts
	}
	return &Poller{
		opts:   opts,
		logger: opts.Logger.With("component", "openmeteo"),
		now:    time.Now,
	}, nil
}

// FetchFailuresTotal returns the cumulative count of locations that
// exhausted their retry envelope without a successful fetch. Useful for
// tests, dashboards, and alerting hooks. Safe to call concurrently.
func (p *Poller) FetchFailuresTotal() uint64 {
	return p.fetchFailuresTotal.Load()
}

// SetNowForTest overrides the poller's wall-time source. Test-only —
// production code must not call this. Passing nil restores time.Now.
func (p *Poller) SetNowForTest(now func() time.Time) {
	if now == nil {
		p.now = time.Now
		return
	}
	p.now = now
}

// Run polls locations on a fixed interval until ctx is cancelled.
// The first fetch happens immediately so Phase 0 has data without waiting
// for the first tick.
func (p *Poller) Run(ctx context.Context) error {
	t := time.NewTicker(p.opts.PollInterval)
	defer t.Stop()

	p.fetchAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			p.fetchAll(ctx)
		}
	}
}

func (p *Poller) fetchAll(ctx context.Context) {
	for _, loc := range p.opts.Locations {
		if ctx.Err() != nil {
			return
		}
		raw, err := p.fetchWithRetry(ctx, loc)
		if err != nil {
			// Parent-context cancellation isn't a fetch failure — it's clean
			// shutdown. Don't bump the counter or warn loudly, and stop the
			// cycle. We gate on ctx.Err()!=nil so a client-side http timeout
			// (which also satisfies errors.Is(DeadlineExceeded) while the
			// parent ctx is live) is instead treated as a real fetch failure:
			// bump the counter and continue to the remaining locations rather
			// than silently abandoning the rest of the tick.
			if ctx.Err() != nil {
				return
			}
			p.fetchFailuresTotal.Add(1)
			// Note: per-attempt details already logged inside fetchWithRetry.
			// This is the aggregate "we gave up" line.
			continue
		}
		p.opts.OnFetch(loc, raw)
	}
}

// fetchWithRetry runs fetchOne inside a bounded exponential-backoff
// envelope. On success returns the raw body. On retry exhaustion returns
// the last error. On context cancellation returns ctx.Err() so the caller
// can distinguish shutdown from failure.
//
// Non-retryable cases (HTTP 4xx other than 429) bail out on the first
// failure — the request is malformed and re-sending it changes nothing.
func (p *Poller) fetchWithRetry(ctx context.Context, loc Location) ([]byte, error) {
	cfg := retry.Config{
		InitialBackoff: p.opts.RetryInitialBackoff,
		MaxBackoff:     p.opts.RetryMaxBackoff,
		MaxAttempts:    p.opts.RetryMaxAttempts,
	}

	var (
		body            []byte
		nonRetryableErr error
		lastErr         error
		lastStatus      int
		attemptsUsed    int
	)

	// Pass nil logger to retry.Do — we emit our own structured per-attempt
	// logs below with the richer field set the spec requires.
	doErr := retry.Do(ctx, cfg, nil, func(attempt int) error {
		attemptsUsed = attempt + 1

		start := p.now()
		b, statusCode, fetchErr := p.fetchOne(ctx, loc)
		latency := p.now().Sub(start)
		lastStatus = statusCode

		if fetchErr == nil {
			// Success path: keep silent unless the operator opted into a
			// trace-level handler. No log here per spec — success is
			// boring, only failures get a per-attempt line.
			body = b
			return nil
		}
		lastErr = fetchErr

		// Compute next_backoff_ms for the log line. Mirrors retry.Do's
		// internal sleep: Jitter(prev) on this iteration's backoff. We
		// don't know the exact randomized value Do will pick, so we log
		// the deterministic ceiling (without jitter) for readability.
		nextBackoff := nextBackoffFor(attempt, p.opts.RetryInitialBackoff, p.opts.RetryMaxBackoff)
		// On the final attempt there is no "next" — clamp to 0.
		if attempt+1 >= p.opts.RetryMaxAttempts {
			nextBackoff = 0
		}

		// Parent-context cancellation propagates immediately as
		// non-retryable so the caller can return ctx.Err() upstream. We gate
		// on ctx.Err()!=nil because an http.Client.Timeout firing also yields
		// an errors.Is(DeadlineExceeded) error while the parent ctx is still
		// live — that is a client-side transient, not shutdown, and must fall
		// through to the retryable transport path below (statusCode==0).
		if ctx.Err() != nil && (errors.Is(fetchErr, context.Canceled) || errors.Is(fetchErr, context.DeadlineExceeded)) {
			nonRetryableErr = fetchErr
			p.logAttempt(ctx, slog.LevelDebug, "openmeteo fetch attempt", loc, attempt, latency, statusCode, fetchErr, 0)
			return nil
		}

		if !isRetryable(fetchErr, statusCode) {
			nonRetryableErr = fetchErr
			p.logAttempt(ctx, slog.LevelDebug, "openmeteo fetch attempt", loc, attempt, latency, statusCode, fetchErr, 0)
			return nil
		}

		// Retryable failure — log at debug and let retry.Do sleep + recurse.
		p.logAttempt(ctx, slog.LevelDebug, "openmeteo fetch attempt", loc, attempt, latency, statusCode, fetchErr, nextBackoff)
		return fetchErr
	})

	// Surface ctx cancellation as an explicit error so the caller can
	// tell shutdown apart from a real failure. retry.Do swallows ctx
	// cancellation and returns nil.
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}

	if nonRetryableErr != nil {
		// One-shot bail-out: log the final warn so ops sees a single line
		// per exhausted location regardless of why we stopped.
		p.logAttempt(ctx, slog.LevelWarn, "openmeteo fetch gave up", loc, attemptsUsed-1, 0, lastStatus, nonRetryableErr, 0)
		return nil, nonRetryableErr
	}

	if doErr != nil {
		// Retry envelope exhausted.
		p.logger.Warn("openmeteo fetch gave up",
			"location_code", loc.Name,
			"attempts_used", attemptsUsed,
			"status_code", lastStatus,
			"err", lastErr,
		)
		return nil, doErr
	}

	if body == nil {
		// Defensive: shouldn't happen — Do returned nil and we recorded no
		// non-retryable err, so the inner fn must have set body. If not,
		// surface the most recent error rather than returning a nil body
		// to OnFetch.
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("openmeteo: empty body without error")
	}
	return body, nil
}

// logAttempt emits a structured per-attempt log line. Centralized so the
// field set stays consistent and easy to grep.
func (p *Poller) logAttempt(ctx context.Context, level slog.Level, msg string, loc Location, attempt int, latency time.Duration, statusCode int, err error, nextBackoff time.Duration) {
	attrs := []any{
		"location_code", loc.Name,
		"attempt", attempt,
		"latency_ms", latency.Milliseconds(),
	}
	if statusCode != 0 {
		attrs = append(attrs, "status_code", statusCode)
	}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	if nextBackoff > 0 {
		attrs = append(attrs, "next_backoff_ms", nextBackoff.Milliseconds())
	}
	p.logger.Log(ctx, level, msg, attrs...)
}

// nextBackoffFor returns the (un-jittered) backoff that would be used
// before the (attempt+1)th call. Mirrors retry.Next's stepping logic so
// our per-attempt log lines stay in sync with retry.Do's actual sleeps.
func nextBackoffFor(attempt int, initial, max time.Duration) time.Duration {
	b := initial
	for range attempt {
		b = retry.Next(b, max)
	}
	return b
}

// isRetryable returns true for transient I/O failures: network errors
// (dial / timeout / reset / EOF — anything where statusCode is 0 because
// we never got a response) and HTTP 5xx / 429. Returns false for HTTP
// 4xx other than 429 (we sent a bad request).
//
// Parent-context cancellation is deliberately NOT special-cased here: the
// sole caller (fetchWithRetry) already filters genuine shutdown via
// ctx.Err()!=nil upstream, so any DeadlineExceeded reaching this function
// is an http.Client.Timeout transient — a statusCode==0 transport failure
// that must remain retryable to honor the reliability contract in the
// package doc.
func isRetryable(err error, statusCode int) bool {
	if err == nil {
		return false
	}
	if statusCode == 0 {
		// Transport-level error: dial failure, connection reset, EOF,
		// client-side timeout. Always retryable — assume transient.
		return true
	}
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	if statusCode >= 500 && statusCode <= 599 {
		return true
	}
	// 4xx other than 429 → bail.
	return false
}

// fetchOne issues a single HTTP request and returns (body, statusCode, err).
// statusCode is 0 when the transport call itself failed (i.e. we never
// got an HTTP response). On non-2xx, body is the (truncated) response
// payload — useful for the final warn log on 4xx.
func (p *Poller) fetchOne(ctx context.Context, loc Location) ([]byte, int, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(loc.Lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(loc.Lon, 'f', 4, 64))
	q.Set("models", p.opts.Model)
	q.Set("forecast_days", strconv.Itoa(p.opts.ForecastDays))
	q.Set("hourly", strings.Join(p.opts.Variables, ","))
	q.Set("timezone", "UTC")

	endpoint := p.opts.BaseURL + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.opts.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	return body, resp.StatusCode, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
