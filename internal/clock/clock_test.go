package clock

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNowPopulatesBothFields(t *testing.T) {
	s := Now()
	require.Positive(t, s.Wall, "wall must be > 0 on any real clock")
	require.GreaterOrEqual(t, s.Mono, int64(0), "mono is non-negative (since process start)")
}

func TestWallMatchesSystemWithinTolerance(t *testing.T) {
	// Sample before + after; a single Now() call must fall within the window.
	before := time.Now().UnixNano()
	s := Now()
	after := time.Now().UnixNano()
	require.GreaterOrEqual(t, s.Wall, before)
	require.LessOrEqual(t, s.Wall, after)
}

func TestMonoIsNonDecreasingUnderRapidCalls(t *testing.T) {
	const N = 10_000
	last := int64(-1)
	for i := 0; i < N; i++ {
		s := Now()
		require.GreaterOrEqual(t, s.Mono, last, "monotonic must never regress")
		last = s.Mono
	}
}

func TestMonoNonDecreasingAcrossGoroutines(t *testing.T) {
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
	d := SinceStart()
	require.Greater(t, d, time.Duration(0), "process started before this test ran")
}

func TestSinceStartIsMonotonic(t *testing.T) {
	a := SinceStart()
	time.Sleep(time.Millisecond)
	b := SinceStart()
	require.Greater(t, b, a)
}
