package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bytedance/sonic"
)

// Book is the reconstructable state of one Kalshi market's two-sided
// order book. Yes and No are maps from integer price-cents (1..99) to
// resting quantity (fractional, per Kalshi's `_fp` fields).
//
// Kalshi binary-outcome markets price Yes and No independently; a
// level on the Yes side at price P means someone will sell a Yes
// contract for P cents, and a level on the No side at price P means
// someone will sell a No contract for P cents. By arbitrage,
// Yes_ask = 1 - No_bid (in cents, 100 - BestNoBid).
//
// A Book has a lifecycle: it starts uninitialized and becomes ready
// after the first successful ApplySnapshot. A snapshot apply error
// (malformed payload) resets state back to uninitialized and clears
// the level maps — a stale partial book is more dangerous than no book.
// Deltas applied while uninitialized would mutate against an empty
// state and produce phantom levels; ApplyFrameToBooks rejects them
// for that reason. Callers reading top-of-book scalars (BestYesBid,
// MidYesCents, …) on an uninitialized book get zero values — gate
// reads with IsReady() if you need to distinguish "no levels yet" from
// "no book yet".
type Book struct {
	Yes map[int]float64
	No  map[int]float64

	// initialized tracks whether this book has received a valid snapshot.
	// Lifecycle: false → ApplySnapshot success → true; ApplySnapshot
	// error → false (and Yes/No are cleared so a partial state can't
	// linger). ApplyDelta does not transition this state.
	initialized bool
}

// NewBook returns an empty book with non-nil level maps. The book is
// in the uninitialized state until the first successful ApplySnapshot.
func NewBook() *Book {
	return &Book{Yes: map[int]float64{}, No: map[int]float64{}}
}

// IsReady reports whether the book has received a valid snapshot and
// is safe to read or apply deltas against. A book is ready after the
// first successful ApplySnapshot and stays ready across subsequent
// successful snapshots and deltas. A snapshot apply error reverts the
// book to uninitialized (not-ready) and clears its level maps.
func (b *Book) IsReady() bool { return b.initialized }

// snapshotMsg is the Msg payload of an `orderbook_snapshot` frame.
type snapshotMsg struct {
	MarketTicker string     `json:"market_ticker"`
	MarketID     string     `json:"market_id"`
	YesDollarsFP [][]string `json:"yes_dollars_fp"`
	NoDollarsFP  [][]string `json:"no_dollars_fp"`
}

// deltaMsg is the Msg payload of an `orderbook_delta` frame.
type deltaMsg struct {
	MarketTicker string `json:"market_ticker"`
	MarketID     string `json:"market_id"`
	Price        string `json:"price_dollars"`
	DeltaFP      string `json:"delta_fp"`
	Side         string `json:"side"`
}

// ApplySnapshot replaces the book with the levels carried by a
// snapshot frame's Msg. The book is wiped first — snapshots are
// authoritative.
//
// On success the book transitions to the ready state (IsReady()==true).
// On parse failure the book is left in the uninitialized state with
// empty Yes/No maps: a partial snapshot is worse than no snapshot
// because subsequent deltas would mutate against levels that may not
// reflect the true exchange state.
func (b *Book) ApplySnapshot(msg json.RawMessage) error {
	var s snapshotMsg
	if err := sonic.Unmarshal(msg, &s); err != nil {
		// Wipe state and stay uninitialized. A half-decoded snapshot
		// is exactly the failure mode we're hardening against.
		b.Yes = map[int]float64{}
		b.No = map[int]float64{}
		b.initialized = false
		return fmt.Errorf("parse snapshot: %w", err)
	}
	b.applySnapshotMsg(&s)
	return nil
}

