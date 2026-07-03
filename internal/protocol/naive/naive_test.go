package naive

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/http2"

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
		DNSNames:     []string{"127.0.0.1"},
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

func TestConnectTunnelWithAuth(t *testing.T) {
	certFile, keyFile := generateSelfSigned(t)
	port := freePort(t)

	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 1, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 9, UUID: "alice", Password: "secret"}},
		TLS:   protocol.TLSConfig{CertFile: certFile, KeyFile: keyFile},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	echo := newEchoUpstream(t)
	defer echo.Close()

	transport := &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	proxyAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	pr, pw := io.Pipe()
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Scheme: "https", Host: proxyAddr},
		Host:   echo.Addr().String(),
		Header: http.Header{
			"Proxy-Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))},
		},
		Body: pr,
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	msg := []byte("hello via naive")
	go pw.Write(msg)

	buf := make([]byte, len(msg))
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, buf)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading echo response")
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", buf, msg)
	}
}

func TestConnectRejectsBadAuth(t *testing.T) {
	certFile, keyFile := generateSelfSigned(t)
	port := freePort(t)

	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 2, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 1, UUID: "alice", Password: "secret"}},
		TLS:   protocol.TLSConfig{CertFile: certFile, KeyFile: keyFile},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	transport := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	proxyAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	pr, _ := io.Pipe()
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Scheme: "https", Host: proxyAddr},
		Host:   "example.invalid:443",
		Body:   pr,
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407, got %d", resp.StatusCode)
	}
}
