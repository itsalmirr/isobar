package bayesian

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadHourlyCSV_RoundTrip covers UTC parse, ascending sort, and the
// best-effort skip of missing ("M"), trace ("T"), and unparseable rows.
// Sole test for this loader — the fitter's mirror copy was deleted.
func TestLoadHourlyCSV_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "KNYC-hourly.csv")
	csv := "station,valid,tmpf\n" +
		"NYC,2024-05-01 02:51,52.00\n" +
		"NYC,2024-05-01 00:51,54.00\n" + // out of order; loader sorts
		"NYC,2024-05-01 01:51,M\n" + // missing
		"NYC,2024-05-01 01:30,T\n" + // trace
		"NYC,2024-05-01 03:39,52.00\n" +
		"NYC,bogus_ts,99\n" // unparseable ts
	require.NoError(t, os.WriteFile(path, []byte(csv), 0o600))

	hr, err := LoadHourlyCSV(path)
	require.NoError(t, err)
	require.Len(t, hr, 3)
	for i := 1; i < len(hr); i++ {
		assert.True(t, hr[i-1].TS.Before(hr[i].TS), "sorted ascending")
	}
	assert.Equal(t, 54.0, hr[0].TmpF)
	assert.Equal(t, 52.0, hr[1].TmpF)
	assert.Equal(t, 52.0, hr[2].TmpF)
	assert.Equal(t, time.UTC, hr[0].TS.Location())
}

// TestLoadHourlyIndex_MissingFile asserts the dir-scan tolerates a
// missing station (no fatal, just no entry).
func TestLoadHourlyIndex_MissingFile(t *testing.T) {
	dir := t.TempDir()
	// Only write KNYC; the other three should be silently skipped.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "KNYC-hourly.csv"),
		[]byte("station,valid,tmpf\nNYC,2024-05-01 12:00,70\n"), 0o600))

	idx, err := LoadHourlyIndex(dir)
	require.NoError(t, err)
	require.NotNil(t, idx)
	assert.Len(t, idx.byStation, 1)
	assert.Contains(t, idx.byStation, "KNYC")
}

// TestHourlyIndex_RunningExtremum_TMAX covers the happy path: an
// asOf time mid-day, running max of obs up to that point, h_remain
// computed in the station's local TZ.
func TestHourlyIndex_RunningExtremum_TMAX(t *testing.T) {
	dir := t.TempDir()
	// Day window: KNYC 2024-05-01 00:00 EDT = 2024-05-01 04:00 UTC,
	// closes 2024-05-02 04:00 UTC. Rising obs 50, 55, 60, 65, 70, 70...
	rows := "station,valid,tmpf\n"
	for h := 0; h < 12; h++ {
		ts := time.Date(2024, 5, 1, 4+h, 0, 0, 0, time.UTC).Format("2006-01-02 15:04")
		rows += fmt.Sprintf("NYC,%s,%.0f\n", ts, 50.0+float64(h)*2)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "KNYC-hourly.csv"), []byte(rows), 0o600))

	idx, err := LoadHourlyIndex(dir)
	require.NoError(t, err)

	// asOf = 2024-05-01 12:00 EDT = 16:00Z. The fixture wrote 12 obs
	// at hours 0..11 of the day (04:00Z..15:00Z, last value 50+11*2=72).
	// No obs at exactly 16:00Z exists, so running max = 72.
	asOf := time.Date(2024, 5, 1, 16, 0, 0, 0, time.UTC)
	r, hRemain, ok := idx.RunningExtremum("KNYC", "tmax", asOf)
	require.True(t, ok)
	assert.Equal(t, 72.0, r)
	// h_remain = ceil((dayClose - asOf) / 1h) = ceil(12h / 1h) = 12.
	assert.Equal(t, 12, hRemain)
}

