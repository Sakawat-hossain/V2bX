package anytls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	anytls "github.com/anytls/sing-anytls"
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

func generateSelfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "v2bx-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0o600)
	return certFile, keyFile
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
	certFile, keyFile := generateSelfSigned(t)
	port := freePort(t)

	const password = "test-password"
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 1, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 88, UUID: "user-uuid", Password: password}},
		TLS:   protocol.TLSConfig{CertFile: certFile, KeyFile: keyFile},
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

	serverAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := anytls.NewClient(ctx, anytls.ClientConfig{
		Password: password,
		Logger:   logger.NOP(),
		DialOut: func(ctx context.Context) (net.Conn, error) {
			raw, err := net.Dial("tcp", serverAddr)
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true})
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	})
	if err != nil {
		t.Fatalf("anytls.NewClient: %v", err)
	}

	conn, err := client.CreateProxy(ctx, M.ParseSocksaddr(echo.Addr().String()))
	if err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello via anytls")
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
		if u, ok := stats.Users[88]; ok {
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

func TestStartMissingTLS(t *testing.T) {
	srv := New()
	cfg := protocol.NodeConfig{NodeID: 2, Port: freePort(t), Users: []protocol.User{{ID: 1, Password: "x"}}}
	if err := srv.Start(cfg); err == nil {
		t.Fatal("expected error for missing TLS cert/key")
	}
}
