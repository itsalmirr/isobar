package coinbase

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

// wsTestServer accepts a websocket upgrade, reads one subscribe message,
// records it, then writes each frame in `frames` to the client.
type wsTestServer struct {
	srv         *httptest.Server
	subscribes  chan string
	onConnected func()
	frames      [][]byte
}

func newWSTestServer(t *testing.T, frames [][]byte) *wsTestServer {
	t.Helper()
	w := &wsTestServer{
		subscribes: make(chan string, 4),
		frames:     frames,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("accept failed: %v", err)
			return
		}
		if w.onConnected != nil {
			w.onConnected()
		}
		ctx := r.Context()
		_, data, err := conn.Read(ctx)
		if err != nil {
			_ = conn.Close(websocket.StatusInternalError, "no subscribe")
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
		// Keep the connection open until the test closes it.
		<-ctx.Done()
		_ = conn.Close(websocket.StatusNormalClosure, "test done")
	})
	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *wsTestServer) url() string {
	// Convert http:// → ws://
	return "ws" + strings.TrimPrefix(w.srv.URL, "http")
}

func TestCoinbaseSubscribeMessage(t *testing.T) {
	frames := [][]byte{
		[]byte(`{"type":"ticker","product_id":"BTC-USD","price":"100000"}`),
	}
	w := newWSTestServer(t, frames)

	var received atomic.Int64
	done := make(chan struct{}, len(frames))
	sub := NewSubscriber(Options{
		WSURL:      w.url(),
		ProductIDs: []string{"BTC-USD"},
		Channels:   []string{"ticker"},
		Logger:     discardLogger(),
		OnFrame: func(raw []byte) {
			received.Add(1)
			done <- struct{}{}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sub.Run(ctx) }()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("never received a frame")
	}

	// Check subscribe message structure.
	select {
	case s := <-w.subscribes:
		var msg subscribeMessage
		require.NoError(t, json.Unmarshal([]byte(s), &msg))
		require.Equal(t, "subscribe", msg.Type)
		require.Equal(t, []string{"BTC-USD"}, msg.ProductIDs)
		require.Equal(t, []string{"ticker"}, msg.Channels)
	case <-time.After(time.Second):
		t.Fatal("no subscribe message observed")
	}

	cancel()
	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	require.GreaterOrEqual(t, received.Load(), int64(1))
}

func TestCoinbaseRunReturnsOnPrecancelledContext(t *testing.T) {
	// Server exists but context is already done; Run should exit cleanly.
	w := newWSTestServer(t, nil)
	sub := NewSubscriber(Options{
		WSURL:      w.url(),
		ProductIDs: []string{"BTC-USD"},
		Channels:   []string{"ticker"},
		Logger:     discardLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, sub.Run(ctx))
}

func TestCoinbaseMaxAttemptsReturnsError(t *testing.T) {
	// Point at a closed port to force dial failure, and cap attempts at 1
	// so the error propagates quickly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	srv.Close() // closed: dial will fail.

	sub := NewSubscriber(Options{
		WSURL:          "ws" + strings.TrimPrefix(srv.URL, "http"),
		ProductIDs:     []string{"BTC-USD"},
		Channels:       []string{"ticker"},
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		MaxAttempts:    1,
		Logger:         discardLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := sub.Run(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max reconnect attempts")
}

func TestNewSubscriberDefaults(t *testing.T) {
	s := NewSubscriber(Options{
		WSURL:      "wss://unused",
		ProductIDs: []string{"BTC-USD"},
		Channels:   []string{"ticker"},
	})
	require.NotNil(t, s)
	require.Equal(t, 500*time.Millisecond, s.opts.InitialBackoff)
	require.Equal(t, 30*time.Second, s.opts.MaxBackoff)
	require.NotNil(t, s.opts.OnFrame)
}
