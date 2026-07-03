package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"

	"github.com/Sakawat-hossain/V2bX/internal/config"
)

// EditConfig opens the config file in $EDITOR (falling back to nano then vi)
// and offers to restart the service afterward so edits take effect.
func EditConfig(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("%s doesn't exist yet — run 'generate' first.\n", path)
		return nil
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		for _, e := range []string{"nano", "vi"} {
			if _, err := exec.LookPath(e); err == nil {
				editor = e
				break
			}
		}
	}
	if editor == "" {
		return fmt.Errorf("no editor found; set $EDITOR or edit %s by hand", path)
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor exited: %w", err)
	}

	// Surface config mistakes immediately rather than at next start.
	if _, err := config.Load(path); err != nil {
		fmt.Fprintf(os.Stderr, "warning: config has a problem: %v\n", err)
	}
	offerRestart(bufio.NewScanner(os.Stdin))
	return nil
}

// OpenFirewall opens all inbound ports on the host. Node listen ports are
// assigned by the panel at runtime, so there's no fixed set to allow — this
// mirrors the "release all ports" helper operators expect, gated behind a
// clear confirmation since it lowers the firewall.
func OpenFirewall() error {
	fmt.Println("This opens ALL inbound ports on this host (it lowers the firewall).")
	fmt.Println("Only do this on a VPS where that's acceptable.")
	in := bufio.NewScanner(os.Stdin)
	if !askYesNo(in, "Continue?", false) {
		fmt.Println("Cancelled.")
		return nil
	}

	switch {
	case commandExists("ufw"):
		run("ufw", "--force", "reset")
		run("ufw", "default", "allow", "incoming")
		run("ufw", "default", "allow", "outgoing")
		run("ufw", "--force", "enable")
		fmt.Println("ufw set to allow all inbound.")
	case commandExists("firewall-cmd"):
		run("systemctl", "stop", "firewalld")
		run("systemctl", "disable", "firewalld")
		fmt.Println("firewalld stopped and disabled.")
	case commandExists("iptables"):
		for _, ipt := range []string{"iptables", "ip6tables"} {
			if commandExists(ipt) {
				run(ipt, "-P", "INPUT", "ACCEPT")
				run(ipt, "-P", "FORWARD", "ACCEPT")
				run(ipt, "-F")
			}
		}
		fmt.Println("iptables policies set to ACCEPT and rules flushed.")
	default:
		fmt.Println("No ufw/firewalld/iptables found — the host likely has no firewall to open.")
	}
	return nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func run(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	_ = cmd.Run()
}
