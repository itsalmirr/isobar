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
	require.Contains(t, err.Error(), "max reconnect attempts")
}

func TestSubscriberDefaultOnFrameIsSafe(t *testing.T) {
	s := NewSubscriber(SubscriberOptions{
		WSURL: "wss://unused",
	})
	require.NotNil(t, s)
	// Touch the default OnFrame to prove it's non-nil and non-panicking.
	s.opts.OnFrame([]byte("ok"))
}

func TestJitterWithinBounds(t *testing.T) {
	// jitter(d) returns d + rand[0, d/4]; so d ≤ result ≤ d + d/4.
	for i := 0; i < 1000; i++ {
		out := jitter(100 * time.Millisecond)
		require.GreaterOrEqual(t, out, 100*time.Millisecond)
		require.LessOrEqual(t, out, 125*time.Millisecond)
	}
	require.Equal(t, time.Duration(0), jitter(0))
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
