package service

import (
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/Sakawat-hossain/V2bX/internal/config"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// fakeUpdatable is a ProtocolServer that also implements UserUpdater and
// counts how it was driven, so we can assert the manager hot-reloads instead
// of restarting.
type fakeUpdatable struct {
	mu                     sync.Mutex
	starts, stops, updates int
	updateErr              error
}

func (f *fakeUpdatable) Start(protocol.NodeConfig) error {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	return nil
}
func (f *fakeUpdatable) Stop() error {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
	return nil
}
func (f *fakeUpdatable) Name() string { return "fake" }
func (f *fakeUpdatable) Stats() protocol.UsageStats {
	return protocol.UsageStats{Users: map[int64]protocol.UserTraffic{}}
}
func (f *fakeUpdatable) UpdateUsers([]protocol.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	return f.updateErr
}

func newTestManager() *Manager {
	return &Manager{
		logger: slog.Default(),
		nodes:  map[int64]*runningNode{},
		acked:  map[int64]map[int64]protocol.UserTraffic{},
	}
}

func TestUserOnlyChangeHotReloads(t *testing.T) {
	mgr := newTestManager()
	entry := config.NodeEntry{NodeID: 1, NodeType: "shadowsocks", Enabled: true}
	v1 := &protocol.NodeConfig{NodeID: 1, Port: 1000, Cipher: "aes-128-gcm", Users: []protocol.User{{ID: 1, Password: "a"}}}
	fake := &fakeUpdatable{}
	mgr.nodes[1] = &runningNode{entry: entry, server: fake, lastGood: v1}

	// Only the user set changed → update in place, no restart.
	v2 := &protocol.NodeConfig{NodeID: 1, Port: 1000, Cipher: "aes-128-gcm", Users: []protocol.User{{ID: 1, Password: "a"}, {ID: 2, Password: "b"}}}
	mgr.applyNodeConfig(entry, v2)

	if fake.updates != 1 || fake.starts != 0 || fake.stops != 0 {
		t.Fatalf("user-only change: got updates=%d starts=%d stops=%d, want 1/0/0", fake.updates, fake.starts, fake.stops)
	}
	if mgr.nodes[1].lastGood != v2 {
		t.Fatal("lastGood not advanced after hot reload")
	}
}

func TestListenerChangeRestarts(t *testing.T) {
	mgr := newTestManager()
	entry := config.NodeEntry{NodeID: 1, NodeType: "shadowsocks", Enabled: true}
	v1 := &protocol.NodeConfig{NodeID: 1, Port: 1000, Cipher: "aes-128-gcm", Users: []protocol.User{{ID: 1, Password: "a"}}}
	fake := &fakeUpdatable{}
	mgr.nodes[1] = &runningNode{entry: entry, server: fake, lastGood: v1}

	// Port changed → must rebind, i.e. restart.
	v2 := &protocol.NodeConfig{NodeID: 1, Port: 2000, Cipher: "aes-128-gcm", Users: []protocol.User{{ID: 1, Password: "a"}}}
	mgr.applyNodeConfig(entry, v2)

	if fake.stops != 1 || fake.starts != 1 || fake.updates != 0 {
		t.Fatalf("listener change: got updates=%d starts=%d stops=%d, want 0/1/1", fake.updates, fake.starts, fake.stops)
	}
}

func TestHotReloadFallsBackToRestartOnError(t *testing.T) {
	mgr := newTestManager()
	entry := config.NodeEntry{NodeID: 1, NodeType: "shadowsocks", Enabled: true}
	v1 := &protocol.NodeConfig{NodeID: 1, Port: 1000, Cipher: "aes-128-gcm", Users: []protocol.User{{ID: 1, Password: "a"}}}
	fake := &fakeUpdatable{updateErr: errors.New("core cannot reload")}
	mgr.nodes[1] = &runningNode{entry: entry, server: fake, lastGood: v1}

	v2 := &protocol.NodeConfig{NodeID: 1, Port: 1000, Cipher: "aes-128-gcm", Users: []protocol.User{{ID: 1, Password: "a"}, {ID: 2, Password: "b"}}}
	mgr.applyNodeConfig(entry, v2)

	// UpdateUsers attempted, failed, then restarted.
	if fake.updates != 1 || fake.stops != 1 || fake.starts != 1 {
		t.Fatalf("error fallback: got updates=%d starts=%d stops=%d, want 1/1/1", fake.updates, fake.starts, fake.stops)
	}
}
