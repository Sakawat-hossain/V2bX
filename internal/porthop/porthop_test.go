package porthop

import (
	"strings"
	"testing"
)

func TestParseRange(t *testing.T) {
	cases := []struct {
		in         string
		start, end int
		wantErr    bool
	}{
		{"20000-40000", 20000, 40000, false},
		{"20000:40000", 20000, 40000, false},
		{" 100 - 200 ", 100, 200, false},
		{"40000-20000", 0, 0, true}, // reversed
		{"0-100", 0, 0, true},       // below 1
		{"1-70000", 0, 0, true},     // above 65535
		{"notarange", 0, 0, true},
		{"1000", 0, 0, true},
	}
	for _, c := range cases {
		s, e, err := ParseRange(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseRange(%q) expected error", c.in)
			}
			continue
		}
		if err != nil || s != c.start || e != c.end {
			t.Errorf("ParseRange(%q) = %d,%d,%v; want %d,%d", c.in, s, e, err, c.start, c.end)
		}
	}
}

func TestRuleArgs(t *testing.T) {
	got := strings.Join(rule("-A", 20000, 40000, 443, "v2bx-node-5"), " ")
	want := "-t nat -A PREROUTING -p udp --dport 20000:40000 -j REDIRECT --to-ports 443 -m comment --comment v2bx-node-5"
	if got != want {
		t.Fatalf("rule args:\n got: %s\nwant: %s", got, want)
	}
	// -D must mirror -A exactly (same match spec) so removal finds the rule.
	del := strings.Join(rule("-D", 20000, 40000, 443, "v2bx-node-5"), " ")
	if strings.Replace(del, "-D", "-A", 1) != want {
		t.Fatalf("delete rule must mirror add rule: %s", del)
	}
}
