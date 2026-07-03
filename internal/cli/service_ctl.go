// Package cli implements the v2bx command-line interface: the "server"
// subcommand runs the agent in the foreground (what the systemd unit
// executes), while start/stop/restart/status/enable/disable/log/reload are
// thin wrappers around systemctl/journalctl for the "v2bx" unit.
package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
)

// UnitName is the systemd unit installed by install.sh.
const UnitName = "v2bx"

const (
	unitPath   = "/etc/systemd/system/v2bx.service"
	configDir  = "/etc/v2bx"
	binaryPath = "/usr/local/bin/v2bx"
)

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

// ServiceActive reports whether the systemd unit is currently running.
func ServiceActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", UnitName).Run() == nil
}

// ServiceEnabled reports whether the systemd unit is set to start on boot.
func ServiceEnabled() bool {
	return exec.Command("systemctl", "is-enabled", "--quiet", UnitName).Run() == nil
}

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

// Uninstall stops and disables the service, removes the systemd unit and the
// binary, and leaves the config in place (removal is offered separately so a
// reinstall keeps your panel details). It confirms before doing anything.
func Uninstall() error {
	fmt.Printf("This will stop v2bx, remove the systemd unit (%s), and delete the binary (%s).\n", unitPath, binaryPath)
	in := bufio.NewScanner(os.Stdin)
	if !askYesNo(in, "Continue?", false) {
		fmt.Println("Cancelled.")
		return nil
	}

	_ = runSystemctl("disable", "--now", UnitName)
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: remove %s: %v\n", unitPath, err)
	}
	_ = runSystemctl("daemon-reload")

	if askYesNo(in, fmt.Sprintf("Also delete the config directory %s?", configDir), false) {
		if err := os.RemoveAll(configDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove %s: %v\n", configDir, err)
		}
	} else {
		fmt.Printf("Kept %s.\n", configDir)
	}

	if err := os.Remove(binaryPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: remove %s: %v\n", binaryPath, err)
	}
	fmt.Println("Uninstalled.")
	return nil
}
