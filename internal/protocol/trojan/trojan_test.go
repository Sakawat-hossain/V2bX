package trojan

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
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

// generateSelfSigned writes a throwaway self-signed cert/key pair to t's
// temp dir and returns their paths.
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
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
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

	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 1, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 7, Password: "test-password"}},
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

	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	defer conn.Close()

	echoAddr := echo.Addr().(*net.TCPAddr)
	req := []byte(hexSHA224("test-password"))
	req = append(req, '\r', '\n')
	req = append(req, cmdConnect, addrIPv4)
	req = append(req, echoAddr.IP.To4()...)
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, uint16(echoAddr.Port))
	req = append(req, portBuf...)
	req = append(req, '\r', '\n')
	msg := []byte("hello via trojan")
	req = append(req, msg...)

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}

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
		if u, ok := stats.Users[7]; ok {
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
}

func TestWrongPasswordDropped(t *testing.T) {
	certFile, keyFile := generateSelfSigned(t)
	port := freePort(t)

	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 2, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 1, Password: "correct-password"}},
		TLS:   protocol.TLSConfig{CertFile: certFile, KeyFile: keyFile},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	defer conn.Close()

	req := []byte(hexSHA224("wrong-password"))
	req = append(req, '\r', '\n', cmdConnect, addrIPv4, 1, 2, 3, 4, 0, 80, '\r', '\n')
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	conn.Write(req)

	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected connection to be dropped for wrong password")
	}
}

func TestMissingTLSConfigRejected(t *testing.T) {
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 3, Port: freePort(t),
		Users: []protocol.User{{ID: 1, Password: "x"}},
	}
	if err := srv.Start(cfg); err == nil {
		t.Fatal("expected error for missing TLS cert/key")
	}
}
