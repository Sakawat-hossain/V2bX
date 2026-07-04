// Package naive implements protocol.ProtocolServer for NaiveProxy: an
// HTTP/2 CONNECT tunnel over TLS, authenticated with HTTP Basic
// Proxy-Authorization, designed to be indistinguishable from an ordinary
// HTTPS server to passive observers. The optional length-padding scheme
// naive clients can layer on top is not implemented here — see
// docs/PROTOCOLS.md.
package naive

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"

	"github.com/Sakawat-hossain/V2bX/internal/online"
	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/ratelimit"
)

func init() {
	protocol.Register("naive", func() protocol.ProtocolServer { return New() })
}

// Server is a NaiveProxy protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	httpSrv  *http.Server
	cfg      protocol.NodeConfig
	// users maps username -> password. Behind an atomic pointer so it can be
	// swapped on a live user reload while request handlers read it.
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

func (s *Server) Name() string { return "naive" }

// Start requires cfg.TLS.CertFile/KeyFile. Every user in cfg.Users must
// carry a UUID (used as username) and Password.
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return fmt.Errorf("naive: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("naive: node %d has no users configured", cfg.NodeID)
	}
	if cfg.TLS.CertFile == "" || cfg.TLS.KeyFile == "" {
		return fmt.Errorf("naive: node %d requires tls cert_file/key_file", cfg.NodeID)
	}

	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return fmt.Errorf("naive: node %d: load cert: %w", cfg.NodeID, err)
	}

	users := buildAuthUsers(cfg.Users)

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
	httpSrv := &http.Server{Handler: http.HandlerFunc(s.handleConnect)}
	if err := http2.ConfigureServer(httpSrv, &http2.Server{}); err != nil {
		return fmt.Errorf("naive: node %d: configure h2: %w", cfg.NodeID, err)
	}
	tlsConfig.NextProtos = append([]string{"h2"}, tlsConfig.NextProtos...)

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	ln, err := tls.Listen("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("naive: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	s.listener = ln
	s.httpSrv = httpSrv
	s.cfg = cfg
	s.limits.Update(cfg.Users)
	s.users.Store(&users)

	go httpSrv.Serve(ln)
	return nil
}

// UpdateUsers swaps the live user set without closing the listener.
func (s *Server) UpdateUsers(users []protocol.User) error {
	s.limits.Update(users)
	m := buildAuthUsers(users)
	s.users.Store(&m)
	return nil
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	err := s.httpSrv.Close()
	s.listener = nil
	s.httpSrv = nil
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

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}

	userID, ok := s.checkAuth(r)
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="naive"`)
		http.Error(w, "", http.StatusProxyAuthRequired)
		return
	}
	s.online.Mark(userID, online.IPString(r.RemoteAddr))

	upstream, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	up, down := s.pipeH2(w, r.Body, s.limits.Limit(userID, upstream), flusher)
	if userID != 0 {
		c := s.counterFor(userID)
		c.upload.Add(up)
		c.download.Add(down)
	}
}

// pipeH2 relays an HTTP/2 CONNECT stream, whose request/response bodies
// stand in for a raw duplex connection (there is no net.Conn to hijack on
// an h2 stream, unlike HTTP/1.1 CONNECT).
func (s *Server) pipeH2(w http.ResponseWriter, reqBody io.ReadCloser, upstream net.Conn, flusher http.Flusher) (up, down uint64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(upstream, reqBody)
		up = uint64(n)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(flushWriter{w, flusher}, upstream)
		down = uint64(n)
	}()
	wg.Wait()
	return
}

// flushWriter flushes after every write so response bytes reach the h2
// client as they're produced instead of waiting for the stream to end.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

func (s *Server) checkAuth(r *http.Request) (int64, bool) {
	header := r.Header.Get("Proxy-Authorization")
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
	want, ok := s.currentUsers()[username]
	if !ok || want != password {
		return 0, false
	}
	return hashUsername(username), true
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
