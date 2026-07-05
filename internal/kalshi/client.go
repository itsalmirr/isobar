package kalshi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrAuth is returned when the API rejects credentials (401 or 403).
// It is not retried — the credentials are wrong, the signature drifted
// beyond the server's clock skew tolerance, or the key was revoked.
// Callers (especially cmd/bot in Phase 1) must treat this as a terminal
// error and surface it for human attention rather than spinning.
var ErrAuth = errors.New("kalshi: authentication failed")

// Client is a typed REST client for the Kalshi Trade API v2.
// It includes per-request rate limiting and automatic retry on 429 responses.
type Client struct {
	baseURL    string
	pathPrefix string
	signer     *Signer
	httpClient *http.Client

	rateMu        sync.Mutex
	lastCall      time.Time
	minDelay      time.Duration // minimum delay between requests
	baseMinDelay  time.Duration // floor for minDelay decay (default 1s)
	consecutive2x int           // consecutive 2xx successes, used by decay

	// now is the wall-time source for throttle accounting AND the
	// Retry-After HTTP-date delta computation. Defaults to time.Now in
	// NewClient; tests override via SetNowForTest. Per ADR-0012
	// (time-source discipline).
	now func() time.Time
}

// newTransport builds the explicit *http.Transport used by NewClient.
// Every timeout is documented in internal/kalshi/CLIENT.md with the
// rationale for the chosen value. Pulled out so tests with httptest can
// share the configured transport without re-stating the magic numbers.
func newTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
}

// NewClient creates a Kalshi API client with built-in rate limiting.
func NewClient(baseURL string, signer *Signer) *Client {
	// Extract path prefix from baseURL for request signing.
	// e.g. "https://api.elections.kalshi.com/trade-api/v2" -> "/trade-api/v2"
	pathPrefix := ""
	if u, err := url.Parse(baseURL); err == nil {
		pathPrefix = u.Path
	}
	return &Client{
		baseURL:    baseURL,
		pathPrefix: pathPrefix,
		signer:     signer,
		httpClient: &http.Client{
			Transport: newTransport(),
			Timeout:   10 * time.Second,
		},
		minDelay:     1000 * time.Millisecond, // ~1 req/s to stay within Kalshi rate limits
		baseMinDelay: 1000 * time.Millisecond, // floor for success-driven decay
		now:          time.Now,
	}
}

// SetNowForTest overrides the wall-time source used by throttle accounting.
// Test-only — production code must not call this. Passing nil restores the
// default time.Now source.
func (c *Client) SetNowForTest(now func() time.Time) {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	if now == nil {
		c.now = time.Now
		return
	}
	c.now = now
}

// ResetRateLimit resets the adaptive rate limit delay back to the base rate.
// Call this after discovery to avoid carrying over inflated delays into polling.
func (c *Client) ResetRateLimit() {
	c.rateMu.Lock()
	c.minDelay = c.baseMinDelay
	c.consecutive2x = 0
	c.rateMu.Unlock()
}

