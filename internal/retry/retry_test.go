package retry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestJitter(t *testing.T) {
	require.Equal(t, time.Duration(0), Jitter(0))
	require.Equal(t, time.Duration(0), Jitter(-time.Second))

	for i := 0; i < 1000; i++ {
		out := Jitter(100 * time.Millisecond)
		require.GreaterOrEqual(t, out, 100*time.Millisecond)
		require.LessOrEqual(t, out, 125*time.Millisecond)
	}
}

func TestNextDoubles(t *testing.T) {
	require.Equal(t, 200*time.Millisecond, Next(100*time.Millisecond, 10*time.Second))
	require.Equal(t, 400*time.Millisecond, Next(200*time.Millisecond, 10*time.Second))
}

func TestNextCaps(t *testing.T) {
	require.Equal(t, time.Second, Next(800*time.Millisecond, time.Second))
	require.Equal(t, 5*time.Second, Next(5*time.Second, 5*time.Second))
}

func TestNextZeroPrev(t *testing.T) {
	require.Equal(t, 500*time.Millisecond, Next(0, 10*time.Second))
}

func TestDoSuccessFirstAttempt(t *testing.T) {
	var calls atomic.Int32
	err := Do(context.Background(), Config{}, discardLogger(), func(a int) error {
		calls.Add(1)
		require.Equal(t, 0, a)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
}

func TestDoRetriesUntilSuccess(t *testing.T) {
	var calls atomic.Int32
	err := Do(context.Background(), Config{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}, discardLogger(), func(a int) error {
		calls.Add(1)
		if a < 3 {
			return errors.New("transient")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(4), calls.Load())
}

func TestDoMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	err := Do(context.Background(), Config{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		MaxAttempts:    3,
	}, discardLogger(), func(a int) error {
		calls.Add(1)
		return errors.New("boom")
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max attempts")
	require.Contains(t, err.Error(), "boom")
	require.Equal(t, int32(3), calls.Load())
}

func TestDoHonorsContextBeforeFirstCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	err := Do(ctx, Config{}, discardLogger(), func(a int) error {
		calls.Add(1)
		return errors.New("never called")
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), calls.Load())
}

func TestDoHonorsContextBetweenAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	start := time.Now()
	err := Do(ctx, Config{
		InitialBackoff: 100 * time.Millisecond, // longer than timeout; retry will wait in select
		MaxBackoff:     time.Second,
	}, discardLogger(), func(a int) error {
		calls.Add(1)
		return errors.New("fail")
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 200*time.Millisecond)
	require.GreaterOrEqual(t, calls.Load(), int32(1))
}

// TestDoResetAfterLongAttempt: an attempt that ran at least ResetAfter
// before failing resets the attempt counter and backoff, so MaxAttempts
// bounds consecutive short-lived failures rather than total failures over
// the loop's lifetime. Nine "ran for 2 minutes, then dropped" attempts
// must not trip MaxAttempts=3.
func TestDoResetAfterLongAttempt(t *testing.T) {
	cur := time.Unix(1000, 0)
	var calls atomic.Int32
	err := Do(context.Background(), Config{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
		MaxAttempts:    3,
		ResetAfter:     time.Minute,
		Now:            func() time.Time { return cur },
	}, discardLogger(), func(int) error {
		if calls.Add(1) < 10 {
			cur = cur.Add(2 * time.Minute) // attempt "ran" longer than ResetAfter
			return errors.New("long-lived failure")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(10), calls.Load())
}

// TestDoResetAfterShortAttemptsStillBounded: with ResetAfter set but every
// attempt failing instantly, MaxAttempts must still bound the loop.
func TestDoResetAfterShortAttemptsStillBounded(t *testing.T) {
	cur := time.Unix(1000, 0)
	var calls atomic.Int32
	err := Do(context.Background(), Config{
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		MaxAttempts:    3,
		ResetAfter:     time.Minute,
		Now:            func() time.Time { return cur }, // never advances: every attempt is instant
	}, discardLogger(), func(int) error {
		calls.Add(1)
		return errors.New("flap")
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max attempts")
	require.Equal(t, int32(3), calls.Load())
}

func TestDoNilLogger(t *testing.T) {
	// Should not panic when logger is nil.
	err := Do(context.Background(), Config{
		InitialBackoff: time.Millisecond,
		MaxAttempts:    2,
	}, nil, func(a int) error { return errors.New("x") })
	require.Error(t, err)
}
