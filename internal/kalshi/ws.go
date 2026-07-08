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
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sync/errgroup"

	"github.com/wh0o0pz/isobar/internal/retry"
)

// Defaults for the WS liveness watchdog. See CLIENT.md for the rationale:
// Kalshi weather brackets see at least one orderbook frame per minute during
// market hours, so 60s is the floor below which we treat the feed as dead.
// Pinging at half the dead-window is the standard pattern.
const (
	defaultPingInterval         = 30 * time.Second
	defaultMaxIdleBetweenFrames = 60 * time.Second
	defaultRefreshTimeout       = 30 * time.Second
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

	// RefreshTickers, when non-nil, is called at the start of every
	// connection attempt (initial connect and every reconnect) and
	// returns the full ticker list to subscribe with. This makes the
	// reconnect path self-healing: a watchdog-forced reconnect always
	// resubscribes with a current market list rather than whatever the
	// subscriber last held. Without it, a stale in-memory list is fatal
	// and unrecoverable — in the 2026-05 outage every held ticker had
	// settled, Kalshi answered each resubscribe with empty snapshot
	// stubs (market_id="") and no deltas, and the starvation watchdog
	// looped forever (see docs/OPERATIONS.md, postmortem 2026-06-09).
	//
	// Errors and empty results are non-fatal: the subscriber logs and
	// falls back to the last-known list, preserving pre-hook behavior.
	RefreshTickers func(ctx context.Context) ([]string, error)
	// RefreshTimeout bounds each RefreshTickers call so a hung discovery
	// source cannot park the reconnect loop. Zero uses
	// defaultRefreshTimeout (30s).
	RefreshTimeout time.Duration

	// PingInterval is how often the watchdog checks frame age and pings.
	// Zero uses defaultPingInterval (30s).
	PingInterval time.Duration
	// MaxIdleBetweenFrames is the maximum allowed gap between inbound
	// frames before the watchdog forces a reconnect. Zero uses
	// defaultMaxIdleBetweenFrames (60s).
	MaxIdleBetweenFrames time.Duration

	// now is a clock injection point for tests (mirrors Signer.now). Zero
	// uses time.Now. The watchdog uses this to compute frame age.
	now func() time.Time
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

	// lastFrameTime holds the unix-nano timestamp of the most recent frame
	// observed in runOnce. Updated on every OnFrame invocation and reset
	// to "now" at the start of each connection so the watchdog doesn't
	// false-trigger on the first frame gap.
	lastFrameTime atomic.Int64
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
	if opts.PingInterval <= 0 {
		opts.PingInterval = defaultPingInterval
	}
	if opts.MaxIdleBetweenFrames <= 0 {
		opts.MaxIdleBetweenFrames = defaultMaxIdleBetweenFrames
	}
	if opts.RefreshTimeout <= 0 {
		opts.RefreshTimeout = defaultRefreshTimeout
	}
	if opts.now == nil {
		opts.now = time.Now
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
		// A connection that outlived two idle windows was genuinely
		// healthy: reset the backoff ladder so its eventual drop
		// reconnects at InitialBackoff instead of inheriting whatever
		// cap earlier flapping ratcheted up to.
		ResetAfter: 2 * s.opts.MaxIdleBetweenFrames,
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

	// Re-resolve the ticker list before dialing so every (re)connect
	// subscribes with a current market set. See SubscriberOptions.
	s.refreshTickers(runCtx)

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

	if subErr := s.subscribe(runCtx, conn); subErr != nil {
		return fmt.Errorf("subscribe: %w", subErr)
	}
	s.mu.RLock()
	tickerCount := len(s.tickers)
	channels := append([]string(nil), s.channels...)
	s.mu.RUnlock()
	s.logger.Info("ws connected", "channels", channels, "tickers", tickerCount)

	// Reset the watchdog clock at connect time so a slow first frame
	// doesn't immediately trip the idle threshold.
	s.lastFrameTime.Store(s.opts.now().UnixNano())

	// Run the read pump, reload watcher, and liveness watchdog concurrently.
	// First non-nil error wins and cancels the group; defer cancel() above
	// ensures conn.Close() runs even on watchdog-triggered shutdown.
	g, gctx := errgroup.WithContext(runCtx)

	g.Go(func() error {
		// Reload watcher: if Reload() fires, return a sentinel error to
		// cancel the group so retry.Do reconnects with the new ticker list.
		select {
		case <-gctx.Done():
			return nil
		case <-s.reloadCh:
			s.logger.Info("ws reload requested — cancelling current connection")
			return errReload
		}
	})

	g.Go(func() error {
		return s.watchdog(gctx, conn)
	})

	g.Go(func() error {
		for {
			_, data, readErr := conn.Read(gctx)
			if readErr != nil {
				return readErr
			}
			// Update lastFrameTime before fan-out so OnFrame can never
			// race with the watchdog seeing a stale value.
			s.lastFrameTime.Store(s.opts.now().UnixNano())
			s.opts.OnFrame(data)
		}
	})

	err = g.Wait()
	// Treat reload and clean parent cancellation as non-errors so retry.Do
	// reconnects cleanly (errReload) or exits cleanly (parent ctx cancel).
	if errors.Is(err, errReload) {
		return errReload
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return err
}

// errReload is returned by the reload watcher to signal a graceful reconnect.
// It bubbles up to retry.Do via runOnce's return; retry.Do treats it as a
// normal failure and re-enters the loop, which is exactly what we want.
var errReload = errors.New("kalshi-ws: reload requested")

// refreshTickers re-resolves the ticker list via opts.RefreshTickers (when
// configured) so the connection about to be made subscribes with a current
// list. Failure is deliberately non-fatal — reconnecting with a
// possibly-stale list beats not reconnecting at all — but both failure
// modes are logged so a stuck or empty discovery source is visible next to
// the starvation/backoff lines it would cause.
func (s *Subscriber) refreshTickers(ctx context.Context) {
	if s.opts.RefreshTickers == nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, s.opts.RefreshTimeout)
	defer cancel()
	fresh, err := s.opts.RefreshTickers(rctx)
	if err != nil {
		s.logger.Warn("ws ticker refresh failed — connecting with last-known list",
			"err", err, "tickers", len(s.Tickers()))
		return
	}
	if len(fresh) == 0 {
		s.logger.Warn("ws ticker refresh returned no markets — keeping last-known list",
			"tickers", len(s.Tickers()))
		return
	}
	s.SetTickers(fresh)
	s.logger.Debug("ws ticker refresh", "tickers", len(fresh))
}

// watchdog ticks every PingInterval and either:
//   - detects frame starvation (last frame age > MaxIdleBetweenFrames) and
//     forces a reconnect by returning an error, or
//   - pings the peer so dead TCP connections fail fast instead of waiting
//     for the read deadline.
//
// A Ping error is also treated as starvation: the connection is gone, force
// a reconnect.
func (s *Subscriber) watchdog(ctx context.Context, conn *websocket.Conn) error {
	ticker := time.NewTicker(s.opts.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			lastNs := s.lastFrameTime.Load()
			age := s.opts.now().Sub(time.Unix(0, lastNs))
			if age > s.opts.MaxIdleBetweenFrames {
				// Include a ticker sample: Kalshi tickers embed the
				// bracket date, so a starving subscription to settled
				// markets is diagnosable from this one line.
				tickers := s.Tickers()
				s.logger.Warn(
					"ws frame starvation — forcing reconnect",
					"last_frame_age_ms", age.Milliseconds(),
					"max_idle_ms", s.opts.MaxIdleBetweenFrames.Milliseconds(),
					"tickers", len(tickers),
					"tickers_sample", tickers[:min(3, len(tickers))],
				)
				return fmt.Errorf("ws watchdog: frame starvation (age=%s)", age)
			}
			// Ping the peer; if the TCP path is dead this fails fast.
			// Bound the ping with PingInterval so a hung peer can't park us.
			pingCtx, cancel := context.WithTimeout(ctx, s.opts.PingInterval)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				s.logger.Warn("ws ping failed — forcing reconnect", "err", err)
				return fmt.Errorf("ws watchdog: ping failed: %w", err)
			}
		}
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
