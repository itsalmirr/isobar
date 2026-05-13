package clock

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests in this file mutate package-level state via InitForTest. To stay
// deterministic across runs, each test that touches that state restores
// it in a t.Cleanup before returning. Tests that only exercise the Now/
// SinceStart wall-clock paths must run before any test reanchors the
// process start, so they are kept first in source order; the t.Cleanup
// pattern protects against test interleaving via -shuffle.

func resetPackageClock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { InitForTest(nil) })
}

func TestNowPopulatesBothFields(t *testing.T) {
	resetPackageClock(t)
	InitForTest(nil) // ensure we're on the wall clock
	s := Now()
	require.Positive(t, s.Wall, "wall must be > 0 on any real clock")
	require.GreaterOrEqual(t, s.Mono, int64(0), "mono is non-negative (since process start)")
}

func TestWallMatchesSystemWithinTolerance(t *testing.T) {
	resetPackageClock(t)
	InitForTest(nil) // wall clock
	// Sample before + after; a single Now() call must fall within the window.
	before := time.Now().UnixNano()
	s := Now()
	after := time.Now().UnixNano()
	require.GreaterOrEqual(t, s.Wall, before)
	require.LessOrEqual(t, s.Wall, after)
}

func TestMonoIsNonDecreasingUnderRapidCalls(t *testing.T) {
	resetPackageClock(t)
	InitForTest(nil)
	const N = 10_000
	last := int64(-1)
	for i := 0; i < N; i++ {
		s := Now()
		require.GreaterOrEqual(t, s.Mono, last, "monotonic must never regress")
		last = s.Mono
	}
}

func TestMonoNonDecreasingAcrossGoroutines(t *testing.T) {
	resetPackageClock(t)
	InitForTest(nil)
	const N = 1_000
	var wg sync.WaitGroup
	results := make([][]int64, 4)
	for g := range results {
		results[g] = make([]int64, N)
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < N; i++ {
				results[g][i] = Now().Mono
			}
		}(g)
	}
	wg.Wait()

	// Within each goroutine the mono value must not regress. (Across
	// goroutines there's no ordering — only within-thread is meaningful
	// for this property.)
	for _, series := range results {
		var last int64
		for _, v := range series {
			require.GreaterOrEqual(t, v, last)
			last = v
		}
	}
}

func TestSinceStartIsPositive(t *testing.T) {
	resetPackageClock(t)
	InitForTest(nil)
	// Sleep a tiny bit to ensure the wall clock has advanced past the
	// reanchor that InitForTest(nil) performed inside this test.
	time.Sleep(time.Millisecond)
	d := SinceStart()
	require.Greater(t, d, time.Duration(0), "process started before this test ran")
}

func TestSinceStartIsMonotonic(t *testing.T) {
	resetPackageClock(t)
	InitForTest(nil)
	a := SinceStart()
	time.Sleep(time.Millisecond)
	b := SinceStart()
	require.Greater(t, b, a)
}

// --- Clock interface (Wall + Fake) ---

func TestWallReturnsRealTime(t *testing.T) {
	c := Wall()
	before := time.Now()
	got := c.Now()
	after := time.Now()
	require.False(t, got.Before(before), "Wall().Now() must be >= time.Now() at call site")
	require.False(t, got.After(after), "Wall().Now() must be <= time.Now() after call site")
}

func TestFakeInitialNow(t *testing.T) {
	anchor := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	c := Fake(anchor)
	require.Equal(t, anchor, c.Now())
}

func TestFakeAdvance(t *testing.T) {
	anchor := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	c := Fake(anchor)
	c.Advance(2 * time.Hour)
	require.Equal(t, anchor.Add(2*time.Hour), c.Now())
	c.Advance(30 * time.Minute)
	require.Equal(t, anchor.Add(2*time.Hour+30*time.Minute), c.Now())
}

func TestFakeAdvanceZeroIsIdentity(t *testing.T) {
	anchor := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	c := Fake(anchor)
	c.Advance(0)
	require.Equal(t, anchor, c.Now())
}

func TestFakeAdvanceNegativePanics(t *testing.T) {
	c := Fake(time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC))
	require.PanicsWithValue(t,
		"clock: FakeClock.Advance with negative duration; use Set to reset",
		func() { c.Advance(-time.Second) },
	)
}

func TestFakeSet(t *testing.T) {
	anchor := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	c := Fake(anchor)
	target := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Set(target)
	require.Equal(t, target, c.Now())
}

func TestFakeIsSafeForConcurrentUse(t *testing.T) {
	anchor := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	c := Fake(anchor)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.Advance(time.Millisecond) }()
		go func() { defer wg.Done(); _ = c.Now() }()
	}
	wg.Wait()
	// After 16 advances of 1ms each, the clock should be exactly +16ms.
	require.Equal(t, anchor.Add(16*time.Millisecond), c.Now())
}

func TestInitForTestSwapsClock(t *testing.T) {
	resetPackageClock(t)
	anchor := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	f := Fake(anchor)
	InitForTest(f)
	s := Now()
	require.Equal(t, anchor.UnixNano(), s.Wall)
	require.Equal(t, int64(0), s.Mono, "Mono is zero immediately after InitForTest(fake)")

	f.Advance(5 * time.Second)
	s2 := Now()
	require.Equal(t, anchor.Add(5*time.Second).UnixNano(), s2.Wall)
	require.Equal(t, int64(5*time.Second), s2.Mono)
}

func TestInitForTestNilRestoresWall(t *testing.T) {
	resetPackageClock(t)
	f := Fake(time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC))
	InitForTest(f)
	require.Equal(t, f.Now().UnixNano(), Now().Wall)

	InitForTest(nil)
	wallSample := time.Now().UnixNano()
	got := Now().Wall
	// Within a tight window of the wall-clock sample either side.
	require.InDelta(t, wallSample, got, float64(time.Second), "InitForTest(nil) should restore wall clock")
}

func TestSinceStartWithFakeClock(t *testing.T) {
	resetPackageClock(t)
	anchor := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	f := Fake(anchor)
	InitForTest(f)
	require.Equal(t, time.Duration(0), SinceStart())
	f.Advance(42 * time.Second)
	require.Equal(t, 42*time.Second, SinceStart())
}
