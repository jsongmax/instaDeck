package httpapi

import (
	"context"
	"errors"
	"github.com/coder/websocket"
	"instadeck/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPrivateProxyClientIP(t *testing.T) {
	for _, tt := range []struct{ mode, remote, xff, want string }{
		{"private", "10.0.0.2:1", "1.1.1.1, 203.0.113.9", "203.0.113.9"},
		{"private", "172.16.0.2:1", "203.0.113.9", "203.0.113.9"},
		{"private", "[fd00::2]:1", "203.0.113.9", "203.0.113.9"},
		{"private", "203.0.113.2:1", "1.1.1.1", "203.0.113.2"},
		{"", "10.0.0.2:1", "1.1.1.1", "10.0.0.2"},
		{"private", "169.254.1.2:1", "1.1.1.1", "169.254.1.2"},
		{"private", "100.64.0.2:1", "1.1.1.1", "100.64.0.2"},
		{"private", "[fe80::2]:1", "1.1.1.1", "fe80::2"},
		{"private", "10.0.0.2:1", "1.1.1.1, invalid", "invalid"},
	} {
		api := &API{dependencies: Dependencies{Config: config.Config{TrustedProxy: tt.mode}}}
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		r.Header.Set("X-Forwarded-For", tt.xff)
		r.Header.Set("CF-Connecting-IP", "8.8.8.8")
		if got := api.clientIP(r); got != tt.want {
			t.Fatalf("%+v: %s", tt, got)
		}
	}
}

func TestHeartbeatFailureAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	websocketHeartbeat(ctx, time.Millisecond, time.Second, func(context.Context) error { t.Fatal("ping after cancellation"); return nil }, func() { t.Fatal("closed after cancellation") })
	closed := false
	websocketHeartbeat(context.Background(), time.Millisecond, time.Second, func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing timeout")
		}
		return errors.New("failed")
	}, func() { closed = true })
	if !closed {
		t.Fatal("failure did not close connection")
	}
	ctx, cancel = context.WithCancel(context.Background())
	websocketHeartbeat(ctx, time.Millisecond, 5*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		cancel()
		return ctx.Err()
	}, func() {})
}

func TestHeartbeatRealConnection(t *testing.T) {
	completed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			completed <- err
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		defer conn.CloseNow()
		go func() { _, _, _ = conn.Read(ctx) }()
		count := 0
		websocketHeartbeat(ctx, 5*time.Millisecond, time.Second, func(ctx context.Context) error {
			err := conn.Ping(ctx)
			if err == nil {
				count++
				if count == 3 {
					cancel()
				}
			}
			return err
		}, func() { _ = conn.CloseNow() })
		if count != 3 {
			completed <- errors.New("ping/pong did not complete")
			return
		}
		completed <- nil
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	frames := &websocketFrames{connection: conn}
	heartbeatContext, stop := context.WithCancel(ctx)
	frames.stop = stop
	frames.done = make(chan struct{})
	go func() {
		defer close(frames.done)
		websocketHeartbeat(heartbeatContext, time.Hour, time.Second, conn.Ping, func() { _ = conn.CloseNow() })
	}()
	defer func() {
		_ = frames.Close()
		_ = frames.Close()
		select {
		case <-frames.done:
		default:
			t.Error("heartbeat did not exit")
		}
	}()
	go func() { _, _, _ = conn.Read(ctx) }()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// The browser sends a resize as soon as the socket opens, before the server reads
// anything. The final error frame must still arrive after that unread frame.
func TestLastFrameSurvivesUnreadClientData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		frames := &websocketFrames{connection: conn}
		defer frames.Close()
		time.Sleep(50 * time.Millisecond)
		_ = frames.WriteFrame(r.Context(), false, []byte(`{"type":"error","code":"address_blocked"}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":80,"rows":24}`)); err != nil {
		t.Fatal(err)
	}
	_, payload, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(payload), "address_blocked") {
		t.Fatal(string(payload), err)
	}
	if _, _, err = conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatal(err)
	}
}
