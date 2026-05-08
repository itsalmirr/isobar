// Package papertrade implements the live "would-have-bet" decision
// engine and append-only bet ledger used by cmd/paper-trader.
//
// It is intentionally narrower than cmd/edge-scan: edge-scan replays
// recorded data and emits one row per (ticker, end-of-window snapshot,
// nearest poll), whereas papertrade processes a stream of (ticker,
// current book, latest poll) snapshots and emits at most one Bet per
// (ticker, side, day) — once we've decided to take a bracket, repeated
// triggers are suppressed until the next session.
//
// Persistence reuses internal/journal so the ledger inherits the same
// rotation, fsync, and gzip-on-rotate behaviour as the recorder. Each
// bet is one JSONL line; cmd/settle (forthcoming) reads them later and
// reconciles outcomes against Kalshi settlement events and NOAA CLI
// observations.
package papertrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/wh0o0pz/isobar/internal/calibration/climate"
	"github.com/wh0o0pz/isobar/internal/clock"
	"github.com/wh0o0pz/isobar/internal/edge"
	"github.com/wh0o0pz/isobar/internal/journal"
	"github.com/wh0o0pz/isobar/internal/market"
	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/replay"
)

// SchemaVersion identifies the on-disk Bet shape. Bump on any field
// add/remove/rename so cmd/settle and cmd/score can refuse mismatched
// ledger files instead of silently misreading them.
const SchemaVersion = "papertrade-bet/v1"

// OutcomeSchemaVersion identifies the on-disk Outcome shape (cmd/settle
// output). Independent from SchemaVersion: the bet schema can evolve
// without forcing an outcome reformat and vice versa.
const OutcomeSchemaVersion = "papertrade-outcome/v1"

// Side is YES or NO — which side of the binary contract we'd buy.
type Side string

const (
	SideYes Side = "yes"
	SideNo  Side = "no"
)

// Model identifies which probability path produced a bet's edge. We
// record this so post-hoc analysis can split win-rate by model — if
// "naive" loses but "climate-shrunk" breaks even, we know the
// shrinkage is doing real work.
type Model string

const (
	ModelNaive          Model = "naive"
	ModelClimateShrunk  Model = "climate_shrunk"
	ModelMOSCalibrated  Model = "mos_calibrated"
)

// Bet is the on-disk record of one paper-trade decision. It captures
// everything needed to score the bet later: the bracket condition, the
// market state at decision time, the model probability, and the
// scheduled settlement instant.
//
// Fields are JSON-tagged for the ledger; field names favour readability
// over byte-count since paper-trade volume is low (10-100 bets/day).
type Bet struct {
	SchemaVersion string  `json:"schema_version"`
	ID            string  `json:"id"`            // ULID-ish, "ticker|side|YYYY-MM-DD"
	DecidedAt     int64   `json:"decided_at"`    // unix nanos, wall clock
	Ticker        string  `json:"ticker"`
	Series        string  `json:"series"`
	Location      string  `json:"location"`      // "NY", "MIA", ...
	BracketType   string  `json:"bracket_type"`  // "above" | "below"
	BracketTempF  float64 `json:"bracket_temp_f"`
	BracketDate   string  `json:"bracket_date"`  // "YYYY-MM-DD" local
	Side          Side    `json:"side"`
	Model         Model   `json:"model"`

	// Market state at decision time.
	MidCents    int `json:"mid_cents"`
	SpreadCents int `json:"spread_cents"`

	// Model output + edge.
	ModelP            float64 `json:"model_p"`
	EdgeCents         float64 `json:"edge_cents"`         // signed, side-specific
	FeeRoundTripCents int     `json:"fee_round_trip_cents"`

	// Ensemble shape at decision (for diagnostics).
	EnsembleMinF     float64 `json:"ensemble_min_f"`
	EnsembleMaxF     float64 `json:"ensemble_max_f"`
	MembersInBracket int     `json:"members_in_bracket"`

	// Forecast context.
	PollAgeSeconds float64 `json:"poll_age_seconds"`
	LeadHours      float64 `json:"lead_hours"`

	// Settlement plan: when we expect Kalshi to settle this market.
	SettlementUnixNano int64 `json:"settlement_unix_nano"`

	// SizingContracts is the (paper) position size. Phase 0 paper bets
	// always size 1 contract; this field exists so cmd/score can
	// compute P&L without re-deriving the policy.
	SizingContracts int `json:"sizing_contracts"`
}

