// Package tlsutil adapts a standard crypto/tls.Config into sing's
// tls.ServerConfig interface, which the QUIC-based protocol backends
// (Hysteria, Hysteria2, TUIC) require to terminate TLS-over-QUIC.
package tlsutil

import (
	"crypto/tls"
	"net"

	stls "github.com/sagernet/sing/common/tls"
)

// StdServerConfig wraps a stdlib *tls.Config to satisfy stls.ServerConfig.
// *tls.Conn already implements stls.Conn directly (NetConn/HandshakeContext/
// ConnectionState all exist on it), so Server() can hand one back as-is.
type StdServerConfig struct {
	config *tls.Config
}

// NewStdServerConfig loads a certificate/key pair and returns a ready-to-use
// ServerConfig for QUIC-based listeners.
func NewStdServerConfig(certFile, keyFile string, nextProtos []string) (*StdServerConfig, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &StdServerConfig{config: &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   nextProtos,
	}}, nil
}

func (c *StdServerConfig) ServerName() string              { return "" }
func (c *StdServerConfig) SetServerName(string)            {}
func (c *StdServerConfig) NextProtos() []string            { return c.config.NextProtos }
func (c *StdServerConfig) SetNextProtos(np []string)       { c.config.NextProtos = np }
func (c *StdServerConfig) STDConfig() (*tls.Config, error) { return c.config, nil }
func (c *StdServerConfig) Clone() stls.Config {
	return &StdServerConfig{config: c.config.Clone()}
}
func (c *StdServerConfig) Start() error { return nil }
func (c *StdServerConfig) Close() error { return nil }

func (c *StdServerConfig) Client(conn net.Conn) (stls.Conn, error) {
	return tls.Client(conn, c.config), nil
}

func (c *StdServerConfig) Server(conn net.Conn) (stls.Conn, error) {
	return tls.Server(conn, c.config), nil
}
