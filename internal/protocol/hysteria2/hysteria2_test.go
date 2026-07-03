package hysteria2

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

	hy2 "github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	stls "github.com/sagernet/sing/common/tls"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer ln.Close()
	return ln.LocalAddr().(*net.UDPAddr).Port
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

// insecureClientTLSConfig adapts a stdlib *tls.Config (InsecureSkipVerify)
// into sing's stls.Config for the Hysteria2 client under test.
type insecureClientTLSConfig struct{ cfg *tls.Config }

func (c *insecureClientTLSConfig) ServerName() string              { return c.cfg.ServerName }
func (c *insecureClientTLSConfig) SetServerName(name string)       { c.cfg.ServerName = name }
func (c *insecureClientTLSConfig) NextProtos() []string            { return c.cfg.NextProtos }
func (c *insecureClientTLSConfig) SetNextProtos(np []string)       { c.cfg.NextProtos = np }
func (c *insecureClientTLSConfig) STDConfig() (*tls.Config, error) { return c.cfg, nil }
func (c *insecureClientTLSConfig) Clone() stls.Config              { return &insecureClientTLSConfig{c.cfg.Clone()} }
func (c *insecureClientTLSConfig) Client(conn net.Conn) (stls.Conn, error) {
	return tls.Client(conn, c.cfg), nil
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

	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 1, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 3, Password: "test-password"}},
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := hy2.NewClient(hy2.ClientOptions{
		Context:       ctx,
		Dialer:        N.SystemDialer,
		Logger:        logger.NOP(),
		ServerAddress: M.ParseSocksaddr(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))),
		ServerPorts:   []string{strconv.Itoa(port) + ":" + strconv.Itoa(port)},
		Password:      "test-password",
		TLSConfig:     &insecureClientTLSConfig{&tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}},
	})
	if err != nil {
		t.Fatalf("hy2.NewClient: %v", err)
	}
	defer client.CloseWithError(nil)

	conn, err := client.DialConn(ctx, M.ParseSocksaddr(echo.Addr().String()))
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello via hysteria2")
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
}

func TestStartMissingTLS(t *testing.T) {
	srv := New()
	cfg := protocol.NodeConfig{NodeID: 2, Port: freePort(t), Users: []protocol.User{{ID: 1, Password: "x"}}}
	if err := srv.Start(cfg); err == nil {
		t.Fatal("expected error for missing TLS cert/key")
	}
}
