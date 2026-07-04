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
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
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

var method2022 = map[string]bool{
	"2022-blake3-aes-128-gcm":       true,
	"2022-blake3-aes-256-gcm":       true,
	"2022-blake3-chacha20-poly1305": true,
}

// Server is a Shadowsocks protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	service  ss.MultiService[int64]
	cfg      protocol.NodeConfig
	logger   *slog.Logger

	counters sync.Map // int64 userID -> *userCounter
}

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

	service, err := newMultiService(cfg.Cipher, s)
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

	s.listener = ln
	s.service = service
	s.cfg = cfg

	go s.acceptLoop(ln)
	s.logger.Info("started", "node_id", cfg.NodeID, "addr", addr, "cipher", cfg.Cipher, "users", len(cfg.Users))
	return nil
}

func newMultiService(cipher string, handler ss.Handler) (ss.MultiService[int64], error) {
	switch {
	case classicMethods[cipher]:
		return shadowaead.NewMultiService[int64](cipher, 300, handler)
	case method2022[cipher]:
		return shadowaead_2022.NewMultiServiceWithPassword[int64](cipher, "", 300, handler, time.Now)
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
	s.mu.Lock()
	cipher := s.cfg.Cipher
	running := s.service != nil
	s.mu.Unlock()
	if !running {
		return fmt.Errorf("shadowsocks: not started")
	}

	fresh, err := newMultiService(cipher, s)
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

	upstream, err := net.DialTimeout("tcp", metadata.Destination.String(), 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial upstream %s: %w", metadata.Destination, err)
	}
	defer upstream.Close()

	up, down := relay.Pipe(conn, upstream)
	counter.upload.Add(up)
	counter.download.Add(down)
	return nil
}

// NewPacketConnection implements sing's N.UDPConnectionHandler, relaying a
// single UDP association to its destination.
func (s *Server) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata M.Metadata) error {
	userID, _ := auth.UserFromContext[int64](ctx)
	counter := s.counterFor(userID)

	upstream, err := net.Dial("udp", metadata.Destination.String())
	if err != nil {
		return fmt.Errorf("dial upstream %s: %w", metadata.Destination, err)
	}
	defer upstream.Close()

	buffer := buf.NewSize(64 * 1024)
	defer buffer.Release()

	dest, err := conn.ReadPacket(buffer)
	if err != nil {
		return err
	}
	_ = dest
	if _, err := upstream.Write(buffer.Bytes()); err != nil {
		return err
	}
	counter.upload.Add(uint64(buffer.Len()))

	upstream.SetReadDeadline(time.Now().Add(60 * time.Second))
	respBuf := make([]byte, 64*1024)
	n, err := upstream.Read(respBuf)
	if err != nil {
		return nil // best-effort single round trip; timeouts are not errors here
	}
	reply := buf.As(respBuf[:n])
	if err := conn.WritePacket(reply, metadata.Destination); err != nil {
		return err
	}
	counter.download.Add(uint64(n))
	return nil
}

// NewError implements sing's E.Handler.
func (s *Server) NewError(ctx context.Context, err error) {
	s.logger.Debug("stream error", "error", err)
}
