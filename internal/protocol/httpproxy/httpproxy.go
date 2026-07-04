// Package httpproxy implements protocol.ProtocolServer for a plain HTTP
// proxy: CONNECT tunneling for HTTPS, and direct request forwarding for
// plain HTTP, both unencrypted (no TLS termination at this layer).
package httpproxy

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/online"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/ratelimit"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
)

func init() {
	protocol.Register("http", func() protocol.ProtocolServer { return New() })
}

// Server is a plain HTTP proxy protocol.ProtocolServer. Zero value is ready
// to use.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	cfg      protocol.NodeConfig
	// users maps username -> password (HTTP Basic Proxy-Authorization);
	// empty = no auth. Behind an atomic pointer for live user reloads.
	users atomic.Pointer[map[string]string]

	counters sync.Map // int64 userID -> *userCounter
	online   online.Tracker
	limits   ratelimit.Store
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

func (s *Server) Name() string { return "http" }

// Start binds a TCP listener. If cfg.Users carries UUID/Password pairs they
// are required as HTTP Basic Proxy-Authorization credentials.
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("http: node %d already started", cfg.NodeID)
	}

	users := buildAuthUsers(cfg.Users)

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	ln = relay.LimitListener(ln, cfg.MaxConnections)
	s.listener = ln
	s.cfg = cfg
	s.limits.Update(cfg.Users)
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
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}

	userID, ok := s.checkAuth(req)
	if !ok {
		resp := "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"v2bx\"\r\nContent-Length: 0\r\n\r\n"
		conn.Write([]byte(resp))
		return
	}
	s.online.Mark(userID, online.IP(conn.RemoteAddr()))

	if req.Method == http.MethodConnect {
		s.handleConnect(conn, req, userID)
		return
	}
	s.handlePlain(conn, reader, req, userID)
}

func (s *Server) checkAuth(req *http.Request) (int64, bool) {
	users := s.currentUsers()
	if len(users) == 0 {
		return 0, true
	}
	header := req.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return 0, false
	}
	raw, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return 0, false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	username, password := parts[0], parts[1]
	want, exists := users[username]
	if !exists || want != password {
		return 0, false
	}
	return hashUsername(username), true
}

// UpdateUsers swaps the live user set without closing the listener.
func (s *Server) UpdateUsers(users []protocol.User) error {
	s.limits.Update(users)
	m := buildAuthUsers(users)
	s.users.Store(&m)
	return nil
}

func (s *Server) handleConnect(conn net.Conn, req *http.Request, userID int64) {
	upstream, err := net.DialTimeout("tcp", req.Host, 10*time.Second)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		return
	}
	defer upstream.Close()

	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})

	up, down := relay.Pipe(conn, s.limits.Limit(userID, upstream))
	s.record(userID, up, down)
}

// handlePlain forwards a single non-CONNECT request (plain HTTP) upstream
// and copies the response back verbatim.
func (s *Server) handlePlain(conn net.Conn, reader *bufio.Reader, req *http.Request, userID int64) {
	host := req.Host
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "80")
	}
	upstream, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		return
	}
	defer upstream.Close()

	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	if err := req.Write(upstream); err != nil {
		return
	}
	// Anything already buffered by ReadRequest (e.g. a pipelined second
	// request) must be forwarded before falling back to the raw pipe,
	// which reads directly off the socket and would otherwise skip it.
	// It counts toward upload since it's part of the client's payload.
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
	s.record(userID, up+preUp, down)
}

func (s *Server) record(userID int64, up, down uint64) {
	if userID == 0 {
		return
	}
	c := s.counterFor(userID)
	c.upload.Add(up)
	c.download.Add(down)
}

func (s *Server) counterFor(userID int64) *userCounter {
	v, _ := s.counters.LoadOrStore(userID, &userCounter{})
	return v.(*userCounter)
}

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