// TestHourlyIndex_RunningExtremum_TMIN inverts the test for tmin
// semantics (running min).
func TestHourlyIndex_RunningExtremum_TMIN(t *testing.T) {
	dir := t.TempDir()
	// Falling overnight: 60, 58, 56, 54, 52, 50 (TMIN at hour 5),
	// then rising again 52, 56, ...
	temps := []float64{60, 58, 56, 54, 52, 50, 52, 56, 60, 64, 68, 70}
	rows := "station,valid,tmpf\n"
	for h, v := range temps {
		ts := time.Date(2024, 5, 1, 4+h, 0, 0, 0, time.UTC).Format("2006-01-02 15:04")
		rows += fmt.Sprintf("NYC,%s,%.0f\n", ts, v)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "KNYC-hourly.csv"), []byte(rows), 0o600))

	idx, err := LoadHourlyIndex(dir)
	require.NoError(t, err)

	// asOf right after TMIN (hour 6 local, includes obs at hour 6 = 52).
	// Running min so far = 50.
	asOf := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	r, hRemain, ok := idx.RunningExtremum("KNYC", "tmin", asOf)
	require.True(t, ok)
	assert.Equal(t, 50.0, r)
	assert.Equal(t, 18, hRemain) // 24 - 6 = 18h remaining
}

// TestHourlyIndex_RunningExtremum_ColdStart returns ok=false when no
// obs have landed yet in the civil day.
func TestHourlyIndex_RunningExtremum_ColdStart(t *testing.T) {
	dir := t.TempDir()
	// One obs at 14:00 EDT = 18:00Z. Querying at 06:00 EDT = 10:00Z
	// must return ok=false (no obs yet).
	rows := "station,valid,tmpf\nNYC,2024-05-01 18:00,70\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "KNYC-hourly.csv"), []byte(rows), 0o600))

	idx, err := LoadHourlyIndex(dir)
	require.NoError(t, err)

	asOf := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	_, _, ok := idx.RunningExtremum("KNYC", "tmax", asOf)
	assert.False(t, ok)
}

// TestHourlyIndex_RunningExtremum_UnknownStation guards the nil-deref
// path and the "we don't know about this station" path.
func TestHourlyIndex_RunningExtremum_UnknownStation(t *testing.T) {
	var nilIdx *HourlyIndex
	_, _, ok := nilIdx.RunningExtremum("KNYC", "tmax", time.Now()) //nolint:clockcheck // test-only
	assert.False(t, ok, "nil index returns ok=false")

	emptyIdx := &HourlyIndex{byStation: map[string]stationHourly{}}
	_, _, ok = emptyIdx.RunningExtremum("KORD", "tmax", time.Now()) //nolint:clockcheck // test-only
	assert.False(t, ok, "unknown station returns ok=false")
}

// TestHourlyIndex_RunningExtremum_DST verifies the civil-day boundary
// computation works during a DST transition. The same test as the
// fitter's DST test, but at the index API.
func TestHourlyIndex_RunningExtremum_DST(t *testing.T) {
	dir := t.TempDir()
	// 2024-11-03 fall-back: NY day is 25 hours long, 04:00Z 11-03 to 05:00Z 11-04.
	rows := "station,valid,tmpf\n"
	for h := 0; h < 25; h++ {
		ts := time.Date(2024, 11, 3, 4+h, 0, 0, 0, time.UTC).Format("2006-01-02 15:04")
		rows += fmt.Sprintf("NYC,%s,%.0f\n", ts, 50.0+float64(h))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "KNYC-hourly.csv"), []byte(rows), 0o600))

	idx, err := LoadHourlyIndex(dir)
	require.NoError(t, err)

	// asOf = mid-day across the DST boundary. Running max should
	// accumulate all observed obs.
	asOf := time.Date(2024, 11, 3, 16, 0, 0, 0, time.UTC) // hour 12 of the 25-hour day
	r, _, ok := idx.RunningExtremum("KNYC", "tmax", asOf)
	require.True(t, ok)
	assert.Equal(t, 62.0, r, "running max should include hours 0..12 of the 25-hour day")
}
