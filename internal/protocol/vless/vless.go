// Package vless implements protocol.ProtocolServer for VLess over raw TCP,
// with optional XTLS/Vision flow control, backed by the
// sagernet/sing-vmess/vless codec library. VLess has no built-in
// encryption, so nodes are expected to sit behind TLS terminated by this
// same listener when TLS config is present, or by a fronting reverse proxy
// otherwise.
package vless

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	svless "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
)

func init() {
	protocol.Register("vless", func() protocol.ProtocolServer { return New() })
}

// Server is a VLess protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	service  *svless.Service[int64]
	cfg      protocol.NodeConfig

	counters sync.Map // int64 userID -> *userCounter
}

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

func New() *Server { return &Server{} }

func (s *Server) Name() string { return "vless" }

// Start binds a listener — TLS-wrapped if cfg.TLS.CertFile/KeyFile are set,
// plain TCP otherwise (e.g. when TLS is terminated upstream) — and begins
// serving VLess connections for every user in cfg.Users.
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("vless: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("vless: node %d has no users configured", cfg.NodeID)
	}

	service := svless.NewService[int64](logger.NOP(), s)

	userIDs := make([]int64, len(cfg.Users))
	uuids := make([]string, len(cfg.Users))
	flows := make([]string, len(cfg.Users))
	for i, u := range cfg.Users {
		userIDs[i] = u.ID
		uuids[i] = u.UUID
		flows[i] = u.Flow
	}
	service.UpdateUsers(userIDs, uuids, flows)

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	var ln net.Listener
	var err error
	if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
		cert, certErr := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if certErr != nil {
			return fmt.Errorf("vless: node %d: load cert: %w", cfg.NodeID, certErr)
		}
		ln, err = tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}})
	} else {
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("vless: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	s.listener = ln
	s.service = service
	s.cfg = cfg

	go s.acceptLoop(ln, service)
	return nil
}

func (s *Server) acceptLoop(ln net.Listener, service *svless.Service[int64]) {
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

// UpdateUsers swaps the live user set without closing the listener.
func (s *Server) UpdateUsers(users []protocol.User) error {
	s.mu.Lock()
	svc := s.service
	s.mu.Unlock()
	if svc == nil {
		return fmt.Errorf("vless: not started")
	}
	ids := make([]int64, len(users))
	uuids := make([]string, len(users))
	flows := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
		uuids[i] = u.UUID
		flows[i] = u.Flow
	}
	svc.UpdateUsers(ids, uuids, flows)
	return nil
}

func (s *Server) counterFor(userID int64) *userCounter {
	v, _ := s.counters.LoadOrStore(userID, &userCounter{})
	return v.(*userCounter)
}

// NewConnectionEx implements sing-vmess/vless's Handler (N.TCPConnectionHandlerEx).
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

// NewPacketConnectionEx implements sing-vmess/vless's Handler
// (N.UDPConnectionHandlerEx). UDP-over-VLess is not yet implemented.
func (s *Server) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	onClose(fmt.Errorf("vless: UDP not supported"))
}
