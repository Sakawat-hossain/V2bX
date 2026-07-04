// Package trojan implements protocol.ProtocolServer for Trojan: a TLS
// listener where the first application-data message is
// SHA224(password)-hex + CRLF + a SOCKS5-style address request + CRLF,
// followed by the proxied payload.
package trojan

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/certutil"
	"github.com/Sakawat-hossain/V2bX/internal/online"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/ratelimit"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
)

func init() {
	protocol.Register("trojan", func() protocol.ProtocolServer { return New() })
}

const (
	cmdConnect = 0x01
	addrIPv4   = 0x01
	addrDomain = 0x03
	addrIPv6   = 0x04
)

// Server is a Trojan protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	cfg      protocol.NodeConfig
	// users maps hex(SHA224(password)) -> panel user ID, so a valid Trojan
	// header identifies which subscriber to attribute traffic to. Behind an
	// atomic pointer so it can be swapped on a live user reload.
	users atomic.Pointer[map[string]int64]

	counters     sync.Map // int64 userID -> *userCounter
	online       online.Tracker
	limits       ratelimit.Store
	fallbackAddr string
}

// Online reports the source IPs each user is currently connected from.
func (s *Server) Online() map[int64][]string { return s.online.Snapshot() }

func buildTrojanUsers(users []protocol.User) map[string]int64 {
	m := make(map[string]int64, len(users))
	for _, u := range users {
		m[hexSHA224(u.Password)] = u.ID
	}
	return m
}

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

func New() *Server { return &Server{} }

func (s *Server) Name() string { return "trojan" }

// Start requires TLS: cfg.TLS.CertFile/KeyFile must point at a valid
// keypair (self-signed is fine — Trojan's security model relies on the
// password, not certificate trust, though a trusted cert avoids client
// warnings).
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("trojan: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("trojan: node %d has no users configured", cfg.NodeID)
	}
	certs, err := certutil.Certificates(cfg.TLS, cfg.ListenIP)
	if err != nil {
		return fmt.Errorf("trojan: node %d: %w", cfg.NodeID, err)
	}

	users := buildTrojanUsers(cfg.Users)

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	ln, err := tls.Listen("tcp", addr, &tls.Config{Certificates: certs})
	if err != nil {
		return fmt.Errorf("trojan: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	ln = relay.LimitListener(ln, cfg.MaxConnections)
	s.listener = ln
	s.cfg = cfg
	s.limits.Update(cfg.Users)
	s.fallbackAddr = cfg.FallbackAddr
	s.users.Store(&users)

	go s.acceptLoop(ln)
	return nil
}

func hexSHA224(password string) string {
	sum := sha256.Sum224([]byte(password))
	return hex.EncodeToString(sum[:])
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	err := s.listener.Close()
	s.listener = nil
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

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(conn)

	hexHash := make([]byte, 56) // hex(SHA224) is 56 chars
	if _, err := io.ReadFull(reader, hexHash); err != nil {
		return
	}
	users := s.users.Load()
	userID, ok := (*users)[string(hexHash)]
	if !ok {
		// Not a valid Trojan client (a probe or a browser). If a decoy
		// backend is configured, forward the connection there so the client
		// sees a real site; otherwise drop.
		s.fallback(conn, reader, hexHash)
		return
	}
	s.online.Mark(userID, online.IP(conn.RemoteAddr()))

	if err := discardCRLF(reader); err != nil {
		return
	}

	dest, err := readAddrRequest(reader)
	if err != nil {
		return
	}

	if err := discardCRLF(reader); err != nil {
		return
	}

	upstream, err := net.DialTimeout("tcp", dest, 10*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()

	// Anything buffered by the header parse must reach upstream first, and
	// counts toward upload since it's part of the client's payload.
	var preUp uint64
	if n := reader.Buffered(); n > 0 {
		buffered := make([]byte, n)
		reader.Read(buffered)
		if _, err := upstream.Write(buffered); err != nil {
			return
		}
		preUp = uint64(n)
	}
	conn.SetReadDeadline(time.Time{})

	up, down := relay.Pipe(conn, s.limits.Limit(userID, upstream))
	c := s.counterFor(userID)
	c.upload.Add(up + preUp)
	c.download.Add(down)
}

// fallback forwards an unauthenticated (post-TLS) connection to the decoy
// backend, replaying the bytes already consumed so the backend sees the
// client's full original request. With no backend configured it drops the
// connection (the prior behavior), which is fine but more probe-detectable.
func (s *Server) fallback(conn net.Conn, reader *bufio.Reader, consumed []byte) {
	if s.fallbackAddr == "" {
		return
	}
	conn.SetReadDeadline(time.Time{}) // this is now a full proxied web session
	backend, err := net.DialTimeout("tcp", s.fallbackAddr, 10*time.Second)
	if err != nil {
		return
	}
	defer backend.Close()

	client := &replayConn{Conn: conn, r: io.MultiReader(bytes.NewReader(consumed), reader)}
	relay.Pipe(client, backend)
}

// replayConn reads from r (the already-consumed prefix followed by the
// buffered reader) while writes still go straight to the connection.
type replayConn struct {
	net.Conn
	r io.Reader
}

func (c *replayConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func discardCRLF(r *bufio.Reader) error {
	buf := make([]byte, 2)
	_, err := io.ReadFull(r, buf)
	if err != nil {
		return err
	}
	if buf[0] != '\r' || buf[1] != '\n' {
		return fmt.Errorf("trojan: expected CRLF")
	}
	return nil
}

func readAddrRequest(r *bufio.Reader) (string, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return "", err
	}
	if head[0] != cmdConnect {
		return "", fmt.Errorf("trojan: unsupported command %d", head[0])
	}

	var host string
	switch head[1] {
	case addrIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case addrIPv6:
		b := make([]byte, 16)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case addrDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return "", err
		}
		b := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", fmt.Errorf("trojan: unsupported address type %d", head[1])
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(r, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// UpdateUsers swaps the live user set without closing the listener.
func (s *Server) UpdateUsers(users []protocol.User) error {
	s.limits.Update(users)
	m := buildTrojanUsers(users)
	s.users.Store(&m)
	return nil
}

func (s *Server) counterFor(userID int64) *userCounter {
	v, _ := s.counters.LoadOrStore(userID, &userCounter{})
	return v.(*userCounter)
}
