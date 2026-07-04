// Package realityutil translates V2bX's Reality node config into the
// sagernet/reality server config, decoding keys and short IDs exactly as the
// library (and Xray clients) expect. Any error here means a partial or
// malformed config — callers must fail closed rather than serve a detectable
// handshake.
package realityutil

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/sagernet/reality"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// BuildServerConfig produces a *reality.Config from the node's Reality block.
func BuildServerConfig(rc *protocol.RealityConfig) (*reality.Config, error) {
	if rc == nil {
		return nil, fmt.Errorf("realityutil: nil config")
	}
	if rc.Dest == "" {
		return nil, fmt.Errorf("realityutil: dest is required")
	}
	if len(rc.ServerNames) == 0 {
		return nil, fmt.Errorf("realityutil: server_names is required")
	}

	priv, err := decodeKey(rc.PrivateKey)
	if err != nil {
		return nil, err
	}

	names := make(map[string]bool, len(rc.ServerNames))
	for _, n := range rc.ServerNames {
		names[n] = true
	}

	shortIDs := make(map[[8]byte]bool)
	if len(rc.ShortIDs) == 0 {
		shortIDs[[8]byte{}] = true // permit the empty short ID
	}
	for _, s := range rc.ShortIDs {
		id, err := decodeShortID(s)
		if err != nil {
			return nil, err
		}
		shortIDs[id] = true
	}

	return &reality.Config{
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		Type:                   "tcp",
		Dest:                   rc.Dest,
		ServerNames:            names,
		PrivateKey:             priv,
		ShortIds:               shortIDs,
		SessionTicketsDisabled: true,
	}, nil
}

// decodeKey parses the 32-byte x25519 private key, accepting the base64
// variants `v2bx x25519` / `xray x25519` emit.
func decodeKey(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("realityutil: private_key is required")
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("realityutil: private_key must be a 32-byte base64 x25519 key")
}

// decodeShortID decodes a hex short ID (0–16 hex chars, even length) into the
// 8-byte, left-aligned form the library compares against.
func decodeShortID(s string) ([8]byte, error) {
	var id [8]byte
	if s == "" {
		return id, nil
	}
	if len(s) > 16 || len(s)%2 != 0 {
		return id, fmt.Errorf("realityutil: short id %q must be 0-16 hex chars of even length", s)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, fmt.Errorf("realityutil: short id %q is not hex: %w", s, err)
	}
	copy(id[:], b)
	return id, nil
}

// ServerHandshake wraps an accepted TCP connection with the Reality server
// handshake. On an authorized client it returns the proxied connection; on a
// probe it returns an error after the library has transparently served the
// decoy site.
func ServerHandshake(ctx context.Context, conn net.Conn, cfg *reality.Config) (net.Conn, error) {
	return reality.Server(ctx, conn, cfg)
}
