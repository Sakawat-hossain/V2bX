// Package service wires config, the panel client, and protocol servers
// together into a running agent: syncing node config/users on an interval,
// starting/stopping protocol listeners as config changes, and reporting
// traffic and online stats back to the panel.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/config"
	"github.com/Sakawat-hossain/V2bX/internal/panel"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// Manager runs the sync loop for every enabled node in the config and keeps
// their protocol servers up to date.
type Manager struct {
	cfg    *config.Config
	client *panel.Client
	logger *slog.Logger

	mu    sync.Mutex
	nodes map[int64]*runningNode
}

type runningNode struct {
	entry  config.NodeEntry
	server protocol.ProtocolServer
	// lastGood is the last config successfully applied; kept so a panel
	// outage doesn't tear down an already-running node.
	lastGood *protocol.NodeConfig
}

// New builds a Manager from a loaded config, constructing its own panel client.
func New(cfg *config.Config, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	client, err := panel.New(panel.Options{
		ApiHost:    cfg.Panel.ApiHost,
		ApiKey:     cfg.Panel.ApiKey,
		ConfigPath: cfg.Panel.ConfigPath,
		UserPath:   cfg.Panel.UserPath,
		PushPath:   cfg.Panel.PushPath,
		AlivePath:  cfg.Panel.AlivePath,
		Logger:     logger,
	})
	if err != nil {
		return nil, err
	}
	return &Manager{
		cfg:    cfg,
		client: client,
		logger: logger,
		nodes:  map[int64]*runningNode{},
	}, nil
}

// Run starts the sync loop and blocks until ctx is cancelled, then stops
// every running protocol server.
func (m *Manager) Run(ctx context.Context) error {
	defer m.stopAll()

	interval := m.cfg.Panel.SyncIntervalDuration()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	m.syncAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.syncAll(ctx)
		}
	}
}

// Sync forces an immediate panel resync of every enabled node, outside the
// regular ticker interval. Used to service SIGHUP/`v2bx reload`.
func (m *Manager) Sync(ctx context.Context) {
	m.syncAll(ctx)
}

func (m *Manager) syncAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, entry := range m.cfg.Nodes {
		if !entry.Enabled {
			continue
		}
		entry := entry
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.syncNode(ctx, entry)
		}()
	}
	wg.Wait()
}

func (m *Manager) syncNode(ctx context.Context, entry config.NodeEntry) {
	rc := panel.RetryConfig{InitialDelay: time.Second, MaxDelay: 30 * time.Second, MaxAttempts: 3}

	var nodeCfg *protocol.NodeConfig
	err := panel.WithRetry(ctx, m.logger, rc, fmt.Sprintf("sync node %d", entry.NodeID), func(ctx context.Context) error {
		resolved, err := m.fetchNodeConfig(ctx, entry)
		if err != nil {
			return err
		}
		nodeCfg = resolved
		return nil
	})
	if err != nil {
		m.logger.Warn("keeping last-known-good config after sync failure", "node_id", entry.NodeID, "error", err)
		return
	}

	m.applyNodeConfig(entry, nodeCfg)
}

