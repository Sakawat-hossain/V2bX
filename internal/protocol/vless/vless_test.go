package vless

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	svless "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
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

func TestStartStopAndRelay(t *testing.T) {
	port := freePort(t)
	srv := New()
	const testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"
	cfg := protocol.NodeConfig{
		NodeID:   1,
		NodeType: "vless",
		ListenIP: "127.0.0.1",
		Port:     port,
		Users:    []protocol.User{{ID: 55, UUID: testUUID}},
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

	client, err := svless.NewClient(testUUID, "", logger.NOP())
	if err != nil {
		t.Fatalf("svless.NewClient: %v", err)
	}

	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer raw.Close()

	dest := M.ParseSocksaddr(echo.Addr().String())
	conn, err := client.DialConn(raw, dest)
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}

	msg := []byte("hello through vless")
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
	conn.Close()
	raw.Close()

	deadline := time.Now().Add(2 * time.Second)
	var tr protocol.UserTraffic
	for time.Now().Before(deadline) {
		stats := srv.Stats()
		if u, ok := stats.Users[55]; ok {
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
}

// TestUDPRelay drives UDP-over-VLESS end to end: it proves DNS/QUIC-style
// datagrams reach an upstream and the replies come back (multiple packets),
// which is what "no internet" clients need beyond TCP.
func TestUDPRelay(t *testing.T) {
	port := freePort(t)
	srv := New()
	const testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"
	cfg := protocol.NodeConfig{
		NodeID: 2, NodeType: "vless", ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 66, UUID: testUUID}},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

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

	client, err := svless.NewClient(testUUID, "", logger.NOP())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer raw.Close()

	dest := M.SocksaddrFromNet(uln.LocalAddr())
	pc, err := client.DialPacketConn(raw, dest)
	if err != nil {
		t.Fatalf("DialPacketConn: %v", err)
	}

	for i := 0; i < 2; i++ {
		payload := []byte("udp-vless-" + strconv.Itoa(i))
		if _, err := pc.Write(payload); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		raw.SetReadDeadline(time.Now().Add(3 * time.Second))
		rb := make([]byte, 2048)
		n, err := pc.Read(rb)
		if err != nil {
			t.Fatalf("Read %d (no UDP data returned): %v", i, err)
		}
		if string(rb[:n]) != string(payload) {
			t.Fatalf("udp echo %d mismatch: got %q want %q", i, rb[:n], payload)
		}
	}
}

func TestStartMissingUsers(t *testing.T) {
	srv := New()
	cfg := protocol.NodeConfig{NodeID: 2, Port: freePort(t)}
	if err := srv.Start(cfg); err == nil {
		t.Fatal("expected error for node with no users")
	}
}