// Resolution is the normalised outcome of a bracket: which side
// settled in-the-money, or "void"/"unsettled" for edge cases.
type Resolution string

const (
	ResolutionYes       Resolution = "yes"
	ResolutionNo        Resolution = "no"
	ResolutionVoid      Resolution = "void"
	ResolutionUnsettled Resolution = "unsettled"
)

// Outcome is the on-disk record of one bet's resolution. cmd/settle
// produces these by joining ledger Bets against Kalshi market
// settlement state. P&L is computed in the same fee-aware units as the
// bet's predicted edge so cmd/score can compare prediction to outcome
// in like terms.
//
// One Outcome per Bet. Idempotency: settle skips bet IDs already
// present in any outcomes-*.jsonl[.gz] file under the bracket date.
type Outcome struct {
	SchemaVersion string `json:"schema_version"`
	BetID         string `json:"bet_id"`
	SettledAt     int64  `json:"settled_at"` // unix nanos when WE recorded the outcome
	Ticker        string `json:"ticker"`

	// Raw Kalshi state — kept verbatim for forensics on resolution
	// disputes (Kalshi has flipped market resolutions in the past).
	KalshiStatus string `json:"kalshi_status"`
	KalshiResult string `json:"kalshi_result"`

	// Normalised resolution.
	Resolution Resolution `json:"resolution"`

	// Bet recap — duplicated so an Outcome row is self-contained for
	// cmd/score without needing to re-join against the bet ledger.
	BetSide      Side    `json:"bet_side"`
	BetMidCents  int     `json:"bet_mid_cents"`
	BetEdgeCents float64 `json:"bet_edge_cents"`
	BetFeeCents  int     `json:"bet_fee_cents"`

	// P&L in cents per contract: (100 if won else 0) - mid - fee.
	// Void resolutions refund the mid (paper assumption); fees still hit.
	Won      bool `json:"won"`
	PnLCents int  `json:"pnl_cents"`
}

// PnLFromBet computes per-contract paper P&L in cents from a Bet and
// the resolved Resolution. Returns (won, pnl).
//
//   - Win:    payout 100 → +100 - mid - fee
//   - Lose:   payout 0   → -mid - fee
//   - Void:   refund mid → 0 - fee  (fees still incurred at venue)
//   - Other:  treated as still-unsettled — caller should not Record an
//     Outcome until resolution lands.
//
// The fee used here is the same RoundTripFeeCents the Bet's edge
// prediction used; this preserves prediction/outcome unit parity for
// cmd/score's analytics.
func PnLFromBet(b Bet, res Resolution) (won bool, pnl int) {
	fee := b.FeeRoundTripCents
	switch res {
	case ResolutionVoid:
		return false, -fee
	case ResolutionYes:
		if b.Side == SideYes {
			return true, 100 - b.MidCents - fee
		}
		return false, -b.MidCents - fee
	case ResolutionNo:
		if b.Side == SideNo {
			return true, 100 - b.MidCents - fee
		}
		return false, -b.MidCents - fee
	default:
		return false, 0
	}
}

// Trigger configures when a snapshot should produce a bet. All fields
// have safe defaults via TriggerDefaults().
type Trigger struct {
	// MinEdgeCents is the per-side fee-aware edge threshold. A bet fires
	// when YesEdgeCents ≥ MinEdgeCents OR NoEdgeCents ≥ MinEdgeCents.
	MinEdgeCents float64

	// MaxSpreadCents drops snapshots with wide books, where the mid is
	// a poor proxy for the achievable execution price. 5¢ is the recorder-
	// observed median for KXHIGH/KXLOW NY around midday.
	MaxSpreadCents int

	// MaxPollAge skips signals based on stale forecasts. Open-Meteo
	// updates every ~6h on the GFS run schedule; 4h is comfortably
	// inside that window.
	MaxPollAge time.Duration

	// MinLeadHours is a guard against the "settlement is in 5 minutes
	// and the ensemble already collapsed" edge — those probabilities
	// are technically correct but the bet has no real informational
	// content. 2h is enough to let the model express a view.
	MinLeadHours float64

	// Model selects which probability path drives the trigger. When
	// ModelClimateShrunk is selected and no climate match exists, the
	// trigger falls back to naive — this matches what edge-scan does
	// today and is documented in Pass 3 §4.b.
	Model Model
}

