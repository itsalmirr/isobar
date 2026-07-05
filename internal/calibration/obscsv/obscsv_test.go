package obscsv

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readAll routes every test's CSV through ReadFile so the open→read→close
// entry point callers use is what the policy tests exercise.
func readAll(t *testing.T, csv string, opts Options) ([]Row, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "obs.csv")
	require.NoError(t, os.WriteFile(path, []byte(csv), 0o600))
	return ReadFile(path, opts)
}

// TestReadFileMissingFile pins ReadFile's open-error contract: the os
// error is returned unwrapped so callers can add their own path context.
func TestReadFileMissingFile(t *testing.T) {
	_, err := ReadFile(filepath.Join(t.TempDir(), "absent.csv"), Options{
		Alias:   StrictAliases,
		Require: []string{ColDate, ColTmax, ColTmin},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// TestCustomAliasProjection covers calibrate-fit's forecast projection: a
// caller-supplied alias set maps differently-spelled source columns
// (gfs_tmax_f) onto the canonical Row fields, and a missing column is
// reported by its caller-facing spelling, not the canonical key.
func TestCustomAliasProjection(t *testing.T) {
	alias := Aliases{
		ColDate: {"valid_date"},
		ColTmax: {"gfs_tmax_f"},
		ColTmin: {"gfs_tmin_f"},
	}
	rows, err := readAll(t, "valid_date,gfs_tmax_f,gfs_tmin_f\n2024-04-29,71.5,55.1\n", Options{
		Alias:      alias,
		Require:    []string{ColDate, ColTmax, ColTmin},
		StrictRows: true,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 71.5, rows[0].Tmax)
	assert.Equal(t, 55.1, rows[0].Tmin)

	_, err = readAll(t, "valid_date,tmax_f,tmin_f\n2024-04-29,71,55\n", Options{
		Alias:   alias,
		Require: []string{ColDate, ColTmax, ColTmin},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required columns")
	assert.Contains(t, err.Error(), "gfs_tmax_f")
}

// TestEdgeAliasesTolerateShortRows covers the best-effort (lenient) path
// the truth loader relies on: aliased headers, a ragged trailing row that
// is kept, and an unparseable-date row that is skipped.
func TestEdgeAliasesTolerateShortRows(t *testing.T) {
	csv := "date,tmax,tmin,issued_at\n" +
		"2024-04-29,87,63,x\n" +
		"bogus,row,here,\n" +
		"2024-05-02,72,58\n" // short trailing cols → kept
	rows, err := readAll(t, csv, Options{
		Alias:   EdgeAliases,
		Require: []string{ColDate, ColTmax, ColTmin},
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "2024-04-29", rows[0].Date.Format("2006-01-02"))
	assert.Equal(t, "87", rows[0].TmaxRaw)
	assert.True(t, rows[0].TmaxPresent)
	assert.Equal(t, 72.0, rows[1].Tmax)
}

// TestStrictRowsRejectShortRecord covers the calibration loaders' hard-fail
// contract: a record with fewer fields than the highest required column is
// an error, not a silent skip.
func TestStrictRowsRejectShortRecord(t *testing.T) {
	csv := "valid_date,tmax_f,tmin_f\n" +
		"2024-04-29,87\n" // only 2 cols, need 3
	_, err := readAll(t, csv, Options{
		Alias:      StrictAliases,
		Require:    []string{ColDate, ColTmax, ColTmin},
		StrictRows: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "short record")
}

// TestStrictAliasesRejectLooseHeader guarantees the calibration loaders are
// not silently loosened to accept edge's "tmax"/"tmin" spellings.
func TestStrictAliasesRejectLooseHeader(t *testing.T) {
	csv := "date,tmax,tmin\n2024-04-29,87,63\n"
	_, err := readAll(t, csv, Options{
		Alias:   StrictAliases,
		Require: []string{ColDate, ColTmax, ColTmin},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required columns")
}

// TestParseErrorFailsLoud is the F01 regression: a mid-file csv parse error
// (bare quote) must surface, not silently truncate the tail.
func TestParseErrorFailsLoud(t *testing.T) {
	csv := "valid_date,tmax_f,tmin_f\n" +
		"2024-04-29,87,63\n" +
		"2024-04-30,\"6\"6,52\n" + // bare quote mid-field → csv.ParseError
		"2024-05-01,70,50\n"
	_, err := readAll(t, csv, Options{
		Alias:   StrictAliases,
		Require: []string{ColDate, ColTmax, ColTmin},
	})
	require.Error(t, err)
	assert.NotErrorIs(t, err, io.EOF)
}

// TestEmptyCellNotPresent covers the calibrate-fit nil-vs-present contract:
// an empty tmax_f cell yields TmaxPresent=false while tmin still parses.
func TestEmptyCellNotPresent(t *testing.T) {
	csv := "valid_date,tmax_f,tmin_f\n2024-04-29,,63\n"
	rows, err := readAll(t, csv, Options{
		Alias:   StrictAliases,
		Require: []string{ColDate, ColTmax, ColTmin},
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].TmaxPresent)
	assert.True(t, rows[0].TminPresent)
	assert.Equal(t, 63.0, rows[0].Tmin)
}
