// Package cliparse parses NWS CLI (Climate Report) text bulletins from the
// IEM AFOS archive zips produced by scripts/calibration-fetch-cli.sh and
// emits per-station daily TMAX/TMIN as CSV. CLI is the canonical y-target
// for NGR calibration — see ADR-0007.
//
// Parser behavior contract (locked in testdata/README.md):
//   - Valid date comes from the bulletin header
//     ("...THE <name> CLIMATE SUMMARY FOR <MONTH> <DAY> <YEAR>..."),
//     not from the YESTERDAY/TODAY label or the issuance timestamp.
//   - When multiple bulletins report the same valid date, the latest
//     issuance wins; ties break on lexicographically latest filename.
//   - "MM" temp value → empty CSV field (record still emitted; gap report
//     fires on calibration-fitness criterion).
//   - Out-of-window valid dates are dropped silently.
package cliparse

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Record is one bulletin's parsed payload, deduped to one per valid date.
type Record struct {
	ValidDate  string    // "YYYY-MM-DD", station-local civil date from header
	TMaxF      *int      // nil iff bulletin reported MM
	TMinF      *int      // nil iff bulletin reported MM
	IssuedAt   time.Time // UTC, parsed from the filename suffix
	BulletinID string    // filename inside the zip
}

// Options configures a Run.
type Options struct {
	ZipPath string       // input zip (CLI<STA>_YYYYMMDDHHMM.txt entries)
	OutPath string       // CSV destination; "" = stdout
	Start   string       // "YYYY-MM-DD" inclusive
	End     string       // "YYYY-MM-DD" inclusive
	GapsOut io.Writer    // human-readable gap stream; nil = os.Stderr
	Logger  *slog.Logger // structured logger; nil = slog.Default()
}

var (
	headerRE = regexp.MustCompile(`^\.\.\.THE .* CLIMATE SUMMARY FOR ([A-Z]+)\s+(\d+)\s+(\d+)`)
	maxRE    = regexp.MustCompile(`^\s+MAXIMUM\s+(MM|-?\d+)`)
	minRE    = regexp.MustCompile(`^\s+MINIMUM\s+(MM|-?\d+)`)
	issuedRE = regexp.MustCompile(`_(\d{12})\.txt$`)
)

var monthNum = map[string]int{
	"JANUARY": 1, "FEBRUARY": 2, "MARCH": 3, "APRIL": 4,
	"MAY": 5, "JUNE": 6, "JULY": 7, "AUGUST": 8,
	"SEPTEMBER": 9, "OCTOBER": 10, "NOVEMBER": 11, "DECEMBER": 12,
}

// Run reads opts.ZipPath, parses every bulletin, dedupes by valid date,
// filters to [opts.Start, opts.End], writes a CSV to opts.OutPath (or
// stdout), and emits a calibration-fitness gap report to opts.GapsOut.
func Run(ctx context.Context, opts Options) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "cliparse")

	gaps := opts.GapsOut
	if gaps == nil {
		gaps = os.Stderr
	}

	if err := validateWindow(opts.Start, opts.End); err != nil {
		return err
	}

	zr, err := zip.OpenReader(opts.ZipPath)
	if err != nil {
		return fmt.Errorf("cliparse: open zip %s: %w", opts.ZipPath, err)
	}
	defer func() { _ = zr.Close() }()

	var (
		records    []Record
		totalFiles int
		skipped    int
	)
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasSuffix(f.Name, ".txt") {
			continue
		}
		totalFiles++
		body, err := readZipEntry(f)
		if err != nil {
			logger.Warn("read zip entry failed", "file", f.Name, "err", err)
			skipped++
			continue
		}
		rec, err := parseBulletin(f.Name, body)
		if err != nil {
			logger.Warn("skipping bulletin", "file", f.Name, "err", err)
			skipped++
			continue
		}
		records = append(records, rec)
	}

	in := records[:0]
	for _, r := range records {
		if r.ValidDate >= opts.Start && r.ValidDate <= opts.End {
			in = append(in, r)
		}
	}
	records = dedupLatest(in)

	sort.Slice(records, func(i, j int) bool {
		return records[i].ValidDate < records[j].ValidDate
	})

	out := io.Writer(os.Stdout)
	if opts.OutPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.OutPath), 0o750); err != nil {
			return fmt.Errorf("cliparse: mkdir %s: %w", filepath.Dir(opts.OutPath), err)
		}
		f, err := os.Create(opts.OutPath)
		if err != nil {
			return fmt.Errorf("cliparse: create %s: %w", opts.OutPath, err)
		}
		defer f.Close()
		out = f
	}
	if err := writeCSV(out, records); err != nil {
		return fmt.Errorf("cliparse: write csv: %w", err)
	}

	gapCount := reportGaps(gaps, records, opts.Start, opts.End)

	usable := 0
	for _, r := range records {
		if r.TMaxF != nil && r.TMinF != nil {
			usable++
		}
	}
	totalDates := len(eachDate(opts.Start, opts.End))
	_, _ = fmt.Fprintf(gaps, "cliparse: %d bulletins → %d records (%d skipped), %d usable / %d dates in window, %d gaps\n",
		totalFiles, len(records), skipped, usable, totalDates, gapCount)
	return nil
}

