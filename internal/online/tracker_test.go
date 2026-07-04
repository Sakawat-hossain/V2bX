package online

import (
	"sort"
	"testing"
	"time"
)

func TestMarkAndSnapshotDedupes(t *testing.T) {
	var tr Tracker
	tr.Mark(1, "1.2.3.4")
	tr.Mark(1, "1.2.3.4") // same IP again -> still one
	tr.Mark(1, "5.6.7.8")
	tr.Mark(2, "9.9.9.9")

	snap := tr.Snapshot()
	got := snap[1]
	sort.Strings(got)
	if len(got) != 2 || got[0] != "1.2.3.4" || got[1] != "5.6.7.8" {
		t.Fatalf("user 1 IPs = %v, want [1.2.3.4 5.6.7.8]", got)
	}
	if len(snap[2]) != 1 || snap[2][0] != "9.9.9.9" {
		t.Fatalf("user 2 IPs = %v", snap[2])
	}
}

func TestMarkIgnoresAnonymousAndEmpty(t *testing.T) {
	var tr Tracker
	tr.Mark(0, "1.2.3.4") // unknown user
	tr.Mark(5, "")        // no IP
	if len(tr.Snapshot()) != 0 {
		t.Fatalf("expected nothing tracked, got %v", tr.Snapshot())
	}
}

func TestSnapshotExpiresStaleIPs(t *testing.T) {
	tr := Tracker{ttl: 20 * time.Millisecond}
	tr.Mark(1, "1.2.3.4")
	if len(tr.Snapshot()[1]) != 1 {
		t.Fatal("expected the IP present immediately")
	}
	time.Sleep(40 * time.Millisecond)
	if got := tr.Snapshot()[1]; len(got) != 0 {
		t.Fatalf("expected IP to expire, got %v", got)
	}
}

func TestIPExtraction(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4:555": "1.2.3.4",
		"[::1]:80":    "::1",
		"nohostport":  "nohostport",
	}
	for in, want := range cases {
		if got := IPString(in); got != want {
			t.Fatalf("IPString(%q) = %q, want %q", in, got, want)
		}
	}
}
