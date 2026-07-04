package metrics

import (
	"strings"
	"testing"

	"github.com/Sakawat-hossain/V2bX/internal/service"
)

func TestWriteExposition(t *testing.T) {
	s := service.Snapshot{
		PushOK: 5, PushFail: 1, SyncOK: 10, SyncFail: 0,
		Nodes: []service.NodeSnapshot{
			{NodeID: 208, NodeType: "shadowsocks", Users: 3, Online: 2, Upload: 100, Download: 200},
		},
	}
	var b strings.Builder
	Write(&b, s, "v1.4.0")
	out := b.String()

	want := []string{
		"# TYPE v2bx_up gauge",
		"v2bx_up 1",
		`v2bx_build_info{version="v1.4.0"} 1`,
		`v2bx_panel_push_total{result="ok"} 5`,
		`v2bx_panel_push_total{result="fail"} 1`,
		`v2bx_panel_sync_total{result="ok"} 10`,
		`v2bx_node_users{node_id="208",node_type="shadowsocks"} 3`,
		`v2bx_node_online_users{node_id="208",node_type="shadowsocks"} 2`,
		`v2bx_node_traffic_bytes_total{direction="up",node_id="208",node_type="shadowsocks"} 100`,
		`v2bx_node_traffic_bytes_total{direction="down",node_id="208",node_type="shadowsocks"} 200`,
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\n---\n%s", w, out)
		}
	}
}

func TestEscapeLabelValue(t *testing.T) {
	if got := escape(`a"b\c`); got != `a\"b\\c` {
		t.Fatalf("escape = %q", got)
	}
}
