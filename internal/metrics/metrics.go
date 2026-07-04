// Package metrics renders the agent's state in the Prometheus text exposition
// format. It's hand-rolled (no client library) to keep the module's
// dependency footprint small — the format is simple and stable.
package metrics

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Sakawat-hossain/V2bX/internal/service"
)

// Handler serves /metrics, rendering a live snapshot on each scrape. version
// labels the build_info metric.
func Handler(snapshot func() service.Snapshot, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		Write(w, snapshot(), version)
	})
}

// Write emits the exposition format for one snapshot.
func Write(w io.Writer, s service.Snapshot, version string) {
	bw := &strings.Builder{}

	metric(bw, "v2bx_up", "gauge", "1 if the agent process is running.")
	line(bw, "v2bx_up", nil, 1)

	metric(bw, "v2bx_build_info", "gauge", "Agent build information.")
	line(bw, "v2bx_build_info", labels{"version": version}, 1)

	metric(bw, "v2bx_panel_push_total", "counter", "Panel traffic-push attempts by result.")
	line(bw, "v2bx_panel_push_total", labels{"result": "ok"}, float64(s.PushOK))
	line(bw, "v2bx_panel_push_total", labels{"result": "fail"}, float64(s.PushFail))

	metric(bw, "v2bx_panel_sync_total", "counter", "Panel config/user sync attempts by result.")
	line(bw, "v2bx_panel_sync_total", labels{"result": "ok"}, float64(s.SyncOK))
	line(bw, "v2bx_panel_sync_total", labels{"result": "fail"}, float64(s.SyncFail))

	metric(bw, "v2bx_node_users", "gauge", "Configured users per node.")
	for _, n := range s.Nodes {
		line(bw, "v2bx_node_users", nodeLabels(n), float64(n.Users))
	}

	metric(bw, "v2bx_node_online_users", "gauge", "Users with a recent connection per node.")
	for _, n := range s.Nodes {
		line(bw, "v2bx_node_online_users", nodeLabels(n), float64(n.Online))
	}

	metric(bw, "v2bx_node_traffic_bytes_total", "counter", "Cumulative bytes relayed per node and direction.")
	for _, n := range s.Nodes {
		up := nodeLabels(n)
		up["direction"] = "up"
		line(bw, "v2bx_node_traffic_bytes_total", up, float64(n.Upload))
		down := nodeLabels(n)
		down["direction"] = "down"
		line(bw, "v2bx_node_traffic_bytes_total", down, float64(n.Download))
	}

	io.WriteString(w, bw.String())
}

type labels map[string]string

func nodeLabels(n service.NodeSnapshot) labels {
	return labels{"node_id": strconv.FormatInt(n.NodeID, 10), "node_type": n.NodeType}
}

func metric(w *strings.Builder, name, typ, help string) {
	w.WriteString("# HELP ")
	w.WriteString(name)
	w.WriteByte(' ')
	w.WriteString(help)
	w.WriteString("\n# TYPE ")
	w.WriteString(name)
	w.WriteByte(' ')
	w.WriteString(typ)
	w.WriteByte('\n')
}

func line(w *strings.Builder, name string, l labels, value float64) {
	w.WriteString(name)
	if len(l) > 0 {
		w.WriteByte('{')
		first := true
		for _, k := range sortedKeys(l) {
			if !first {
				w.WriteByte(',')
			}
			first = false
			w.WriteString(k)
			w.WriteString(`="`)
			w.WriteString(escape(l[k]))
			w.WriteByte('"')
		}
		w.WriteByte('}')
	}
	w.WriteByte(' ')
	w.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	w.WriteByte('\n')
}

// sortedKeys keeps label order stable across scrapes.
func sortedKeys(l labels) []string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	// small maps; insertion sort keeps it dependency-free
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

func escape(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n")
	return r.Replace(v)
}
