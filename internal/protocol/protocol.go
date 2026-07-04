// Package protocol defines the common interface that every proxy protocol
// implementation (Shadowsocks, VMess, VLess, Trojan, ...) must satisfy so the
// panel-sync and CLI layers can manage them uniformly.
package protocol

import (
	"context"
	"fmt"
	"sync"
)

// User is a single subscriber assigned to a node, as delivered by the panel.
type User struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	Password    string `json:"password"`
	Flow        string `json:"flow,omitempty"`    // VLess flow control, e.g. "xtls-rprx-vision"; empty = none
	SpeedLimit  uint64 `json:"speed_limit_bytes"` // bytes/sec, 0 = unlimited
	DeviceLimit int    `json:"device_limit"`      // 0 = unlimited
}

// TLSConfig describes how a node terminates TLS, independent of protocol.
type TLSConfig struct {
	Mode     string `json:"mode"` // none|http|dns|self
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	Domain   string `json:"domain,omitempty"`
}

// NodeConfig is the fully-resolved configuration for a single running node,
// merging panel-provided settings with local overrides.
type NodeConfig struct {
	NodeID   int64
	NodeType string // shadowsocks|vmess|vless|trojan|hysteria|hysteria2|tuic|socks5|naive|http|mieru|anytls
	ListenIP string
	Port     int

	Cipher string // shadowsocks cipher method, ignored otherwise
	Users  []User

	TLS      TLSConfig
	Sniffing bool
	TFO      bool
	Fallback []FallbackRule

	Extra map[string]any // protocol-specific options that don't warrant a dedicated field
}

// FallbackRule describes a fallback destination for TLS/HTTP multiplexing.
type FallbackRule struct {
	SNI  string `json:"sni,omitempty"`
	Path string `json:"path,omitempty"`
	Dest string `json:"dest"`
}

// UsageStats reports accumulated traffic for a node, broken down per user.
type UsageStats struct {
	NodeID int64
	Users  map[int64]UserTraffic // keyed by User.ID
}

// UserTraffic is the upload/download byte count accrued since the last report.
type UserTraffic struct {
	Upload   uint64
	Download uint64
}

// ProtocolServer is implemented by every protocol backend. Implementations
// must be safe to Start/Stop repeatedly and must not block in Start beyond
// what's needed to bind the listener.
type ProtocolServer interface {
	// Start binds the listener(s) described by cfg and begins serving.
	Start(cfg NodeConfig) error
	// Stop closes all listeners and releases resources. Safe to call on an
	// already-stopped server.
	Stop() error
	// Stats returns cumulative per-user traffic totals since the server
	// started (monotonic, never reset). Callers compute deltas themselves so
	// a failed report never loses or double-counts traffic.
	Stats() UsageStats
	// Name returns the protocol identifier, e.g. "shadowsocks".
	Name() string
}

// OnlineReporter is an optional interface a ProtocolServer implements to
// report which source IPs each user is currently connected from, so the
// manager can forward device presence to the panel.
type OnlineReporter interface {
	Online() map[int64][]string
}

// UserUpdater is an optional interface a ProtocolServer can implement to
// swap its user set live, without closing the listener or dropping active
// connections. When a sync changes only the user list (not the port, cipher,
// or TLS), the manager prefers this over a restart. Servers whose underlying
// core can't reload users leave it unimplemented and get a restart instead.
type UserUpdater interface {
	UpdateUsers(users []User) error
}

// Factory builds a fresh, unstarted ProtocolServer instance.
type Factory func() ProtocolServer

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a protocol factory to the global registry. Called from each
// protocol package's init(). Panics on duplicate registration since that
// indicates a programming error, not a runtime condition.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("protocol: duplicate registration for %q", name))
	}
	registry[name] = f
}

// New constructs a new ProtocolServer for the given protocol name.
func New(name string) (ProtocolServer, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("protocol: unknown node type %q", name)
	}
	return f(), nil
}

// Known returns the list of currently registered protocol names.
func Known() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}

// contextKey avoids collisions when protocol servers stash values on a
// context passed down to per-connection handlers.
type contextKey string

// NodeIDKey is the context key protocol servers use to attach the owning
// node's ID to per-connection contexts, so stats attribution survives
// goroutine boundaries.
const NodeIDKey contextKey = "v2bx.node_id"

// WithNodeID returns a child context carrying the node ID.
func WithNodeID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, NodeIDKey, id)
}
