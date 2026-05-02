package cliparse

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// loadTestdata reads a fixture from internal/calibration/cliparse/testdata/.
func loadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // test-local fixture path
	require.NoError(t, err)
	return b
}

// silentLogger returns a *slog.Logger that discards output. For tests that
// don't care about the log stream.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseBulletin_StandardYesterdayProducesValidRecord(t *testing.T) {
	body := loadTestdata(t, "CLIORD_202601010635.txt")
	rec, err := parseBulletin("CLIORD_202601010635.txt", body)
	require.NoError(t, err)
	require.Equal(t, "2025-12-31", rec.ValidDate)
	require.NotNil(t, rec.TMaxF)
	require.Equal(t, 35, *rec.TMaxF)
	require.NotNil(t, rec.TMinF)
	require.Equal(t, 16, *rec.TMinF)
	require.Equal(t, "CLIORD_202601010635.txt", rec.BulletinID)
	require.Equal(t, time.Date(2026, 1, 1, 6, 35, 0, 0, time.UTC), rec.IssuedAt)
}

func TestParseBulletin_FullMMEmitsNilTempsButKeepsRecord(t *testing.T) {
	body := loadTestdata(t, "CLINYC_202502220629.txt")
	rec, err := parseBulletin("CLINYC_202502220629.txt", body)
	require.NoError(t, err)
	require.Equal(t, "2025-02-21", rec.ValidDate)
	require.Nil(t, rec.TMaxF)
	require.Nil(t, rec.TMinF)
}

func TestParseBulletin_NumericValueWithMMTimeKeepsValue(t *testing.T) {
	body := loadTestdata(t, "CLIMIA_202505092030.txt")
	rec, err := parseBulletin("CLIMIA_202505092030.txt", body)
	require.NoError(t, err)
	require.Equal(t, "2025-05-09", rec.ValidDate)
	require.NotNil(t, rec.TMaxF)
	require.Equal(t, 86, *rec.TMaxF)
	require.NotNil(t, rec.TMinF)
	require.Equal(t, 75, *rec.TMinF)
}

func TestParseBulletin_TodayPreliminaryUsesHeaderDate(t *testing.T) {
	body := loadTestdata(t, "CLILAX_202604020141.txt")
	rec, err := parseBulletin("CLILAX_202604020141.txt", body)
	require.NoError(t, err)
	require.Equal(t, "2026-04-01", rec.ValidDate, "header date wins, not issuance date")
}

func TestParseBulletin_ReturnsErrorWhenHeaderMissing(t *testing.T) {
	_, err := parseBulletin("CLINYC_202501010600.txt", []byte("garbage with no header line"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no header date")
}

func TestParseHeaderDate_ParsesAllStationNameVariants(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"NYC central park", "...THE CENTRAL PARK NY CLIMATE SUMMARY FOR APRIL 29 2024...", "2024-04-29"},
		{"MIA short name", "...THE MIAMI CLIMATE SUMMARY FOR APRIL 28 2024...", "2024-04-28"},
		{"LAX long name", "...THE LOS ANGELES INTL AIRPORT CA CLIMATE SUMMARY FOR APRIL 28 2024...", "2024-04-28"},
		{"ORD hyphenated", "...THE CHICAGO-OHARE CLIMATE SUMMARY FOR APRIL 28 2024...", "2024-04-28"},
		{"single-digit day", "...THE FOO CLIMATE SUMMARY FOR JANUARY 1 2025...", "2025-01-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseHeaderDate(tc.line)
			require.True(t, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseHeaderDate_ReturnsFalseWhenLineDoesNotMatch(t *testing.T) {
	cases := []string{
		"",
		"random text",
		"CLIMATE REPORT",
		"...THE FOO CLIMATE SUMMARY FOR FOOBAR 1 2025...",
		"...THE FOO CLIMATE SUMMARY FOR APRIL TWENTY 2025...",
		"...THE FOO CLIMATE SUMMARY FOR APRIL 32 2025...", // day out of range
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			_, ok := parseHeaderDate(line)
			require.False(t, ok)
		})
	}
}

func TestParseIssuedAt_FromFilenameSuffix(t *testing.T) {
	cases := []struct {
		name string
		want time.Time
	}{
		{"CLINYC_202502220629.txt", time.Date(2025, 2, 22, 6, 29, 0, 0, time.UTC)},
		{"CLIMIA_202505092030.txt", time.Date(2025, 5, 9, 20, 30, 0, 0, time.UTC)},
		{"CLILAX_202604020141.txt", time.Date(2026, 4, 2, 1, 41, 0, 0, time.UTC)},
		{"CLILAX_202604020834.txt", time.Date(2026, 4, 2, 8, 34, 0, 0, time.UTC)},
		{"CLILAX_202604020841.txt", time.Date(2026, 4, 2, 8, 41, 0, 0, time.UTC)},
		{"CLIORD_202601010635.txt", time.Date(2026, 1, 1, 6, 35, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseIssuedAt(tc.name)
			require.NoError(t, err)
			require.True(t, got.Equal(tc.want), "got %v want %v", got, tc.want)
		})
	}
}

func TestParseIssuedAt_RejectsMalformedFilenames(t *testing.T) {
	cases := []string{
		"random.txt",
		"CLINYC.txt",
		"CLINYC_202502220629",   // missing .txt
		"CLINYC_2025022206.txt", // 10 digits, not 12
		"__MACOSX/foo.txt",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseIssuedAt(name)
			require.Error(t, err)
		})
	}
}

