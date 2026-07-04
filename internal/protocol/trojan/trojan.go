// Package trojan implements protocol.ProtocolServer for Trojan: a TLS
// listener where the first application-data message is
// SHA224(password)-hex + CRLF + a SOCKS5-style address request + CRLF,
// followed by the proxied payload.
package trojan

import (
	"bufio"
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

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
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

	counters sync.Map // int64 userID -> *userCounter
}

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
	if cfg.TLS.CertFile == "" || cfg.TLS.KeyFile == "" {
		return fmt.Errorf("trojan: node %d requires tls cert_file/key_file", cfg.NodeID)
	}

	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return fmt.Errorf("trojan: node %d: load cert: %w", cfg.NodeID, err)
	}

	users := buildTrojanUsers(cfg.Users)

	addr := net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	ln, err := tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return fmt.Errorf("trojan: node %d: listen %s: %w", cfg.NodeID, addr, err)
	}

	s.listener = ln
	s.cfg = cfg
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
		return // not a valid Trojan client; drop silently rather than fingerprint ourselves
	}

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

	up, down := relay.Pipe(conn, upstream)
	c := s.counterFor(userID)
	c.upload.Add(up + preUp)
	c.download.Add(down)
}

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
	m := buildTrojanUsers(users)
	s.users.Store(&m)
	return nil
}

func (s *Server) counterFor(userID int64) *userCounter {
	v, _ := s.counters.LoadOrStore(userID, &userCounter{})
	return v.(*userCounter)
}
