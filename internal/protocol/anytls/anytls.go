// Package anytls implements protocol.ProtocolServer for AnyTLS, backed by the
// anytls/sing-anytls session library. We terminate TLS on the listener and
// hand the plaintext connection to the AnyTLS service, which parses its
// padded session framing and calls back with each proxied stream.
package anytls

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
)

func init() {
	protocol.Register("anytls", func() protocol.ProtocolServer { return New() })
}

// Server is an AnyTLS protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	service  *anytls.Service
	cfg      protocol.NodeConfig
	// usersByName maps the AnyTLS username (we use the panel UUID, or the
	// stringified user ID as a fallback) back to the panel user ID. Stored
	// behind an atomic pointer so it can be swapped on a live user reload
	// while connection handlers read it.
	usersByName atomic.Pointer[map[string]int64]

	counters sync.Map // int64 userID -> *userCounter
}

// buildAnyTLSUsers translates panel users into the sing-anytls user list and
// the name→id lookup used for stats attribution.
func buildAnyTLSUsers(users []protocol.User) ([]anytls.User, map[string]int64) {
	byName := make(map[string]int64, len(users))
	out := make([]anytls.User, 0, len(users))
	for _, u := range users {
		name := u.UUID
		if name == "" {
			name = strconv.FormatInt(u.ID, 10)
		}
		byName[name] = u.ID
		out = append(out, anytls.User{Name: name, Password: u.Password})
	}
	return out, byName
}

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

func New() *Server { return &Server{} }

func (s *Server) Name() string { return "anytls" }

// Start requires cfg.TLS.CertFile/KeyFile. Each user is keyed by password
// (AnyTLS authenticates on SHA-256 of the password); the panel UUID is used
// as the display name for stats attribution.
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("anytls: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("anytls: node %d has no users configured", cfg.NodeID)
	}
	if cfg.TLS.CertFile == "" || cfg.TLS.KeyFile == "" {
		return fmt.Errorf("anytls: node %d requires tls cert_file/key_file", cfg.NodeID)
	}

	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return fmt.Errorf("anytls: node %d: load cert: %w", cfg.NodeID, err)
	}

	users, usersByName := buildAnyTLSUsers(cfg.Users)

	service, err := anytls.NewService(anytls.ServiceConfig{
		PaddingScheme: padding.DefaultPaddingScheme,
		Users:         users,
		Handler:       s,
		Logger:        logger.NOP(),
	})
	if err != nil {
		return fmt.Errorf("anytls: node %d: %w", cfg.NodeID, err)
	}

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	ln, err := tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return fmt.Errorf("anytls: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	s.listener = ln
	s.service = service
	s.cfg = cfg
	s.usersByName.Store(&usersByName)

	go s.acceptLoop(ln, service)
	return nil
}

// UpdateUsers swaps the live user set without closing the listener.
func (s *Server) UpdateUsers(users []protocol.User) error {
	s.mu.Lock()
	svc := s.service
	s.mu.Unlock()
	if svc == nil {
		return fmt.Errorf("anytls: not started")
	}
	list, byName := buildAnyTLSUsers(users)
	svc.UpdateUsers(list)
	s.usersByName.Store(&byName)
	return nil
}

func (s *Server) acceptLoop(ln net.Listener, service *anytls.Service) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			ctx := context.Background()
			_ = service.NewConnection(ctx, conn, M.SocksaddrFromNet(conn.RemoteAddr()), func(error) {})
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

// NewConnectionEx implements AnyTLS's handler (N.TCPConnectionHandlerEx). It
// is invoked once per proxied stream with the decrypted destination.
func (s *Server) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if onClose == nil {
		onClose = func(error) {}
	}
	var userID int64
	if name, ok := auth.UserFromContext[string](ctx); ok {
		if m := s.usersByName.Load(); m != nil {
			userID = (*m)[name]
		}
	}

	upstream, err := net.Dial("tcp", destination.String())
	if err != nil {
		onClose(err)
		return
	}
	defer upstream.Close()

	up, down := relay.Pipe(conn, upstream)
	if userID != 0 {
		c := s.counterFor(userID)
		c.upload.Add(up)
		c.download.Add(down)
	}
	onClose(nil)
}
