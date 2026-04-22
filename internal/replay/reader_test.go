package replay

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeJSONL(t *testing.T, path string, lines []string) {
	t.Helper()
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func writeJSONLGz(t *testing.T, path string, lines []string) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	for _, l := range lines {
		_, err := gw.Write([]byte(l + "\n"))
		require.NoError(t, err)
	}
	require.NoError(t, gw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

func TestListJournalsSortsAndFilters(t *testing.T) {
	dir := t.TempDir()
	// Create entries in reverse-chronological name order so the sort has work to do.
	writeJSONL(t, filepath.Join(dir, "kalshi-zzz.jsonl"), []string{"{}"})
	writeJSONL(t, filepath.Join(dir, "kalshi-aaa.jsonl"), []string{"{}"})
	writeJSONLGz(t, filepath.Join(dir, "kalshi-mmm.jsonl.gz"), []string{"{}"})
	// Non-matching files must be excluded.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kalshi-notjsonl.txt"), []byte("nope"), 0o600))
	// A subdirectory must be skipped.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested"), 0o750))

	got, err := ListJournals(dir)
	require.NoError(t, err)
	require.Equal(t, []string{
		filepath.Join(dir, "kalshi-aaa.jsonl"),
		filepath.Join(dir, "kalshi-mmm.jsonl.gz"),
		filepath.Join(dir, "kalshi-zzz.jsonl"),
	}, got)
}

func TestListJournalsMissingDir(t *testing.T) {
	_, err := ListJournals(filepath.Join(t.TempDir(), "no-such-dir"))
	require.Error(t, err)
}

func TestOpenPlainAndGz(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "a.jsonl")
	gz := filepath.Join(dir, "a.jsonl.gz")
	writeJSONL(t, plain, []string{`{"t":1,"src":"x","k":"y"}`})
	writeJSONLGz(t, gz, []string{`{"t":2,"src":"x","k":"y"}`})

	r1, err := Open(plain)
	require.NoError(t, err)
	defer r1.Close()
	b1, err := io.ReadAll(r1)
	require.NoError(t, err)
	require.Contains(t, string(b1), `"t":1`)

	r2, err := Open(gz)
	require.NoError(t, err)
	defer r2.Close()
	b2, err := io.ReadAll(r2)
	require.NoError(t, err)
	require.Contains(t, string(b2), `"t":2`)
}

func TestOpenGzWithCorruptHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.jsonl.gz")
	require.NoError(t, os.WriteFile(path, []byte("not a gzip file"), 0o600))
	_, err := Open(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gzip")
}

func TestOpenMissingFile(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "absent.jsonl"))
	require.Error(t, err)
}

func TestForEachFrameReadsInOrderAndFlagsFirstInFile(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.jsonl")
	f2 := filepath.Join(dir, "b.jsonl.gz")
	writeJSONL(t, f1, []string{
		`{"t":100,"src":"kalshi","k":"ws","p":{"type":"subscribed"}}`,
		`{"t":200,"src":"kalshi","k":"ws","p":{"type":"orderbook_delta"}}`,
	})
	writeJSONLGz(t, f2, []string{
		`{"t":300,"src":"kalshi","k":"ws","p":{"type":"trade"}}`,
	})

	type tick struct {
		wall       int64
		firstFile  bool
		bytesCount int
	}
	var seen []tick
	err := ForEachFrame([]string{f1, f2}, func(env Envelope, n int, firstInFile bool) error {
		seen = append(seen, tick{env.Wall, firstInFile, n})
		return nil
	})
	require.NoError(t, err)
	require.Len(t, seen, 3)
	require.Equal(t, int64(100), seen[0].wall)
	require.True(t, seen[0].firstFile, "frame 0 is first in a.jsonl")
	require.False(t, seen[1].firstFile, "frame 1 is a continuation of a.jsonl")
	require.Equal(t, int64(300), seen[2].wall)
	require.True(t, seen[2].firstFile, "frame 2 is first in b.jsonl.gz")
	// bytes should be roughly the length of each JSONL line (payload in-line).
	require.Greater(t, seen[0].bytesCount, 20)
}

func TestForEachFrameSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	writeJSONL(t, path, []string{
		`{"t":1,"src":"x","k":"y"}`,
		`not-json-at-all`,
		`{"t":2,"src":"x","k":"y"}`,
		`{broken`,
		`{"t":3,"src":"x","k":"y"}`,
	})
	var walls []int64
	err := ForEachFrame([]string{path}, func(env Envelope, _ int, _ bool) error {
		walls = append(walls, env.Wall)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3}, walls)
}

func TestForEachFrameStopsOnCallerEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	writeJSONL(t, path, []string{
		`{"t":1,"src":"x","k":"y"}`,
		`{"t":2,"src":"x","k":"y"}`,
		`{"t":3,"src":"x","k":"y"}`,
	})
	var seen []int64
	err := ForEachFrame([]string{path}, func(env Envelope, _ int, _ bool) error {
		seen = append(seen, env.Wall)
		if env.Wall == 2 {
			return io.EOF
		}
		return nil
	})
	require.NoError(t, err, "io.EOF from fn must be treated as early-stop, not an error")
	require.Equal(t, []int64{1, 2}, seen)
}

func TestForEachFramePropagatesFnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	writeJSONL(t, path, []string{`{"t":1,"src":"x","k":"y"}`})
	sentinel := errors.New("boom")
	err := ForEachFrame([]string{path}, func(env Envelope, _ int, _ bool) error {
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
}

func TestForEachFrameReadsLargeLines(t *testing.T) {
	// 256 KiB payload, well under MaxLineBytes but above Scanner's 64 KiB default.
	big := make([]byte, 256<<10)
	for i := range big {
		big[i] = 'x'
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")
	line := `{"t":1,"src":"x","k":"y","p":"` + string(big) + `"}`
	writeJSONL(t, path, []string{line})

	count := 0
	err := ForEachFrame([]string{path}, func(_ Envelope, n int, _ bool) error {
		count++
		require.Greater(t, n, 256<<10)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