// TriggerDefaults returns the recommended live-trading thresholds —
// 3¢ minimum edge, 5¢ max spread, 4h max poll age, 2h min lead.
func TriggerDefaults() Trigger {
	return Trigger{
		MinEdgeCents:   3.0,
		MaxSpreadCents: 5,
		MaxPollAge:     4 * time.Hour,
		MinLeadHours:   2.0,
		Model:          ModelClimateShrunk,
	}
}

// Snapshot is the input to Decide: one (ticker, market state, latest
// matching poll) tuple at decision time. MidCents and SpreadCents are
// extracted from a replay.Book by the caller — Decide is intentionally
// race-safe given that the underlying book may be mutated by other
// goroutines between extraction and decision. PollWall is the
// unix-nano wall timestamp of the poll, used to compute PollAgeSeconds
// against DecidedAt.
type Snapshot struct {
	Ticker      string
	MidCents    int
	SpreadCents int
	Poll        *openmeteo.EnsemblePoll
	PollWall    int64
	DecidedAt   int64 // unix nanos
}

// Decide evaluates one snapshot against the trigger. It returns
// (Bet, true) when a bet should be recorded, otherwise (zero, false).
// The decision is pure given a fixed climate lookup; no I/O.
//
// reasonSkip is non-empty when the snapshot was rejected by a guard
// (bad ticker, one-sided book, stale poll, etc.) — useful for log lines
// and metrics on the trigger-rejection rate.
func Decide(snap Snapshot, trig Trigger, clim *climate.Lookup) (bet Bet, reasonSkip string, ok bool) {
	tk, err := market.ParseTicker(snap.Ticker)
	if err != nil {
		return Bet{}, "unparseable ticker", false
	}
	if snap.MidCents <= 0 || snap.MidCents >= 100 {
		return Bet{}, "one-sided book", false
	}
	if trig.MaxSpreadCents > 0 && snap.SpreadCents > trig.MaxSpreadCents {
		return Bet{}, "spread too wide", false
	}
	if snap.Poll == nil {
		return Bet{}, "no poll", false
	}
	pollAge := time.Duration(snap.DecidedAt - snap.PollWall)
	if trig.MaxPollAge > 0 && pollAge > trig.MaxPollAge {
		return Bet{}, "poll too stale", false
	}

	maxTemps, minTemps, err := snap.Poll.DailyExtremes(tk.Date, tk.UTCOffset())
	if err != nil {
		return Bet{}, "bracket date outside forecast window", false
	}
	var members [openmeteo.NumMembers]float64
	switch tk.TempVar {
	case market.DailyHigh:
		members = maxTemps
	case market.DailyLow:
		members = minTemps
	}

	settle := edge.SettlementUTC(tk.Date, tk.UTCOffset())
	bookTime := time.Unix(0, snap.DecidedAt).UTC()
	leadHours := settle.Sub(bookTime).Hours()
	if trig.MinLeadHours > 0 && leadHours < trig.MinLeadHours {
		return Bet{}, "too close to settlement", false
	}

	row := edge.FromExtremes(tk, snap.MidCents, snap.SpreadCents, members, pollAge, leadHours, nil, clim)

	// Pick the probability path. Climate-shrunk falls back to naive when
	// no climate match exists — this matches edge-scan's behaviour and
	// avoids wasting live signals on unsupported brackets.
	var (
		modelP    float64
		yesEdge   float64
		noEdge    float64
		modelName Model
	)
	switch trig.Model {
	case ModelClimateShrunk:
		if !math.IsNaN(row.ClimateShrunkP) {
			modelP = row.ClimateShrunkP
			yesEdge = row.ClimateShrunkYesEdge
			noEdge = row.ClimateShrunkNoEdge
			modelName = ModelClimateShrunk
		} else {
			modelP = row.NaiveP
			yesEdge = row.YesEdgeCents
			noEdge = row.NoEdgeCents
			modelName = ModelNaive
		}
	case ModelMOSCalibrated:
		if math.IsNaN(row.CalibratedP) {
			return Bet{}, "no MOS bin matches", false
		}
		modelP = row.CalibratedP
		yesEdge = row.CalibratedYesEdgeCents
		noEdge = row.CalibratedNoEdgeCents
		modelName = ModelMOSCalibrated
	default:
		modelP = row.NaiveP
		yesEdge = row.YesEdgeCents
		noEdge = row.NoEdgeCents
		modelName = ModelNaive
	}

	// Trigger logic: take whichever side has the larger edge IFF it
	// clears the threshold. Symmetric ties (yesEdge == noEdge >=
	// threshold) pick YES — fee math makes simultaneous YES+NO edge
	// impossible after fees, so this is a degenerate case.
	var side Side
	var edgeCents float64
	switch {
	case yesEdge >= trig.MinEdgeCents && yesEdge >= noEdge:
		side = SideYes
		edgeCents = yesEdge
	case noEdge >= trig.MinEdgeCents:
		side = SideNo
		edgeCents = noEdge
	default:
		return Bet{}, "no side clears threshold", false
	}

	return Bet{
		SchemaVersion:      SchemaVersion,
		ID:                 betID(tk, side),
		DecidedAt:          snap.DecidedAt,
		Ticker:             tk.Raw,
		Series:             tk.Series,
		Location:           tk.Location(),
		BracketType:        tk.Type.String(),
		BracketTempF:       tk.TempF,
		BracketDate:        tk.Date.Format("2006-01-02"),
		Side:               side,
		Model:              modelName,
		MidCents:           snap.MidCents,
		SpreadCents:        snap.SpreadCents,
		ModelP:             modelP,
		EdgeCents:          edgeCents,
		FeeRoundTripCents:  row.FeeRoundTripCents,
		EnsembleMinF:       row.EnsembleMinF,
		EnsembleMaxF:       row.EnsembleMaxF,
		MembersInBracket:   row.MembersInBracket,
		PollAgeSeconds:     row.PollAgeSeconds,
		LeadHours:          row.LeadHours,
		SettlementUnixNano: settle.UnixNano(),
		SizingContracts:    1,
	}, "", true
}

