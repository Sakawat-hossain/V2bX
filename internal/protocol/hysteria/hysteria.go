// Package hysteria implements protocol.ProtocolServer for Hysteria (v1), a
// QUIC-based protocol, backed by sagernet/sing-quic/hysteria.
package hysteria

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	shy "github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing/common/auth"
	sbuf "github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Sakawat-hossain/V2bX/internal/certutil"
	"github.com/Sakawat-hossain/V2bX/internal/online"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/ratelimit"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
	"github.com/Sakawat-hossain/V2bX/internal/tlsutil"
)

func init() {
	protocol.Register("hysteria", func() protocol.ProtocolServer { return New() })
}

const udpIdleTimeout = 300 * time.Second

// defaultBPS is used when no per-node bandwidth is configured. Hysteria v1
// requires a nonzero server-side send/receive rate.
const defaultBPS = 1_000_000_000 // 1 Gbps

// bpsOrDefault converts a Mbps figure to bytes/sec, falling back to
// defaultBPS when unset (0).
func bpsOrDefault(mbps int) uint64 {
	if mbps <= 0 {
		return defaultBPS
	}
	return uint64(mbps) * 125_000 // 1 Mbps = 125,000 bytes/s
}

// Server is a Hysteria (v1) protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu         sync.Mutex
	packetConn net.PacketConn
	service    *shy.Service[int64]
	cancel     context.CancelFunc
	cfg        protocol.NodeConfig

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

func New() *Server { return &Server{} }

func (s *Server) Name() string { return "hysteria" }

// Start binds a UDP listener and begins serving Hysteria (v1) sessions.
// Requires cfg.TLS.CertFile/KeyFile.
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.packetConn != nil {
		return fmt.Errorf("hysteria: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("hysteria: node %d has no users configured", cfg.NodeID)
	}
	certs, err := certutil.Certificates(cfg.TLS, cfg.ListenIP)
	if err != nil {
		return fmt.Errorf("hysteria: node %d: %w", cfg.NodeID, err)
	}
	tlsConfig := tlsutil.NewServerConfig(certs, []string{"hysteria"})

	ctx, cancel := context.WithCancel(context.Background())
	service, err := shy.NewService[int64](shy.ServiceOptions{
		Context:    ctx,
		Logger:     logger.NOP(),
		TLSConfig:  tlsConfig,
		SendBPS:    bpsOrDefault(cfg.DownMbps), // server -> client = user download
		ReceiveBPS: bpsOrDefault(cfg.UpMbps),   // client -> server = user upload
		UDPTimeout: udpIdleTimeout,
		Handler:    s,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("hysteria: node %d: %w", cfg.NodeID, err)
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
		return fmt.Errorf("hysteria: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}
	if err := service.Start(packetConn); err != nil {
		packetConn.Close()
		cancel()
		return fmt.Errorf("hysteria: node %d: start: %w", cfg.NodeID, err)
	}

	s.packetConn = packetConn
	s.service = service
	s.cancel = cancel
	s.cfg = cfg
	s.limits.Update(cfg.Users)
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

// NewConnectionEx implements Hysteria's ServerHandler (N.TCPConnectionHandlerEx).
func (s *Server) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if onClose == nil {
		onClose = func(error) {}
	}
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

// NewPacketConnectionEx implements Hysteria's ServerHandler
// (N.UDPConnectionHandlerEx), relaying a UDP association bidirectionally
// until either side goes idle for udpIdleTimeout.
func (s *Server) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if onClose == nil {
		onClose = func(error) {}
	}
	userID, _ := auth.UserFromContext[int64](ctx)
	counter := s.counterFor(userID)
	s.online.Mark(userID, online.IPString(source.String()))

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
