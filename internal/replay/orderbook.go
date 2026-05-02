package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
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
type Book struct {
	Yes map[int]float64
	No  map[int]float64
}

// NewBook returns an empty book with non-nil level maps.
func NewBook() *Book {
	return &Book{Yes: map[int]float64{}, No: map[int]float64{}}
}

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
func (b *Book) ApplySnapshot(msg json.RawMessage) error {
	var s snapshotMsg
	if err := json.Unmarshal(msg, &s); err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}
	b.Yes = make(map[int]float64, len(s.YesDollarsFP))
	b.No = make(map[int]float64, len(s.NoDollarsFP))
	for _, lvl := range s.YesDollarsFP {
		pc, qty, err := parseLevel(lvl)
		if err != nil {
			continue // tolerate malformed level
		}
		b.Yes[pc] = qty
	}
	for _, lvl := range s.NoDollarsFP {
		pc, qty, err := parseLevel(lvl)
		if err != nil {
			continue
		}
		b.No[pc] = qty
	}
	return nil
}

// ApplyDelta adjusts one level by the signed delta quantity. A delta
// that drives a level to zero (or below) removes that level.
func (b *Book) ApplyDelta(msg json.RawMessage) error {
	var d deltaMsg
	if err := json.Unmarshal(msg, &d); err != nil {
		return fmt.Errorf("parse delta: %w", err)
	}
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

// ApplyFrameToBooks updates the books map (and optional walls map) by
// applying one Kalshi frame. Snapshots create or replace the book for a
// ticker; deltas before any snapshot are silently skipped (book state is
// undefined). Returns true if the frame was malformed and dropped.
//
// This is the shared book-reconstruction kernel used by cmd/analyze and
// cmd/edge-scan. Pass walls=nil if you don't need per-ticker wall stamps.
func ApplyFrameToBooks(books map[string]*Book, walls map[string]int64, env Envelope, f KalshiFrame) (malformed bool) {
	ticker := f.MarketTicker()
	if ticker == "" {
		return false
	}
	switch f.Type {
	case "orderbook_snapshot":
		if books[ticker] == nil {
			books[ticker] = NewBook()
		}
		if err := books[ticker].ApplySnapshot(f.Msg); err != nil {
			return true
		}
		if walls != nil {
			walls[ticker] = env.Wall
		}
	case "orderbook_delta":
		if books[ticker] == nil {
			return false
		}
		if err := books[ticker].ApplyDelta(f.Msg); err != nil {
			return true
		}
		if walls != nil {
			walls[ticker] = env.Wall
		}
	}
	return false
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