// betID builds the deduplication key. Once we've taken a side on a
// (ticker, day) we suppress further triggers for that key — paper or
// real, you don't want the same edge firing 5,000 times as the book
// drifts a cent. Day comes from the bracket date, not the wall clock,
// so a bet decided at 23:59 on day-1 for day-2's bracket dedupes
// correctly with bets decided after midnight.
func betID(tk market.Ticker, side Side) string {
	return fmt.Sprintf("%s|%s|%s", tk.Raw, side, tk.Date.Format("2006-01-02"))
}

// Ledger is an append-only sink for Bets. The default implementation
// writes to a journal.Journal under data/papertrade/, with one JSONL
// line per bet plus an in-memory dedup set so repeated Decide hits for
// the same key after a process restart don't blow up the log.
//
// Recover() is called at startup to populate the dedup set from any
// existing ledger files for today's date.
type Ledger struct {
	jrn *journal.Journal

	mu      sync.Mutex
	written map[string]struct{}
}

// LedgerOptions configures Open(). Dir is the parent directory under
// which papertrade journals are written; the source name is fixed to
// "papertrade".
type LedgerOptions struct {
	Dir              string
	MaxFileBytes     int64
	MaxFileAge       time.Duration
	FlushEvery       time.Duration
	QueueSize        int
	CompressOnRotate bool
}