// applySnapshotMsg populates the book from an already-decoded snapshot
// struct. It is the decode-once inner half of ApplySnapshot: the frame
// kernel (ApplyFrameToBooks) decodes the Msg a single time to read the
// ticker, then hands the struct here rather than paying a second
// sonic.Unmarshal of the same bytes — the dominant allocation on the
// offline replay hot path. The caller owns the decode-error path (see
// ApplyFrameToBooks); this method assumes s is well-formed and never
// fails.
//
// Negative or zero level quantities are rejected here, mirroring the
// delta path (ApplyDelta drops a level once its quantity reaches <=0):
// a snapshot claiming a negative resting quantity is malformed, and
// storing it would let a top-of-book scalar read a book that violates
// the never-negative resting-quantity contract.
func (b *Book) applySnapshotMsg(s *snapshotMsg) {
	b.Yes = make(map[int]float64, len(s.YesDollarsFP))
	b.No = make(map[int]float64, len(s.NoDollarsFP))
	for _, lvl := range s.YesDollarsFP {
		pc, qty, err := parseLevel(lvl)
		if err != nil || qty <= 0 {
			continue // tolerate malformed or non-positive level
		}
		b.Yes[pc] = qty
	}
	for _, lvl := range s.NoDollarsFP {
		pc, qty, err := parseLevel(lvl)
		if err != nil || qty <= 0 {
			continue
		}
		b.No[pc] = qty
	}
	b.initialized = true
}

// ApplyDelta adjusts one level by the signed delta quantity. A delta
// that drives a level to zero (or below) removes that level.
//
// This is a low-level primitive: it does not gate on IsReady().
// The state-machine enforcement lives in ApplyFrameToBooks, which
// is the supported entrypoint for replaying tape frames. Callers
// that invoke ApplyDelta directly are responsible for ensuring the
// book has a valid snapshot first.
func (b *Book) ApplyDelta(msg json.RawMessage) error {
	var d deltaMsg
	if err := sonic.Unmarshal(msg, &d); err != nil {
		return fmt.Errorf("parse delta: %w", err)
	}
	return b.applyDeltaMsg(&d)
}

// applyDeltaMsg applies an already-decoded delta struct. It is the
// decode-once inner half of ApplyDelta, letting ApplyFrameToBooks reuse
// the single decode it performed to read the ticker instead of
// re-parsing the same Msg bytes. See applySnapshotMsg for the rationale.
func (b *Book) applyDeltaMsg(d *deltaMsg) error {
	pc, err := priceDollarsToCents(d.Price)
	if err != nil {
		return fmt.Errorf("delta price %q: %w", d.Price, err)
	}
	dq, err := strconv.ParseFloat(d.DeltaFP, 64)
	if err != nil {
		return fmt.Errorf("delta qty %q: %w", d.DeltaFP, err)
	}
	var side map[int]float64
	switch d.Side {
	case "yes":
		side = b.Yes
	case "no":
		side = b.No
	default:
		return fmt.Errorf("unknown side %q", d.Side)
	}
	newQty := side[pc] + dq
	if newQty <= 0 {
		delete(side, pc)
		return nil
	}
	side[pc] = newQty
	return nil
}

// --- typed errors & observability -------------------------------------------

// ErrBookUninitialized is returned when a delta frame arrives for a
// ticker whose book has not received a valid snapshot. Applying the
// delta would mutate an empty state and produce phantom levels that
// don't exist in any real snapshot, so the frame is dropped.
var ErrBookUninitialized = errors.New("orderbook: book is uninitialized; awaiting snapshot")

// ErrSnapshotApplyFailed wraps a snapshot-apply error. The book's
// state has been reset to uninitialized and its level maps cleared;
// subsequent deltas will be rejected with ErrBookUninitialized until
// a fresh successful snapshot arrives.
var ErrSnapshotApplyFailed = errors.New("orderbook: snapshot apply failed; book state was reset to uninitialized")

// Package-level counters. atomic.Uint64 so reads in tests don't race
// with the producer goroutine. Read via the accessors below.
var (
	rejectedDeltasTotal  atomic.Uint64
	snapshotErrorsTotal  atomic.Uint64
	orderbookLogSuppress sync.Map // key "<ticker>:<reason>" → *logSuppressEntry
)

