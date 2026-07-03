// Package relay provides the bidirectional TCP byte-pump shared by every
// protocol backend that terminates a client connection and forwards it to
// an upstream destination.
package relay

import (
	"io"
	"net"
	"sync"
)

// Pipe copies bytes between client and upstream until both directions are
// done, returning bytes sent client->upstream (up) and upstream->client
// (down). Closing the write side of one connection after its copy finishes
// unblocks the other, so a half-closed connection can't hang the relay.
func Pipe(client, upstream net.Conn) (up, down uint64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(upstream, client)
		up = uint64(n)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(client, upstream)
		down = uint64(n)
		closeWrite(client)
	}()
	wg.Wait()
	return
}

func closeWrite(conn net.Conn) {
	type writeCloser interface {
		CloseWrite() error
	}
	if wc, ok := conn.(writeCloser); ok {
		wc.CloseWrite()
		return
	}
	conn.Close()
}
