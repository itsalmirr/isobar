// Command analyze reads recorded JSONL journals and produces a
// Phase-0.5 sanity report. MVP scope: does the recorder produce usable
// data? Frame counts, gap distributions, per-market activity,
// monotonicity checks. Orderbook reconstruction and edge math are
// follow-ups.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wh0o0pz/isobar/internal/replay"
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
		files, err := replay.ListJournals(dir)
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

// --- Kalshi report ----------------------------------------------------------

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

	_ = replay.ForEachFrame(files, func(env replay.Envelope, n int, firstInFile bool) error {
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

		var f replay.KalshiFrame
		if err := json.Unmarshal(env.Payload, &f); err != nil {
			return nil //nolint:nilerr // skip malformed frame, don't abort the whole report
		}
		typeHist[f.Type]++
		if f.Type == "error" {
			errorFrames++
		}
		if t := f.MarketTicker(); t != "" {
			tickerHist[t]++
			totalTickers[t] = struct{}{}
			if prefix := replay.SeriesPrefix(t); prefix != "" {
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

	_ = replay.ForEachFrame(files, func(env replay.Envelope, n int, firstInFile bool) error {
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

// --- histogram helper (internal to this CLI; promote to internal/ when reused)

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