// logSuppressEntry tracks per-(ticker, reason) log suppression so that
// repeated identical failures don't spam the log. The first failure
// in a window logs immediately (allocating the entry). Subsequent
// failures increment current without logging. On recovery
// (clearLogSuppression), current is moved into priorWindow and the
// entry is removed; the next first-failure log entry for the same
// key surfaces priorWindow as a "suppressed_prev_window" attribute so
// operators can see how noisy the prior failure window actually was.
type logSuppressEntry struct {
	current atomic.Uint64
}

// priorSuppressed is a side table holding the suppressed-count from
// the most recently closed window for each key. Reads consume the
// entry — the next first-log emits it once, then it disappears.
var priorSuppressed sync.Map // key "<ticker>:<reason>" → uint64

// RejectedDeltasTotal returns the cumulative count of delta frames
// dropped because the destination book was uninitialized. Exposed as
// a counter accessor for tests and for the Phase 6 Prometheus bridge.
func RejectedDeltasTotal() uint64 { return rejectedDeltasTotal.Load() }

// SnapshotErrorsTotal returns the cumulative count of snapshot-apply
// errors observed. Exposed as a counter accessor for tests and for
// the Phase 6 Prometheus bridge.
func SnapshotErrorsTotal() uint64 { return snapshotErrorsTotal.Load() }

// ResetCountersForTest zeros all package-level counters and clears
// the log-suppression maps. Intended for test isolation; do not call
// from production code.
func ResetCountersForTest() {
	rejectedDeltasTotal.Store(0)
	snapshotErrorsTotal.Store(0)
	orderbookLogSuppress.Range(func(k, _ any) bool {
		orderbookLogSuppress.Delete(k)
		return true
	})
	priorSuppressed.Range(func(k, _ any) bool {
		priorSuppressed.Delete(k)
		return true
	})
}

// logOnceOrSuppress dedupes log messages by (ticker, reason) key.
// The first time a key is seen in a window, it logs immediately and
// stores a suppression entry. Subsequent calls with the same key
// increment the entry's `current` counter without logging. The first
// log entry per key surfaces any suppressed-count carried over from
// the previous window (set by clearLogSuppression) as a
// `suppressed_prev_window` attribute so operators can see how noisy
// the prior failure window actually was.
//
// clearLogSuppression(ticker) closes the current window for all keys
// of that ticker — call it after a recovery event (successful
// snapshot) so the next failure logs again.
func logOnceOrSuppress(level slog.Level, ticker, reason, msg string, extra ...slog.Attr) {
	key := ticker + ":" + reason
	if _, loaded := orderbookLogSuppress.LoadOrStore(key, &logSuppressEntry{}); loaded {
		// Already in a suppression window — bump current and skip.
		if cur, ok := orderbookLogSuppress.Load(key); ok {
			cur.(*logSuppressEntry).current.Add(1)
		}
		return
	}
	// First log in this window. Drain any prior-window suppressed
	// count so we can surface it on this entry exactly once.
	attrs := make([]slog.Attr, 0, len(extra)+3)
	attrs = append(attrs, slog.String("ticker", ticker), slog.String("reason", reason))
	if prev, ok := priorSuppressed.LoadAndDelete(key); ok {
		if n, ok := prev.(uint64); ok && n > 0 {
			attrs = append(attrs, slog.Uint64("suppressed_prev_window", n))
		}
	}
	attrs = append(attrs, extra...)
	slog.Default().LogAttrs(context.Background(), level, msg, attrs...)
}