func validateWindow(start, end string) error {
	if _, err := time.Parse("2006-01-02", start); err != nil {
		return fmt.Errorf("cliparse: bad --start %q: %w", start, err)
	}
	if _, err := time.Parse("2006-01-02", end); err != nil {
		return fmt.Errorf("cliparse: bad --end %q: %w", end, err)
	}
	if start > end {
		return fmt.Errorf("cliparse: start (%s) must be <= end (%s)", start, end)
	}
	return nil
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func parseBulletin(name string, body []byte) (Record, error) {
	issued, err := parseIssuedAt(name)
	if err != nil {
		return Record{}, err
	}
	rec := Record{BulletinID: filepath.Base(name), IssuedAt: issued}

	headerSeen, tempBlock, maxSet, minSet := false, false, false, false
	for _, line := range strings.Split(string(body), "\n") {
		if !headerSeen {
			if d, ok := parseHeaderDate(line); ok {
				rec.ValidDate = d
				headerSeen = true
			}
			continue
		}
		if !tempBlock {
			if strings.Contains(line, "TEMPERATURE (F)") {
				tempBlock = true
			}
			continue
		}
		if !maxSet {
			if v, ok := parseTempLine(maxRE, line); ok {
				rec.TMaxF = v
				maxSet = true
				continue
			}
		}
		if !minSet {
			if v, ok := parseTempLine(minRE, line); ok {
				rec.TMinF = v
				minSet = true
			}
		}
		if maxSet && minSet {
			break
		}
	}
	if !headerSeen {
		return Record{}, errors.New("no header date")
	}
	return rec, nil
}

func parseHeaderDate(line string) (string, bool) {
	m := headerRE.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	mon, ok := monthNum[m[1]]
	if !ok {
		return "", false
	}
	day, err := strconv.Atoi(m[2])
	if err != nil || day < 1 || day > 31 {
		return "", false
	}
	yr, err := strconv.Atoi(m[3])
	if err != nil || yr < 1900 || yr > 2100 {
		return "", false
	}
	return fmt.Sprintf("%04d-%02d-%02d", yr, mon, day), true
}

func parseTempLine(re *regexp.Regexp, line string) (*int, bool) {
	m := re.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	if m[1] == "MM" {
		return nil, true
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return nil, false
	}
	return &v, true
}

func parseIssuedAt(filename string) (time.Time, error) {
	base := filepath.Base(filename)
	m := issuedRE.FindStringSubmatch(base)
	if m == nil {
		return time.Time{}, fmt.Errorf("bad filename: %s", base)
	}
	t, err := time.Parse("200601021504", m[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("parse issuance time %q: %w", m[1], err)
	}
	return t, nil
}

func dedupLatest(recs []Record) []Record {
	byDate := make(map[string]Record, len(recs))
	for _, r := range recs {
		ex, ok := byDate[r.ValidDate]
		if !ok {
			byDate[r.ValidDate] = r
			continue
		}
		switch {
		case r.IssuedAt.After(ex.IssuedAt):
			byDate[r.ValidDate] = r
		case r.IssuedAt.Equal(ex.IssuedAt) && r.BulletinID > ex.BulletinID:
			byDate[r.ValidDate] = r
		}
	}
	out := make([]Record, 0, len(byDate))
	for _, r := range byDate {
		out = append(out, r)
	}
	return out
}

func writeCSV(w io.Writer, recs []Record) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"valid_date", "tmax_f", "tmin_f", "issued_at", "bulletin_id"}); err != nil {
		return err
	}
	for _, r := range recs {
		if err := cw.Write([]string{
			r.ValidDate,
			tempStr(r.TMaxF),
			tempStr(r.TMinF),
			r.IssuedAt.UTC().Format(time.RFC3339),
			r.BulletinID,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func tempStr(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

func reportGaps(w io.Writer, recs []Record, start, end string) int {
	have := make(map[string]Record, len(recs))
	for _, r := range recs {
		have[r.ValidDate] = r
	}
	gaps := 0
	for _, d := range eachDate(start, end) {
		r, ok := have[d]
		switch {
		case !ok:
			_, _ = fmt.Fprintf(w, "cliparse: missing valid_date %s (no bulletin)\n", d)
			gaps++
		case r.TMaxF == nil || r.TMinF == nil:
			_, _ = fmt.Fprintf(w, "cliparse: missing valid_date %s (no usable temps)\n", d)
			gaps++
		}
	}
	return gaps
}

func eachDate(start, end string) []string {
	s, _ := time.Parse("2006-01-02", start)
	e, _ := time.Parse("2006-01-02", end)
	var out []string
	for d := s; !d.After(e); d = d.Add(24 * time.Hour) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}
