// Package obscsv is the single header-aware reader for the canonical
// K<STA>.csv daily-observation schema produced by cmd/calibrate-parse-cli:
//
//	valid_date,tmax_f,tmin_f,issued_at,bulletin_id
//	2024-04-29,70,54,2024-04-30T06:41:00Z,CLIMDW_202404300641.txt
//
// Four callers used to hand-roll this parse with divergent, load-bearing
// contracts — integer vs float64 vs pointer-float temps, exact-name vs
// alias header matching, silent-skip vs hard-error row policy, and (in
// cmd/calibrate-bayesian) a header-agnostic positional rec[0]/rec[1]/rec[2]
// variant that would misparse the instant column order shifted. This
// package is the superset each caller now projects: it matches columns by
// name (with per-caller alias support), returns each row's temperatures as
// both parsed float64+Present flags and the raw trimmed strings, and lets
// the caller choose whether ragged/short records are skipped or rejected.
// The domain policies (edge's int/Atoi rejection of "70.0", calibrate-fit's
// nil-vs-present distinction, climate's exact-name strictness) live in the
// callers' thin projections, not here.
package obscsv

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Column names in canonical form. Callers key Row lookups off these.
const (
	ColDate = "valid_date"
	ColTmax = "tmax_f"
	ColTmin = "tmin_f"
)

// Aliases declares, for a canonical column, the set of header spellings
// that resolve to it. Comparison is case-insensitive and whitespace-trimmed.
type Aliases map[string][]string

// StrictAliases matches only the exact canonical column names. Used by the
// calibration loaders (climate, calibrate-fit) which demand tmax_f/tmin_f
// and must NOT silently accept a looser "tmax"/"tmin" header.
var StrictAliases = Aliases{
	ColDate: {ColDate},
	ColTmax: {ColTmax},
	ColTmin: {ColTmin},
}

// EdgeAliases matches the wider spellings the realized-PnL truth loader
// tolerates: valid_date|date, tmax_f|tmax, tmin_f|tmin.
var EdgeAliases = Aliases{
	ColDate: {ColDate, "date"},
	ColTmax: {ColTmax, "tmax"},
	ColTmin: {ColTmin, "tmin"},
}

// Options configures a Reader.
type Options struct {
	// Alias is the header alias set (per-caller; e.g. StrictAliases or
	// EdgeAliases). Required.
	Alias Aliases

	// Require lists the canonical columns that must be present in the
	// header; a missing one fails Open loudly. Typically all three of
	// ColDate/ColTmax/ColTmin.
	Require []string

	// StrictRows, when true, makes a record with fewer fields than the
	// highest required column index a hard error (climate/calibrate-fit
	// contract). When false, a short record is skipped and Next continues
	// (edge's best-effort truth contract).
	StrictRows bool
}

// Row is one parsed daily-observation record.
type Row struct {
	// Date is the valid_date parsed as UTC midnight.
	Date time.Time

	// Tmax/Tmin are the parsed float64 temperatures; the *Present flag is
	// false when the source cell was empty (calibrate-fit's nil-vs-present
	// distinction) or unparseable as a float.
	Tmax, Tmin               float64
	TmaxPresent, TminPresent bool

	// TmaxRaw/TminRaw are the trimmed source cells, retained so callers
	// with a stricter numeric policy than ParseFloat (edge rejects "70.0"
	// via Atoi) can re-parse without the reader having to pick a winner.
	TmaxRaw, TminRaw string
}

