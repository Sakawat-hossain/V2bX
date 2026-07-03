package socks5

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func newEchoUpstream(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listener: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return ln
}

func TestStartStopAnonymous(t *testing.T) {
	port := freePort(t)
	srv := New()
	cfg := protocol.NodeConfig{NodeID: 1, ListenIP: "127.0.0.1", Port: port}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	echo := newEchoUpstream(t)
	defer echo.Close()

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	// Method negotiation: version 5, 1 method, no-auth.
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("unexpected method reply: %v", resp)
	}

	echoAddr := echo.Addr().(*net.TCPAddr)
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, echoAddr.IP.To4()...)
	req = append(req, byte(echoAddr.Port>>8), byte(echoAddr.Port))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write connect request: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	if reply[1] != replySuccess {
		t.Fatalf("expected success reply, got %v", reply)
	}

	msg := []byte("hello via socks5")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", buf, msg)
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("second Stop should be a no-op: %v", err)
	}
}

func TestAuthRequiredRejectsNoAuth(t *testing.T) {
	port := freePort(t)
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 2, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 1, UUID: "alice", Password: "secret"}},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil { // offer only no-auth
		t.Fatalf("write: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp[1] != authNoAccept {
		t.Fatalf("expected server to reject no-auth when users are configured, got %v", resp)
	}
}
