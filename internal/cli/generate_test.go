package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Sakawat-hossain/V2bX/internal/config"
)

// feedStdin replaces os.Stdin with a pipe pre-loaded with answers and
// silences os.Stdout for the duration of fn, restoring both afterward.
func feedStdin(t *testing.T, answers string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(answers); err != nil {
		t.Fatalf("write answers: %v", err)
	}
	w.Close()

	devnull, _ := os.Open(os.DevNull)
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = r, devnull
	defer func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		r.Close()
		if devnull != nil {
			devnull.Close()
		}
	}()
	fn()
}

func TestGenerateWritesValidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	// panel URL, key, then a shadowsocks node (no TLS) and a trojan node
	// (TLS with an explicit cert), then stop.
	answers := "https://panel.example.com\nsecret\n" +
		"1\n1\n\n\n\ny\n" + // node 1: id, protocol=shadowsocks, listen, TLS=no, enable, add another
		"2\ntrojan\n\n/tmp/c.crt\n/tmp/c.key\n\nn\n" // node 2: id, protocol=trojan, listen, certfile, keyfile, enable, stop

	feedStdin(t, answers, func() {
		if err := Generate(path); err != nil {
			t.Fatalf("Generate: %v", err)
		}
	})

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("generated config failed to load: %v", err)
	}
	if cfg.Panel.ApiHost != "https://panel.example.com" || cfg.Panel.ApiKey != "secret" {
		t.Fatalf("panel not captured: %+v", cfg.Panel)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(cfg.Nodes))
	}
	if cfg.Nodes[0].NodeType != "shadowsocks" || cfg.Nodes[1].NodeType != "trojan" {
		t.Fatalf("unexpected node types: %+v", cfg.Nodes)
	}
	if cfg.Nodes[1].CertMode != "self" || cfg.Nodes[1].CertFile == "" || cfg.Nodes[1].KeyFile == "" {
		t.Fatalf("trojan node missing cert: %+v", cfg.Nodes[1])
	}
}