// throttle enforces a minimum gap between API calls. It computes the
// required delay under the mutex, reserves the next slot by advancing
// lastCall, then releases the lock BEFORE waiting so concurrent
// callers can queue their own slots instead of serializing on the
// wait. Without this correction, N concurrent callers would wait
// N*minDelay total even though the rate limit is per-process.
//
// The wait is ctx-aware: a cancelled context returns immediately with
// the ctx error rather than holding the caller until the (potentially
// 2s) sleep elapses. Every caller threads ctx through, so this is
// drop-in.
func (c *Client) throttle(ctx context.Context) error {
	c.rateMu.Lock()
	now := c.now()
	gap := c.minDelay - now.Sub(c.lastCall)
	if gap < 0 {
		gap = 0
	}
	// Reserve our slot at now+gap; subsequent callers will queue past us.
	c.lastCall = now.Add(gap)
	c.rateMu.Unlock()
	if gap <= 0 {
		return nil
	}
	timer := time.NewTimer(gap)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- Market Data ---

// GetMarketsPage lists markets and returns the next pagination cursor when present.
func (c *Client) GetMarketsPage(ctx context.Context, params map[string]string) ([]Market, string, error) {
	var resp getMarketsResponse
	if err := c.get(ctx, "/markets", params, &resp); err != nil {
		return nil, "", err
	}
	return resp.Markets, resp.Cursor, nil
}

// DiscoverOpenTickers enumerates open market tickers for the given series
// prefixes. It paginates GET /markets for each series, deduplicates
// across pages, and filters to tickers that actually begin with the
// series prefix (the API occasionally returns near-matches).
//
// A small inter-page pause keeps the call friendly to the API; the
// context bounds total wall time. A non-nil logger emits one Debug
// per series with the running total.
func (c *Client) DiscoverOpenTickers(ctx context.Context, series []string, logger *slog.Logger) ([]string, error) {
	const (
		pageSize      = "200"
		interPagePace = 50 * time.Millisecond
	)
	seen := map[string]struct{}{}
	var out []string

	for _, s := range series {
		params := map[string]string{
			"series_ticker": s,
			"status":        "open",
			"limit":         pageSize,
		}
		cursor := ""
		for {
			if cursor != "" {
				params["cursor"] = cursor
			}
			markets, next, err := c.GetMarketsPage(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("series %s: %w", s, err)
			}
			for _, m := range markets {
				if !strings.HasPrefix(m.Ticker, s) {
					continue
				}
				if _, dup := seen[m.Ticker]; dup {
					continue
				}
				seen[m.Ticker] = struct{}{}
				out = append(out, m.Ticker)
			}
			// Guard against API returning a non-empty cursor with no markets:
			// would spin forever. One empty page is enough to declare "done".
			if next == "" || len(markets) == 0 {
				break
			}
			cursor = next
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(interPagePace):
			}
		}
		if logger != nil {
			logger.Debug("kalshi: series enumerated", "series", s, "running_total", len(out))
		}
	}
	return out, nil
}

// --- HTTP helpers ---

// perRequestTimeout is the outer per-call deadline applied to every HTTP
// call. It matches httpClient.Timeout so a caller that passes a parent
// context without its own deadline still gets bounded latency. Callers
// with tighter deadlines win because we only wrap if the inherited ctx
// has no deadline or one further out than perRequestTimeout.
const perRequestTimeout = 10 * time.Second

// withCallDeadline returns ctx unchanged if it already has a deadline
// tighter than perRequestTimeout; otherwise it wraps with WithTimeout.
// This ensures no naked client.Do can hang past perRequestTimeout even
// if the caller forgot a deadline.
func withCallDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if d, ok := ctx.Deadline(); ok && time.Until(d) <= perRequestTimeout { //nolint:clockcheck // ctx.Deadline returns wall time; comparison is intrinsically wall-bound
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, perRequestTimeout)
}

func (c *Client) get(ctx context.Context, path string, params map[string]string, out interface{}) error {
	ctx, cancel := withCallDeadline(ctx)
	defer cancel()

	fullPath := path
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		fullPath = path + "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+fullPath, nil)
	if err != nil {
		return fmt.Errorf("kalshi: build GET request: %w", err)
	}

	// Signature covers path without base URL or query string.
	if err := c.sign(req, path); err != nil {
		return err
	}

	return c.do(ctx, req, out)
}

func (c *Client) sign(req *http.Request, path string) error {
	if c.signer == nil {
		return nil
	}
	signPath := c.pathPrefix + path
	key, sig, ts, err := c.signer.AuthHeaders(req.Method, signPath)
	if err != nil {
		return err
	}
	req.Header.Set("KALSHI-ACCESS-KEY", key)
	req.Header.Set("KALSHI-ACCESS-SIGNATURE", sig)
	req.Header.Set("KALSHI-ACCESS-TIMESTAMP", ts)
	return nil
}

const (
	maxRetries       = 5
	maxAdaptiveDelay = 2 * time.Second
	// retryAfterCap bounds how long Retry-After can park a caller, so a
	// misbehaving server (or a 3600-second header) can't stall us for an
	// hour. RFC 9110 allows arbitrary values; this is our policy ceiling.
	retryAfterCap = 60 * time.Second
)

