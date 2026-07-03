// Package mieru implements protocol.ProtocolServer for Mieru, backed by the
// enfein/mieru v3 embedding API (apis/server). Mieru terminates its own
// obfuscated session layer and hands us decrypted SOCKS5-shaped requests via
// Accept(); we dial the requested destination and relay.
package mieru

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	"github.com/enfein/mieru/v3/apis/model"
	miserver "github.com/enfein/mieru/v3/apis/server"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/proto"

	"github.com/Sakawat-hossain/V2bX/internal/protocol"
	"github.com/Sakawat-hossain/V2bX/internal/relay"
)

func init() {
	protocol.Register("mieru", func() protocol.ProtocolServer { return New() })
}

// Server is a Mieru protocol.ProtocolServer. Zero value is ready to use.
type Server struct {
	mu      sync.Mutex
	server  miserver.Server
	cfg     protocol.NodeConfig
	running bool
	// usersByName maps the mieru username (we use the panel UUID) back to the
	// panel user ID so Accept()'d connections attribute traffic correctly.
	usersByName map[string]int64

	counters sync.Map // int64 userID -> *userCounter
}

type userCounter struct {
	upload   atomic.Uint64
	download atomic.Uint64
}

func New() *Server { return &Server{} }

func (s *Server) Name() string { return "mieru" }

// Start builds a mieru server config from cfg and begins accepting sessions.
// The transport defaults to TCP; set Extra["transport"]="UDP" to bind a UDP
// mieru transport instead. Each user's UUID is used as the mieru username.
func (s *Server) Start(cfg protocol.NodeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("mieru: node %d already started", cfg.NodeID)
	}
	if len(cfg.Users) == 0 {
		return fmt.Errorf("mieru: node %d has no users configured", cfg.NodeID)
	}

	transport := appctlpb.TransportProtocol_TCP.Enum()
	if v, ok := cfg.Extra["transport"].(string); ok && strings.EqualFold(v, "UDP") {
		transport = appctlpb.TransportProtocol_UDP.Enum()
	}

	usersByName := make(map[string]int64, len(cfg.Users))
	pbUsers := make([]*appctlpb.User, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		name := u.UUID
		if name == "" {
			return fmt.Errorf("mieru: node %d: user %d has no uuid to use as mieru username", cfg.NodeID, u.ID)
		}
		usersByName[name] = u.ID
		pbUsers = append(pbUsers, &appctlpb.User{
			Name:     proto.String(name),
			Password: proto.String(u.Password),
		})
	}

	srv := miserver.NewServer()
	if err := srv.Store(&miserver.ServerConfig{
		Config: &appctlpb.ServerConfig{
			PortBindings: []*appctlpb.PortBinding{
				{Port: proto.Int32(int32(cfg.Port)), Protocol: transport},
			},
			Users: pbUsers,
		},
	}); err != nil {
		return fmt.Errorf("mieru: node %d: store config: %w", cfg.NodeID, err)
	}
	if err := srv.Start(); err != nil {
		return fmt.Errorf("mieru: node %d: start: %w", cfg.NodeID, err)
	}

	s.server = srv
	s.cfg = cfg
	s.usersByName = usersByName
	s.running = true

	go s.acceptLoop(srv)
	return nil
}

func (s *Server) acceptLoop(srv miserver.Server) {
	for {
		conn, req, err := srv.Accept()
		if err != nil {
			// Accept returns an error once the server is stopped.
			return
		}
		go s.handle(conn, req)
	}
}

func (s *Server) handle(conn net.Conn, req *model.Request) {
	defer conn.Close()

	var userID int64
	if uc, ok := conn.(apicommon.UserContext); ok {
		userID = s.usersByName[uc.UserName()]
	}

	// Only TCP CONNECT is relayed; UDP associate is declined for now.
	if req.Command != constant.Socks5ConnectCmd {
		_ = writeReply(conn, constant.Socks5ReplyCommandNotSupported)
		return
	}

	upstream, err := net.Dial("tcp", req.DstAddr.String())
	if err != nil {
		_ = writeReply(conn, constant.Socks5ReplyConnectionRefused)
		return
	}
	defer upstream.Close()

	local := upstream.LocalAddr().(*net.TCPAddr)
	resp := &model.Response{
		Reply:    constant.Socks5ReplySuccess,
		BindAddr: model.AddrSpec{IP: local.IP, Port: local.Port},
	}
	if err := resp.WriteToSocks5(conn); err != nil {
		return
	}

	up, down := relay.Pipe(conn, upstream)
	if userID != 0 {
		c := s.counterFor(userID)
		c.upload.Add(up)
		c.download.Add(down)
	}
}

func writeReply(conn net.Conn, reply byte) error {
	resp := &model.Response{Reply: reply, BindAddr: model.AddrSpec{IP: net.IPv4zero, Port: 0}}
	return resp.WriteToSocks5(conn)
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return nil
	}
	err := s.server.Stop()
	s.server = nil
	s.running = false
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
