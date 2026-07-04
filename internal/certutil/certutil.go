// Package certutil resolves the TLS certificate a node should serve. If the
// operator provided a cert/key it's loaded; otherwise a self-signed
// certificate is generated in memory, so TLS and QUIC nodes work out of the
// box (the common self-signed + client-"insecure" deployment) without a
// manual cert-provisioning step.
package certutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// selfSignedCache memoizes generated certs per host so repeated node restarts
// (and multiple listeners for one host) don't regenerate keys needlessly.
var (
	cacheMu         sync.Mutex
	selfSignedCache = map[string]tls.Certificate{}
)

// Certificates returns the certificate chain a node should present. When
// cfg.CertFile and cfg.KeyFile are both set they're loaded from disk;
// otherwise a self-signed certificate for host is generated (and cached).
func Certificates(cfg protocol.TLSConfig, host string) ([]tls.Certificate, error) {
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("certutil: load %s/%s: %w", cfg.CertFile, cfg.KeyFile, err)
		}
		return []tls.Certificate{cert}, nil
	}

	name := cfg.Domain
	if name == "" {
		name = host
	}
	cert, err := selfSigned(name)
	if err != nil {
		return nil, err
	}
	return []tls.Certificate{cert}, nil
}

func selfSigned(name string) (tls.Certificate, error) {
	if name == "" || name == "0.0.0.0" || name == "::" {
		name = "v2bx.local"
	}

	cacheMu.Lock()
	if c, ok := selfSignedCache[name]; ok {
		cacheMu.Unlock()
		return c, nil
	}
	cacheMu.Unlock()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certutil: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certutil: serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(name); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{name}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certutil: create certificate: %w", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: tmpl}

	cacheMu.Lock()
	selfSignedCache[name] = cert
	cacheMu.Unlock()
	return cert, nil
}
