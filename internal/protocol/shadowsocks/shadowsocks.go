// Package shadowsocks implements protocol.ProtocolServer for Shadowsocks,
// covering both classic AEAD ciphers (aes-*-gcm, chacha20-ietf-poly1305) and
// Shadowsocks-2022 (blake3-derived AEAD with multi-user EIH), backed by the
// sagernet/sing-shadowsocks codec library.
package shadowsocks

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	ss "github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/online"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/ratelimit"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
)

func init() {
	protocol.Register("shadowsocks", func() protocol.ProtocolServer { return New() })
}

var classicMethods = map[string]bool{
	"aes-128-gcm":            true,
	"aes-192-gcm":            true,
	"aes-256-gcm":            true,
	"chacha20-ietf-poly1305": true,
}

// method2022 covers the Shadowsocks-2022 ciphers the sing multi-user 2022
// service can build. Note: this codec's multi-user path supports only the two
// GCM variants — 2022-blake3-chacha20-poly1305 has no multi-user service here,
// so it is intentionally omitted (selecting it yields "unsupported cipher").
var method2022 = map[string]bool{
	"2022-blake3-aes-128-gcm": true,
	"2022-blake3-aes-256-gcm": true,
}

// Server is a Shadowsocks protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu         sync.Mutex
	listener   net.Listener
	packetConn net.PacketConn // UDP listener, for DNS/QUIC over Shadowsocks
	service    ss.MultiService[int64]
	cfg        protocol.NodeConfig
	logger     *slog.Logger

	counters sync.Map // int64 userID -> *userCounter
	online   online.Tracker
	limits   ratelimit.Store
}

// Online reports the source IPs each user is currently connected from.
func (s *Server) Online() map[int64][]string { return s.online.Snapshot() }

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

// New builds an unstarted Shadowsocks server.
func New() *Server {
	return &Server{logger: slog.Default().With("protocol", "shadowsocks")}
}

func (s *Server) Name() string { return "shadowsocks" }

