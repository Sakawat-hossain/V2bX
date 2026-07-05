// Package relay provides the bidirectional TCP byte-pump shared by every
// protocol backend that terminates a client connection and forwards it to
// an upstream destination.
package relay

import (
	"io"
	"net"
	"sync"

	"golang.org/x/net/netutil"
)

// bufferSize is the per-direction copy buffer size. 32 KiB matches io.Copy's
// default and is large enough to keep throughput high without wasting memory
// per connection.
const bufferSize = 32 * 1024

// bufPool recycles copy buffers so a burst of connections doesn't churn the
// garbage collector allocating and freeing 32 KiB buffers per direction.
var bufPool = sync.Pool{New: func() any { b := make([]byte, bufferSize); return &b }}

// Pipe copies bytes between client and upstream until both directions are
// done, returning bytes sent client->upstream (up) and upstream->client
// (down). Closing the write side of one connection after its copy finishes
// unblocks the other, so a half-closed connection can't hang the relay.
func Pipe(client, upstream net.Conn) (up, down uint64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		up = copyBuf(upstream, client)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		down = copyBuf(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
	return
}

// copyBuf is io.Copy with a pooled fallback buffer. io.CopyBuffer still
// prefers the kernel splice/sendfile fast path when both conns are raw TCP
// (in which case the buffer is untouched and nothing is copied in userspace);
// the pooled buffer only kicks in for the cases that actually allocate — TLS
// and our rate-limited/decrypted wrapper conns — where it avoids churning a
// fresh 32 KiB buffer per connection.
func copyBuf(dst, src net.Conn) uint64 {
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	n, _ := io.CopyBuffer(dst, src, *bp)
	return uint64(n)
}

// LimitListener caps the number of concurrent connections a listener will
// accept; further accepts block until one closes. max <= 0 returns ln
// unchanged (unlimited). This bounds goroutine/fd growth under a connection
// flood.
func LimitListener(ln net.Listener, max int) net.Listener {
	if max <= 0 {
		return ln
	}
	return netutil.LimitListener(ln, max)
}

// closeWrite half-closes the write side of conn so the peer sees EOF in one
// direction while the other keeps flowing. Codec wrappers (e.g. the sing
// Shadowsocks serverConn) don't expose CloseWrite themselves, so we unwrap
// through their Upstream() chain to reach the underlying *net.TCPConn, whose
// CloseWrite does a real half-close. Only if no layer offers CloseWrite do we
// fall back to a full Close — which would otherwise truncate a still-active
// transfer in the opposite direction.
func closeWrite(conn net.Conn) {
	type writeCloser interface{ CloseWrite() error }
	type upstreamer interface{ Upstream() any }
	for {
		if wc, ok := conn.(writeCloser); ok {
			wc.CloseWrite()
			return
		}
		u, ok := conn.(upstreamer)
		if !ok {
			break
		}
		next, ok := u.Upstream().(net.Conn)
		if !ok {
			break
		}
		conn = next
	}
	conn.Close()
}
