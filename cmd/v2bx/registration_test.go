package main

import (
	"sort"
	"testing"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// TestAllProtocolsRegistered guards against a protocol package being added
// without its blank import here (or vice versa), which would silently make a
// node type unusable at runtime.
func TestAllProtocolsRegistered(t *testing.T) {
	want := []string{
		"anytls", "http", "hysteria", "hysteria2", "mieru", "naive",
		"shadowsocks", "socks5", "trojan", "tuic", "vless", "vmess",
	}
	got := protocol.Known()
	sort.Strings(got)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("registered protocols = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered protocols = %v, want %v", got, want)
		}
	}
}