// OpenLedger opens (or creates) a paper-trade ledger under opts.Dir.
// Existing JSONL files are NOT scanned at boot — dedup recovery is the
// caller's responsibility via Recover, since reading historical files
// requires a path the journal package doesn't expose.
func OpenLedger(opts LedgerOptions, logger interface{ Debug(string, ...any) }) (*Ledger, error) {
	if opts.Dir == "" {
		return nil, errors.New("papertrade: ledger Dir required")
	}
	jrn, err := journal.Open(journal.Options{
		Dir:              opts.Dir,
		Source:           "papertrade",
		MaxFileBytes:     opts.MaxFileBytes,
		MaxFileAge:       opts.MaxFileAge,
		FlushEvery:       opts.FlushEvery,
		QueueSize:        opts.QueueSize,
		CompressOnRotate: opts.CompressOnRotate,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("papertrade: open journal: %w", err)
	}
	return &Ledger{
		jrn:     jrn,
		written: map[string]struct{}{},
	}, nil
}

// Record appends a bet to the ledger if its ID hasn't been seen yet.
// Returns true when the bet was written, false on dedup hit. Returns
// an error only on payload-marshal failure.
func (l *Ledger) Record(b Bet) (bool, error) {
	l.mu.Lock()
	if _, dup := l.written[b.ID]; dup {
		l.mu.Unlock()
		return false, nil
	}
	l.written[b.ID] = struct{}{}
	l.mu.Unlock()

	payload, err := json.Marshal(b)
	if err != nil {
		return false, fmt.Errorf("marshal bet: %w", err)
	}
	l.jrn.Write(journal.Frame{
		Stamp:   clock.Now(),
		Source:  "papertrade",
		Kind:    "bet",
		Payload: payload,
	})
	return true, nil
}

// MarkRecorded seeds the dedup set with an existing bet ID. Used at
// startup to skip bets that survived a process restart, when the caller
// has externally scanned the ledger files.
func (l *Ledger) MarkRecorded(id string) {
	l.mu.Lock()
	l.written[id] = struct{}{}
	l.mu.Unlock()
}

// Count returns how many distinct bets the ledger has seen this
// session (written + recovered). Useful for boot logging.
func (l *Ledger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.written)
}

// Close flushes pending bets and closes the underlying journal.
func (l *Ledger) Close() error {
	if l == nil || l.jrn == nil {
		return nil
	}
	return l.jrn.Close()
}

// IterBets walks ledger JSONL files under dir for date subdirs >=
// since (cushioned by one day for cross-midnight brackets) and calls
// fn for each decoded Bet. Returns the first traversal error; per-line
// decode failures are silently skipped so one corrupt frame doesn't
// poison the run.
//
// This is the read-side counterpart to OpenLedger+Record: cmd/settle
// uses it to find bets that need outcome resolution, and cmd/score
// uses it to walk the historical ledger.
func IterBets(dir string, since time.Time, fn func(Bet) error) error {
	cutoff := since.AddDate(0, 0, -1).UTC().Format("2006-01-02")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("papertrade: iter read %s: %w", dir, err)
	}
	var dateDirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) != 10 {
			continue
		}
		if _, parseErr := time.Parse("2006-01-02", name); parseErr != nil {
			continue
		}
		if name < cutoff {
			continue
		}
		dateDirs = append(dateDirs, name)
	}
	sort.Strings(dateDirs)

	for _, dd := range dateDirs {
		files, err := replay.ListJournals(filepath.Join(dir, dd))
		if err != nil {
			return fmt.Errorf("papertrade: iter list %s: %w", dd, err)
		}
		// Filter to bet files only — we co-locate outcomes in the same
		// directory but those are walked via IterOutcomes.
		bets := make([]string, 0, len(files))
		for _, f := range files {
			base := filepath.Base(f)
			if startsWith(base, "papertrade-") {
				bets = append(bets, f)
			}
		}
		var iterErr error
		_ = replay.ForEachFrame(bets, func(env replay.Envelope, _ int, _ bool) error {
			if env.Source != "papertrade" || env.Kind != "bet" {
				return nil
			}
			var b Bet
			if err := json.Unmarshal(env.Payload, &b); err != nil {
				return nil
			}
			if iterErr = fn(b); iterErr != nil {
				return iterErr
			}
			return nil
		})
		if iterErr != nil {
			return iterErr
		}
	}
	return nil
}