// clearLogSuppression closes the suppression window for every key
// belonging to a ticker. Any non-zero `current` count is moved to
// priorSuppressed so the next first-log entry can surface it. After
// this call, the next failure for the same (ticker, reason) logs
// again as the start of a fresh window.
func clearLogSuppression(ticker string) {
	prefix := ticker + ":"
	orderbookLogSuppress.Range(func(k, v any) bool {
		ks, ok := k.(string)
		if !ok {
			return true
		}
		if !strings.HasPrefix(ks, prefix) {
			return true
		}
		if entry, ok := v.(*logSuppressEntry); ok {
			if n := entry.current.Load(); n > 0 {
				priorSuppressed.Store(ks, n)
			}
		}
		orderbookLogSuppress.Delete(k)
		return true
	})
}

// ApplyFrameToBooks updates the books map (and optional walls map) by
// applying one Kalshi frame. This is the supported entrypoint for
// replaying tape frames: it enforces the book lifecycle state machine
// (snapshot first, then deltas) and updates the package-level
// observability counters.
//
// Return values:
//   - malformed: true if the frame's payload could not be decoded or
//     applied. Used by callers (cmd/analyze) for malformed-frame counts.
//   - err: a typed error describing why the frame was not applied.
//     nil on success and on no-op frames (no ticker, non-book frame
//     type, stray delta for an unknown ticker). ErrSnapshotApplyFailed
//     for a malformed snapshot (book is left uninitialized).
//     ErrBookUninitialized for a delta whose target book has no valid
//     snapshot yet (book is unchanged).
//
// This is the shared book-reconstruction kernel used by cmd/analyze and
// cmd/edge-scan. Pass walls=nil if you don't need per-ticker wall stamps.
func ApplyFrameToBooks(books map[string]*Book, walls map[string]int64, env Envelope, f KalshiFrame) (malformed bool, err error) {
	// Book frames carry market_ticker inside the snapshot/delta body,
	// so decode the Msg exactly once here and read the ticker off the
	// decoded struct rather than paying a second full sonic.Unmarshal
	// via f.MarketTicker(). That redundant decode was the dominant
	// allocation on the offline replay hot path (analyze walks every
	// recorded frame). Non-book frames fall through to MarketTicker(),
	// which is cheap enough at their far lower rate.
	switch f.Type {
	case "orderbook_snapshot":
		var s snapshotMsg
		// A decode error still populates s.MarketTicker for any
		// well-formed prefix of the body, so read the ticker before
		// branching on the error: the state machine must create and
		// reset books[ticker] even for a malformed snapshot so that
		// subsequent deltas for it are rejected rather than replayed
		// against phantom state.
		decErr := sonic.Unmarshal(f.Msg, &s)
		ticker := s.MarketTicker
		if ticker == "" {
			return false, nil
		}
		if books[ticker] == nil {
			books[ticker] = NewBook()
		}
		book := books[ticker]
		if decErr != nil {
			// Wipe state and stay uninitialized. A half-decoded
			// snapshot is exactly the failure mode we're hardening
			// against.
			book.Yes = map[int]float64{}
			book.No = map[int]float64{}
			book.initialized = false
			snapshotErrorsTotal.Add(1)
			logOnceOrSuppress(slog.LevelError, ticker, "snapshot_apply_failed",
				"orderbook: snapshot apply failed; book reset to uninitialized",
				slog.String("err", decErr.Error()),
			)
			return true, fmt.Errorf("%w: ticker=%s: parse snapshot: %w", ErrSnapshotApplyFailed, ticker, decErr)
		}
		book.applySnapshotMsg(&s)
		// Successful snapshot — book is ready. Clear any prior
		// suppression window so the next failure logs again (and
		// includes the prior window's suppressed-count).
		clearLogSuppression(ticker)
		if walls != nil {
			walls[ticker] = env.Wall
		}
		return false, nil
	case "orderbook_delta":
		var d deltaMsg
		// A malformed delta body still yields a usable ticker for its
		// well-formed prefix; if even that is missing the frame is a
		// no-op, matching the empty-ticker guard for other frame types.
		decErr := sonic.Unmarshal(f.Msg, &d)
		ticker := d.MarketTicker
		if ticker == "" {
			return false, nil
		}
		book := books[ticker]
		if book == nil {
			// Stray delta for an unknown ticker — pre-existing
			// behavior. No counter bump: this is the "no subscription
			// yet" case, not the "snapshot failed" case.
			return false, nil
		}
		if !book.IsReady() {
			// Snapshot is missing or was reset by a prior failure.
			// Rejecting the delta is the whole point of this state
			// machine: applying it would mutate empty maps and produce
			// phantom levels that don't exist in any real snapshot.
			rejectedDeltasTotal.Add(1)
			logOnceOrSuppress(slog.LevelWarn, ticker, "delta_uninitialized",
				"orderbook: delta rejected; book is uninitialized",
			)
			return false, fmt.Errorf("%w: ticker=%s", ErrBookUninitialized, ticker)
		}
		if decErr != nil {
			return true, fmt.Errorf("apply delta ticker=%s: parse delta: %w", ticker, decErr)
		}
		if applyErr := book.applyDeltaMsg(&d); applyErr != nil {
			return true, fmt.Errorf("apply delta ticker=%s: %w", ticker, applyErr)
		}
		if walls != nil {
			walls[ticker] = env.Wall
		}
		return false, nil
	}
	// Non-book frame type (subscribed, trade, heartbeat, …): nothing to
	// reconstruct. f.MarketTicker() remains the accessor for callers
	// that need the ticker of such frames; the book kernel does not.
	return false, nil
}

