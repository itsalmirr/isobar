// Package kalshi implements a REST + WS client for the Kalshi Trade API v2.
//
// Authentication uses RSA-PSS signed request headers. The REST client exposes
// typed methods for market data, order management, and portfolio queries.
// The WS subscriber (ws.go) writes raw frames out via a callback so the
// recorder can persist them without decoding.
package kalshi

import (
	"strconv"
	"time"
)

// BaseURL is the production Kalshi Trade API base URL.
const BaseURL = "https://api.elections.kalshi.com/trade-api/v2"

// Order sides.
const (
	SideYes = "yes"
	SideNo  = "no"
)

// Order actions.
const (
	ActionBuy  = "buy"
	ActionSell = "sell"
)

// Time-in-force options (Kalshi API v2 requires full names).
const (
	TIFGoodTilCanceled   = "good_till_canceled"
	TIFFillOrKill        = "fill_or_kill"
	TIFImmediateOrCancel = "immediate_or_cancel"
)

// Market represents a Kalshi market from GET /markets/{ticker}.
// The Kalshi API v2 returns pricing as dollar strings (e.g. "0.09") and
// volume/OI as string-encoded floats (e.g. "55713.00").
type Market struct {
	Ticker                 string `json:"ticker"`
	EventTicker            string `json:"event_ticker"`
	Title                  string `json:"title"`
	Subtitle               string `json:"subtitle"`
	Status                 string `json:"status"`
	YesBidDollars          string `json:"yes_bid_dollars"`
	YesAskDollars          string `json:"yes_ask_dollars"`
	NoBidDollars           string `json:"no_bid_dollars"`
	NoAskDollars           string `json:"no_ask_dollars"`
	LastPriceDollars       string `json:"last_price_dollars"`
	VolumeFP               string `json:"volume_fp"`
	Volume24hFP            string `json:"volume_24h_fp"`
	OpenInterestFP         string `json:"open_interest_fp"`
	LiquidityDollars       string `json:"liquidity_dollars"`
	PreviousYesBidDollars  string `json:"previous_yes_bid_dollars"`
	PreviousYesAskDollars  string `json:"previous_yes_ask_dollars"`
	PreviousPriceDollars   string `json:"previous_price_dollars"`
	NotionalValueDollars   string `json:"notional_value_dollars"`
	Result                 string `json:"result"`
	ExpirationTime         string `json:"expiration_time"`
	ClosingTime            string `json:"close_time"`
	CanCloseEarly          bool   `json:"can_close_early"`
	ExpectedExpirationTime string `json:"expected_expiration_time"`
	MarketType             string `json:"market_type"`
	TickSize               int    `json:"tick_size"`
	ResponsePriceUnits     string `json:"response_price_units"`
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

func parseFP(s string) int {
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int(f + 0.5)
}

func parseFPCents(s string) int {
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int(f*100 + 0.5)
}

// OrderbookLevel is a [price, quantity] pair from the orderbook endpoint.
// Legacy format uses integer cents; new "fp" format uses dollar strings.
type OrderbookLevel [2]int

// OrderbookLevelFP is a [price_dollars, quantity] string pair from the new API format.
type OrderbookLevelFP [2]string

// Orderbook represents the response from GET /markets/{ticker}/orderbook.
type Orderbook struct {
	Yes []OrderbookLevel `json:"yes"`
	No  []OrderbookLevel `json:"no"`
}

// OrderbookFP represents the new "fp" (floating point dollars) format.
type OrderbookFP struct {
	YesDollars []OrderbookLevelFP `json:"yes_dollars"`
	NoDollars  []OrderbookLevelFP `json:"no_dollars"`
}

// CreateOrderRequest is the body for POST /portfolio/orders.
type CreateOrderRequest struct {
	Ticker        string `json:"ticker"`
	ClientOrderID string `json:"client_order_id,omitempty"`
	Side          string `json:"side"`
	Action        string `json:"action"`
	Count         int    `json:"count"`
	Type          string `json:"type"`
	YesPrice      int    `json:"yes_price,omitempty"`
	NoPrice       int    `json:"no_price,omitempty"`
	TimeInForce   string `json:"time_in_force,omitempty"`
	PostOnly      bool   `json:"post_only,omitempty"`
	ReduceOnly    bool   `json:"reduce_only,omitempty"`
	Expiration    string `json:"expiration_ts,omitempty"`
}

// Order represents a Kalshi order from the API.
// The API returns both legacy int fields and new dollar-string (FP) fields.
// Use the helper methods (YesPriceCents, TotalFillCount) which prefer the new format.
type Order struct {
	OrderID        string `json:"order_id"`
	ClientOrderID  string `json:"client_order_id"`
	Ticker         string `json:"ticker"`
	Side           string `json:"side"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Type           string `json:"type"`
	YesPrice       int    `json:"yes_price"`
	NoPrice        int    `json:"no_price"`
	RemainingCount int    `json:"remaining_count"`
	PlacedCount    int    `json:"place_count"`
	TakerFillCount int    `json:"taker_fill_count"`
	MakerFillCount int    `json:"maker_fill_count"`
	TakerFillCost  int    `json:"taker_fill_cost"`
	MakerFillCost  int    `json:"maker_fill_cost"`
	TimeInForce    string `json:"time_in_force,omitempty"`
	PostOnly       bool   `json:"post_only,omitempty"`
	CreatedTime    string `json:"created_time"`
	ExpirationTime string `json:"expiration_time,omitempty"`
	ClosedTime     string `json:"close_time,omitempty"`

	// FP (dollar-string) fields from API v2.
	YesPriceDollars  string `json:"yes_price_dollars,omitempty"`
	NoPriceDollars   string `json:"no_price_dollars,omitempty"`
	FillCountFP      string `json:"fill_count_fp,omitempty"`
	RemainingCountFP string `json:"remaining_count_fp,omitempty"`
	PlaceCountFP     string `json:"place_count_fp,omitempty"`
	MakerFillCountFP string `json:"maker_fill_count_fp,omitempty"`
	TakerFillCountFP string `json:"taker_fill_count_fp,omitempty"`
}

// YesPriceCents returns the YES price in cents, preferring the dollar-string format.
func (o Order) YesPriceCents() int {
	if o.YesPriceDollars != "" {
		return parseFPCents(o.YesPriceDollars)
	}
	return o.YesPrice
}

// TotalFillCount returns maker + taker fills, preferring FP format.
func (o Order) TotalFillCount() int {
	if o.FillCountFP != "" {
		return parseFP(o.FillCountFP)
	}
	if o.MakerFillCountFP != "" || o.TakerFillCountFP != "" {
		return parseFP(o.MakerFillCountFP) + parseFP(o.TakerFillCountFP)
	}
	return o.MakerFillCount + o.TakerFillCount
}

// RemainingQty returns the remaining quantity, preferring the FP format.
func (o Order) RemainingQty() int {
	if o.RemainingCountFP != "" {
		return parseFP(o.RemainingCountFP)
	}
	return o.RemainingCount
}

// Fill represents a trade execution from GET /portfolio/fills.
type Fill struct {
	TradeID         string    `json:"trade_id"`
	OrderID         string    `json:"order_id"`
	Ticker          string    `json:"ticker"`
	Side            string    `json:"side"`
	Action          string    `json:"action"`
	Count           int       `json:"count"`
	YesPrice        int       `json:"yes_price"`
	NoPrice         int       `json:"no_price"`
	IsTaker         bool      `json:"is_taker"`
	CreatedAt       time.Time `json:"created_time"`
	CountFP         string    `json:"count_fp,omitempty"`
	YesPriceDollars string    `json:"yes_price_dollars,omitempty"`
	NoPriceDollars  string    `json:"no_price_dollars,omitempty"`
}

// YesPriceCents returns the YES price in cents, preferring the dollar-string format.
func (f Fill) YesPriceCents() int {
	if f.YesPriceDollars != "" {
		return parseFPCents(f.YesPriceDollars)
	}
	return f.YesPrice
}

// Qty returns the fill quantity, preferring the FP format.
func (f Fill) Qty() int {
	if f.CountFP != "" {
		return parseFP(f.CountFP)
	}
	return f.Count
}

// Position represents a portfolio position from GET /portfolio/positions.
type Position struct {
	Ticker             string `json:"ticker"`
	EventTicker        string `json:"event_ticker"`
	EventExposure      int    `json:"event_exposure"`
	MarketExposure     int    `json:"market_exposure"`
	RealizedPnl        int    `json:"realized_pnl"`
	TotalTraded        int    `json:"total_traded"`
	RestingOrdersCount int    `json:"resting_orders_count"`
}

// --- API response wrappers ---

// Event represents a Kalshi event (a group of related markets).
type Event struct {
	EventTicker       string `json:"event_ticker"`
	SeriesTicker      string `json:"series_ticker"`
	Title             string `json:"title"`
	Category          string `json:"category"`
	MutuallyExclusive bool   `json:"mutually_exclusive"`
	MarketsCount      int    `json:"markets_count,omitempty"`
}

type getMarketResponse struct {
	Market Market `json:"market"`
}

type getOrderbookResponse struct {
	Orderbook   Orderbook   `json:"orderbook"`
	OrderbookFP OrderbookFP `json:"orderbook_fp"`
}

type createOrderResponse struct {
	Order Order `json:"order"`
}

type getOrderResponse struct {
	Order Order `json:"order"`
}

type getOrdersResponse struct {
	Orders []Order `json:"orders"`
	Cursor string  `json:"cursor"`
}

type getFillsResponse struct {
	Fills  []Fill `json:"fills"`
	Cursor string `json:"cursor"`
}

type getMarketsResponse struct {
	Markets []Market `json:"markets"`
	Cursor  string   `json:"cursor"`
}

type getPositionsResponse struct {
	MarketPositions []Position `json:"market_positions"`
	Cursor          string     `json:"cursor"`
}

type cancelOrderResponse struct {
	Order     Order `json:"order"`
	ReducedBy int   `json:"reduced_by"`
}

type getEventsResponse struct {
	Events []Event `json:"events"`
	Cursor string  `json:"cursor"`
}

// Balance represents the portfolio balance from GET /portfolio/balance.
type Balance struct {
	Balance        int `json:"balance"`
	PortfolioValue int `json:"portfolio_value"`
	PayoutPending  int `json:"payout_pending"`
}
