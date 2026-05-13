package kalshi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type wsKalshiServer struct {
	srv        *httptest.Server
	headers    chan http.Header
	subscribes chan string
	frames     [][]byte
}

func newKalshiWSServer(t *testing.T, frames [][]byte) *wsKalshiServer {
	t.Helper()
	w := &wsKalshiServer{
		headers:    make(chan http.Header, 4),
		subscribes: make(chan string, 4),
		frames:     frames,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/trade-api/ws/v2", func(rw http.ResponseWriter, r *http.Request) {
		// Capture the auth headers before the upgrade strips them.
		h := r.Header.Clone()
		select {
		case w.headers <- h:
		default:
		}
		conn, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("accept failed: %v", err)
			return
		}
		ctx := r.Context()
		_, data, err := conn.Read(ctx)
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, "no sub")
			return
		}
		select {
		case w.subscribes <- string(data):
		default:
		}
		for _, f := range w.frames {
			if err := conn.Write(ctx, websocket.MessageText, f); err != nil {
				return
			}
		}
		<-ctx.Done()
		_ = conn.Close(websocket.StatusNormalClosure, "test done")
	})
	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *wsKalshiServer) wsURL() string {
	return "ws" + strings.TrimPrefix(w.srv.URL, "http") + "/trade-api/ws/v2"
}

