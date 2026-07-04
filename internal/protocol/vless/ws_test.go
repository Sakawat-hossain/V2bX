package vless

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	svless "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// TestWebSocketTransportRelays runs a full VLESS-over-WebSocket round-trip:
// the client speaks WS to the node (as it would through a CDN), VLESS rides on
// top, and traffic reaches the destination.
func TestWebSocketTransportRelays(t *testing.T) {
	const testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"
	const wsPath = "/vlessws"

	port := freePort(t)
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 1, ListenIP: "127.0.0.1", Port: port,
		Users:     []protocol.User{{ID: 9, UUID: testUUID}},
		Transport: "ws",
		WSPath:    wsPath,
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start ws node: %v", err)
	}
	defer srv.Stop()

	echo := newEchoUpstream(t)
	defer echo.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Client: open a WebSocket to the node, then run VLESS over it.
	wsc, _, err := websocket.Dial(ctx, "ws://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+wsPath, nil)
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}
	defer wsc.CloseNow()
	wsNet := websocket.NetConn(ctx, wsc, websocket.MessageBinary)

	client, err := svless.NewClient(testUUID, "", logger.NOP())
	if err != nil {
		t.Fatalf("vless client: %v", err)
	}
	conn, err := client.DialConn(wsNet, M.ParseSocksaddr(echo.Addr().String()))
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}

	msg := []byte("hello over vless-ws")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", buf, msg)
	}
	// Close so the relay finishes and flushes the byte counters.
	conn.Close()
	wsc.CloseNow()

	// Traffic must be attributed to the user.
	deadline := time.Now().Add(2 * time.Second)
	var tr protocol.UserTraffic
	for time.Now().Before(deadline) {
		if u, ok := srv.Stats().Users[9]; ok {
			tr = u
		}
		if tr.Upload != 0 && tr.Download != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tr.Upload == 0 || tr.Download == 0 {
		t.Fatalf("expected nonzero traffic, got %+v", tr)
	}
}
