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
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
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
	httpSrv  *http.Server // set when the transport is WebSocket
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

	isWS := strings.EqualFold(cfg.Transport, "ws")
	if isWS && cfg.Reality != nil {
		return fmt.Errorf("vless: node %d: reality and ws transport are mutually exclusive", cfg.NodeID)
	}

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
		// Terminate TLS ourselves (WSS, or plain VLESS-TLS) when a cert is set.
		certs, certErr := certutil.Certificates(cfg.TLS, cfg.ListenIP)
		if certErr != nil {
			return fmt.Errorf("vless: node %d: %w", cfg.NodeID, certErr)
		}
		ln, err = tls.Listen("tcp", addr, &tls.Config{Certificates: certs})
	default:
		// Plaintext — for WS, this is the CDN-fronted case (the CDN does TLS).
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

	if isWS {
		path := cfg.WSPath
		if path == "" {
			path = "/"
		}
		mux := http.NewServeMux()
		mux.HandleFunc(path, s.handleWS)
		s.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go s.httpSrv.Serve(ln)
	} else {
		go s.acceptLoop(ln)
	}
	return nil
}

// handleWS upgrades an HTTP request to WebSocket and feeds the resulting
// stream to the VLESS codec. Used when the node sits behind a CDN or a
// WS-terminating front.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	svc := s.service
	s.mu.Unlock()
	if svc == nil {
		http.Error(w, "", http.StatusServiceUnavailable)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ctx := context.Background()
	conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
	// r.RemoteAddr is the peer; behind a CDN that's the CDN's IP.
	s.serveConn(ctx, conn, M.ParseSocksaddr(r.RemoteAddr), svc, nil)
}

// serveConn runs one accepted stream through the (optional) Reality handshake
// and then the VLESS codec. Shared by the TCP accept loop and the WS handler.
func (s *Server) serveConn(ctx context.Context, conn net.Conn, source M.Socksaddr, svc *svless.Service[int64], rc *reality.Config) {
	defer conn.Close()
	stream := conn
	if rc != nil {
		rconn, err := realityutil.ServerHandshake(ctx, conn, rc)
		if err != nil {
			return
		}
		stream = rconn
	}
	svc.NewConnection(ctx, stream, source, func(error) {})
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
		go s.serveConn(context.Background(), conn, M.SocksaddrFromNet(conn.RemoteAddr()), svc, rc)
	}
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	if s.httpSrv != nil {
		s.httpSrv.Close()
		s.httpSrv = nil
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
