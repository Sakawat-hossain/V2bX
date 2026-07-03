package panel

// NodeConfigResponse is the payload returned by GET {config_path}. Field
// coverage follows the UniProxy contract shared across V2board-family panels
// (XBoard, V2Board, and compatible forks): protocol type, listen settings,
// and any protocol-specific options the panel wants to push down.
type NodeConfigResponse struct {
	NodeID   int64  `json:"node_id"`
	NodeType string `json:"node_type"`
	Port     int    `json:"port"`
	Cipher   string `json:"cipher,omitempty"`

	Host            string         `json:"host,omitempty"`
	ServerName      string         `json:"server_name,omitempty"`
	TLS             int            `json:"tls,omitempty"` // 0=none 1=tls 2=reality/xtls, panel-defined
	NetworkSettings map[string]any `json:"network_settings,omitempty"`

	// Raw carries any fields the panel sends that this client doesn't model
	// explicitly yet, so protocol-specific config isn't silently dropped.
	Raw map[string]any `json:"-"`
}

// UserResponse is a single entry from GET {user_path}.
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
type TrafficRecord struct {
	UID      int64  `json:"user_id"`
	Upload   uint64 `json:"u"`
	Download uint64 `json:"d"`
}

// AliveRecord reports one online user via POST {alive_path}.
type AliveRecord struct {
	UID int64  `json:"user_id"`
	IP  string `json:"ip"`
}
