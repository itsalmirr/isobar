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
type Subscriber struct {
	opts   SubscriberOptions
	logger *slog.Logger
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
	return &Subscriber{opts: opts, logger: opts.Logger.With("component", "kalshi-ws")}
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
	header, err := HandshakeHeaders(s.opts.Signer, s.opts.WSURL)
	if err != nil {
		return err
	}
	conn, resp, err := websocket.Dial(ctx, s.opts.WSURL, &websocket.DialOptions{
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

	if err := s.subscribe(ctx, conn); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	s.logger.Info("ws connected", "channels", s.opts.Channels, "tickers", len(s.opts.Tickers))

	for {
		_, data, err := conn.Read(ctx)
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
	msg := subscribeMessage{
		ID:  1,
		Cmd: "subscribe",
		Params: subscribeArg{
			Channels:      s.opts.Channels,
			MarketTickers: s.opts.Tickers,
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
