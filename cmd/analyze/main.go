// Command analyze reads recorded JSONL journals and produces a
// Phase-0.5 sanity report. MVP scope: does the recorder produce usable
// data? Frame counts, gap distributions, per-market activity,
// monotonicity checks. Edge computation is a follow-up.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	dataDir := flag.String("data", "./data", "root data dir")
	date := flag.String("date", time.Now().UTC().Format("2006-01-02"), "YYYY-MM-DD (UTC)")
	source := flag.String("source", "all", "kalshi | openmeteo | all")
	flag.Parse()

	sources := []string{"kalshi", "openmeteo"}
	if *source != "all" {
		sources = []string{*source}
	}

	fmt.Printf("# isobar analyze — %s UTC\n\n", *date)

	for _, src := range sources {
		dir := filepath.Join(*dataDir, src, *date)
		files, err := listJournals(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", src, err)
			continue
		}
		if len(files) == 0 {
			fmt.Printf("## %s\n_no journals found in %s_\n\n", src, dir)
			continue
		}
		switch src {
		case "kalshi":
			reportKalshi(dir, files)
		case "openmeteo":
			reportOpenMeteo(dir, files)
		}
	}
}

// --- file enumeration -------------------------------------------------------

func listJournals(dir string) ([]string, error) {
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

// openJournal returns a reader for the given file, transparently ungzipping
// when the path ends in .gz.
func openJournal(path string) (io.ReadCloser, error) {
	f, err := os.Open(path) //nolint:gosec // path from listJournals, built from our own data dir
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	gr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
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

// --- frame iteration --------------------------------------------------------

type envelope struct {
	Wall    int64           `json:"t"`
	Mono    int64           `json:"m"`
	Source  string          `json:"src"`
	Kind    string          `json:"k"`
	Payload json.RawMessage `json:"p,omitempty"`
}

// forEachFrame streams each NDJSON line from files (in order), decoding
// into an envelope. The bool passed to fn is true on the first frame of a
// new file — consumers use it to reset per-file state (e.g., gap tracking
// should not span file boundaries). fn may stop early by returning an
// error; io.EOF is treated as "stop iteration without error".
func forEachFrame(files []string, fn func(env envelope, bytes int, firstInFile bool) error) error {
	for _, path := range files {
		r, err := openJournal(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		sc := bufio.NewScanner(r)
		// Raw Kalshi frames can be tens of KB; raise the scanner buffer.
		sc.Buffer(make([]byte, 64<<10), 8<<20)
		firstInFile := true
		for sc.Scan() {
			line := sc.Bytes()
			var env envelope
			if err := json.Unmarshal(line, &env); err != nil {
				// Skip malformed lines rather than abort — a single bad
				// frame shouldn't tank a whole-day report.
				continue
			}
			if err := fn(env, len(line), firstInFile); err != nil {
				_ = r.Close()
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			firstInFile = false
		}
		_ = r.Close()
		if err := sc.Err(); err != nil {
			return fmt.Errorf("scan %s: %w", path, err)
		}
	}
	return nil
}

// --- Kalshi report ----------------------------------------------------------

type kalshiPayload struct {
	Type string `json:"type"`
	Seq  int64  `json:"seq,omitempty"`
	Sid  int64  `json:"sid,omitempty"`
	Msg  struct {
		MarketTicker string `json:"market_ticker,omitempty"`
	} `json:"msg"`
}

func reportKalshi(dir string, files []string) {
	fmt.Printf("## kalshi\n\n")
	fmt.Printf("Journals: %d in %s\n\n", len(files), dir)

	var (
		frames       int
		bytesTotal   int
		firstWall    int64
		lastWall     int64
		regressions  int
		prevWall     int64
		typeHist     = map[string]int{}
		seriesHist   = map[string]int{}
		tickerHist   = map[string]int{}
		gapHist      = newHist() // inter-frame gaps in ms
		errorFrames  = 0
		totalTickers = map[string]struct{}{}
	)

	_ = forEachFrame(files, func(env envelope, n int, firstInFile bool) error {
		frames++
		bytesTotal += n
		if firstWall == 0 {
			firstWall = env.Wall
		}
		lastWall = env.Wall
		if firstInFile {
			prevWall = 0 // do not span runs
		}
		if prevWall != 0 {
			if env.Wall < prevWall {
				regressions++
			} else {
				gapHist.add(float64(env.Wall-prevWall) / 1e6) // ns → ms
			}
		}
		prevWall = env.Wall

		var p kalshiPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil //nolint:nilerr // skip malformed frame, don't abort the whole report
		}
		typeHist[p.Type]++
		if p.Type == "error" {
			errorFrames++
		}
		if t := p.Msg.MarketTicker; t != "" {
			tickerHist[t]++
			totalTickers[t] = struct{}{}
			if prefix := seriesPrefix(t); prefix != "" {
				seriesHist[prefix]++
			}
		}
		return nil
	})

	dur := time.Duration(lastWall - firstWall).Round(time.Millisecond)
	fmt.Printf("- **Frames**: %d (%.2f MiB)\n", frames, float64(bytesTotal)/(1<<20))
	fmt.Printf("- **Duration**: %s (first→last wall)\n", dur)
	fmt.Printf("- **Unique tickers seen**: %d\n", len(totalTickers))
	fmt.Printf("- **Wall-clock regressions**: %d\n", regressions)
	fmt.Printf("- **Error frames**: %d\n\n", errorFrames)

	fmt.Printf("### Message type histogram\n\n")
	printHistMap(typeHist, 10)

	fmt.Printf("### Series frame counts\n\n")
	printHistMap(seriesHist, 20)

	fmt.Printf("### Top 10 active markets\n\n")
	printHistMap(tickerHist, 10)

	fmt.Printf("### Inter-frame gap distribution (ms)\n\n")
	gapHist.print()
	fmt.Println()
}

// seriesPrefix extracts the leading A-Z prefix from a market ticker.
// e.g. "KXHIGHNY-26APR22-T64" → "KXHIGHNY".
func seriesPrefix(ticker string) string {
	for i, r := range ticker {
		if r < 'A' || r > 'Z' {
			return ticker[:i]
		}
	}
	return ticker
}

// --- Open-Meteo report ------------------------------------------------------

func reportOpenMeteo(dir string, files []string) {
	fmt.Printf("## openmeteo\n\n")
	fmt.Printf("Journals: %d in %s\n\n", len(files), dir)

	var (
		frames     int
		bytesTotal int
		firstWall  int64
		lastWall   int64
		perLoc     = map[string]int{}
		perLocGap  = map[string]*hist{}
		prevLoc    = map[string]int64{}
		sizeHist   = newHist()
	)

	_ = forEachFrame(files, func(env envelope, n int, firstInFile bool) error {
		frames++
		bytesTotal += n
		sizeHist.add(float64(len(env.Payload)))
		if firstWall == 0 {
			firstWall = env.Wall
		}
		lastWall = env.Wall
		if firstInFile {
			prevLoc = map[string]int64{}
		}
		loc := strings.TrimPrefix(env.Kind, "rest:")
		perLoc[loc]++
		if prev, ok := prevLoc[loc]; ok {
			if perLocGap[loc] == nil {
				perLocGap[loc] = newHist()
			}
			perLocGap[loc].add(float64(env.Wall-prev) / 1e9) // ns → s
		}
		prevLoc[loc] = env.Wall
		return nil
	})

	dur := time.Duration(lastWall - firstWall).Round(time.Second)
	fmt.Printf("- **Frames**: %d (%.2f MiB)\n", frames, float64(bytesTotal)/(1<<20))
	fmt.Printf("- **Duration**: %s\n", dur)
	fmt.Printf("- **Avg payload size**: %.1f KiB\n\n", sizeHist.mean()/1024)

	fmt.Printf("### Polls per location\n\n")
	printHistMap(perLoc, 20)

	if len(perLocGap) > 0 {
		fmt.Printf("### Inter-poll gap distribution per location (seconds)\n\n")
		keys := make([]string, 0, len(perLocGap))
		for k := range perLocGap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("**%s:** ", k)
			perLocGap[k].printInline()
			fmt.Println()
		}
		fmt.Println()
	}
}

// --- histogram helper -------------------------------------------------------

type hist struct{ vals []float64 }

func newHist() *hist { return &hist{} }

func (h *hist) add(v float64) { h.vals = append(h.vals, v) }

func (h *hist) mean() float64 {
	if len(h.vals) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range h.vals {
		s += v
	}
	return s / float64(len(h.vals))
}

func (h *hist) percentile(p float64) float64 {
	if len(h.vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(h.vals))
	copy(sorted, h.vals)
	sort.Float64s(sorted)
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

func (h *hist) max() float64 {
	if len(h.vals) == 0 {
		return 0
	}
	m := h.vals[0]
	for _, v := range h.vals {
		if v > m {
			m = v
		}
	}
	return m
}

func (h *hist) print() {
	if len(h.vals) == 0 {
		fmt.Println("_(no samples)_")
		return
	}
	fmt.Printf("- samples: %d\n", len(h.vals))
	fmt.Printf("- mean: %.2f, p50: %.2f, p95: %.2f, p99: %.2f, max: %.2f\n",
		h.mean(), h.percentile(0.5), h.percentile(0.95), h.percentile(0.99), h.max())
}

func (h *hist) printInline() {
	if len(h.vals) == 0 {
		fmt.Print("(no samples)")
		return
	}
	fmt.Printf("n=%d  mean=%.1f  p50=%.1f  p95=%.1f  max=%.1f",
		len(h.vals), h.mean(), h.percentile(0.5), h.percentile(0.95), h.max())
}

func printHistMap(m map[string]int, topN int) {
	type kv struct {
		k string
		v int
	}
	items := make([]kv, 0, len(m))
	for k, v := range m {
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].v > items[j].v })
	if topN > 0 && len(items) > topN {
		items = items[:topN]
	}
	for _, it := range items {
		fmt.Printf("- %-30s %6d\n", it.k, it.v)
	}
	fmt.Println()
}
