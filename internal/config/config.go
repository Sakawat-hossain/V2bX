// Package config defines and parses the V2bX agent configuration file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config is the top-level agent configuration, loaded from config.json.
type Config struct {
	Log     LogConfig     `json:"log"`
	Panel   PanelConfig   `json:"panel"`
	Metrics MetricsConfig `json:"metrics,omitempty"`
	Nodes   []NodeEntry   `json:"nodes"`
}

// MetricsConfig controls the Prometheus-compatible metrics endpoint.
type MetricsConfig struct {
	// Listen is the address to serve /metrics on, e.g. "127.0.0.1:9095".
	// Empty disables it. Bind to localhost (or a private interface) and
	// scrape over it — the endpoint is unauthenticated.
	Listen string `json:"listen,omitempty"`
}

// LogConfig controls agent-wide logging.
type LogConfig struct {
	Level  string `json:"level"`  // debug|info|warn|error
	Output string `json:"output"` // "stdout" or a file path
}

// PanelConfig describes how to reach the V2board-family panel.
type PanelConfig struct {
	ApiHost      string `json:"api_host"`
	ApiKey       string `json:"api_key"`
	SyncInterval int    `json:"sync_interval_seconds"`

	// Endpoint paths are overridable so any panel implementing the same
	// UniProxy-shaped contract (XBoard, V2Board, forks) can be targeted
	// without hardcoding one panel's routes.
	ConfigPath string `json:"config_path,omitempty"`
	UserPath   string `json:"user_path,omitempty"`
	PushPath   string `json:"push_path,omitempty"`
	AlivePath  string `json:"alive_path,omitempty"`
}

// SyncIntervalDuration returns the configured sync interval, defaulting to 60s.
func (p PanelConfig) SyncIntervalDuration() time.Duration {
	if p.SyncInterval <= 0 {
		return 60 * time.Second
	}
	return time.Duration(p.SyncInterval) * time.Second
}

// NodeEntry is a single node this agent instance should run, with local
// overrides layered on top of whatever the panel reports for NodeID.
type NodeEntry struct {
	NodeID   int64  `json:"node_id"`
	NodeType string `json:"node_type"`
	Enabled  bool   `json:"enabled"`

	ListenIP string `json:"listen_ip,omitempty"`

	CertMode string `json:"cert_mode,omitempty"` // none|http|dns|self
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`

	TFO      bool `json:"tfo,omitempty"`
	Sniffing bool `json:"sniffing,omitempty"`

	// Hysteria overrides, used when the panel doesn't supply them. UpMbps/
	// DownMbps set the Brutal congestion-control rate (Mbps); Obfs is the
	// Hysteria2 Salamander password.
	UpMbps   int    `json:"up_mbps,omitempty"`
	DownMbps int    `json:"down_mbps,omitempty"`
	Obfs     string `json:"obfs,omitempty"`

	// PortHopRange enables UDP port hopping for QUIC nodes, e.g. "20000-40000".
	// The agent installs an iptables redirect from the range to the node port
	// (needs root + iptables). Empty = off.
	PortHopRange string `json:"port_hop_range,omitempty"`

	Limits NodeLimits `json:"limits,omitempty"`
}

// NodeLimits holds global defaults that apply unless the panel overrides
// them per-user.
type NodeLimits struct {
	DefaultSpeedLimitBytes uint64 `json:"default_speed_limit_bytes,omitempty"`
	DeviceLimit            int    `json:"device_limit,omitempty"`
	IPLimit                int    `json:"ip_limit,omitempty"`
	TrafficResetDay        int    `json:"traffic_reset_day,omitempty"` // day-of-month, 0 = panel default
	MaxConnections         int    `json:"max_connections,omitempty"`   // concurrent accepted conns, 0 = unlimited
}

// NodeTypes lists every supported node type, in a stable presentation order
// (grouped by wire family). It is the single source of truth for both
// validation and the interactive config wizard.
var NodeTypes = []string{
	"shadowsocks", "vmess", "vless",
	"trojan", "naive", "anytls",
	"hysteria", "hysteria2", "tuic",
	"socks5", "http", "mieru",
}

// TLSRequired reports whether a node type must have a TLS certificate to run.
func TLSRequired(nodeType string) bool {
	switch nodeType {
	case "trojan", "naive", "anytls", "hysteria", "hysteria2", "tuic":
		return true
	default:
		return false
	}
}

var validNodeTypes = func() map[string]bool {
	m := make(map[string]bool, len(NodeTypes))
	for _, t := range NodeTypes {
		m[t] = true
	}
	return m
}()

var validCertModes = map[string]bool{"": true, "none": true, "http": true, "dns": true, "self": true}

var validLogLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// Load reads and validates a config file from path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks the config for structural and semantic errors, returning
// the first problem found.
func (c *Config) Validate() error {
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if !validLogLevels[c.Log.Level] {
		return fmt.Errorf("config: invalid log.level %q", c.Log.Level)
	}
	if c.Log.Output == "" {
		c.Log.Output = "stdout"
	}

	if c.Panel.ApiHost == "" {
		return fmt.Errorf("config: panel.api_host is required")
	}
	if c.Panel.ApiKey == "" {
		return fmt.Errorf("config: panel.api_key is required")
	}
	if c.Panel.ConfigPath == "" {
		c.Panel.ConfigPath = "/api/v1/server/UniProxy/config"
	}
	if c.Panel.UserPath == "" {
		c.Panel.UserPath = "/api/v1/server/UniProxy/user"
	}
	if c.Panel.PushPath == "" {
		c.Panel.PushPath = "/api/v1/server/UniProxy/push"
	}
	if c.Panel.AlivePath == "" {
		c.Panel.AlivePath = "/api/v1/server/UniProxy/alive"
	}

	if len(c.Nodes) == 0 {
		return fmt.Errorf("config: at least one entry in nodes is required")
	}
	seen := map[int64]bool{}
	for i, n := range c.Nodes {
		if n.NodeID == 0 {
			return fmt.Errorf("config: nodes[%d].node_id is required", i)
		}
		if seen[n.NodeID] {
			return fmt.Errorf("config: nodes[%d]: duplicate node_id %d", i, n.NodeID)
		}
		seen[n.NodeID] = true
		if !validNodeTypes[n.NodeType] {
			return fmt.Errorf("config: nodes[%d]: invalid node_type %q", i, n.NodeType)
		}
		if !validCertModes[n.CertMode] {
			return fmt.Errorf("config: nodes[%d]: invalid cert_mode %q", i, n.CertMode)
		}
		// cert_file/key_file are optional: when omitted, a self-signed
		// certificate is generated at runtime.
	}
	return nil
}
