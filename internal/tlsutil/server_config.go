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

// NewServerConfig wraps resolved certificates into a ServerConfig for
// QUIC-based listeners.
func NewServerConfig(certs []tls.Certificate, nextProtos []string) *StdServerConfig {
	return &StdServerConfig{config: &tls.Config{
		Certificates: certs,
		NextProtos:   nextProtos,
	}}
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