// Start binds a TCP listener and begins serving connections for every user
// in cfg.Users, all sharing cfg.Cipher.
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener != nil {
		return fmt.Errorf("shadowsocks: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("shadowsocks: node %d has no users configured", cfg.NodeID)
	}

	service, err := newMultiService(cfg.Cipher, serverKey(cfg), s)
	if err != nil {
		return fmt.Errorf("shadowsocks: node %d: %w", cfg.NodeID, err)
	}

	userIDs := make([]int64, len(cfg.Users))
	passwords := make([]string, len(cfg.Users))
	for i, u := range cfg.Users {
		userIDs[i] = u.ID
		passwords[i] = u.Password
	}
	if err := service.UpdateUsersWithPasswords(userIDs, passwords); err != nil {
		return fmt.Errorf("shadowsocks: node %d: invalid user passwords: %w", cfg.NodeID, err)
	}

	addr := fmt.Sprintf("%s:%d", cfg.ListenIP, cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("shadowsocks: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	// Bind UDP on the same address so DNS/QUIC over Shadowsocks works. A
	// missing UDP listener is invisible to a TCP handshake — the client
	// "connects" but every UDP query (typically DNS) has nowhere to go, which
	// looks like "connected, no internet". Treat a UDP bind failure as
	// non-fatal so a TCP-only node still serves.
	udpAddr, uerr := net.ResolveUDPAddr("udp", addr)
	if uerr == nil {
		if pc, perr := net.ListenUDP("udp", udpAddr); perr == nil {
			s.packetConn = pc
			go s.udpLoop(bufio.NewPacketConn(pc))
		} else {
			s.logger.Warn("udp listen failed; node is TCP-only", "node_id", cfg.NodeID, "addr", addr, "error", perr)
		}
	}

	ln = relay.LimitListener(ln, cfg.MaxConnections)
	s.listener = ln
	s.service = service
	s.cfg = cfg
	s.limits.Update(cfg.Users)

	go s.acceptLoop(ln)
	s.logger.Info("started", "node_id", cfg.NodeID, "addr", addr, "cipher", cfg.Cipher, "users", len(cfg.Users))
	return nil
}

// udpLoop reads datagrams off the UDP listener and hands each to the sing
// service, whose UDP NAT decrypts it, extracts the real destination, and
// invokes NewPacketConnection for the association. Replies go back out pc.
func (s *Server) udpLoop(pc N.NetPacketConn) {
	for {
		buffer := buf.NewPacket()
		source, err := pc.ReadPacket(buffer)
		if err != nil {
			buffer.Release()
			return // listener closed
		}
		s.mu.Lock()
		svc := s.service
		s.mu.Unlock()
		if svc == nil {
			buffer.Release()
			continue
		}
		// NewPacket takes ownership of buffer (the NAT may hold it for the
		// session), so we don't release it here.
		metadata := M.Metadata{Source: source}
		if err := svc.NewPacket(context.Background(), pc, buffer, metadata); err != nil {
			s.logger.Debug("udp packet error", "error", err)
		}
	}
}

// serverKey extracts the node-level PSK (Shadowsocks-2022 `server_key`) the
// panel pushes down. Empty for classic AEAD ciphers, which don't use one.
func serverKey(cfg protocol.NodeConfig) string {
	if cfg.Extra == nil {
		return ""
	}
	if v, ok := cfg.Extra["server_key"].(string); ok {
		return v
	}
	return ""
}

func newMultiService(cipher, psk string, handler ss.Handler) (ss.MultiService[int64], error) {
	switch {
	case classicMethods[cipher]:
		return shadowaead.NewMultiService[int64](cipher, 300, handler)
	case method2022[cipher]:
		// Shadowsocks-2022 requires a base64 node-level PSK (the panel's
		// server_key). Without it the codec rejects the service with
		// "missing psk", so surface a clearer error pointing at the cause.
		if psk == "" {
			return nil, fmt.Errorf("cipher %q needs a server PSK — panel didn't send server_key", cipher)
		}
		return shadowaead_2022.NewMultiServiceWithPassword[int64](cipher, psk, 300, handler, time.Now)
	default:
		return nil, fmt.Errorf("unsupported cipher %q", cipher)
	}
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		// Load the current service per connection so a live user reload
		// (which swaps in a fresh service) takes effect for new connections
		// without racing the map that in-flight connections are reading.
		s.mu.Lock()
		svc := s.service
		s.mu.Unlock()
		if svc == nil {
			conn.Close()
			continue
		}
		go func() {
			defer conn.Close()
			ctx := context.Background()
			if err := svc.NewConnection(ctx, conn, M.Metadata{}); err != nil {
				s.logger.Debug("connection error", "error", err)
			}
		}()
	}
}

// Stop closes the listener. Safe to call multiple times.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	err := s.listener.Close()
	if s.packetConn != nil {
		s.packetConn.Close()
		s.packetConn = nil
	}
	s.listener = nil
	s.service = nil
	s.logger.Info("stopped", "node_id", s.cfg.NodeID)
	return err
}

// Stats returns cumulative per-user traffic totals (never reset).
func (s *Server) Stats() protocol.UsageStats {
	out := protocol.UsageStats{NodeID: s.cfg.NodeID, Users: map[int64]protocol.UserTraffic{}}
	s.counters.Range(func(key, value any) bool {
		id := key.(int64)
		c := value.(*userCounter)
		up := c.upload.Load()
		down := c.download.Load()
		if up != 0 || down != 0 {
			out.Users[id] = protocol.UserTraffic{Upload: up, Download: down}
		}
		return true
	})
	return out
}

// UpdateUsers swaps the live user set without closing the listener. Rather
// than mutate the running service's user map (which the sing codec reads
// without locking), it builds a fresh service and atomically swaps it in;
// in-flight connections keep serving on the old, now-immutable service.
func (s *Server) UpdateUsers(users []protocol.User) error {
	s.limits.Update(users)
	s.mu.Lock()
	cipher := s.cfg.Cipher
	psk := serverKey(s.cfg)
	running := s.service != nil
	s.mu.Unlock()
	if !running {
		return fmt.Errorf("shadowsocks: not started")
	}

	fresh, err := newMultiService(cipher, psk, s)
	if err != nil {
		return err
	}
	ids := make([]int64, len(users))
	passwords := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
		passwords[i] = u.Password
	}
	if err := fresh.UpdateUsersWithPasswords(ids, passwords); err != nil {
		return err
	}

	s.mu.Lock()
	s.service = fresh
	s.mu.Unlock()
	return nil
}

func (s *Server) counterFor(userID int64) *userCounter {
	v, _ := s.counters.LoadOrStore(userID, &userCounter{})
	return v.(*userCounter)
}

