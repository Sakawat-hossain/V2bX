package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const bbrSysctlPath = "/etc/sysctl.d/99-v2bx-bbr.conf"

const bbrSysctl = `# Managed by v2bx — enable the BBR congestion control algorithm.
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
`

// EnableBBR turns on the BBR congestion control algorithm, which usually
// improves throughput on high-latency links. It's a no-op if BBR is already
// active. Requires a kernel new enough to ship BBR (Linux 4.9+).
func EnableBBR() error {
	if current := currentCongestion(); strings.EqualFold(current, "bbr") {
		fmt.Println("BBR is already active.")
		return nil
	}

	if err := os.WriteFile(bbrSysctlPath, []byte(bbrSysctl), 0o644); err != nil {
		return fmt.Errorf("write %s (try sudo): %w", bbrSysctlPath, err)
	}
	out, err := exec.Command("sysctl", "--system").CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply sysctl: %w\n%s", err, out)
	}

	if got := currentCongestion(); strings.EqualFold(got, "bbr") {
		fmt.Println("BBR enabled.")
		return nil
	}
	return fmt.Errorf("wrote %s but the kernel is still using %q — it may not support BBR (needs Linux 4.9+)", bbrSysctlPath, currentCongestion())
}

func currentCongestion() string {
	out, err := exec.Command("sysctl", "-n", "net.ipv4.tcp_congestion_control").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
