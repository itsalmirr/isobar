// Package replay reads back the JSONL journals produced by cmd/tape.
// It is the shared input layer for cmd/analyze and any future offline
// tool (backtester, calibration fitter) that consumes recorded data.
//
// The on-disk format is append-only NDJSON (one Envelope per line).
// Journal files rotate on size or age; rotated files may be gzipped.
// This package transparently handles `.jsonl` and `.jsonl.gz` files
// and preserves file-boundary information so callers can reset
// per-file statistics (e.g., inter-frame gap tracking should not span
// file boundaries — the gap across rotations is a recording artifact,
// not a market stall).
package replay

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
)

// Envelope is the on-disk shape of one recorded frame. Payload is the
// raw producer bytes (Kalshi WS frame, Open-Meteo JSON response, …) —
// this package does not decode it.
//
// Version is the journal envelope schema version, stamped at write time
// (see internal/journal.EnvelopeSchemaVersion). Frames written before
// the "v" field landed (the ~700 MB of legacy tape on prod) decode with
// an empty Version off-disk; the reader rewrites that to
// EnvelopeSchemaLegacyV0 so downstream callers always see a populated
// label and can switch on schema generation without a nil check.
type Envelope struct {
	Wall    int64           `json:"t"` // unix-nanoseconds, UTC
	Mono    int64           `json:"m"` // process-monotonic nanoseconds
	Source  string          `json:"src"`
	Kind    string          `json:"k"`
	Version string          `json:"v,omitempty"`
	Payload json.RawMessage `json:"p,omitempty"`
}

// EnvelopeSchemaLegacyV0 is the placeholder version string the reader
// stamps on frames whose on-disk envelope predates the "v" field. It
// is NOT a value any writer ever emits — it is a synthetic label for
// "absence of v on read" so downstream consumers always see a
// populated Version and can switch on schema generation.
const EnvelopeSchemaLegacyV0 = "isobar.journal/v0-legacy"

// ListJournals returns every .jsonl or .jsonl.gz file directly under
// dir, sorted lexicographically (which for our filename convention is
// also chronological). Subdirectories are ignored.
func ListJournals(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".jsonl.gz") {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Open returns a reader for path. If path ends in .gz, the returned
// reader transparently decompresses. Callers must Close() the result.
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path) //nolint:gosec // path provenance is the caller's responsibility
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	gr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("gzip %s: %w", path, err)
	}
	return &gzReader{gr: gr, f: f}, nil
}

type gzReader struct {
	gr *gzip.Reader
	f  *os.File
}

func (r *gzReader) Read(p []byte) (int, error) { return r.gr.Read(p) }

func (r *gzReader) Close() error {
	_ = r.gr.Close()
	return r.f.Close()
}

// MaxLineBytes caps a single decoded frame at 8 MiB. Real Kalshi
// snapshot frames for heavy markets are <100 KiB; the cap is generous
// to absorb unforeseen growth without tanking on malformed input.
const MaxLineBytes = 8 << 20

// ForEachFrame streams every NDJSON frame from files (in order),
// decoding each line into an Envelope. firstInFile is true on the
// first frame of each new file — consumers use it to reset per-file
// state such as gap tracking.
//
// Malformed lines are skipped silently: a single broken frame should
// not abort a day-long report. To stop iteration early, fn may return
// io.EOF (sentinel, not an error); any other non-nil error halts
// iteration and returns that error.
func ForEachFrame(files []string, fn func(env Envelope, bytes int, firstInFile bool) error) error {
	for _, path := range files {
		if err := forEachFrameInFile(path, fn); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return nil
}

func forEachFrameInFile(path string, fn func(env Envelope, bytes int, firstInFile bool) error) error {
	r, err := Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer r.Close()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLineBytes)

	firstInFile := true
	for sc.Scan() {
		line := sc.Bytes()
		var env Envelope
		// sonic.Unmarshal: ~3-5x stdlib on this hot path. Replays of
		// multi-million-frame tapes spend most of their wall time here.
		if err := sonic.Unmarshal(line, &env); err != nil {
			continue // tolerate malformed frames
		}
		// Tape predating the schema-version field decodes with Version
		// == "". Stamp a synthetic v0-legacy label so consumers see a
		// populated schema string and can switch on it. The writer
		// never emits this value — it only appears via back-compat.
		if env.Version == "" {
			env.Version = EnvelopeSchemaLegacyV0
		}
		if err := fn(env, len(line), firstInFile); err != nil {
			return err
		}
		firstInFile = false
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	return nil
}
