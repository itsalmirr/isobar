// Package coinbase implements a public WebSocket subscriber for the
// Coinbase Exchange feed (wss://ws-feed.exchange.coinbase.com).
//
// Public market-data channels (ticker, matches, level2_batch, full) require
// no authentication, so this client carries no credentials. For the BTC
// lead-lag analysis we only need ticker + matches.
package coinbase

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/coder/websocket"
)

// Options configures the public WS subscriber.
type Options struct {
	WSURL          string
	ProductIDs     []string // e.g., ["BTC-USD"]
	Channels       []string // e.g., ["ticker", "matches"]
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxAttempts    int // 0 = infinite
	Logger         *slog.Logger
	// OnFrame receives each raw WS message. Bytes are owned by the
	// transport's read buffer; consume synchronously or copy.
	OnFrame func(raw []byte)
}

// Subscriber maintains a public Coinbase WS connection.
type Subscriber struct {
	opts   Options
	logger *slog.Logger
}

// NewSubscriber returns a Subscriber. Call Run to start it.
func NewSubscriber(opts Options) *Subscriber {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.InitialBackoff <= 0 {
		opts.InitialBackoff = 500 * time.Millisecond
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.OnFrame == nil {
		opts.OnFrame = func([]byte) {}
	}
	return &Subscriber{opts: opts, logger: opts.Logger.With("component", "coinbase-ws")}
}

// Run pumps frames until ctx is cancelled. Returns nil on clean exit.
func (s *Subscriber) Run(ctx context.Context) error {
	backoff := s.opts.InitialBackoff
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := s.runOnce(ctx)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		attempt++
		if s.opts.MaxAttempts > 0 && attempt >= s.opts.MaxAttempts {
			return fmt.Errorf("coinbase-ws: max reconnect attempts exceeded: %w", err)
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/4)+1))
		s.logger.Warn("ws disconnected; reconnecting", "err", err, "wait", wait, "attempt", attempt)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff *= 2
		if backoff > s.opts.MaxBackoff {
			backoff = s.opts.MaxBackoff
		}
	}
}

func (s *Subscriber) runOnce(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, s.opts.WSURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close(websocket.StatusInternalError, "subscriber exit")

	conn.SetReadLimit(8 << 20)

	if err := s.subscribe(ctx, conn); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	s.logger.Info("ws connected", "products", s.opts.ProductIDs, "channels", s.opts.Channels)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		s.opts.OnFrame(data)
	}
}

type subscribeMessage struct {
	Type       string   `json:"type"`
	ProductIDs []string `json:"product_ids"`
	Channels   []string `json:"channels"`
}

func (s *Subscriber) subscribe(ctx context.Context, conn *websocket.Conn) error {
	body, err := json.Marshal(subscribeMessage{
		Type:       "subscribe",
		ProductIDs: s.opts.ProductIDs,
		Channels:   s.opts.Channels,
	})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, body)
}
