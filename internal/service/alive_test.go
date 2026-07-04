package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/Sakawat-hossain/V2bX/internal/config"
)

// fakeOnline is a ProtocolServer that also reports online IPs.
type fakeOnline struct {
	fakeServer
	ips map[int64][]string
}

func (f *fakeOnline) Online() map[int64][]string { return f.ips }

func TestReportAlivePostsPanelFormat(t *testing.T) {
	var mu sync.Mutex
	var got map[string][]string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/UniProxy/alive", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewDecoder(r.Body).Decode(&got)
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

	fo := &fakeOnline{ips: map[int64][]string{42: {"1.2.3.4", "5.6.7.8"}}}
	mgr.nodes[1] = &runningNode{entry: cfg.Nodes[0], server: fo}

	if err := mgr.ReportAlive(context.Background()); err != nil {
		t.Fatalf("ReportAlive: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	ips := got["42"]
	sort.Strings(ips)
	if len(ips) != 2 || ips[0] != "1.2.3.4" || ips[1] != "5.6.7.8" {
		t.Fatalf("panel received alive = %v, want {42:[1.2.3.4 5.6.7.8]}", got)
	}
}
