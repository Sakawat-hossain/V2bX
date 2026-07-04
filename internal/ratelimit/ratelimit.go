// Package ratelimit throttles each user's throughput to their configured
// speed limit, shared across all of that user's connections (so opening more
// connections doesn't multiply their bandwidth).
package ratelimit

import (
	"context"
	"net"
	"sync"

	"golang.org/x/time/rate"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
)

// minBurst is the token-bucket burst floor, so a single Read/Write chunk
// never exceeds the burst (which would make WaitN fail).
const minBurst = 128 * 1024

// Store holds one shared rate limiter per user. The zero value is ready to
// use and safe for concurrent access.
type Store struct {
	mu       sync.RWMutex
	limiters map[int64]*rate.Limiter
}

// Update sets each user's byte/sec cap from the user list. A zero limit means
// unlimited. Existing limiters are re-rated in place (so throttling on
// in-flight connections continues seamlessly) and users no longer present
// are dropped.
func (s *Store) Update(users []protocol.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limiters == nil {
		s.limiters = make(map[int64]*rate.Limiter)
	}
	seen := make(map[int64]bool, len(users))
	for _, u := range users {
		seen[u.ID] = true
		if u.SpeedLimit == 0 {
			delete(s.limiters, u.ID)
			continue
		}
		burst := int(u.SpeedLimit)
		if burst < minBurst {
			burst = minBurst
		}
		if lim, ok := s.limiters[u.ID]; ok {
			lim.SetLimit(rate.Limit(u.SpeedLimit))
			lim.SetBurst(burst)
		} else {
			s.limiters[u.ID] = rate.NewLimiter(rate.Limit(u.SpeedLimit), burst)
		}
	}
	for id := range s.limiters {
		if !seen[id] {
			delete(s.limiters, id)
		}
	}
}

// Limit wraps conn so the user's traffic through it is throttled to their
// limit. If the user is unlimited, conn is returned unchanged. The same
// limiter instance is shared across all of a user's connections.
func (s *Store) Limit(userID int64, conn net.Conn) net.Conn {
	s.mu.RLock()
	lim := s.limiters[userID]
	s.mu.RUnlock()
	if lim == nil {
		return conn
	}
	return &limitedConn{Conn: conn, lim: lim}
}

type limitedConn struct {
	net.Conn
	lim *rate.Limiter
}

// CloseWrite forwards to the underlying connection's half-close so callers
// like relay.Pipe can signal EOF in one direction without tearing down the
// other. Without this, the embedded net.Conn's CloseWrite would be hidden by
// the wrapper and the relay would fall back to a full close.
func (c *limitedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

func (c *limitedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		_ = c.lim.WaitN(context.Background(), n)
	}
	return n, err
}

func (c *limitedConn) Write(p []byte) (int, error) {
	burst := c.lim.Burst()
	written := 0
	for written < len(p) {
		chunk := len(p) - written
		if chunk > burst {
			chunk = burst
		}
		if err := c.lim.WaitN(context.Background(), chunk); err != nil {
			return written, err
		}
		n, err := c.Conn.Write(p[written : written+chunk])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
