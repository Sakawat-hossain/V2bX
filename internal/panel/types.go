package panel

// NodeConfigResponse is the payload returned by GET {config_path}. Field
// coverage follows the UniProxy contract shared across V2board-family panels
// (XBoard, V2Board, and compatible forks): protocol type, listen settings,
// and any protocol-specific options the panel wants to push down.
//
// The panel reports the listen port as "server_port"; a few older forks used
// "port", so both are accepted and ListenPort() prefers whichever is set.
type NodeConfigResponse struct {
	Protocol   string `json:"protocol,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
	Port       int    `json:"port,omitempty"`
	Cipher     string `json:"cipher,omitempty"`

	// ServerKey is the node-level PSK for Shadowsocks-2022 ciphers (empty for
	// classic ciphers).
	ServerKey string `json:"server_key,omitempty"`

	Host       string `json:"host,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	TLS        int    `json:"tls,omitempty"` // 0=none 1=tls 2=reality/xtls, panel-defined

	NetworkSettings map[string]any `json:"networkSettings,omitempty"`
	TLSSettings     map[string]any `json:"tls_settings,omitempty"`

	// BaseConfig carries the panel's suggested push/pull intervals.
	BaseConfig struct {
		PushInterval int `json:"push_interval"`
		PullInterval int `json:"pull_interval"`
	} `json:"base_config"`
}

// ListenPort returns the port the node should listen on, preferring the
// canonical server_port field.
func (r NodeConfigResponse) ListenPort() int {
	if r.ServerPort != 0 {
		return r.ServerPort
	}
	return r.Port
}

// UserResponse is a single entry from GET {user_path}. The panel does not
// send a distinct password — for Shadowsocks/Trojan/etc. the user's UUID is
// the credential, so Password is usually empty and callers fall back to UUID.
type UserResponse struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	Password    string `json:"password,omitempty"`
	Flow        string `json:"flow,omitempty"`
	SpeedLimit  uint64 `json:"speed_limit"`
	DeviceLimit int    `json:"device_limit"`
}

// UserListResponse wraps the user list; some panels nest it under "users",
// others return a bare array — Unmarshal handles both.
type UserListResponse struct {
	Users []UserResponse `json:"users"`
}

// TrafficRecord is one user's usage delta submitted via POST {push_path}.
// It's serialized into the panel's `{"<uid>": [upload, download]}` shape by
// PushTraffic.
type TrafficRecord struct {
	UID      int64
	Upload   uint64
	Download uint64
}

// AliveRecord reports one online user's connection IP via POST {alive_path},
// serialized into the panel's `{"<uid>": ["<ip>", ...]}` shape.
type AliveRecord struct {
	UID int64
	IP  string
}
