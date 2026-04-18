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
	backoff := cfg.InitialBackoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}
	maxBackoff := cfg.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := fn(attempt)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
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
