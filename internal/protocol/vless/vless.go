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

	"github.com/sagernet/reality"
	svless "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/certutil"
	"github.com/Sakawat-hossain/V2bX/internal/online"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/ratelimit"
	"github.com/Sakawat-hossain/V2bX/internal/realityutil"
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
	online   online.Tracker
	limits   ratelimit.Store
	reality  *reality.Config
}

// Online reports the source IPs each user is currently connected from.
func (s *Server) Online() map[int64][]string { return s.online.Snapshot() }

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

	service := buildVLessService(s, cfg.Users)

	// Reality replaces the TLS layer: it does its own handshake on the raw
	// TCP connection (impersonating a real site), so the listener stays plain.
	var realityCfg *reality.Config
	if cfg.Reality != nil {
		var rerr error
		realityCfg, rerr = realityutil.BuildServerConfig(cfg.Reality)
		if rerr != nil {
			return fmt.Errorf("vless: node %d: reality: %w", cfg.NodeID, rerr)
		}
	}

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	var ln net.Listener
	var err error
	switch {
	case realityCfg != nil:
		// Reality handles the handshake per-connection in acceptLoop.
		ln, err = net.Listen("tcp", addr)
	case tlsWanted(cfg.TLS):
		// VLESS terminates TLS itself when a cert mode/cert is provided.
		certs, certErr := certutil.Certificates(cfg.TLS, cfg.ListenIP)
		if certErr != nil {
			return fmt.Errorf("vless: node %d: %w", cfg.NodeID, certErr)
		}
		ln, err = tls.Listen("tcp", addr, &tls.Config{Certificates: certs})
	default:
		// Plaintext (behind a fronting TLS proxy).
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("vless: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	ln = relay.LimitListener(ln, cfg.MaxConnections)
	s.listener = ln
	s.service = service
	s.cfg = cfg
	s.reality = realityCfg
	s.limits.Update(cfg.Users)

	go s.acceptLoop(ln)
	return nil
}

// tlsWanted reports whether a VLESS node should terminate TLS itself.
func tlsWanted(t protocol.TLSConfig) bool {
	if t.CertFile != "" && t.KeyFile != "" {
		return true
	}
	return t.Mode != "" && t.Mode != "none"
}

func buildVLessService(handler svless.Handler, users []protocol.User) *svless.Service[int64] {
	svc := svless.NewService[int64](logger.NOP(), handler)
	ids := make([]int64, len(users))
	uuids := make([]string, len(users))
	flows := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
		uuids[i] = u.UUID
		flows[i] = u.Flow
	}
	svc.UpdateUsers(ids, uuids, flows)
	return svc
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		svc := s.service
		rc := s.reality
		s.mu.Unlock()
		if svc == nil {
			conn.Close()
			continue
		}
		go func() {
			defer conn.Close()
			ctx := context.Background()
			stream := net.Conn(conn)
			if rc != nil {
				// Reality authenticates the client (or transparently serves a
				// probe the decoy site and returns an error). Only authorized
				// clients reach the VLESS layer.
				rconn, err := realityutil.ServerHandshake(ctx, conn, rc)
				if err != nil {
					return
				}
				stream = rconn
			}
			svc.NewConnection(ctx, stream, M.SocksaddrFromNet(conn.RemoteAddr()), func(error) {})
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

// UpdateUsers builds a fresh service with the new users and atomically swaps
// it in, so in-flight connections keep serving on the old, now-immutable
// service instead of racing its user map.
func (s *Server) UpdateUsers(users []protocol.User) error {
	s.limits.Update(users)
	s.mu.Lock()
	running := s.service != nil
	s.mu.Unlock()
	if !running {
		return fmt.Errorf("vless: not started")
	}
	fresh := buildVLessService(s, users)
	s.mu.Lock()
	s.service = fresh
	s.mu.Unlock()
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
	s.online.Mark(userID, online.IPString(source.String()))

	upstream, err := net.Dial("tcp", destination.String())
	if err != nil {
		onClose(err)
		return
	}
	defer upstream.Close()

	up, down := relay.Pipe(conn, s.limits.Limit(userID, upstream))
	counter.upload.Add(up)
	counter.download.Add(down)
	onClose(nil)
}

// NewPacketConnectionEx implements sing-vmess/vless's Handler
// (N.UDPConnectionHandlerEx). UDP-over-VLess is not yet implemented.
func (s *Server) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	onClose(fmt.Errorf("vless: UDP not supported"))
}
