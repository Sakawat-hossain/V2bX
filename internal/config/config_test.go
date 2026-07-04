package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestRealityFailsClosed(t *testing.T) {
	base := `{
		"panel": {"api_host": "https://p.example.com", "api_key": "k"},
		"nodes": [{"node_id": 1, "node_type": "vless", "enabled": true, "reality": %s}]
	}`
	// Missing required fields must be rejected.
	for _, reality := range []string{
		`{"server_names":["a.com"],"private_key":"x"}`,  // no dest
		`{"dest":"a.com:443","private_key":"x"}`,        // no server_names
		`{"dest":"a.com:443","server_names":["a.com"]}`, // no private_key
	} {
		if _, err := Load(writeTemp(t, fmt.Sprintf(base, reality))); err == nil {
			t.Errorf("expected fail-closed error for reality %s", reality)
		}
	}
	// A complete Reality block loads.
	full := `{"dest":"a.com:443","server_names":["a.com"],"private_key":"AAAA"}`
	if _, err := Load(writeTemp(t, fmt.Sprintf(base, full))); err != nil {
		t.Fatalf("complete reality config rejected: %v", err)
	}
	// Reality on a non-vless node is rejected.
	nonVless := `{
		"panel": {"api_host": "https://p.example.com", "api_key": "k"},
		"nodes": [{"node_id": 1, "node_type": "trojan", "enabled": true, "reality": {"dest":"a.com:443","server_names":["a.com"],"private_key":"AAAA"}}]
	}`
	if _, err := Load(writeTemp(t, nonVless)); err == nil {
		t.Fatal("expected error: reality only on vless")
	}
}

func TestLoadValidConfig(t *testing.T) {
	path := writeTemp(t, `{
		"log": {"level": "debug", "output": "stdout"},
		"panel": {"api_host": "https://panel.example.com", "api_key": "secret", "sync_interval_seconds": 30},
		"nodes": [{"node_id": 1, "node_type": "shadowsocks", "enabled": true}]
	}`)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Panel.ConfigPath != "/api/v1/server/UniProxy/config" {
		t.Fatalf("expected default config path filled in, got %q", c.Panel.ConfigPath)
	}
	if c.Nodes[0].NodeType != "shadowsocks" {
		t.Fatalf("unexpected node type: %+v", c.Nodes[0])
	}
}

func TestLoadMissingApiHost(t *testing.T) {
	path := writeTemp(t, `{
		"panel": {"api_key": "secret"},
		"nodes": [{"node_id": 1, "node_type": "shadowsocks"}]
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for missing api_host")
	}
}

func TestLoadInvalidNodeType(t *testing.T) {
	path := writeTemp(t, `{
		"panel": {"api_host": "https://panel.example.com", "api_key": "secret"},
		"nodes": [{"node_id": 1, "node_type": "not-a-real-protocol"}]
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid node_type")
	}
}

func TestLoadDuplicateNodeID(t *testing.T) {
	path := writeTemp(t, `{
		"panel": {"api_host": "https://panel.example.com", "api_key": "secret"},
		"nodes": [
			{"node_id": 1, "node_type": "shadowsocks"},
			{"node_id": 1, "node_type": "vmess"}
		]
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for duplicate node_id")
	}
}

func TestLoadSelfCertWithoutFilesIsAllowed(t *testing.T) {
	// cert_file/key_file are optional now — a self-signed cert is generated
	// at runtime when they're omitted.
	path := writeTemp(t, `{
		"panel": {"api_host": "https://panel.example.com", "api_key": "secret"},
		"nodes": [{"node_id": 1, "node_type": "trojan", "cert_mode": "self"}]
	}`)
	if _, err := Load(path); err != nil {
		t.Fatalf("self cert_mode without files should be allowed: %v", err)
	}
}

func TestLoadNoNodes(t *testing.T) {
	path := writeTemp(t, `{
		"panel": {"api_host": "https://panel.example.com", "api_key": "secret"},
		"nodes": []
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for empty nodes list")
	}
}
