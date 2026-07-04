package ratelimit

import (
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

func TestStoreRoutesLimitedVsUnlimited(t *testing.T) {
	var s Store
	s.Update([]protocol.User{
		{ID: 1, SpeedLimit: 0},      // unlimited
		{ID: 2, SpeedLimit: 100000}, // capped
	})

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	if got := s.Limit(1, a); got != a {
		t.Fatal("unlimited user should get the raw connection back")
	}
	if _, ok := s.Limit(2, a).(*limitedConn); !ok {
		t.Fatal("capped user should get a rate-limited wrapper")
	}
	// A user not in the store is unlimited.
	if got := s.Limit(99, a); got != a {
		t.Fatal("unknown user should be unlimited")
	}
}

func TestUpdateReRatesInPlace(t *testing.T) {
	var s Store
	s.Update([]protocol.User{{ID: 1, SpeedLimit: 100000}})
	first := s.limiters[1]
	s.Update([]protocol.User{{ID: 1, SpeedLimit: 200000}})
	if s.limiters[1] != first {
		t.Fatal("limiter should be re-rated in place, not replaced")
	}
	if s.limiters[1].Limit() != rate.Limit(200000) {
		t.Fatalf("rate not updated: %v", s.limiters[1].Limit())
	}
	// Dropping the user removes the limiter.
	s.Update(nil)
	if _, ok := s.limiters[1]; ok {
		t.Fatal("removed user's limiter should be gone")
	}
}

func TestLimitedConnThrottles(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go io.Copy(io.Discard, b) // drain the peer so writes proceed

	// 100 KB/s with a 64 KB burst.
	lc := &limitedConn{Conn: a, lim: rate.NewLimiter(100*1024, 64*1024)}

	data := make([]byte, 200*1024) // 200 KB
	start := time.Now()
	if _, err := lc.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	elapsed := time.Since(start)

	// After the 64 KB burst, ~136 KB remains at 100 KB/s ≈ 1.36s. Allow slack
	// but require clear evidence of throttling.
	if elapsed < 700*time.Millisecond {
		t.Fatalf("expected throttling; wrote 200KB in %v", elapsed)
	}
}
