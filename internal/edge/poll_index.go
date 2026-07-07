package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wh0o0pz/isobar/internal/replay"
)

// pollIndex is a station-keyed time index of decoded Open-Meteo polls.
// "Latest poll before time T for station S" is a binary search.
type pollIndex struct {
	byStation map[string]*stationSeries
}

type stationSeries struct {
	times []time.Time
	polls []*Poll
}

// latest returns the most-recent poll at or before `before` for station,
// the wall time of that poll, and ok=false if no such poll exists.
func (idx *pollIndex) latest(station string, before time.Time) (*Poll, time.Time, bool) {
	ss, ok := idx.byStation[station]
	if !ok {
		return nil, time.Time{}, false
	}
	// Find first index with time > before; the poll just before it is our match.
	i := sort.Search(len(ss.times), func(i int) bool { return ss.times[i].After(before) })
	if i == 0 {
		return nil, time.Time{}, false
	}
	return ss.polls[i-1], ss.times[i-1], true
}

// loadPolls walks DataDir/openmeteo/<YYYY-MM-DD>/*.jsonl{,.gz}, decodes
// every frame's Payload as a Poll, and indexes by station + wall time.
//
// We use Frame.Kind ("rest:NY", "rest:MIA", "rest:LAX", "rest:CHI") to
// derive the station — same station codes the rest of edge/ uses.
func loadPolls(omRoot string, start, end time.Time) (*pollIndex, error) {
	files, err := dailyFiles(omRoot, start, end)
	if err != nil {
		return nil, err
	}
	idx := &pollIndex{byStation: map[string]*stationSeries{}}

	walkErr := replay.ForEachFrame(files, func(env replay.Envelope, _ int, _ bool) error {
		station, ok := stationFromKind(env.Kind)
		if !ok {
			return nil
		}
		poll, parseErr := ParsePoll(env.Payload)
		if parseErr != nil {
			return nil //nolint:nilerr // malformed polls are skipped; scans count missing pairings downstream
		}
		ss := idx.byStation[station]
		if ss == nil {
			ss = &stationSeries{}
			idx.byStation[station] = ss
		}
		ss.times = append(ss.times, time.Unix(0, env.Wall).UTC())
		ss.polls = append(ss.polls, poll)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, io.EOF) {
		return nil, walkErr
	}
	for _, ss := range idx.byStation {
		// Polls were written in time order per source, but across rotated
		// files we still resort to be safe.
		entries := make([]pollEntry, len(ss.times))
		for i := range ss.times {
			entries[i] = pollEntry{time: ss.times[i], poll: ss.polls[i]}
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].time.Before(entries[j].time) })
		for i, entry := range entries {
			ss.times[i] = entry.time
			ss.polls[i] = entry.poll
		}
	}
	return idx, nil
}

type pollEntry struct {
	time time.Time
	poll *Poll
}

// stationFromKind maps the Open-Meteo Frame.Kind ("rest:NY") to a Kalshi
// station code (KNYC/KMIA/KLAX/KMDW).
func stationFromKind(kind string) (string, bool) {
	switch kind {
	case "rest:NY":
		return "KNYC", true
	case "rest:MIA":
		return "KMIA", true
	case "rest:LAX":
		return "KLAX", true
	case "rest:CHI":
		return "KMDW", true
	}
	return "", false
}

// dailyFiles returns all journal files (.jsonl + .jsonl.gz) under
// root/<YYYY-MM-DD>/ for dates in [start, end). Files are returned in
// chronological order (filesystem name sort matches time order because
// the recorder timestamps file names).
func dailyFiles(root string, start, end time.Time) ([]string, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("stat %s: %w", root, err)
	}
	var out []string
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		dayDir := filepath.Join(root, d.Format("2006-01-02"))
		entries, err := os.ReadDir(dayDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", dayDir, err)
		}
		var dayFiles []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".jsonl.gz") {
				dayFiles = append(dayFiles, filepath.Join(dayDir, n))
			}
		}
		sort.Strings(dayFiles)
		out = append(out, dayFiles...)
	}
	return out, nil
}

// tradeMsg is the Kalshi WS trade-message inner shape. Recorded frames use
// string yes_price_dollars/no_price_dollars and taker_side; older v2 docs
// describe int yes_price/no_price with side. priceDollars()/side() handle
// both.
type tradeMsg struct {
	MarketTicker    string          `json:"market_ticker"`
	YesPrice        int             `json:"yes_price"`
	NoPrice         int             `json:"no_price"`
	YesPriceDollars string          `json:"yes_price_dollars,omitempty"`
	NoPriceDollars  string          `json:"no_price_dollars,omitempty"`
	PriceDollars    json.RawMessage `json:"price_dollars,omitempty"`
	Side            string          `json:"side"`
	TakerSide       string          `json:"taker_side,omitempty"`
	Count           int             `json:"count"`
	TSMs            int64           `json:"ts_ms"`
}

// side returns the trade's taker side, preferring `taker_side` (recorded
// Kalshi WS shape) and falling back to `side` (older docs/tests).
func (m tradeMsg) side() string {
	if m.TakerSide != "" {
		return m.TakerSide
	}
	return m.Side
}

// priceDollars returns the executed YES-price as a fraction in (0,1), or
// -1 if it can't be derived. Tries, in order: int yes_price/no_price (v2
// docs shape), string yes_price_dollars/no_price_dollars (recorded WS),
// legacy bare price_dollars string.
func (m tradeMsg) priceDollars() float64 {
	if m.YesPrice > 0 || m.NoPrice > 0 {
		// The YES-price of the print is yes_price regardless of which
		// side the taker was on; derive it from no_price (100 − no)
		// only when yes_price is absent. The side switch is kept as
		// shape validation: a missing/unknown side means we can't
		// trust the message. (A previous revision returned the raw
		// no_price for side="no" — the *NO* price — inverting the
		// price for every no-side taker in the int-cents shape; the
		// string path below always converted correctly.)
		var c int
		switch m.side() {
		case "yes", "no":
			c = m.YesPrice
			if c == 0 {
				c = 100 - m.NoPrice
			}
		default:
			return -1
		}
		if c <= 0 || c >= 100 {
			return -1
		}
		return float64(c) / 100
	}
	if m.YesPriceDollars != "" {
		var v float64
		if _, err := fmt.Sscanf(m.YesPriceDollars, "%f", &v); err == nil && v > 0 && v < 1 {
			return v
		}
	}
	if m.NoPriceDollars != "" {
		var v float64
		if _, err := fmt.Sscanf(m.NoPriceDollars, "%f", &v); err == nil && v > 0 && v < 1 {
			return 1 - v
		}
	}
	if len(m.PriceDollars) > 0 {
		var s string
		if err := json.Unmarshal(m.PriceDollars, &s); err == nil {
			var v float64
			if _, err := fmt.Sscanf(s, "%f", &v); err == nil && v > 0 && v < 1 {
				return v
			}
		}
	}
	return -1
}
