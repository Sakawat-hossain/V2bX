package shadowsocks

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/buf"
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

// TestStart2022RequiresServerKey proves a Shadowsocks-2022 node fails with a
// clear, actionable error (not the codec's opaque "missing psk") when the
// panel didn't push a server_key.
func TestStart2022RequiresServerKey(t *testing.T) {
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 4, ListenIP: "127.0.0.1", Port: freePort(t),
		Cipher: "2022-blake3-aes-128-gcm",
		Users:  []protocol.User{{ID: 1, Password: "AAAAAAAAAAAAAAAAAAAAAA=="}},
	}
	if err := srv.Start(cfg); err == nil {
		srv.Stop()
		t.Fatal("expected error for 2022 cipher without server_key")
	}
}

// TestStart2022WithServerKey proves the node starts once the panel's
// server_key (node-level PSK) is threaded through Extra — the regression fix.
func TestStart2022WithServerKey(t *testing.T) {
	const b64key16 = "AAAAAAAAAAAAAAAAAAAAAA==" // 16 zero bytes, sized for aes-128-gcm
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 5, ListenIP: "127.0.0.1", Port: freePort(t),
		Cipher: "2022-blake3-aes-128-gcm",
		Users:  []protocol.User{{ID: 1, Password: b64key16}},
		Extra:  map[string]any{"server_key": b64key16},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("expected 2022 node to start with server_key, got: %v", err)
	}
	srv.Stop()
}

// TestRoundTrip2022MultiUser drives real bytes through the production path for
// a Shadowsocks-2022 multi-user node: listener -> EIH decode -> dial upstream
// -> relay.Pipe. This is the path node 1004 uses; the "starts OK" tests never
// exercised it.
func TestRoundTrip2022MultiUser(t *testing.T) {
	const method = "2022-blake3-aes-128-gcm"
	var iPSK, uPSK [16]byte
	rand.Read(iPSK[:])
	rand.Read(uPSK[:])
	serverKey := base64.StdEncoding.EncodeToString(iPSK[:])
	userPwd := base64.StdEncoding.EncodeToString(uPSK[:])

	port := freePort(t)
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 7, ListenIP: "127.0.0.1", Port: port, Cipher: method,
		Users: []protocol.User{{ID: 99, Password: userPwd}},
		Extra: map[string]any{"server_key": serverKey},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	echo := newEchoUpstream(t)
	defer echo.Close()

	// Multi-user 2022 client keys are [identityPSK, userPSK] (raw bytes).
	client, err := shadowaead_2022.New(method, [][]byte{iPSK[:], uPSK[:]}, nil)
	if err != nil {
		t.Fatalf("client New: %v", err)
	}
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer raw.Close()

	conn, err := client.DialConn(raw, M.ParseSocksaddr(echo.Addr().String()))
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}

	msg := []byte("hello via ss2022 multi-user")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo (no data passed back): %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", buf, msg)
	}
	conn.Close()
	raw.Close()

	deadline := time.Now().Add(2 * time.Second)
	var tr protocol.UserTraffic
	for time.Now().Before(deadline) {
		if u, ok := srv.Stats().Users[99]; ok {
			tr = u
		}
		if tr.Upload != 0 && tr.Download != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tr.Upload == 0 || tr.Download == 0 {
		t.Fatalf("expected nonzero traffic through 2022 relay, got %+v", tr)
	}
}

// TestRoundTripUDP2022 proves the UDP relay pumps multiple packets to a
// destination and returns replies — the path DNS/QUIC needs. The old
// single-shot handler would relay one packet then close, which read as
// "connected, no internet".
func TestRoundTripUDP2022(t *testing.T) {
	const method = "2022-blake3-aes-128-gcm"
	var iPSK, uPSK [16]byte
	rand.Read(iPSK[:])
	rand.Read(uPSK[:])
	serverKey := base64.StdEncoding.EncodeToString(iPSK[:])
	userPwd := base64.StdEncoding.EncodeToString(uPSK[:])

	port := freePort(t)
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 8, ListenIP: "127.0.0.1", Port: port, Cipher: method,
		Users: []protocol.User{{ID: 100, Password: userPwd}},
		Extra: map[string]any{"server_key": serverKey},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	// UDP echo upstream.
	uln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("udp echo: %v", err)
	}
	defer uln.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := uln.ReadFromUDP(b)
			if err != nil {
				return
			}
			uln.WriteToUDP(b[:n], from)
		}
	}()

	client, err := shadowaead_2022.New(method, [][]byte{iPSK[:], uPSK[:]}, nil)
	if err != nil {
		t.Fatalf("client New: %v", err)
	}
	// Real SS UDP: datagrams go to the server's UDP port, not over TCP.
	serverUDP, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("dial server udp: %v", err)
	}
	defer serverUDP.Close()

	pc := client.DialPacketConn(serverUDP)
	echoAddr := M.SocksaddrFromNet(uln.LocalAddr())

	// Send two packets to the same destination and read both echoes back.
	for i := 0; i < 2; i++ {
		payload := []byte("udp-packet-" + strconv.Itoa(i))
		wbuf := buf.NewPacket()
		wbuf.Resize(128, 0) // reserve 128 bytes of front headroom for the SS-2022 header
		wbuf.Write(payload)
		if err := pc.WritePacket(wbuf, echoAddr); err != nil {
			t.Fatalf("WritePacket %d: %v", i, err)
		}
		rbuf := buf.NewPacket()
		serverUDP.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := pc.ReadPacket(rbuf); err != nil {
			t.Fatalf("ReadPacket %d (no UDP data returned): %v", i, err)
		}
		if string(rbuf.Bytes()) != string(payload) {
			t.Fatalf("udp echo %d mismatch: got %q want %q", i, rbuf.Bytes(), payload)
		}
		rbuf.Release()
	}
}

// newEchoUpstream starts a plain TCP echo server standing in for the
// "destination" a shadowsocks client tunnels to.
// TestUpdateUsersLive proves a user added via UpdateUsers can connect without
// the listener being restarted (the port and existing service stay up).
func TestUpdateUsersLive(t *testing.T) {
	port := freePort(t)
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 1, ListenIP: "127.0.0.1", Port: port, Cipher: "aes-256-gcm",
		Users: []protocol.User{{ID: 1, Password: "pw-a"}},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	echo := newEchoUpstream(t)
	defer echo.Close()

	// Original user works.
	roundTripThroughProxy(t, "127.0.0.1", port, "aes-256-gcm", "pw-a", echo.Addr().String())

	// Add a second user live — no restart.
	if err := srv.UpdateUsers([]protocol.User{{ID: 1, Password: "pw-a"}, {ID: 2, Password: "pw-b"}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}

	// The new user can now connect on the same, still-open listener.
	roundTripThroughProxy(t, "127.0.0.1", port, "aes-256-gcm", "pw-b", echo.Addr().String())
	// And the original user still works.
	roundTripThroughProxy(t, "127.0.0.1", port, "aes-256-gcm", "pw-a", echo.Addr().String())
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
