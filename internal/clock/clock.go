// Package clock provides timestamp pairs (wall, monotonic) suitable for
// recording market data across heterogeneous sources, plus a [Clock]
// injection seam so downstream packages and their tests can swap wall
// time for a deterministic fake.
//
// Wall (UnixNano) is the only axis that can be compared across processes
// and machines — it is the cross-source sync axis. Monotonic is anchored
// to process start; use it to measure intervals without being affected by
// NTP, DST, or clock-set adjustments.
//
// # Injection seam
//
// Per ADR-0012, every wall-time read in internal/* flows through one of:
//
//  1. [Clock] passed in a constructor (preferred for new code)
//  2. `now func() time.Time` field on a struct, defaulting to time.Now
//     (acceptable for existing patterns; see internal/kalshi/auth.go)
//  3. [Now] for envelope timestamps (existing convention preserved)
//
// Production wires a [Wall] at the top of the binary and threads it
// downward. Tests construct a [Fake] and Advance / Set the time
// deterministically. See ADR-0012 for the full discipline; the
// clockcheck analyzer (`tools/clockcheck/`) enforces this at lint time.
package clock

import (
	"sync"
	"time"
)

// Stamp is a wall + monotonic timestamp pair.
type Stamp struct {
	Wall int64 `json:"t"` // unix-nanoseconds (UTC)
	Mono int64 `json:"m"` // process monotonic-nanoseconds
}

// Clock is the injection seam for wall-time access. Production callers
// pass [Wall]; tests pass a [Fake]. Implementations must be safe for
// concurrent use — both [Wall] and [FakeClock] are.
type Clock interface {
	// Now returns the current wall-clock time. Implementations should
	// preserve monotonic-clock readings where the host supports them
	// (the standard time.Now does; FakeClock does not, because tests
	// that need monotonic durations should use Advance to move the
	// clock by a known delta).
	Now() time.Time
}

// wallClock is the production [Clock] backed by time.Now. Singleton.
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// Wall returns the production [Clock]. The returned value is a singleton —
// it is safe to call repeatedly and to compare for equality.
func Wall() Clock { return wallClock{} }

// FakeClock is a deterministic [Clock] for tests. Initial time is set in
// [Fake]; advance with [FakeClock.Advance] or jump with [FakeClock.Set].
// All methods are safe for concurrent use.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// Fake returns a [FakeClock] anchored at initial. Use [FakeClock.Advance]
// or [FakeClock.Set] to step the clock forward in test code.
func Fake(initial time.Time) *FakeClock {
	return &FakeClock{now: initial}
}

// Now returns the FakeClock's current time. Safe for concurrent use.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the FakeClock forward by d. Negative durations panic so
// tests don't silently regress the clock; if you need to reset, use Set.
func (f *FakeClock) Advance(d time.Duration) {
	if d < 0 {
		panic("clock: FakeClock.Advance with negative duration; use Set to reset")
	}
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// Set jumps the FakeClock to t. Allows backward motion (use sparingly —
// usually a test bug; prefer constructing a fresh FakeClock per case).
func (f *FakeClock) Set(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

// processStart is the anchor for Mono in the [Stamp] returned by [Now].
// In production it is set to time.Now() the first time the package is
// loaded. Tests that need deterministic Mono values call [InitForTest].
var (
	processStart time.Time = time.Now()
	stampClock   Clock     = Wall()
)

// InitForTest reanchors the package's clock and monotonic origin for
// deterministic [Now] output. PRODUCTION CALLERS MUST NOT CALL THIS —
// it mutates package state and is only safe in tests where a single
// goroutine controls the package. The clockcheck analyzer's allowlist
// does not exempt callers of this function; review-time discipline is
// the only enforcement.
//
// Passing nil restores the wall clock and re-anchors processStart to
// the real time.Now(), which is useful for test teardown.
func InitForTest(c Clock) {
	if c == nil {
		c = Wall()
		processStart = time.Now()
		stampClock = c
		return
	}
	stampClock = c
	processStart = c.Now()
}

// Now returns a Stamp for the current instant. Both fields are read from
// the same clock observation so they refer to the same physical event.
//
// In production this reads time.Now(); in tests that have called
// [InitForTest] with a [FakeClock], it reads the fake. The Mono field
// is the elapsed duration from the most recent [InitForTest] anchor
// (or the process start, in production).
func Now() Stamp {
	now := stampClock.Now()
	return Stamp{
		Wall: now.UnixNano(),
		Mono: int64(now.Sub(processStart)),
	}
}

// SinceStart returns the duration elapsed since the package was first
// loaded (or since the most recent [InitForTest] anchor in tests).
func SinceStart() time.Duration { return stampClock.Now().Sub(processStart) }
