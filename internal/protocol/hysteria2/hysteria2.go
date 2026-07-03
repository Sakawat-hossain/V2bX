// Package hysteria2 implements protocol.ProtocolServer for Hysteria2, a
// QUIC-based protocol, backed by sagernet/sing-quic/hysteria2.
package hysteria2

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	hy2 "github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing/common/auth"
	sbuf "github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
	"github.com/Sakawat-hossain/V2bX/internal/tlsutil"
)

func init() {
	protocol.Register("hysteria2", func() protocol.ProtocolServer { return New() })
}

// Server is a Hysteria2 protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu         sync.Mutex
	packetConn net.PacketConn
	service    *hy2.Service[int64]
	cancel     context.CancelFunc
	cfg        protocol.NodeConfig

	counters sync.Map // int64 userID -> *userCounter
}

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

func New() *Server { return &Server{} }

func (s *Server) Name() string { return "hysteria2" }

// Start binds a UDP listener and begins serving Hysteria2 sessions. Requires
// cfg.TLS.CertFile/KeyFile (self-signed certs are fine; Hysteria2 clients
// typically pin or skip verification rather than rely on a CA).
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.packetConn != nil {
		return fmt.Errorf("hysteria2: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("hysteria2: node %d has no users configured", cfg.NodeID)
	}
	if cfg.TLS.CertFile == "" || cfg.TLS.KeyFile == "" {
		return fmt.Errorf("hysteria2: node %d requires tls cert_file/key_file", cfg.NodeID)
	}

	tlsConfig, err := tlsutil.NewStdServerConfig(cfg.TLS.CertFile, cfg.TLS.KeyFile, []string{"h3"})
	if err != nil {
		return fmt.Errorf("hysteria2: node %d: load cert: %w", cfg.NodeID, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	service, err := hy2.NewService[int64](hy2.ServiceOptions{
		Context:    ctx,
		Logger:     logger.NOP(),
		TLSConfig:  tlsConfig,
		UDPTimeout: 300 * time.Second,
		Handler:    s,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("hysteria2: node %d: %w", cfg.NodeID, err)
	}

	userIDs := make([]int64, len(cfg.Users))
	passwords := make([]string, len(cfg.Users))
	for i, u := range cfg.Users {
		userIDs[i] = u.ID
		passwords[i] = u.Password
	}
	service.UpdateUsers(userIDs, passwords)

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	packetConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		cancel()
		return fmt.Errorf("hysteria2: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}
	if err := service.Start(packetConn); err != nil {
		packetConn.Close()
		cancel()
		return fmt.Errorf("hysteria2: node %d: start: %w", cfg.NodeID, err)
	}

	s.packetConn = packetConn
	s.service = service
	s.cancel = cancel
	s.cfg = cfg
	return nil
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.packetConn == nil {
		return nil
	}
	s.cancel()
	err := s.service.Close()
	s.packetConn.Close()
	s.packetConn = nil
	s.service = nil
	return err
}

func (s *Server) Stats() protocol.UsageStats {
	out := protocol.UsageStats{NodeID: s.cfg.NodeID, Users: map[int64]protocol.UserTraffic{}}
	s.counters.Range(func(key, value any) bool {
		id := key.(int64)
		c := value.(*userCounter)
		up, down := c.upload.Swap(0), c.download.Swap(0)
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

// NewConnectionEx implements hysteria2's ServerHandler (N.TCPConnectionHandlerEx).
func (s *Server) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if onClose == nil {
		onClose = func(error) {}
	}
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

// NewPacketConnectionEx implements hysteria2's ServerHandler
// (N.UDPConnectionHandlerEx), relaying a UDP association bidirectionally
// until either side goes idle for udpIdleTimeout.
func (s *Server) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if onClose == nil {
		onClose = func(error) {}
	}
	userID, _ := auth.UserFromContext[int64](ctx)
	counter := s.counterFor(userID)

	upstream, err := net.Dial("udp", destination.String())
	if err != nil {
		onClose(err)
		return
	}

	go func() {
		defer upstream.Close()
		buf := make([]byte, 64*1024)
		for {
			upstream.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			n, err := upstream.Read(buf)
			if err != nil {
				return
			}
			if err := conn.WritePacket(sbuf.As(buf[:n]), destination); err != nil {
				return
			}
			counter.download.Add(uint64(n))
		}
	}()

	buffer := sbuf.NewSize(64 * 1024)
	defer buffer.Release()
	for {
		buffer.Reset()
		conn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		if _, err := conn.ReadPacket(buffer); err != nil {
			upstream.Close()
			onClose(err)
			return
		}
		if _, err := upstream.Write(buffer.Bytes()); err != nil {
			upstream.Close()
			onClose(err)
			return
		}
		counter.upload.Add(uint64(buffer.Len()))
	}
}

const udpIdleTimeout = 300 * time.Second