func (m *Manager) fetchNodeConfig(ctx context.Context, entry config.NodeEntry) (*protocol.NodeConfig, error) {
	remoteCfg, err := m.client.FetchNodeConfig(ctx, entry.NodeID, entry.NodeType)
	if err != nil {
		return nil, err
	}
	users, err := m.client.FetchUsers(ctx, entry.NodeID, entry.NodeType)
	if err != nil {
		return nil, err
	}

	port := remoteCfg.ListenPort()
	if port == 0 {
		return nil, fmt.Errorf("panel returned no server_port for node %d", entry.NodeID)
	}

	nc := &protocol.NodeConfig{
		NodeID:   entry.NodeID,
		NodeType: entry.NodeType,
		ListenIP: entry.ListenIP,
		Port:     port,
		Cipher:   remoteCfg.Cipher,
		TFO:      entry.TFO,
		Sniffing: entry.Sniffing,
		TLS: protocol.TLSConfig{
			Mode:     entry.CertMode,
			CertFile: entry.CertFile,
			KeyFile:  entry.KeyFile,
		},
		Extra: map[string]any{},
	}
	if remoteCfg.ServerKey != "" {
		nc.Extra["server_key"] = remoteCfg.ServerKey
	}
	for _, u := range users {
		// The panel doesn't send a separate password — the user's UUID is the
		// credential for Shadowsocks, Trojan, TUIC, etc.
		password := u.Password
		if password == "" {
			password = u.UUID
		}
		speedLimit := u.SpeedLimit
		if speedLimit == 0 {
			speedLimit = entry.Limits.DefaultSpeedLimitBytes
		}
		deviceLimit := u.DeviceLimit
		if deviceLimit == 0 {
			deviceLimit = entry.Limits.DeviceLimit
		}
		nc.Users = append(nc.Users, protocol.User{
			ID: u.ID, UUID: u.UUID, Password: password, Flow: u.Flow,
			SpeedLimit: speedLimit, DeviceLimit: deviceLimit,
		})
	}
	return nc, nil
}

// applyNodeConfig starts the node if it isn't running, or restarts it if the
// resolved config has changed since the last successful sync.
func (m *Manager) applyNodeConfig(entry config.NodeEntry, nc *protocol.NodeConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rn, exists := m.nodes[entry.NodeID]
	if exists && rn.lastGood != nil && configEqual(rn.lastGood, nc) {
		return // no change, avoid needlessly bouncing the listener
	}

	if exists {
		if err := rn.server.Stop(); err != nil {
			m.logger.Warn("failed to stop node for restart", "node_id", entry.NodeID, "error", err)
		}
	} else {
		server, err := protocol.New(entry.NodeType)
		if err != nil {
			m.logger.Error("cannot start node: unsupported protocol", "node_id", entry.NodeID, "node_type", entry.NodeType, "error", err)
			return
		}
		rn = &runningNode{entry: entry, server: server}
		m.nodes[entry.NodeID] = rn
	}

	if err := rn.server.Start(*nc); err != nil {
		m.logger.Error("failed to start node", "node_id", entry.NodeID, "error", err)
		return
	}
	rn.lastGood = nc
	m.logger.Info("node running",
		"node_id", entry.NodeID, "type", entry.NodeType,
		"listen", fmt.Sprintf("%s:%d", nc.ListenIP, nc.Port), "users", len(nc.Users))
}

func configEqual(a, b *protocol.NodeConfig) bool {
	if a.Port != b.Port || a.Cipher != b.Cipher || len(a.Users) != len(b.Users) {
		return false
	}
	for i := range a.Users {
		if a.Users[i] != b.Users[i] {
			return false
		}
	}
	return true
}

// PushStats collects Stats() from every running node and reports them to
// the panel, resetting each server's counters.
func (m *Manager) PushStats(ctx context.Context) error {
	m.mu.Lock()
	nodes := make([]*runningNode, 0, len(m.nodes))
	for _, rn := range m.nodes {
		nodes = append(nodes, rn)
	}
	m.mu.Unlock()

	for _, rn := range nodes {
		stats := rn.server.Stats()
		if len(stats.Users) == 0 {
			continue
		}
		records := make([]panel.TrafficRecord, 0, len(stats.Users))
		for uid, tr := range stats.Users {
			records = append(records, panel.TrafficRecord{UID: uid, Upload: tr.Upload, Download: tr.Download})
		}
		if err := m.client.PushTraffic(ctx, rn.entry.NodeID, rn.entry.NodeType, records); err != nil {
			m.logger.Warn("push traffic failed", "node_id", rn.entry.NodeID, "error", err)
		}
	}
	return nil
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rn := range m.nodes {
		if err := rn.server.Stop(); err != nil {
			m.logger.Warn("failed to stop node", "node_id", id, "error", err)
		}
	}
}
