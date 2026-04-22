package replay

import "encoding/json"

// KalshiFrame is the decoded top-level shape of a Kalshi WS frame as
// stored in our journal Payload. Consumers typically discriminate on
// Type before reading the fuller Msg body.
type KalshiFrame struct {
	Type string          `json:"type"`
	ID   int64           `json:"id,omitempty"`
	Seq  int64           `json:"seq,omitempty"`
	Sid  int64           `json:"sid,omitempty"`
	Msg  json.RawMessage `json:"msg,omitempty"`
}

// KalshiMarketRef is a thin sub-shape of Msg common to book and trade
// frames: a market_ticker plus the full raw body. Use it when all you
// need is "which market is this for?"
type KalshiMarketRef struct {
	MarketTicker string `json:"market_ticker,omitempty"`
}

// MarketTicker returns the market ticker referenced by the frame's
// Msg, or the empty string if the frame has no Msg or no ticker.
func (f KalshiFrame) MarketTicker() string {
	if len(f.Msg) == 0 {
		return ""
	}
	var ref KalshiMarketRef
	if err := json.Unmarshal(f.Msg, &ref); err != nil {
		return ""
	}
	return ref.MarketTicker
}

// SeriesPrefix extracts the leading A-Z prefix from a market ticker.
// Returns the whole ticker if it contains no non-letter characters.
// Examples:
//
//	"KXHIGHNY-26APR22-T64" → "KXHIGHNY"
//	"KXLOWTNYC-26APR22-B48" → "KXLOWTNYC"
func SeriesPrefix(ticker string) string {
	for i, r := range ticker {
		if r < 'A' || r > 'Z' {
			return ticker[:i]
		}
	}
	return ticker
}
