// Package cli implements the v2bx command-line interface: the "server"
// subcommand runs the agent in the foreground (what the systemd unit
// executes), while start/stop/restart/status/enable/disable/log/reload are
// thin wrappers around systemctl/journalctl for the "v2bx" unit.
package cli

import (
	"fmt"
	"os"
	"os/exec"
)

// UnitName is the systemd unit installed by install.sh.
const UnitName = "v2bx"

func runSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %v: %w", args, err)
	}
	return nil
}

func StartService() error   { return runSystemctl("start", UnitName) }
func StopService() error    { return runSystemctl("stop", UnitName) }
func RestartService() error { return runSystemctl("restart", UnitName) }
func StatusService() error  { return runSystemctl("status", UnitName, "--no-pager") }
func EnableService() error  { return runSystemctl("enable", UnitName) }
func DisableService() error { return runSystemctl("disable", UnitName) }

// ReloadService asks the running agent to resync with the panel immediately
// without dropping active connections, via SIGHUP.
func ReloadService() error {
	cmd := exec.Command("systemctl", "kill", "-s", "HUP", UnitName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("reload %s: %w", UnitName, err)
	}
	return nil
}

// TailLogs streams the unit's journal, following new entries like `tail -f`.
func TailLogs() error {
	cmd := exec.Command("journalctl", "-u", UnitName, "-f", "--no-pager")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