// IterOutcomes walks outcome JSONL files under dir for date subdirs
// >= since (with the same one-day cushion as IterBets) and calls fn
// for each decoded Outcome. Used by cmd/settle to skip already-
// resolved bets and by cmd/score to read the resolution stream.
func IterOutcomes(dir string, since time.Time, fn func(Outcome) error) error {
	cutoff := since.AddDate(0, 0, -1).UTC().Format("2006-01-02")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("papertrade: iter outcomes read %s: %w", dir, err)
	}
	var dateDirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) != 10 {
			continue
		}
		if _, parseErr := time.Parse("2006-01-02", name); parseErr != nil {
			continue
		}
		if name < cutoff {
			continue
		}
		dateDirs = append(dateDirs, name)
	}
	sort.Strings(dateDirs)

	for _, dd := range dateDirs {
		files, err := replay.ListJournals(filepath.Join(dir, dd))
		if err != nil {
			return fmt.Errorf("papertrade: iter outcomes list %s: %w", dd, err)
		}
		outs := make([]string, 0, len(files))
		for _, f := range files {
			base := filepath.Base(f)
			if startsWith(base, "outcomes-") {
				outs = append(outs, f)
			}
		}
		// outcomes files are written by cmd/settle directly (not via
		// internal/journal), so they are plain NDJSON of Outcome
		// records — not journal-wrapped. Open + line-read each.
		for _, f := range outs {
			if err := decodeOutcomeFile(f, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeOutcomeFile(path string, fn func(Outcome) error) error {
	rc, err := replay.Open(path)
	if err != nil {
		return fmt.Errorf("papertrade: open outcome %s: %w", path, err)
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var o Outcome
		if err := dec.Decode(&o); err != nil {
			if err.Error() == "EOF" || err == io.EOF {
				return nil
			}
			// Tolerate one bad line — skip and continue.
			continue
		}
		if err := fn(o); err != nil {
			return err
		}
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// AppendOutcome serializes o as a JSONL line and appends to a file in
// dir/<bracket-date>/outcomes-<unix>.jsonl. The caller passes the
// path; we don't manage rotation since cmd/settle is one-shot and
// produces small files (a few hundred outcomes per run max). Returns
// the bytes written (one line) for caller-side accounting.
func AppendOutcome(path string, o Outcome) (int, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("papertrade: mkdir for outcome: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("papertrade: open outcome %s: %w", path, err)
	}
	defer f.Close()
	body, err := json.Marshal(o)
	if err != nil {
		return 0, fmt.Errorf("papertrade: marshal outcome: %w", err)
	}
	body = append(body, '\n')
	n, err := f.Write(body)
	if err != nil {
		return 0, fmt.Errorf("papertrade: write outcome: %w", err)
	}
	return n, nil
}

// Recover scans existing ledger files under dir and seeds the dedup
// set with bets whose settlement is at or after `since`. This makes
// process restart safe: if paper-trader was killed and relaunched
// mid-day, every bet we already recorded for an unsettled bracket
// stays deduped.
//
// Returns the number of bets restored, plus the first error
// encountered during file traversal (per-file decode errors are logged
// to the journal source's logger, not returned, so a single corrupt
// line doesn't poison the whole recovery).
//
// dir is the parent directory passed to OpenLedger; the journal nests
// files under dir/<YYYY-MM-DD>/papertrade-*.jsonl[.gz]. We walk every
// date subdir whose name parses as a date >= the date of `since` minus
// one day (cushion for late-night brackets that stamp into the prior
// UTC day).
func (l *Ledger) Recover(dir string, since time.Time) (int, error) {
	if l == nil {
		return 0, errors.New("papertrade: nil ledger")
	}
	cutoff := since.AddDate(0, 0, -1) // walk one extra day for cross-midnight brackets
	cutoffStr := cutoff.UTC().Format("2006-01-02")

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // first run — nothing to recover
		}
		return 0, fmt.Errorf("papertrade: recover read %s: %w", dir, err)
	}
	var dateDirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) != 10 {
			continue
		}
		if _, parseErr := time.Parse("2006-01-02", name); parseErr != nil {
			continue
		}
		if name < cutoffStr {
			continue
		}
		dateDirs = append(dateDirs, name)
	}
	sort.Strings(dateDirs)

	restored := 0
	for _, dd := range dateDirs {
		files, err := replay.ListJournals(filepath.Join(dir, dd))
		if err != nil {
			return restored, fmt.Errorf("papertrade: recover list %s: %w", dd, err)
		}
		_ = replay.ForEachFrame(files, func(env replay.Envelope, _ int, _ bool) error {
			if env.Source != "papertrade" || env.Kind != "bet" {
				return nil
			}
			var b Bet
			if err := json.Unmarshal(env.Payload, &b); err != nil {
				return nil // tolerate corrupt line
			}
			if b.ID == "" {
				return nil
			}
			// Skip bets whose settlement is already in the past — the
			// market can't trade again, so we don't need a dedup entry.
			if b.SettlementUnixNano > 0 && time.Unix(0, b.SettlementUnixNano).Before(since) {
				return nil
			}
			l.MarkRecorded(b.ID)
			restored++
			return nil
		})
	}
	return restored, nil
}
