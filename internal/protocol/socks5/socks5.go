// Package socks5 implements protocol.ProtocolServer for plain SOCKS5
// (RFC 1928), with optional username/password auth (RFC 1929) keyed off the
// panel's per-node user list.
package socks5

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/online"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
)

func init() {
	protocol.Register("socks5", func() protocol.ProtocolServer { return New() })
}

const (
	socksVersion5 = 0x05

	authNone     = 0x00
	authPassword = 0x02
	authNoAccept = 0xFF

	cmdConnect = 0x01

	addrIPv4   = 0x01
	addrDomain = 0x03
	addrIPv6   = 0x04

	replySuccess        = 0x00
	replyGeneralFailure = 0x01
)

// Server is a SOCKS5 protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	cfg      protocol.NodeConfig
	// users maps username -> password; empty when no auth is configured.
	// Behind an atomic pointer so it can be swapped on a live user reload.
	users atomic.Pointer[map[string]string]

	counters sync.Map // int64 userID -> *userCounter
	online   online.Tracker
}

// Online reports the source IPs each user is currently connected from.
func (s *Server) Online() map[int64][]string { return s.online.Snapshot() }

func buildAuthUsers(users []protocol.User) map[string]string {
	m := make(map[string]string, len(users))
	for _, u := range users {
		if u.UUID != "" {
			m[u.UUID] = u.Password
		}
	}
	return m
}

func (s *Server) currentUsers() map[string]string {
	if m := s.users.Load(); m != nil {
		return *m
	}
	return nil
}

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

func New() *Server { return &Server{} }

func (s *Server) Name() string { return "socks5" }

// Start binds a TCP listener. If cfg.Users carries UUID/Password pairs they
// are treated as username/password credentials (RFC 1929); an empty user
// list means anonymous access.
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("socks5: node %d already started", cfg.NodeID)
	}

	users := buildAuthUsers(cfg.Users)

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("socks5: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	s.listener = ln
	s.cfg = cfg
	s.users.Store(&users)

	go s.acceptLoop(ln)
	return nil
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
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	userID, err := s.negotiate(conn)
	if err != nil {
		return
	}
	s.online.Mark(userID, online.IP(conn.RemoteAddr()))

	dest, err := readConnectRequest(conn)
	if err != nil {
		writeReply(conn, replyGeneralFailure)
		return
	}

	upstream, err := net.DialTimeout("tcp", dest, 10*time.Second)
	if err != nil {
		writeReply(conn, replyGeneralFailure)
		return
	}
	defer upstream.Close()

	if err := writeReply(conn, replySuccess); err != nil {
		return
	}
	conn.SetDeadline(time.Time{})

	up, down := relay.Pipe(conn, upstream)
	if userID != 0 {
		c := s.counterFor(userID)
		c.upload.Add(up)
		c.download.Add(down)
	}
}

// negotiate performs the SOCKS5 method-selection and, if the node has users
// configured, username/password auth. Returns the authenticated user's ID
// (0 if anonymous/no users configured).
func (s *Server) negotiate(conn net.Conn) (int64, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return 0, err
	}
	if header[0] != socksVersion5 {
		return 0, fmt.Errorf("socks5: unsupported version %d", header[0])
	}
	methods := make([]byte, header[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return 0, err
	}

	users := s.currentUsers()
	requireAuth := len(users) > 0
	wantMethod := byte(authNone)
	if requireAuth {
		wantMethod = authPassword
	}

	has := false
	for _, m := range methods {
		if m == wantMethod {
			has = true
			break
		}
	}
	if !has {
		conn.Write([]byte{socksVersion5, authNoAccept})
		return 0, fmt.Errorf("socks5: no acceptable auth method")
	}
	if _, err := conn.Write([]byte{socksVersion5, wantMethod}); err != nil {
		return 0, err
	}
	if !requireAuth {
		return 0, nil
	}
	return s.authenticate(conn, users)
}

func (s *Server) authenticate(conn net.Conn, users map[string]string) (int64, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return 0, err
	}
	uLen := int(head[1])
	userBuf := make([]byte, uLen)
	if _, err := io.ReadFull(conn, userBuf); err != nil {
		return 0, err
	}
	pLenBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, pLenBuf); err != nil {
		return 0, err
	}
	passBuf := make([]byte, pLenBuf[0])
	if _, err := io.ReadFull(conn, passBuf); err != nil {
		return 0, err
	}

	username, password := string(userBuf), string(passBuf)
	want, ok := users[username]
	if !ok || want != password {
		conn.Write([]byte{0x01, 0x01})
		return 0, fmt.Errorf("socks5: auth failed for user %q", username)
	}
	if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
		return 0, err
	}
	return hashUsername(username), nil
}

// hashUsername maps a SOCKS5 username to a stable pseudo-ID for stats
// bucketing when the panel's numeric user ID isn't available at this layer.
func hashUsername(u string) int64 {
	var h int64 = 1469598103934665603
	for _, b := range []byte(u) {
		h ^= int64(b)
		h *= 1099511628211
	}
	if h < 0 {
		h = -h
	}
	return h
}

func readConnectRequest(conn net.Conn) (string, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", err
	}
	if head[0] != socksVersion5 || head[1] != cmdConnect {
		return "", fmt.Errorf("socks5: unsupported command %d", head[1])
	}

	var host string
	switch head[3] {
	case addrIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case addrIPv6:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case addrDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", err
		}
		b := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", fmt.Errorf("socks5: unsupported address type %d", head[3])
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func writeReply(conn net.Conn, code byte) error {
	_, err := conn.Write([]byte{socksVersion5, code, 0x00, addrIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

// UpdateUsers swaps the live user set without closing the listener.
func (s *Server) UpdateUsers(users []protocol.User) error {
	m := buildAuthUsers(users)
	s.users.Store(&m)
	return nil
}

func (s *Server) counterFor(userID int64) *userCounter {
	v, _ := s.counters.LoadOrStore(userID, &userCounter{})
	return v.(*userCounter)
}
