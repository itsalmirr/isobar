package kalshi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/wh0o0pz/isobar/internal/retry"
)

// SubscriberOptions configures a WS subscriber.
type SubscriberOptions struct {
	WSURL     string
	Signer    *Signer
	Channels  []string // e.g., ["orderbook_delta", "trade"] — ticker_v2 is rejected by the current API; see ADR-0006
	Tickers   []string // empty = all
	Reconnect ReconnectOptions
	Logger    *slog.Logger
	// OnFrame is called for each raw WS message. The slice is owned by the
	// caller's transport (coder/websocket reuses its read buffer between
	// Read calls), so OnFrame must either consume synchronously or copy.
	OnFrame func(raw []byte)
}

// ReconnectOptions tunes WS reconnect backoff. MaxAttempts == 0 means
// retry indefinitely.
type ReconnectOptions struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxAttempts    int
}

// Subscriber maintains an authenticated Kalshi WS connection and forwards
// raw frame bytes to OnFrame. It auto-reconnects with jittered exponential
// backoff and exits cleanly on context cancellation.
//
// The ticker list is mutable: callers may swap it via SetTickers and
// then trigger a graceful reconnect via Reload to pick up the new list.
// This is how the recorder handles daily bracket settlement — at midnight
// local time, old markets close and new "tomorrow" markets open; periodic
// re-discovery keeps the subscription current.
type Subscriber struct {
	opts   SubscriberOptions
	logger *slog.Logger

	mu       sync.RWMutex
	tickers  []string
	channels []string

	reloadCh chan struct{}
}

// NewSubscriber returns a Subscriber. Call Run to start it.
func NewSubscriber(opts SubscriberOptions) *Subscriber {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Reconnect.InitialBackoff <= 0 {
		opts.Reconnect.InitialBackoff = 500 * time.Millisecond
	}
	if opts.Reconnect.MaxBackoff <= 0 {
		opts.Reconnect.MaxBackoff = 30 * time.Second
	}
	if opts.OnFrame == nil {
		opts.OnFrame = func([]byte) {}
	}
	return &Subscriber{
		opts:     opts,
		logger:   opts.Logger.With("component", "kalshi-ws"),
		tickers:  append([]string(nil), opts.Tickers...),
		channels: append([]string(nil), opts.Channels...),
		reloadCh: make(chan struct{}, 1),
	}
}

// Tickers returns a copy of the currently-subscribed ticker list.
func (s *Subscriber) Tickers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.tickers...)
}

// SetTickers replaces the ticker list. The new list takes effect on the
// next reconnect — call Reload to force one immediately, otherwise the
// next natural reconnect (network drop, retry) will pick it up.
func (s *Subscriber) SetTickers(tickers []string) {
	s.mu.Lock()
	s.tickers = append([]string(nil), tickers...)
	s.mu.Unlock()
}

// Reload triggers a graceful reconnect: the current WS connection is
// cancelled, retry.Do reconnects, and the new run reads the latest
// tickers via SetTickers. Idempotent and non-blocking; multiple calls
// before the reconnect completes coalesce into one.
func (s *Subscriber) Reload() {
	select {
	case s.reloadCh <- struct{}{}:
	default:
	}
}

// Run connects, subscribes, and pumps frames until ctx is cancelled.
// Returns nil on clean cancellation; otherwise the last error after
// exhausting MaxAttempts (when set). Reconnect/backoff is delegated
// to internal/retry so every WS-ish consumer behaves identically.
func (s *Subscriber) Run(ctx context.Context) error {
	err := retry.Do(ctx, retry.Config{
		InitialBackoff: s.opts.Reconnect.InitialBackoff,
		MaxBackoff:     s.opts.Reconnect.MaxBackoff,
		MaxAttempts:    s.opts.Reconnect.MaxAttempts,
	}, s.logger, func(int) error {
		return s.runOnce(ctx)
	})
	if err != nil {
		return fmt.Errorf("kalshi-ws: %w", err)
	}
	return nil
}

func (s *Subscriber) runOnce(ctx context.Context) error {
	// Drain any stale reload signal from a prior run so this connection
	// doesn't get killed on first read.
	select {
	case <-s.reloadCh:
	default:
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Watcher: if Reload() fires, cancel runCtx so conn.Read returns and
	// retry.Do picks up the new ticker list on the next attempt.
	go func() {
		select {
		case <-runCtx.Done():
		case <-s.reloadCh:
			s.logger.Info("ws reload requested — cancelling current connection")
			cancel()
		}
	}()

	header, err := HandshakeHeaders(s.opts.Signer, s.opts.WSURL)
	if err != nil {
		return err
	}
	conn, resp, err := websocket.Dial(runCtx, s.opts.WSURL, &websocket.DialOptions{
		HTTPHeader: header,
	})
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer conn.Close(websocket.StatusInternalError, "subscriber exit")

	conn.SetReadLimit(8 << 20) // 8 MiB max single message

	if err := s.subscribe(runCtx, conn); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	s.mu.RLock()
	tickerCount := len(s.tickers)
	channels := append([]string(nil), s.channels...)
	s.mu.RUnlock()
	s.logger.Info("ws connected", "channels", channels, "tickers", tickerCount)

	for {
		_, data, err := conn.Read(runCtx)
		if err != nil {
			return err
		}
		s.opts.OnFrame(data)
	}
}

// subscribeMessage is the v2 WS subscribe command body.
type subscribeMessage struct {
	ID     int          `json:"id"`
	Cmd    string       `json:"cmd"`
	Params subscribeArg `json:"params"`
}

type subscribeArg struct {
	Channels      []string `json:"channels"`
	MarketTickers []string `json:"market_tickers,omitempty"`
}

func (s *Subscriber) subscribe(ctx context.Context, conn *websocket.Conn) error {
	s.mu.RLock()
	tickers := append([]string(nil), s.tickers...)
	channels := append([]string(nil), s.channels...)
	s.mu.RUnlock()

	msg := subscribeMessage{
		ID:  1,
		Cmd: "subscribe",
		Params: subscribeArg{
			Channels:      channels,
			MarketTickers: tickers,
		},
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, body)
}

// HandshakeHeaders builds the auth headers required for the Kalshi WS
// upgrade request. Useful for callers that build their own dialer.
func HandshakeHeaders(signer *Signer, wsURL string) (http.Header, error) {
	if signer == nil {
		return nil, errors.New("kalshi: signer required")
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("parse ws url: %w", err)
	}
	path := strings.TrimSpace(u.Path)
	if path == "" {
		path = "/"
	}
	key, sig, ts, err := signer.AuthHeaders(http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("KALSHI-ACCESS-KEY", key)
	h.Set("KALSHI-ACCESS-SIGNATURE", sig)
	h.Set("KALSHI-ACCESS-TIMESTAMP", ts)
	return h, nil
}
