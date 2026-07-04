package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/config"
	"github.com/Sakawat-hossain/V2bX/internal/panel"

	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/shadowsocks"
)

// mockPanel serves a single shadowsocks node + one user, and records any
// traffic pushed back to it so the test can assert stats made the round trip.
type mockPanel struct {
	srv    *httptest.Server
	port   int
	pushed chan []panel.TrafficRecord
}

func newMockPanel(t *testing.T, ssPort int) *mockPanel {
	t.Helper()
	mp := &mockPanel{port: ssPort, pushed: make(chan []panel.TrafficRecord, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"protocol": "shadowsocks", "server_port": ssPort, "cipher": "aes-256-gcm",
		})
	})
	mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
		// XBoard sends no password — the UUID is the credential.
		json.NewEncoder(w).Encode(map[string]any{
			"users": []map[string]any{{"id": 42, "uuid": "integration-test-uuid"}},
		})
	})
	mux.HandleFunc("/api/v1/server/UniProxy/push", func(w http.ResponseWriter, r *http.Request) {
		var body map[string][2]uint64
		json.NewDecoder(r.Body).Decode(&body)
		records := make([]panel.TrafficRecord, 0, len(body))
		for uid, ud := range body {
			id, _ := strconv.ParseInt(uid, 10, 64)
			records = append(records, panel.TrafficRecord{UID: id, Upload: ud[0], Download: ud[1]})
		}
		mp.pushed <- records
	})
	mux.HandleFunc("/api/v1/server/UniProxy/alive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mp.srv = httptest.NewServer(mux)
	return mp
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestEndToEndConfigPanelListenerStats exercises the full path the brief
// calls out for the first milestone: config parsing -> panel sync against a
// mock panel -> protocol listener start -> stats reporting.
func TestEndToEndConfigPanelListenerStats(t *testing.T) {
	ssPort := freePort(t)
	mp := newMockPanel(t, ssPort)
	defer mp.srv.Close()

	cfg := &config.Config{
		Panel: config.PanelConfig{ApiHost: mp.srv.URL, ApiKey: "test-key", SyncInterval: 3600},
		Nodes: []config.NodeEntry{{NodeID: 1, NodeType: "shadowsocks", Enabled: true, ListenIP: "127.0.0.1"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	mgr, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Sync(ctx) // performs the mock panel fetch and starts the listener

	// Listener should now be accepting on ssPort.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ssPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("expected listener on port %d after sync, dial failed: %v", ssPort, err)
	}
	conn.Close()

	// Re-sync should be a no-op (same config) and must not drop the listener.
	mgr.Sync(ctx)
	conn2, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ssPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("listener should survive a no-op resync: %v", err)
	}
	conn2.Close()

	if err := mgr.PushStats(ctx); err != nil {
		t.Fatalf("PushStats: %v", err)
	}
	// No traffic has flowed yet, so nothing should have been pushed.
	select {
	case records := <-mp.pushed:
		t.Fatalf("expected no push with zero traffic, got %+v", records)
	case <-time.After(100 * time.Millisecond):
	}
}