// NewConnection implements sing's N.TCPConnectionHandler: it is invoked by
// the shadowsocks codec once a client's stream has been decrypted and the
// requested destination address parsed out.
func (s *Server) NewConnection(ctx context.Context, conn net.Conn, metadata M.Metadata) error {
	userID, _ := auth.UserFromContext[int64](ctx)
	counter := s.counterFor(userID)
	s.online.Mark(userID, online.IP(conn.RemoteAddr()))

	upstream, err := net.DialTimeout("tcp", metadata.Destination.String(), 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial upstream %s: %w", metadata.Destination, err)
	}
	defer upstream.Close()

	up, down := relay.Pipe(conn, s.limits.Limit(userID, upstream))
	counter.upload.Add(up)
	counter.download.Add(down)
	return nil
}

// udpIdleTimeout bounds how long an idle upstream UDP socket lingers before it
// is reaped, so a client that opens an association and goes quiet doesn't leak
// sockets/goroutines.
const udpIdleTimeout = 60 * time.Second

// NewPacketConnection implements sing's N.UDPConnectionHandler with a full
// bidirectional UDP relay (NAT), not a single round trip. A client association
// can carry packets to many destinations (DNS resolvers, QUIC, game servers),
// so we keep one upstream UDP socket per distinct destination and pump replies
// back for the life of the association. The earlier single-shot version
// resolved one DNS query then closed — enough to show "connected" while real
// browsing (which needs repeated/QUIC UDP) got no data.
func (s *Server) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata M.Metadata) error {
	userID, _ := auth.UserFromContext[int64](ctx)
	counter := s.counterFor(userID)
	if metadata.Source.IsValid() {
		s.online.Mark(userID, metadata.Source.Addr.String())
	}

	var (
		mu      sync.Mutex
		writeMu sync.Mutex // serializes conn.WritePacket: the session writer's
		//                   packet-id counter and RNG are not concurrency-safe
		sockets = map[string]*net.UDPConn{}
		wg      sync.WaitGroup
	)
	defer func() {
		mu.Lock()
		for _, u := range sockets {
			u.Close()
		}
		mu.Unlock()
		wg.Wait()
	}()

	for {
		buffer := buf.NewSize(64 * 1024)
		dest, err := conn.ReadPacket(buffer)
		if err != nil {
			buffer.Release()
			return nil // association closed or timed out
		}

		key := dest.String()
		mu.Lock()
		up := sockets[key]
		if up == nil {
			addr, rErr := net.ResolveUDPAddr("udp", key)
			if rErr != nil {
				mu.Unlock()
				buffer.Release()
				continue
			}
			uc, dErr := net.DialUDP("udp", nil, addr)
			if dErr != nil {
				mu.Unlock()
				buffer.Release()
				continue
			}
			sockets[key] = uc
			up = uc
			wg.Add(1)
			// One reader goroutine per destination pumps replies back to the
			// client, tagged with the destination as the source address.
			go func(uc *net.UDPConn, d M.Socksaddr) {
				defer wg.Done()
				scratch := make([]byte, 64*1024)
				for {
					uc.SetReadDeadline(time.Now().Add(udpIdleTimeout))
					n, rErr := uc.Read(scratch)
					if n > 0 {
						// Reserve front headroom so the codec can prepend the
						// SS-2022 UDP header without a buffer overflow.
						reply := buf.NewPacket()
						reply.Resize(512, 0) // 512 bytes of front headroom, empty content
						reply.Write(scratch[:n])
						// WritePacket takes ownership of reply and releases it on
						// every path (success or error), so we must not release
						// it again here — doing so would double-free the pooled
						// buffer.
						writeMu.Lock()
						wErr := conn.WritePacket(reply, d)
						writeMu.Unlock()
						if wErr != nil {
							return
						}
						counter.download.Add(uint64(n))
					}
					if rErr != nil {
						return
					}
				}
			}(uc, dest)
		}
		mu.Unlock()

		n := buffer.Len()
		if _, err := up.Write(buffer.Bytes()); err == nil {
			counter.upload.Add(uint64(n))
		}
		buffer.Release()
	}
}

// NewError implements sing's E.Handler.
func (s *Server) NewError(ctx context.Context, err error) {
	s.logger.Debug("stream error", "error", err)
}
