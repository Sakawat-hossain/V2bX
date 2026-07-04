package relay

import (
	"net"
	"testing"
	"time"
)

func TestLimitListenerZeroIsUnlimited(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	if LimitListener(ln, 0) != ln {
		t.Fatal("max<=0 should return the listener unchanged")
	}
}

func TestLimitListenerBlocksAtCap(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	limited := LimitListener(ln, 1)

	c1, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer c1.Close()
	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer c2.Close()

	a1, err := limited.Accept()
	if err != nil {
		t.Fatalf("accept 1: %v", err)
	}

	// The cap is 1, so a second accept must block until a1 is closed.
	accepted := make(chan net.Conn, 1)
	go func() {
		a2, err := limited.Accept()
		if err == nil {
			accepted <- a2
		}
	}()

	select {
	case <-accepted:
		t.Fatal("second accept should have blocked at cap 1")
	case <-time.After(200 * time.Millisecond):
	}

	a1.Close() // frees the slot
	select {
	case a2 := <-accepted:
		a2.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("second accept should proceed after the first closes")
	}
}
