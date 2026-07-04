package vless

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// destTLSCert builds a self-signed cert with the given common name for the
// decoy "dest" server.
func destTLSCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

// startDecoy runs a TLS echo server standing in for the real site Reality
// impersonates and proxies probes to.
func startDecoy(t *testing.T, cn string) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{destTLSCert(t, cn)}})
	if err != nil {
		t.Fatalf("decoy listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func x25519Key(t *testing.T) string {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("x25519: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(k.Bytes())
}

// TestRealityProbeReachesDecoy verifies the core anti-active-probing property:
// an unauthorized client (a plain TLS probe, not a Reality client) connecting
// to the node is transparently proxied to the real decoy site and sees the
// decoy's certificate — so a censor probing the IP finds a real website, not a
// proxy.
func TestRealityProbeReachesDecoy(t *testing.T) {
	const decoyName = "decoy.example"
	decoyAddr := startDecoy(t, decoyName)

	port := freePort(t)
	srv := New()
	cfg := protocol.NodeConfig{
		NodeID: 1, ListenIP: "127.0.0.1", Port: port,
		Users: []protocol.User{{ID: 1, UUID: "b831381d-6324-4d53-ad4f-8cda48b30811"}},
		Reality: &protocol.RealityConfig{
			Dest:        decoyAddr,
			ServerNames: []string{decoyName},
			PrivateKey:  x25519Key(t),
			ShortIDs:    []string{""},
		},
	}
	if err := srv.Start(cfg); err != nil {
		t.Fatalf("Start reality node: %v", err)
	}
	defer srv.Stop()

	// A plain TLS client (the "probe") with no Reality auth.
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 5 * time.Second},
		"tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		&tls.Config{ServerName: decoyName, InsecureSkipVerify: true},
	)
	if err != nil {
		t.Fatalf("probe handshake failed (should transparently reach decoy): %v", err)
	}
	defer conn.Close()

	// The certificate it received must be the decoy's — proving the probe was
	// proxied to the real site, not answered by a proxy.
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 || certs[0].Subject.CommonName != decoyName {
		t.Fatalf("probe did not receive the decoy certificate: %+v", certs)
	}

	// And the tunnel actually reaches the decoy (it echoes).
	msg := []byte("probe payload")
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write to decoy: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read decoy echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("decoy echo mismatch: %q", buf)
	}
}
