package shadowsocks

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-shadowsocks/shadowaead"
	M "github.com/sagernet/sing/common/metadata"

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

func TestStartStopClassicCipher(t *testing.T) {
	port := freePort(t)
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID:   1,
		NodeType: "shadowsocks",
		ListenIP: "127.0.0.1",
		Port:     port,
		Cipher:   "aes-256-gcm",
		Users:    []protocol.User{{ID: 42, Password: "test-password"}},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if err := srv.Start(cfg); err == nil {
		t.Fatal("expected error starting an already-started server")
	}

	echo := newEchoUpstream(t)
	defer echo.Close()

	roundTripThroughProxy(t, "127.0.0.1", port, "aes-256-gcm", "test-password", echo.Addr().String())

	deadline := time.Now().Add(2 * time.Second)
	var tr protocol.UserTraffic
	for time.Now().Before(deadline) {
		stats := srv.Stats()
		if u, ok := stats.Users[42]; ok {
			tr.Upload += u.Upload
			tr.Download += u.Download
		}
		if tr.Upload != 0 && tr.Download != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tr.Upload == 0 || tr.Download == 0 {
		t.Fatalf("expected nonzero upload/download, got %+v", tr)
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("second Stop should be a no-op, got: %v", err)
	}
}

func TestStartMissingUsers(t *testing.T) {
	srv := New()
	cfg := protocol.NodeConfig{NodeID: 2, Port: freePort(t), Cipher: "aes-256-gcm"}
	if err := srv.Start(cfg); err == nil {
		t.Fatal("expected error for node with no users")
	}
}

func TestStartUnsupportedCipher(t *testing.T) {
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 3, Port: freePort(t), Cipher: "not-a-cipher",
		Users: []protocol.User{{ID: 1, Password: "x"}},
	}
	if err := srv.Start(cfg); err == nil {
		t.Fatal("expected error for unsupported cipher")
	}
}

// newEchoUpstream starts a plain TCP echo server standing in for the
// "destination" a shadowsocks client tunnels to.
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

// roundTripThroughProxy dials the running shadowsocks server as a raw client
// using the same AEAD codec, requests the echo upstream as destination, and
// verifies a byte written comes back unchanged.
func roundTripThroughProxy(t *testing.T, host string, port int, cipher, password, destAddr string) {
	t.Helper()
	method, err := shadowaead.New(cipher, nil, password)
	if err != nil {
		t.Fatalf("shadowaead.New: %v", err)
	}

	raw, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer raw.Close()

	dest := M.ParseSocksaddr(destAddr)
	conn, err := method.DialConn(raw, dest)
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}

	msg := []byte("hello through shadowsocks")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", buf, msg)
	}
	// Closing signals EOF to the server-side relay goroutines so their
	// io.Copy calls return and the byte counters get flushed to Stats().
	conn.Close()
	raw.Close()
}
