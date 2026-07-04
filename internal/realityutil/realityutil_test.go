package realityutil

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

func genPrivKey(t *testing.T) string {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(k.Bytes())
}

func TestDecodeShortID(t *testing.T) {
	cases := []struct {
		in      string
		want    [8]byte
		wantErr bool
	}{
		{"", [8]byte{}, false},
		{"01ab", [8]byte{0x01, 0xab}, false},
		{"0123456789abcdef", [8]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}, false},
		{"abc", [8]byte{}, true},                // odd length
		{"0123456789abcdef01", [8]byte{}, true}, // too long
		{"zz", [8]byte{}, true},                 // not hex
	}
	for _, c := range cases {
		got, err := decodeShortID(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("decodeShortID(%q) expected error", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("decodeShortID(%q) = %v,%v; want %v", c.in, got, err, c.want)
		}
	}
}

func TestDecodeKeyRejectsBadLength(t *testing.T) {
	if _, err := decodeKey(base64.RawURLEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
	if _, err := decodeKey("not base64 @@@"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
	if _, err := decodeKey(genPrivKey(t)); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
}

func TestBuildServerConfigFailsClosed(t *testing.T) {
	good := &protocol.RealityConfig{
		Dest:        "www.example.com:443",
		ServerNames: []string{"www.example.com"},
		PrivateKey:  genPrivKey(t),
		ShortIDs:    []string{"", "01ab"},
	}
	cfg, err := BuildServerConfig(good)
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if !cfg.ServerNames["www.example.com"] || len(cfg.PrivateKey) != 32 {
		t.Fatalf("config not populated: %+v", cfg)
	}
	if !cfg.ShortIds[[8]byte{}] || !cfg.ShortIds[[8]byte{0x01, 0xab}] {
		t.Fatal("short ids not populated")
	}

	// Each missing required field must be rejected.
	for _, mut := range []func(*protocol.RealityConfig){
		func(r *protocol.RealityConfig) { r.Dest = "" },
		func(r *protocol.RealityConfig) { r.ServerNames = nil },
		func(r *protocol.RealityConfig) { r.PrivateKey = "" },
	} {
		bad := *good
		mut(&bad)
		if _, err := BuildServerConfig(&bad); err == nil {
			t.Errorf("expected fail-closed error for %+v", bad)
		}
	}
}