// ReadFile opens path, applies the header and row policy in opts, and
// returns every Row in file order. It is the one-call form of
// NewReader+Next for the small per-station daily CSVs every caller loads
// whole; callers apply their numeric projections to the returned rows.
// Errors carry no path prefix beyond what os.Open itself reports, so each
// caller can wrap once with its own context.
func ReadFile(path string, opts Options) ([]Row, error) {
	f, err := os.Open(path) //nolint:gosec // caller-supplied data path
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	rd, err := NewReader(f, opts)
	if err != nil {
		return nil, err
	}
	var rows []Row
	for {
		row, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
}

// Reader streams Rows from an obs CSV. It owns column resolution and the
// per-record field-count policy; numeric domain policy stays with callers.
type Reader struct {
	r       *csv.Reader
	col     map[string]int
	minCols int
	opts    Options
	lineNo  int
}

// NewReader validates the header of an already-opened obs CSV and returns a
// streaming Reader. The caller owns the underlying file/reader lifecycle.
func NewReader(src io.Reader, opts Options) (*Reader, error) {
	if opts.Alias == nil {
		return nil, errors.New("obscsv: nil alias set")
	}
	cr := csv.NewReader(src)
	cr.FieldsPerRecord = -1 // tolerate trailing columns; per-row policy below

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col, err := resolveColumns(header, opts.Alias, opts.Require)
	if err != nil {
		return nil, err
	}
	return &Reader{
		r:       cr,
		col:     col,
		minCols: maxIndex(col) + 1,
		opts:    opts,
		lineNo:  1,
	}, nil
}

// Next returns the next Row. It returns io.EOF when the file is exhausted.
// A mid-file csv.ParseError is always returned (fail-loud): a malformed
// byte must not silently truncate the tail of the file. Records whose
// valid_date is unparseable are skipped. Short records are skipped in
// lenient mode and returned as an error in strict mode.
func (rd *Reader) Next() (Row, error) {
	for {
		rd.lineNo++
		rec, err := rd.r.Read()
		if errors.Is(err, io.EOF) {
			return Row{}, io.EOF
		}
		if err != nil {
			return Row{}, fmt.Errorf("read line %d: %w", rd.lineNo, err)
		}
		if len(rec) < rd.minCols {
			if rd.opts.StrictRows {
				return Row{}, fmt.Errorf("short record at line %d (%d cols, need %d)", rd.lineNo, len(rec), rd.minCols)
			}
			continue
		}
		dateStr := strings.TrimSpace(rec[rd.col[ColDate]])
		date, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		row := Row{Date: date.UTC()}
		if i, ok := rd.col[ColTmax]; ok {
			row.TmaxRaw = strings.TrimSpace(rec[i])
			if v, e := strconv.ParseFloat(row.TmaxRaw, 64); e == nil {
				row.Tmax, row.TmaxPresent = v, true
			}
		}
		if i, ok := rd.col[ColTmin]; ok {
			row.TminRaw = strings.TrimSpace(rec[i])
			if v, e := strconv.ParseFloat(row.TminRaw, 64); e == nil {
				row.Tmin, row.TminPresent = v, true
			}
		}
		return row, nil
	}
}

// resolveColumns maps every declared canonical column to its header index
// via the alias set, and errors if any Require column is unresolved.
func resolveColumns(header []string, alias Aliases, require []string) (map[string]int, error) {
	// Normalise the header once: trimmed-lowercased spelling → index.
	norm := make(map[string]int, len(header))
	for i, h := range header {
		norm[strings.ToLower(strings.TrimSpace(h))] = i
	}
	col := make(map[string]int, len(alias))
	for canon, spellings := range alias {
		for _, s := range spellings {
			if i, ok := norm[strings.ToLower(strings.TrimSpace(s))]; ok {
				col[canon] = i
				break
			}
		}
	}
	var missing []string
	for _, c := range require {
		if _, ok := col[c]; ok {
			continue
		}
		// Report the caller-facing spelling (first alias), not the internal
		// canonical key — for calibrate-fit's forecast alias set the two
		// differ (gfs_tmax_f vs tmax_f).
		name := c
		if sp := alias[c]; len(sp) > 0 {
			name = sp[0]
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required columns: %v (got header %v)", missing, header)
	}
	return col, nil
}

func maxIndex(col map[string]int) int {
	m := -1
	for _, i := range col {
		if i > m {
			m = i
		}
	}
	return m
}
