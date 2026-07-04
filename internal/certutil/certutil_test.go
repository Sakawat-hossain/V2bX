package certutil

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

func TestSelfSignedWhenNoFiles(t *testing.T) {
	certs, err := Certificates(protocol.TLSConfig{}, "203.0.113.5")
	if err != nil {
		t.Fatalf("Certificates: %v", err)
	}
	if len(certs) != 1 || certs[0].Leaf == nil {
		t.Fatal("expected one self-signed certificate")
	}
	if got := certs[0].Leaf.IPAddresses; len(got) != 1 || got[0].String() != "203.0.113.5" {
		t.Fatalf("expected IP SAN 203.0.113.5, got %v", got)
	}
}

func TestSelfSignedUsesDomainOverHost(t *testing.T) {
	certs, err := Certificates(protocol.TLSConfig{Domain: "vpn.example.com"}, "10.0.0.1")
	if err != nil {
		t.Fatalf("Certificates: %v", err)
	}
	if names := certs[0].Leaf.DNSNames; len(names) != 1 || names[0] != "vpn.example.com" {
		t.Fatalf("expected DNS SAN vpn.example.com, got %v", names)
	}
}

func TestSelfSignedCachedPerHost(t *testing.T) {
	a, _ := Certificates(protocol.TLSConfig{}, "example.test")
	b, _ := Certificates(protocol.TLSConfig{}, "example.test")
	if a[0].Leaf.SerialNumber.Cmp(b[0].Leaf.SerialNumber) != 0 {
		t.Fatal("same host should return the cached certificate")
	}
}

func TestLoadsProvidedFiles(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	sc, err := selfSigned("loadme.test")
	if err != nil {
		t.Fatalf("selfSigned: %v", err)
	}
	writePEM(t, certFile, "CERTIFICATE", sc.Certificate[0])
	key, err := x509.MarshalPKCS8PrivateKey(sc.PrivateKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	writePEM(t, keyFile, "PRIVATE KEY", key)

	certs, err := Certificates(protocol.TLSConfig{CertFile: certFile, KeyFile: keyFile}, "ignored")
	if err != nil {
		t.Fatalf("Certificates from files: %v", err)
	}
	if len(certs) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(certs))
	}
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
