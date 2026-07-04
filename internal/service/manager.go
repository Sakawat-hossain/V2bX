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
	"sync/atomic"
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
	// acked[nodeID][userID] is the cumulative traffic total the panel has
	// confirmed. Deltas are computed against it and it only advances after a
	// successful push, so a failed report is retried in full rather than lost.
	acked map[int64]map[int64]protocol.UserTraffic

	// Counters surfaced via the metrics endpoint.
	pushOK, pushFail atomic.Uint64
	syncOK, syncFail atomic.Uint64
}

// Snapshot is a point-in-time view of the agent's state for metrics.
type Snapshot struct {
	PushOK, PushFail uint64
	SyncOK, SyncFail uint64
	Nodes            []NodeSnapshot
}

// NodeSnapshot is the per-node slice of a Snapshot.
type NodeSnapshot struct {
	NodeID           int64
	NodeType         string
	Users            int
	Online           int
	Upload, Download uint64
}

// Snapshot reads live per-node state (user counts, online IPs, cumulative
// traffic) plus the panel push/sync counters.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	nodes := make([]*runningNode, 0, len(m.nodes))
	for _, rn := range m.nodes {
		nodes = append(nodes, rn)
	}
	m.mu.Unlock()

	snap := Snapshot{
		PushOK:   m.pushOK.Load(),
		PushFail: m.pushFail.Load(),
		SyncOK:   m.syncOK.Load(),
		SyncFail: m.syncFail.Load(),
	}
	for _, rn := range nodes {
		ns := NodeSnapshot{NodeID: rn.entry.NodeID, NodeType: rn.entry.NodeType}
		if rn.lastGood != nil {
			ns.Users = len(rn.lastGood.Users)
		}
		if r, ok := rn.server.(protocol.OnlineReporter); ok {
			ns.Online = len(r.Online())
		}
		for _, tr := range rn.server.Stats().Users {
			ns.Upload += tr.Upload
			ns.Download += tr.Download
		}
		snap.Nodes = append(snap.Nodes, ns)
	}
	return snap
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
		acked:  map[int64]map[int64]protocol.UserTraffic{},
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
		m.syncFail.Add(1)
		m.logger.Warn("keeping last-known-good config after sync failure", "node_id", entry.NodeID, "error", err)
		return
	}
	m.syncOK.Add(1)

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
	if exists && rn.lastGood != nil {
		switch {
		case configEqual(rn.lastGood, nc):
			return // nothing changed
		case listenerEqual(rn.lastGood, nc):
			// Only the user set changed. Update it in place if the backend
			// can, so active connections aren't dropped; fall back to a
			// restart otherwise.
			if uu, ok := rn.server.(protocol.UserUpdater); ok {
				if err := uu.UpdateUsers(nc.Users); err != nil {
					m.logger.Warn("live user reload failed, restarting node", "node_id", entry.NodeID, "error", err)
					break
				}
				rn.lastGood = nc
				m.logger.Info("reloaded users", "node_id", entry.NodeID, "users", len(nc.Users))
				return
			}
		}
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

// listenerEqual reports whether the listener-level config (everything that
// requires rebinding the socket) is unchanged between a and b. When true, a
// difference must be purely in the user set.
func listenerEqual(a, b *protocol.NodeConfig) bool {
	return a.Port == b.Port && a.Cipher == b.Cipher && a.TLS == b.TLS
}

func configEqual(a, b *protocol.NodeConfig) bool {
	if !listenerEqual(a, b) || len(a.Users) != len(b.Users) {
		return false
	}
	for i := range a.Users {
		if a.Users[i] != b.Users[i] {
			return false
		}
	}
	return true
}

// PushStats reports the traffic accrued since the last acknowledged push for
// every running node. Protocol servers keep cumulative counters; here we
// diff against the last-acked totals and advance them only on a successful
// push, so a transient panel failure is retried in full on the next tick
// instead of silently dropping traffic.
func (m *Manager) PushStats(ctx context.Context) error {
	m.mu.Lock()
	nodes := make([]*runningNode, 0, len(m.nodes))
	for _, rn := range m.nodes {
		nodes = append(nodes, rn)
	}
	m.mu.Unlock()

	for _, rn := range nodes {
		nodeID := rn.entry.NodeID
		stats := rn.server.Stats()

		m.mu.Lock()
		acked := m.acked[nodeID]
		if acked == nil {
			acked = map[int64]protocol.UserTraffic{}
		}
		next := make(map[int64]protocol.UserTraffic, len(stats.Users))
		records := make([]panel.TrafficRecord, 0, len(stats.Users))
		for uid, cur := range stats.Users {
			next[uid] = cur
			prev := acked[uid]
			// Counters are monotonic; if a server restarted mid-run they can
			// reset, so treat cur < prev as "start fresh from cur".
			up := delta(cur.Upload, prev.Upload)
			down := delta(cur.Download, prev.Download)
			if up != 0 || down != 0 {
				records = append(records, panel.TrafficRecord{UID: uid, Upload: up, Download: down})
			}
		}
		m.mu.Unlock()

		if len(records) == 0 {
			continue
		}
		if err := m.client.PushTraffic(ctx, nodeID, rn.entry.NodeType, records); err != nil {
			// Leave acked untouched so this delta is resent next tick.
			m.pushFail.Add(1)
			m.logger.Warn("push traffic failed, will retry", "node_id", nodeID, "error", err)
			continue
		}
		m.pushOK.Add(1)
		// Push confirmed — advance the acknowledged totals.
		m.mu.Lock()
		m.acked[nodeID] = next
		m.mu.Unlock()
	}
	return nil
}

// delta returns cur-prev, or cur if the counter appears to have reset
// (cur < prev), avoiding uint64 underflow.
func delta(cur, prev uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// ReportAlive tells the panel which source IPs each user is currently
// connected from, per node, so it can enforce device/IP limits. Reporting
// keeps the node shown as online too, alongside the /user heartbeat.
func (m *Manager) ReportAlive(ctx context.Context) error {
	m.mu.Lock()
	nodes := make([]*runningNode, 0, len(m.nodes))
	for _, rn := range m.nodes {
		nodes = append(nodes, rn)
	}
	m.mu.Unlock()

	for _, rn := range nodes {
		reporter, ok := rn.server.(protocol.OnlineReporter)
		if !ok {
			continue
		}
		records := make([]panel.AliveRecord, 0)
		for uid, ips := range reporter.Online() {
			for _, ip := range ips {
				records = append(records, panel.AliveRecord{UID: uid, IP: ip})
			}
		}
		if len(records) == 0 {
			continue
		}
		if err := m.client.ReportAlive(ctx, rn.entry.NodeID, rn.entry.NodeType, records); err != nil {
			m.logger.Warn("report alive failed", "node_id", rn.entry.NodeID, "error", err)
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
