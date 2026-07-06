// Package vmess implements protocol.ProtocolServer for VMess over raw TCP
// (AEAD, alterId 0), backed by the sagernet/sing-vmess codec library.
// WebSocket/gRPC transports are not yet wired up — see docs/PROTOCOLS.md.
package vmess

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	vm "github.com/sagernet/sing-vmess"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/online"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/ratelimit"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
)

func init() {
	protocol.Register("vmess", func() protocol.ProtocolServer { return New() })
}

// Server is a VMess protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	service  *vm.Service[int64]
	cfg      protocol.NodeConfig

	counters sync.Map // int64 userID -> *userCounter
	online   online.Tracker
	limits   ratelimit.Store
	logger   *slog.Logger
}

// udpTimeout bounds how long an idle upstream UDP socket lingers.
const udpTimeout = 60 * time.Second

// Online reports the source IPs each user is currently connected from.
func (s *Server) Online() map[int64][]string { return s.online.Snapshot() }

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

func New() *Server { return &Server{logger: slog.Default().With("protocol", "vmess")} }

func (s *Server) Name() string { return "vmess" }

// Start binds a plain TCP listener and begins serving VMess connections for
// every user in cfg.Users (UUID + optional legacy AlterID via Extra["alter_id"]).
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("vmess: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("vmess: node %d has no users configured", cfg.NodeID)
	}

	service, err := buildVMessService(s, cfg.Users)
	if err != nil {
		return fmt.Errorf("vmess: node %d: invalid user UUIDs: %w", cfg.NodeID, err)
	}

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("vmess: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	ln = relay.LimitListener(ln, cfg.MaxConnections)
	s.listener = ln
	s.service = service
	s.cfg = cfg
	s.limits.Update(cfg.Users)

	go s.acceptLoop(ln)
	return nil
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
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
			svc.NewConnection(ctx, conn, M.SocksaddrFromNet(conn.RemoteAddr()), func(error) {})
		}()
	}
}

func buildVMessService(handler vm.Handler, users []protocol.User) (*vm.Service[int64], error) {
	svc := vm.NewService[int64](handler)
	ids := make([]int64, len(users))
	uuids := make([]string, len(users))
	alterIDs := make([]int, len(users))
	for i, u := range users {
		ids[i] = u.ID
		uuids[i] = u.UUID
	}
	if err := svc.UpdateUsers(ids, uuids, alterIDs); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	err := s.listener.Close()
	s.listener = nil
	s.service = nil
	return err
}

func (s *Server) Stats() protocol.UsageStats {
	out := protocol.UsageStats{NodeID: s.cfg.NodeID, Users: map[int64]protocol.UserTraffic{}}
	s.counters.Range(func(key, value any) bool {
		id := key.(int64)
		c := value.(*userCounter)
		up, down := c.upload.Load(), c.download.Load()
		if up != 0 || down != 0 {
			out.Users[id] = protocol.UserTraffic{Upload: up, Download: down}
		}
		return true
	})
	return out
}

// UpdateUsers builds a fresh service with the new users and atomically swaps
// it in, so in-flight connections keep serving on the old, now-immutable
// service instead of racing its user map.
func (s *Server) UpdateUsers(users []protocol.User) error {
	s.limits.Update(users)
	s.mu.Lock()
	running := s.service != nil
	s.mu.Unlock()
	if !running {
		return fmt.Errorf("vmess: not started")
	}
	fresh, err := buildVMessService(s, users)
	if err != nil {
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

// NewConnectionEx implements sing-vmess's Handler (N.TCPConnectionHandlerEx).
func (s *Server) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	userID, _ := auth.UserFromContext[int64](ctx)
	counter := s.counterFor(userID)
	s.online.Mark(userID, online.IPString(source.String()))

	upstream, err := net.Dial("tcp", destination.String())
	if err != nil {
		// Usual "connected but no data" cause: node can't reach the destination.
		s.logger.Debug("dial upstream failed", "dest", destination.String(), "error", err)
		onClose(err)
		return
	}
	defer upstream.Close()

	up, down := relay.Pipe(conn, s.limits.Limit(userID, upstream))
	counter.upload.Add(up)
	counter.download.Add(down)
	onClose(nil)
}

// NewPacketConnectionEx implements sing-vmess's Handler (N.UDPConnectionHandlerEx):
// UDP-over-VMess. The codec binds the packet conn to one destination, so we
// dial a single upstream UDP socket and relay both ways for the association.
func (s *Server) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	userID, _ := auth.UserFromContext[int64](ctx)
	counter := s.counterFor(userID)
	s.online.Mark(userID, online.IPString(source.String()))

	upstream, err := net.Dial("udp", destination.String())
	if err != nil {
		s.logger.Debug("dial udp upstream failed", "dest", destination.String(), "error", err)
		onClose(err)
		return
	}
	uc := upstream.(*net.UDPConn)
	defer uc.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		scratch := make([]byte, 64*1024)
		for {
			uc.SetReadDeadline(time.Now().Add(udpTimeout))
			n, rErr := uc.Read(scratch)
			if n > 0 {
				reply := buf.NewPacket()
				reply.Resize(16, 0)
				reply.Write(scratch[:n])
				if wErr := conn.WritePacket(reply, destination); wErr != nil {
					return
				}
				counter.download.Add(uint64(n))
			}
			if rErr != nil {
				return
			}
		}
	}()

	for {
		buffer := buf.NewPacket()
		if _, rErr := conn.ReadPacket(buffer); rErr != nil {
			buffer.Release()
			break
		}
		n := buffer.Len()
		if _, wErr := uc.Write(buffer.Bytes()); wErr == nil {
			counter.upload.Add(uint64(n))
		}
		buffer.Release()
	}
	uc.Close()
	<-done
	onClose(nil)
}