func TestKalshiWSAuthAndSubscribe(t *testing.T) {
	frames := [][]byte{
		[]byte(`{"type":"orderbook_delta","msg":{"market_ticker":"TEST-X"}}`),
	}
	w := newKalshiWSServer(t, frames)

	_, pem := testKeyPair(t)
	signer, err := NewSigner("test-key-id", pem)
	require.NoError(t, err)

	received := make(chan []byte, 1)
	sub := NewSubscriber(SubscriberOptions{
		WSURL:    w.wsURL(),
		Signer:   signer,
		Channels: []string{"orderbook_delta", "ticker_v2"},
		Tickers:  []string{"TEST-X"},
		Logger:   discardLogger(),
		OnFrame: func(raw []byte) {
			cp := make([]byte, len(raw))
			copy(cp, raw)
			select {
			case received <- cp:
			default:
			}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sub.Run(ctx) }()

	// Auth headers must be present on the upgrade request.
	select {
	case h := <-w.headers:
		require.Equal(t, "test-key-id", h.Get("KALSHI-ACCESS-KEY"))
		require.NotEmpty(t, h.Get("KALSHI-ACCESS-SIGNATURE"))
		require.NotEmpty(t, h.Get("KALSHI-ACCESS-TIMESTAMP"))
	case <-time.After(2 * time.Second):
		t.Fatal("no upgrade captured")
	}

	// Subscribe message is well-formed.
	select {
	case s := <-w.subscribes:
		var msg subscribeMessage
		require.NoError(t, json.Unmarshal([]byte(s), &msg))
		require.Equal(t, "subscribe", msg.Cmd)
		require.Equal(t, 1, msg.ID)
		require.Equal(t, []string{"orderbook_delta", "ticker_v2"}, msg.Params.Channels)
		require.Equal(t, []string{"TEST-X"}, msg.Params.MarketTickers)
	case <-time.After(2 * time.Second):
		t.Fatal("no subscribe message observed")
	}

	// A forwarded frame appears in OnFrame.
	select {
	case raw := <-received:
		require.Contains(t, string(raw), "TEST-X")
	case <-time.After(2 * time.Second):
		t.Fatal("no frame forwarded")
	}

	cancel()
	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestHandshakeHeadersRequiresSigner(t *testing.T) {
	_, err := HandshakeHeaders(nil, "wss://example/ws")
	require.Error(t, err)
}

func TestHandshakeHeadersPopulatesAuth(t *testing.T) {
	_, pem := testKeyPair(t)
	signer, err := NewSigner("keyid", pem)
	require.NoError(t, err)

	h, err := HandshakeHeaders(signer, "wss://example.com/some/path")
	require.NoError(t, err)
	require.Equal(t, "keyid", h.Get("KALSHI-ACCESS-KEY"))
	require.NotEmpty(t, h.Get("KALSHI-ACCESS-SIGNATURE"))
	require.NotEmpty(t, h.Get("KALSHI-ACCESS-TIMESTAMP"))
}

func TestHandshakeHeadersEmptyPathDefaultsToSlash(t *testing.T) {
	_, pem := testKeyPair(t)
	signer, err := NewSigner("keyid", pem)
	require.NoError(t, err)

	// A URL without a path should still produce headers.
	h, err := HandshakeHeaders(signer, "wss://example.com")
	require.NoError(t, err)
	require.NotEmpty(t, h.Get("KALSHI-ACCESS-SIGNATURE"))
}

func TestKalshiWSRespectsPrecancelledContext(t *testing.T) {
	w := newKalshiWSServer(t, nil)
	_, pem := testKeyPair(t)
	signer, err := NewSigner("keyid", pem)
	require.NoError(t, err)

	sub := NewSubscriber(SubscriberOptions{
		WSURL:    w.wsURL(),
		Signer:   signer,
		Channels: []string{"ticker_v2"},
		Logger:   discardLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, sub.Run(ctx))
}

func TestKalshiWSMaxAttemptsReturnsError(t *testing.T) {
	// Closed server → dial fails every time.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	srv.Close()

	_, pem := testKeyPair(t)
	signer, err := NewSigner("keyid", pem)
	require.NoError(t, err)

	sub := NewSubscriber(SubscriberOptions{
		WSURL:    "ws" + strings.TrimPrefix(srv.URL, "http"),
		Signer:   signer,
		Channels: []string{"ticker_v2"},
		Reconnect: ReconnectOptions{
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     10 * time.Millisecond,
			MaxAttempts:    1,
		},
		Logger: discardLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = sub.Run(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max attempts")
}

func TestSubscriberDefaultOnFrameIsSafe(t *testing.T) {
	s := NewSubscriber(SubscriberOptions{
		WSURL: "wss://unused",
	})
	require.NotNil(t, s)
	// Touch the default OnFrame to prove it's non-nil and non-panicking.
	s.opts.OnFrame([]byte("ok"))
}

// TestSubscriberReloadResubscribesWithNewTickers verifies that calling
// SetTickers + Reload causes the subscriber to disconnect and re-subscribe
// with the new ticker list. This is the core mechanism for handling daily
// bracket settlement: when old markets close at midnight, we discover new
// ones and Reload to subscribe without losing the WS lifecycle benefits
// of retry.Do (auto-reconnect, backoff).
func TestSubscriberReloadResubscribesWithNewTickers(t *testing.T) {
	w := newKalshiWSServer(t, nil)
	_, pem := testKeyPair(t)
	signer, err := NewSigner("keyid", pem)
	require.NoError(t, err)

	sub := NewSubscriber(SubscriberOptions{
		WSURL:    w.wsURL(),
		Signer:   signer,
		Channels: []string{"orderbook_delta"},
		Tickers:  []string{"OLD-TICKER"},
		Logger:   discardLogger(),
		Reconnect: ReconnectOptions{
			InitialBackoff: 5 * time.Millisecond,
			MaxBackoff:     10 * time.Millisecond,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sub.Run(ctx) }()

	// First subscribe carries OLD-TICKER.
	select {
	case s := <-w.subscribes:
		var msg subscribeMessage
		require.NoError(t, json.Unmarshal([]byte(s), &msg))
		require.Equal(t, []string{"OLD-TICKER"}, msg.Params.MarketTickers)
	case <-time.After(2 * time.Second):
		t.Fatal("first subscribe never observed")
	}

	// Swap tickers and trigger reload.
	sub.SetTickers([]string{"NEW-TICKER-A", "NEW-TICKER-B"})
	sub.Reload()

	// After reload the subscriber should reconnect and re-subscribe with
	// the new tickers.
	select {
	case s := <-w.subscribes:
		var msg subscribeMessage
		require.NoError(t, json.Unmarshal([]byte(s), &msg))
		require.Equal(t, []string{"NEW-TICKER-A", "NEW-TICKER-B"}, msg.Params.MarketTickers)
	case <-time.After(3 * time.Second):
		t.Fatal("reload did not trigger resubscribe")
	}
}

// TestSubscriberTickersIsCopySafe verifies that the public Tickers()
// returns a defensive copy and SetTickers doesn't share state with caller.
func TestSubscriberTickersIsCopySafe(t *testing.T) {
	sub := NewSubscriber(SubscriberOptions{
		WSURL:   "wss://unused",
		Tickers: []string{"A", "B"},
	})

	got := sub.Tickers()
	require.Equal(t, []string{"A", "B"}, got)

	got[0] = "MUTATED" // caller mutation must not affect internal state
	require.Equal(t, []string{"A", "B"}, sub.Tickers())

	caller := []string{"X", "Y"}
	sub.SetTickers(caller)
	caller[0] = "MUTATED" // caller mutation post-Set must not affect internal state
	require.Equal(t, []string{"X", "Y"}, sub.Tickers())
}

// Ensure using the subscriber type plus its internal fields doesn't
// introduce silent data races.
func TestSubscriberConcurrentOnFrameIsLocal(t *testing.T) {
	var seen atomic.Int64
	s := NewSubscriber(SubscriberOptions{
		WSURL:    "wss://unused",
		Channels: []string{"ticker_v2"},
		OnFrame:  func([]byte) { seen.Add(1) },
	})
	for i := 0; i < 8; i++ {
		go s.opts.OnFrame([]byte("x"))
	}
	time.Sleep(50 * time.Millisecond)
	require.GreaterOrEqual(t, seen.Load(), int64(1))
}

// TestSubscriber_ReconnectsOnFrameStarvation verifies that the liveness
// watchdog forces a reconnect when no frames arrive for longer than
// MaxIdleBetweenFrames. The stub server accepts subscribe and then goes
// silent; the client must detect the gap and re-enter retry.Do.
func TestSubscriber_ReconnectsOnFrameStarvation(t *testing.T) {
	// Stub server that accepts subscribe + sends nothing.
	var subscribes atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/trade-api/ws/v2", func(rw http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		ctx := r.Context()
		if _, _, err := conn.Read(ctx); err == nil {
			subscribes.Add(1)
		}
		<-ctx.Done()
		_ = conn.Close(websocket.StatusNormalClosure, "test done")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, pem := testKeyPair(t)
	signer, err := NewSigner("keyid", pem)
	require.NoError(t, err)

	sub := NewSubscriber(SubscriberOptions{
		WSURL:                "ws" + strings.TrimPrefix(srv.URL, "http") + "/trade-api/ws/v2",
		Signer:               signer,
		Channels:             []string{"orderbook_delta"},
		Tickers:              []string{"X"},
		Logger:               discardLogger(),
		PingInterval:         100 * time.Millisecond,
		MaxIdleBetweenFrames: 200 * time.Millisecond,
		Reconnect: ReconnectOptions{
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     20 * time.Millisecond,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = sub.Run(ctx) }()

	// Wait for at least two subscribe attempts — the first connect plus a
	// reconnect after the watchdog trips. Detect within ~2*PingInterval +
	// reconnect overhead.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if subscribes.Load() >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected watchdog to trigger reconnect, got %d subscribes", subscribes.Load())
}

// TestSubscriber_PingFailureTriggersReconnect verifies that a peer that
// closes the connection mid-stream (so the watchdog's Ping fails) causes
// a reconnect. We close on the second read (the first read returns the
// subscribe message; the second is the Ping read) by hijacking the conn
// and closing it.
func TestSubscriber_PingFailureTriggersReconnect(t *testing.T) {
	var subscribes atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/trade-api/ws/v2", func(rw http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		ctx := r.Context()
		if _, _, err := conn.Read(ctx); err == nil {
			subscribes.Add(1)
		}
		// Immediately abnormally close so the next Ping fails.
		_ = conn.CloseNow()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, pem := testKeyPair(t)
	signer, err := NewSigner("keyid", pem)
	require.NoError(t, err)

	sub := NewSubscriber(SubscriberOptions{
		WSURL:                "ws" + strings.TrimPrefix(srv.URL, "http") + "/trade-api/ws/v2",
		Signer:               signer,
		Channels:             []string{"orderbook_delta"},
		Tickers:              []string{"X"},
		Logger:               discardLogger(),
		PingInterval:         100 * time.Millisecond,
		MaxIdleBetweenFrames: 10 * time.Second, // High so only Ping failure can trip
		Reconnect: ReconnectOptions{
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     20 * time.Millisecond,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = sub.Run(ctx) }()

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if subscribes.Load() >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected ping failure to trigger reconnect, got %d subscribes", subscribes.Load())
}
