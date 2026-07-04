package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sakawat-hossain/V2bX/internal/config"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// fakeServer is a protocol.ProtocolServer whose cumulative Stats() the test
// controls directly, so accounting can be exercised without real traffic.
type fakeServer struct {
	mu  sync.Mutex
	cum map[int64]protocol.UserTraffic
}

func (f *fakeServer) Start(protocol.NodeConfig) error { return nil }
func (f *fakeServer) Stop() error                     { return nil }
func (f *fakeServer) Name() string                    { return "fake" }
func (f *fakeServer) Stats() protocol.UsageStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	users := make(map[int64]protocol.UserTraffic, len(f.cum))
	for k, v := range f.cum {
		users[k] = v
	}
	return protocol.UsageStats{NodeID: 1, Users: users}
}
func (f *fakeServer) set(uid int64, up, down uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cum == nil {
		f.cum = map[int64]protocol.UserTraffic{}
	}
	f.cum[uid] = protocol.UserTraffic{Upload: up, Download: down}
}

// TestPushStatsDeltaSinceAckIsLossless verifies that a failed push does not
// drop traffic: the delta is retried in full on the next push, and a
// successful push is never re-counted.
func TestPushStatsDeltaSinceAckIsLossless(t *testing.T) {
	var fail atomic.Bool
	var mu sync.Mutex
	received := map[int64][2]uint64{} // uid -> cumulative [up, down] the panel actually recorded

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/UniProxy/push", func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var body map[string][2]uint64
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		for uid, ud := range body {
			id, _ := strconv.ParseInt(uid, 10, 64)
			cur := received[id]
			received[id] = [2]uint64{cur[0] + ud[0], cur[1] + ud[1]}
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &config.Config{
		Panel: config.PanelConfig{ApiHost: srv.URL, ApiKey: "k"},
		Nodes: []config.NodeEntry{{NodeID: 1, NodeType: "shadowsocks", Enabled: true}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	mgr, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	fs := &fakeServer{}
	mgr.nodes[1] = &runningNode{entry: cfg.Nodes[0], server: fs}
	ctx := context.Background()

	// First push: 100/200 accrued -> panel records it, ack advances.
	fs.set(42, 100, 200)
	mustPush(t, mgr, ctx)
	assertReceived(t, &mu, received, 42, 100, 200)

	// More traffic while the panel is DOWN: push fails, nothing acked.
	fs.set(42, 150, 260)
	fail.Store(true)
	mustPush(t, mgr, ctx)
	assertReceived(t, &mu, received, 42, 100, 200) // unchanged — not lost, just not delivered

	// Panel recovers: the full 50/60 delta is resent, so the panel's total
	// catches up to the cumulative 150/260 with no loss and no double-count.
	fail.Store(false)
	mustPush(t, mgr, ctx)
	assertReceived(t, &mu, received, 42, 150, 260)

	// No new traffic -> nothing pushed (no double counting).
	mustPush(t, mgr, ctx)
	assertReceived(t, &mu, received, 42, 150, 260)
}

func mustPush(t *testing.T, mgr *Manager, ctx context.Context) {
	t.Helper()
	if err := mgr.PushStats(ctx); err != nil {
		t.Fatalf("PushStats: %v", err)
	}
}

func assertReceived(t *testing.T, mu *sync.Mutex, received map[int64][2]uint64, uid int64, up, down uint64) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	got := received[uid]
	if got[0] != up || got[1] != down {
		t.Fatalf("panel total for user %d = %v, want [%d %d]", uid, got, up, down)
	}
}