func TestDedupLatest_KeepsLexicographicallyLatestFilename(t *testing.T) {
	v := func(n int) *int { return &n }
	t1 := time.Date(2026, 4, 2, 1, 41, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 2, 8, 34, 0, 0, time.UTC)
	t3 := time.Date(2026, 4, 2, 8, 41, 0, 0, time.UTC)

	in := []Record{
		{ValidDate: "2026-04-01", TMaxF: v(68), TMinF: v(58), IssuedAt: t1, BulletinID: "CLILAX_202604020141.txt"},
		{ValidDate: "2026-04-01", TMaxF: v(68), TMinF: v(58), IssuedAt: t3, BulletinID: "CLILAX_202604020841.txt"},
		{ValidDate: "2026-04-01", TMaxF: v(68), TMinF: v(58), IssuedAt: t2, BulletinID: "CLILAX_202604020834.txt"},
	}
	out := dedupLatest(in)
	require.Len(t, out, 1)
	require.Equal(t, "CLILAX_202604020841.txt", out[0].BulletinID)
}

func TestDedupLatest_TieBreaksOnLexLatestFilenameWhenIssuedAtEqual(t *testing.T) {
	v := func(n int) *int { return &n }
	tEq := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	in := []Record{
		{ValidDate: "2026-01-01", TMaxF: v(10), IssuedAt: tEq, BulletinID: "CLIA_202601010000.txt"},
		{ValidDate: "2026-01-01", TMaxF: v(20), IssuedAt: tEq, BulletinID: "CLIB_202601010000.txt"},
	}
	out := dedupLatest(in)
	require.Len(t, out, 1)
	require.Equal(t, "CLIB_202601010000.txt", out[0].BulletinID)
}

// buildTestZip creates a zip in t.TempDir() containing the named fixture
// files copied from testdata/. Returns the zip path.
func buildTestZip(t *testing.T, names ...string) string {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "test.zip")
	f, err := os.Create(zipPath) //nolint:gosec // test-local path under t.TempDir()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	for _, name := range names {
		body := loadTestdata(t, name)
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return zipPath
}

func TestRun_BuildsZipAndProducesExpectedCSV(t *testing.T) {
	zipPath := buildTestZip(t,
		"CLINYC_202502220629.txt", // valid_date 2025-02-21, full MM
		"CLIMIA_202505092030.txt", // valid_date 2025-05-09, partial MM (numeric value)
		"CLILAX_202604020141.txt", // valid_date 2026-04-01, preliminary
		"CLILAX_202604020834.txt", // valid_date 2026-04-01, first overnight
		"CLILAX_202604020841.txt", // valid_date 2026-04-01, dedup winner
		"CLIORD_202601010635.txt", // valid_date 2025-12-31
	)
	outPath := filepath.Join(t.TempDir(), "out.csv")
	gaps := &bytes.Buffer{}
	err := Run(context.Background(), Options{
		ZipPath: zipPath,
		OutPath: outPath,
		Start:   "2025-02-01",
		End:     "2026-04-30",
		GapsOut: gaps,
		Logger:  silentLogger(),
	})
	require.NoError(t, err)

	got, err := os.ReadFile(outPath) //nolint:gosec // test-local path under t.TempDir()
	require.NoError(t, err)
	want := strings.Join([]string{
		"valid_date,tmax_f,tmin_f,issued_at,bulletin_id",
		"2025-02-21,,,2025-02-22T06:29:00Z,CLINYC_202502220629.txt",
		"2025-05-09,86,75,2025-05-09T20:30:00Z,CLIMIA_202505092030.txt",
		"2025-12-31,35,16,2026-01-01T06:35:00Z,CLIORD_202601010635.txt",
		"2026-04-01,68,59,2026-04-02T08:41:00Z,CLILAX_202604020841.txt",
		"",
	}, "\n")
	require.Equal(t, want, string(got))

	require.Contains(t, gaps.String(), "6 bulletins → 4 records (0 skipped)")
}