// parseRetryAfter implements RFC 9110 Retry-After parsing. Both forms
// are valid:
//
//   - delta-seconds: a non-negative integer (e.g. "120")
//   - HTTP-date:     an RFC 7231 IMF-fixdate (e.g. "Wed, 21 Oct 2015 ...")
//
// Returns (0, false) when the header is missing or unparseable. The
// caller is responsible for applying the policy cap (retryAfterCap).
func parseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	h := strings.TrimSpace(header)
	if h == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func (c *Client) doRaw(ctx context.Context, req *http.Request) ([]byte, error) {
	req.Header.Set("Accept", "application/json")
	isGET := req.Method == http.MethodGet

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return nil, err
		}

		// Clone request body for retries.
		if req.Body != nil && req.GetBody != nil {
			newBody, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("kalshi: reset request body: %w", err)
			}
			req.Body = newBody
		}

		// Re-sign on retry (timestamp may have drifted).
		if attempt > 0 {
			signPath := req.URL.Path
			// Strip the base URL path prefix that was already prepended in sign().
			if len(c.pathPrefix) > 0 && len(signPath) > len(c.pathPrefix) {
				signPath = signPath[len(c.pathPrefix):]
			}
			if err := c.sign(req, signPath); err != nil {
				return nil, err
			}
		}

		resp, err := c.httpClient.Do(req) //nolint:gosec // URL comes from our own baseURL + typed path helpers
		if err != nil {
			// Network error. Retry on GET (idempotent); fail fast on any
			// other method so we never double-submit an order.
			if isGET && attempt < maxRetries && ctx.Err() == nil {
				backoff := time.Duration(1<<uint(attempt+1)) * time.Second
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoff):
					continue
				}
			}
			return nil, fmt.Errorf("kalshi: HTTP request failed: %w", err)
		}

		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		retryAfterHdr := resp.Header.Get("Retry-After")
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("kalshi: read response body: %w", err)
		}

		status := resp.StatusCode

		// 401/403 are terminal — credentials, not transient.
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, fmt.Errorf("%w: API error %d: %s", ErrAuth, status, string(bodyBytes))
		}

		// 429 retries on every method (preserve existing behavior) — the
		// adaptive throttle is also applied here so subsequent calls are
		// gentler. 5xx retries only on GET (idempotent).
		retryable := false
		switch {
		case status == http.StatusTooManyRequests:
			retryable = attempt < maxRetries
		case isGET && (status == http.StatusBadGateway ||
			status == http.StatusServiceUnavailable ||
			status == http.StatusGatewayTimeout):
			retryable = attempt < maxRetries
		}

		if retryable {
			// Bit-shift backoff: 2^(attempt+1) seconds.
			backoff := time.Duration(1<<uint(attempt+1)) * time.Second

			if status == http.StatusTooManyRequests {
				// Adaptive throttle: bump minDelay so the next call is gentler.
				c.rateMu.Lock()
				if c.minDelay < maxAdaptiveDelay {
					c.minDelay *= 2
					if c.minDelay > maxAdaptiveDelay {
						c.minDelay = maxAdaptiveDelay
					}
				}
				c.consecutive2x = 0
				c.rateMu.Unlock()

				// Retry-After is a floor, not an override. RFC 9110 says
				// the server may indicate the minimum wait; we apply our
				// own backoff if it's larger, and cap at retryAfterCap so
				// a misbehaving server can't park us.
				if ra, ok := parseRetryAfter(retryAfterHdr, c.now()); ok {
					if ra > backoff {
						backoff = ra
					}
				}
				if backoff > retryAfterCap {
					backoff = retryAfterCap
				}
			}

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("kalshi: API error %d: %s", status, string(bodyBytes))
		}
		c.recordSuccess()
		return bodyBytes, nil
	}
	return nil, fmt.Errorf("kalshi: exhausted retries")
}

// recordSuccess implements the success-counter decay for minDelay.
// After every 2xx, increment consecutive2x. When the counter reaches
// successDecayThreshold, halve minDelay (floor at baseMinDelay) and
// reset the counter. Without this decay, a single 429 cluster would
// leave minDelay clamped at maxAdaptiveDelay (2s) for process lifetime
// because the only previous reset was the rarely-called ResetRateLimit().
//
// Threshold of 10 was chosen so callers see decay after a short healthy
// burst but transient single-call 2xx flickers don't immediately undo
// the protective throttle bump from a 429.
const successDecayThreshold = 10

func (c *Client) recordSuccess() {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	c.consecutive2x++
	if c.consecutive2x < successDecayThreshold {
		return
	}
	c.consecutive2x = 0
	if c.minDelay > c.baseMinDelay {
		c.minDelay /= 2
		if c.minDelay < c.baseMinDelay {
			c.minDelay = c.baseMinDelay
		}
	}
}

func (c *Client) do(ctx context.Context, req *http.Request, out interface{}) error {
	bodyBytes, err := c.doRaw(ctx, req)
	if err != nil {
		return err
	}

	if out != nil && len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, out); err != nil {
			return fmt.Errorf("kalshi: decode response: %w", err)
		}
	}
	return nil
}
