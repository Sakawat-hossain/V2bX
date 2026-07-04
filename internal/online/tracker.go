// Package online tracks which source IPs each user is currently connected
// from, so the agent can report device presence to the panel. The panel
// aggregates these across all nodes and enforces device/IP limits centrally;
// the node's only job is accurate reporting.
package online

import (
	"net"
	"sync"
	"time"
)

// DefaultTTL is how long an IP is considered "present" after it was last
// seen. A connection re-marks its IP as it's used, so an actively proxying
// device stays present; one idle past the TTL drops off.
const DefaultTTL = 2 * time.Minute

// Tracker records the source IPs seen per user. The zero value is usable and
// uses DefaultTTL. It is safe for concurrent use.
type Tracker struct {
	ttl  time.Duration
	mu   sync.Mutex
	seen map[int64]map[string]time.Time // userID -> ip -> last seen
}

func (t *Tracker) window() time.Duration {
	if t.ttl > 0 {
		return t.ttl
	}
	return DefaultTTL
}

// Mark records that userID is connected from ip right now. Unknown users
// (id 0) and empty IPs are ignored.
func (t *Tracker) Mark(userID int64, ip string) {
	if userID == 0 || ip == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = make(map[int64]map[string]time.Time)
	}
	ips := t.seen[userID]
	if ips == nil {
		ips = make(map[string]time.Time)
		t.seen[userID] = ips
	}
	ips[ip] = time.Now()
}

// Snapshot returns the distinct, non-expired IPs each user is currently
// connected from, pruning stale entries as it goes.
func (t *Tracker) Snapshot() map[int64][]string {
	cutoff := time.Now().Add(-t.window())
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make(map[int64][]string, len(t.seen))
	for uid, ips := range t.seen {
		for ip, last := range ips {
			if last.Before(cutoff) {
				delete(ips, ip)
				continue
			}
			out[uid] = append(out[uid], ip)
		}
		if len(ips) == 0 {
			delete(t.seen, uid)
		}
	}
	return out
}

// IP extracts the host portion of a net.Addr (dropping the port).
func IP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return IPString(addr.String())
}

// IPString extracts the host portion of a "host:port" string.
func IPString(hostPort string) string {
	if hostPort == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(hostPort); err == nil {
		return host
	}
	return hostPort
}