func TestRun_ReportsGapsWithCalibrationFitnessSemantics(t *testing.T) {
	zipPath := buildTestZip(t,
		"CLINYC_202502220629.txt", // valid_date 2025-02-21, full MM → "no usable temps"
		"CLIORD_202601010635.txt", // valid_date 2025-12-31 — out of test window below
	)
	gaps := &bytes.Buffer{}
	err := Run(context.Background(), Options{
		ZipPath: zipPath,
		OutPath: filepath.Join(t.TempDir(), "out.csv"),
		Start:   "2025-02-21",
		End:     "2025-02-23",
		GapsOut: gaps,
		Logger:  silentLogger(),
	})
	require.NoError(t, err)
	out := gaps.String()
	require.Contains(t, out, "missing valid_date 2025-02-21 (no usable temps)")
	require.Contains(t, out, "missing valid_date 2025-02-22 (no bulletin)")
	require.Contains(t, out, "missing valid_date 2025-02-23 (no bulletin)")
	require.Contains(t, out, "3 gaps")
}

func TestRun_SkipsBulletinsWithUnparseableHeaderAndLogsWarn(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "synth.zip")
	f, err := os.Create(zipPath) //nolint:gosec // test-local path under t.TempDir()
	require.NoError(t, err)
	zw := zip.NewWriter(f)

	// Bulletin 1: valid filename suffix but no header in body
	w1, err := zw.Create("CLINYC_202404300618.txt")
	require.NoError(t, err)
	_, err = w1.Write([]byte("malformed bulletin with no header line"))
	require.NoError(t, err)

	// Bulletin 2: valid bulletin
	w2, err := zw.Create("CLIORD_202601010635.txt")
	require.NoError(t, err)
	_, err = w2.Write(loadTestdata(t, "CLIORD_202601010635.txt"))
	require.NoError(t, err)

	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())

	logBuf := &bytes.Buffer{}
	outPath := filepath.Join(t.TempDir(), "out.csv")
	err = Run(context.Background(), Options{
		ZipPath: zipPath,
		OutPath: outPath,
		Start:   "2024-01-01",
		End:     "2026-12-31",
		GapsOut: io.Discard,
		Logger:  slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	require.NoError(t, err)

	logs := logBuf.String()
	require.Contains(t, logs, "skipping bulletin")
	require.Contains(t, logs, "CLINYC_202404300618.txt")

	// Good bulletin still appeared in output.
	got, err := os.ReadFile(outPath) //nolint:gosec // test-local path under t.TempDir()
	require.NoError(t, err)
	require.Contains(t, string(got), "2025-12-31,35,16")
}

func TestRun_RejectsInvalidWindow(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		errMatch   string
	}{
		{"bad start format", "20250101", "2025-12-31", "bad --start"},
		{"bad end format", "2025-01-01", "12/31/2025", "bad --end"},
		{"start after end", "2025-12-31", "2025-01-01", "must be <="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(context.Background(), Options{
				ZipPath: "ignored",
				Start:   tc.start,
				End:     tc.end,
				GapsOut: io.Discard,
				Logger:  silentLogger(),
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errMatch)
		})
	}
}

func TestRun_ReturnsErrorWhenZipMissing(t *testing.T) {
	err := Run(context.Background(), Options{
		ZipPath: filepath.Join(t.TempDir(), "does-not-exist.zip"),
		Start:   "2025-01-01",
		End:     "2025-12-31",
		GapsOut: io.Discard,
		Logger:  silentLogger(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "open zip")
}