// BestYesBid returns the highest yes-side price in cents, or 0 if the
// yes side is empty.
func (b *Book) BestYesBid() int { return maxKey(b.Yes) }

// BestNoBid returns the highest no-side price in cents, or 0 if the
// no side is empty.
func (b *Book) BestNoBid() int { return maxKey(b.No) }

// BestYesAsk returns the implied yes-side ask (100 − BestNoBid), or
// 100 if the no side is empty.
func (b *Book) BestYesAsk() int {
	bid := maxKey(b.No)
	if bid == 0 {
		return 100
	}
	return 100 - bid
}

// MidYesCents returns the midpoint between yes bid and yes ask in
// cents, or 0 if either side is empty. Used as an implied-probability
// proxy: P(yes) ≈ MidYesCents / 100.
func (b *Book) MidYesCents() int {
	ybid := b.BestYesBid()
	yask := b.BestYesAsk()
	if ybid == 0 || yask == 100 {
		return 0
	}
	return (ybid + yask) / 2
}

// SpreadYesCents returns BestYesAsk − BestYesBid in cents, or 100 if
// either side is empty (wide-open market).
func (b *Book) SpreadYesCents() int {
	ybid := b.BestYesBid()
	yask := b.BestYesAsk()
	if ybid == 0 || yask == 100 {
		return 100
	}
	return yask - ybid
}

// --- helpers ----------------------------------------------------------------

// parseLevel turns a ["0.5500", "12.50"] pair into (55, 12.5).
func parseLevel(lvl []string) (int, float64, error) {
	if len(lvl) != 2 {
		return 0, 0, errors.New("level must be [price, qty]")
	}
	pc, err := priceDollarsToCents(lvl[0])
	if err != nil {
		return 0, 0, err
	}
	qty, err := strconv.ParseFloat(lvl[1], 64)
	if err != nil {
		return 0, 0, fmt.Errorf("qty %q: %w", lvl[1], err)
	}
	return pc, qty, nil
}

// priceDollarsToCents converts Kalshi's "0.5500"-style dollar string
// to integer cents. Rounds to nearest cent. Returns an error for
// non-numeric or out-of-range inputs.
func priceDollarsToCents(s string) (int, error) {
	d, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if d < 0 || d > 1.0 {
		return 0, fmt.Errorf("price %v out of [0, 1]", d)
	}
	return int(math.Round(d * 100)), nil
}

func maxKey(m map[int]float64) int {
	best := 0
	for k := range m {
		if k > best {
			best = k
		}
	}
	return best
}
