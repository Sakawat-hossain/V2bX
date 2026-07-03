package mieru

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	miclient "github.com/enfein/mieru/v3/apis/client"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/proto"

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
	const (
		username = "test-user-uuid"
		password = "a-reasonably-strong-password-123"
	)

	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 1, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 77, UUID: username, Password: password}},
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

	client := miclient.NewClient()
	if err := client.Store(&miclient.ClientConfig{
		Profile: &appctlpb.ClientProfile{
			ProfileName: proto.String("test"),
			User:        &appctlpb.User{Name: proto.String(username), Password: proto.String(password)},
			Servers: []*appctlpb.ServerEndpoint{
				{
					IpAddress: proto.String("127.0.0.1"),
					PortBindings: []*appctlpb.PortBinding{
						{Port: proto.Int32(int32(port)), Protocol: appctlpb.TransportProtocol_TCP.Enum()},
					},
				},
			},
			Mtu: proto.Int32(1400),
		},
	}); err != nil {
		t.Fatalf("client.Store: %v", err)
	}
	if _, err := client.Load(); err != nil {
		t.Fatalf("client.Load: %v", err)
	}
	if err := client.Start(); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	defer client.Stop()

	echoAddr := echo.Addr().(*net.TCPAddr)
	var dst model.NetAddrSpec
	if err := dst.From(echoAddr); err != nil {
		t.Fatalf("NetAddrSpec.From: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := client.DialContext(ctx, dst)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello via mieru")
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

	deadline := time.Now().Add(2 * time.Second)
	var tr protocol.UserTraffic
	for time.Now().Before(deadline) {
		stats := srv.Stats()
		if u, ok := stats.Users[77]; ok {
			tr.Upload += u.Upload
			tr.Download += u.Download
		}
		if tr.Upload != 0 && tr.Download != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tr.Upload == 0 || tr.Download == 0 {
		t.Fatalf("expected nonzero upload/download, got %+v", tr)
	}
}

func TestStartMissingUsers(t *testing.T) {
	srv := New()
	cfg := protocol.NodeConfig{NodeID: 2, Port: freePort(t)}
	if err := srv.Start(cfg); err == nil {
		t.Fatal("expected error for node with no users")
	}
}
