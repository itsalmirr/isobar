// Package kalshi implements a REST + WS client for the Kalshi Trade API v2.
//
// Authentication uses RSA-PSS signed request headers. The REST client exposes
// typed methods for market-data discovery (market listing + pagination); the
// order-management and portfolio-query surface was deleted in the Phase-1
// reduction sweep (see CLIENT.md — ADR-0014's "no code path that can place a
// real order" rail is now structural, and recovery is git history per the
// ADR-0002 delete-don't-freeze precedent). The WS subscriber (ws.go) writes
// raw frames out via a callback so the recorder can persist them without
// decoding.
package kalshi

import (
	"strconv"
)

// Market represents a Kalshi market from GET /markets/{ticker}.
// The Kalshi API v2 returns pricing as dollar strings (e.g. "0.09") and
// volume/OI as string-encoded floats (e.g. "55713.00").
//
// Deliberately a subset of the API response: only fields something
// reads (discovery filters on Ticker/Status; the parse accessors
// below) are declared — unmarshal ignores the rest.
type Market struct {
	Ticker           string `json:"ticker"`
	Status           string `json:"status"`
	YesBidDollars    string `json:"yes_bid_dollars"`
	YesAskDollars    string `json:"yes_ask_dollars"`
	NoBidDollars     string `json:"no_bid_dollars"`
	NoAskDollars     string `json:"no_ask_dollars"`
	LastPriceDollars string `json:"last_price_dollars"`
	VolumeFP         string `json:"volume_fp"`
	Volume24hFP      string `json:"volume_24h_fp"`
	OpenInterestFP   string `json:"open_interest_fp"`
}

// YesBid returns the yes bid price as a float64 (in dollars).
func (m Market) YesBid() float64 { return parseFloat(m.YesBidDollars) }

// YesAsk returns the yes ask price as a float64 (in dollars).
func (m Market) YesAsk() float64 { return parseFloat(m.YesAskDollars) }

// NoBid returns the no bid price as a float64 (in dollars).
func (m Market) NoBid() float64 { return parseFloat(m.NoBidDollars) }

// NoAsk returns the no ask price as a float64 (in dollars).
func (m Market) NoAsk() float64 { return parseFloat(m.NoAskDollars) }

// LastPrice returns the last trade price as a float64 (in dollars).
func (m Market) LastPrice() float64 { return parseFloat(m.LastPriceDollars) }

// Volume returns the total volume as an int.
func (m Market) Volume() int { return parseInt(m.VolumeFP) }

// Volume24H returns the 24-hour volume as an int.
func (m Market) Volume24H() int { return parseInt(m.Volume24hFP) }

// OpenInterest returns the open interest as an int.
func (m Market) OpenInterest() int { return parseInt(m.OpenInterestFP) }

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseInt(s string) int {
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int(f)
}

// --- API response wrappers ---

type getMarketsResponse struct {
	Markets []Market `json:"markets"`
	Cursor  string   `json:"cursor"`
}
