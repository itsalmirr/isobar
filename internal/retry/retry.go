// Package retry provides a small, context-aware exponential-backoff retry
// helper that is shared by every component that needs to run under
// unreliable I/O — WS subscribers, REST pollers, execution adapters.
//
// Semantics:
//   - Do(ctx, cfg, log, fn) calls fn(attempt) repeatedly until it returns
//     nil, the context is cancelled, or MaxAttempts is reached.
//   - Between failed attempts it sleeps InitialBackoff, doubling up to
//     MaxBackoff, with +0..25% random jitter so peers don't thunder.
//   - ctx cancellation is treated as clean shutdown and returns nil.
//
// This exists so the same reconnect/backoff loop is not reinvented in
// every package; see `internal/kalshi/ws.go` for the canonical use.
package retry

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Config tunes the retry loop. Zero values are replaced by sensible
// defaults (500ms initial, 30s cap, unlimited attempts).
type Config struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxAttempts    int // 0 = unlimited

	// ResetAfter, when > 0, treats any attempt that ran at least this
	// long before failing as evidence the operation was healthy (e.g. a
	// WS connection that streamed for an hour, then dropped). Backoff
	// and the attempt counter reset, so the next retry waits
	// InitialBackoff instead of whatever earlier flapping accumulated,
	// and MaxAttempts bounds consecutive short-lived failures rather
	// than total failures over the loop's lifetime. Without this, a
	// long-running consumer that ever reached MaxBackoff stays at
	// MaxBackoff forever (observed in the 2026-05 recorder outage:
	// every reconnect paid the full 30s cap for days).
	ResetAfter time.Duration

	// Now is the clock seam used to measure attempt duration for
	// ResetAfter (ADR-0012 convention #2). Zero uses time.Now.
	Now func() time.Time
}

// Jitter returns d + a uniform random in [0, d/4]. Returns 0 for d<=0.
func Jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d + time.Duration(rand.Int64N(int64(d/4)+1))
}

// Next returns the next backoff duration: prev*2 capped at max.
// It does NOT apply jitter; callers compose with Jitter.
func Next(prev, max time.Duration) time.Duration {
	if prev <= 0 {
		return 500 * time.Millisecond
	}
	next := prev * 2
	if max > 0 && next > max {
		next = max
	}
	return next
}

// Do runs fn repeatedly until it returns nil, ctx is cancelled, or
// MaxAttempts is reached. On clean context cancellation it returns nil.
// On MaxAttempts exhaustion it returns the last error wrapped.
//
// logger may be nil; when non-nil, a Warn is emitted between attempts
// with (err, wait, attempt) so operators see the backoff in the log.
func Do(ctx context.Context, cfg Config, logger *slog.Logger, fn func(attempt int) error) error {
	initial := cfg.InitialBackoff
	if initial <= 0 {
		initial = 500 * time.Millisecond
	}
	backoff := initial
	maxBackoff := cfg.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil //nolint:nilerr // ctx cancel is clean shutdown per godoc
		}
		started := now()
		err := fn(attempt)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil //nolint:nilerr // ctx cancel is clean shutdown per godoc
		}
		if cfg.ResetAfter > 0 && now().Sub(started) >= cfg.ResetAfter {
			// Long-lived attempt: the old backoff ramp no longer
			// describes the current failure; start the ladder over.
			attempt = 0
			backoff = initial
		}
		attempt++
		if cfg.MaxAttempts > 0 && attempt >= cfg.MaxAttempts {
			return fmt.Errorf("retry: max attempts exceeded: %w", err)
		}
		wait := Jitter(backoff)
		if logger != nil {
			logger.Warn("retry: backing off", "err", err, "wait", wait, "attempt", attempt)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = Next(backoff, maxBackoff)
	}
}
