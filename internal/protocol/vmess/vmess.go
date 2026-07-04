// Package vmess implements protocol.ProtocolServer for VMess over raw TCP
// (AEAD, alterId 0), backed by the sagernet/sing-vmess codec library.
// WebSocket/gRPC transports are not yet wired up — see docs/PROTOCOLS.md.
package vmess

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	vm "github.com/sagernet/sing-vmess"
	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
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
}

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

func New() *Server { return &Server{} }

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

	service := vm.NewService[int64](s)

	userIDs := make([]int64, len(cfg.Users))
	uuids := make([]string, len(cfg.Users))
	alterIDs := make([]int, len(cfg.Users))
	for i, u := range cfg.Users {
		userIDs[i] = u.ID
		uuids[i] = u.UUID
		alterIDs[i] = 0
	}
	if err := service.UpdateUsers(userIDs, uuids, alterIDs); err != nil {
		return fmt.Errorf("vmess: node %d: invalid user UUIDs: %w", cfg.NodeID, err)
	}

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("vmess: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	s.listener = ln
	s.service = service
	s.cfg = cfg

	go s.acceptLoop(ln, service)
	return nil
}

func (s *Server) acceptLoop(ln net.Listener, service *vm.Service[int64]) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			ctx := context.Background()
			service.NewConnection(ctx, conn, M.SocksaddrFromNet(conn.RemoteAddr()), func(error) {})
		}()
	}
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

func (s *Server) counterFor(userID int64) *userCounter {
	v, _ := s.counters.LoadOrStore(userID, &userCounter{})
	return v.(*userCounter)
}

// NewConnectionEx implements sing-vmess's Handler (N.TCPConnectionHandlerEx).
func (s *Server) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	userID, _ := auth.UserFromContext[int64](ctx)
	counter := s.counterFor(userID)

	upstream, err := net.Dial("tcp", destination.String())
	if err != nil {
		onClose(err)
		return
	}
	defer upstream.Close()

	up, down := relay.Pipe(conn, upstream)
	counter.upload.Add(up)
	counter.download.Add(down)
	onClose(nil)
}

// NewPacketConnectionEx implements sing-vmess's Handler (N.UDPConnectionHandlerEx).
// UDP-over-VMess is not yet implemented; associations are rejected cleanly.
func (s *Server) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	onClose(fmt.Errorf("vmess: UDP not supported"))
}
