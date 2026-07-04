// Package porthop installs the firewall NAT rule that makes UDP "port
// hopping" work for the QUIC protocols (Hysteria/Hysteria2/TUIC): the client
// sprays packets across a port range to evade per-flow throttling and
// single-port blocking, and the host redirects that whole range to the one
// port the node actually listens on.
//
// It shells out to iptables/ip6tables. Installation is best-effort and
// idempotent (any prior rule with the same tag is removed first), so a node
// still serves on its base port even if the firewall can't be programmed.
package porthop

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// MaybeInstall installs a port-hopping rule when rangeSpec is non-empty,
// logging the outcome. A failure (no iptables, not root) is non-fatal: it
// returns nil and the node keeps serving on its base port. The nil result is
// safe to call Remove on.
func MaybeInstall(nodeID int64, rangeSpec string, toPort int) *Redirect {
	if rangeSpec == "" {
		return nil
	}
	r, err := Install(nodeID, rangeSpec, toPort)
	if err != nil {
		slog.Warn("port hopping not active", "node_id", nodeID, "error", err)
		return nil
	}
	slog.Info("port hopping active", "node_id", nodeID, "rule", r.Summary())
	return r
}

// Redirect is an installed port-hopping rule that can be removed later.
type Redirect struct {
	start, end int
	toPort     int
	comment    string
	families   []string // "ip4"/"ip6" that were successfully programmed
}

// ParseRange accepts "20000-40000" or "20000:40000" and returns the bounds.
func ParseRange(spec string) (start, end int, err error) {
	sep := "-"
	if strings.Contains(spec, ":") {
		sep = ":"
	}
	parts := strings.SplitN(spec, sep, 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("porthop: invalid range %q (want start-end)", spec)
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("porthop: non-numeric range %q", spec)
	}
	if start < 1 || end > 65535 || start > end {
		return 0, 0, fmt.Errorf("porthop: range %q out of bounds", spec)
	}
	return start, end, nil
}

// rule builds the iptables argument vector for one chain operation ("-A"/"-D"),
// as a pure function so it can be tested without touching the firewall.
func rule(op string, start, end, toPort int, comment string) []string {
	return []string{
		"-t", "nat", op, "PREROUTING",
		"-p", "udp",
		"--dport", fmt.Sprintf("%d:%d", start, end),
		"-j", "REDIRECT", "--to-ports", strconv.Itoa(toPort),
		"-m", "comment", "--comment", comment,
	}
}

func binaries() map[string]string {
	return map[string]string{"ip4": "iptables", "ip6": "ip6tables"}
}

// Install redirects the UDP range to toPort for the given node. It removes any
// existing rule with the same tag first, so restarts don't stack duplicates.
// Returns a handle even when only some families were programmed; err is set
// only when nothing could be installed.
func Install(nodeID int64, rangeSpec string, toPort int) (*Redirect, error) {
	start, end, err := ParseRange(rangeSpec)
	if err != nil {
		return nil, err
	}
	comment := fmt.Sprintf("v2bx-node-%d", nodeID)
	r := &Redirect{start: start, end: end, toPort: toPort, comment: comment}

	var lastErr error
	for fam, bin := range binaries() {
		path, err := exec.LookPath(bin)
		if err != nil {
			lastErr = err
			continue
		}
		// Idempotency: delete any stale copy (ignore failure), then add.
		_ = exec.Command(path, rule("-D", start, end, toPort, comment)...).Run()
		if out, err := exec.Command(path, rule("-A", start, end, toPort, comment)...).CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(string(out)))
			continue
		}
		r.families = append(r.families, fam)
	}
	if len(r.families) == 0 {
		return nil, fmt.Errorf("porthop: could not install any rule: %w", lastErr)
	}
	return r, nil
}

// Remove deletes the rules Install added. Safe to call once.
func (r *Redirect) Remove() {
	if r == nil {
		return
	}
	bins := binaries()
	for _, fam := range r.families {
		if path, err := exec.LookPath(bins[fam]); err == nil {
			_ = exec.Command(path, rule("-D", r.start, r.end, r.toPort, r.comment)...).Run()
		}
	}
	r.families = nil
}

// Summary describes what was installed, for logging.
func (r *Redirect) Summary() string {
	if r == nil || len(r.families) == 0 {
		return "not installed"
	}
	return fmt.Sprintf("udp %d:%d -> %d (%s)", r.start, r.end, r.toPort, strings.Join(r.families, ","))
}
