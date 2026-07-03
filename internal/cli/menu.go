package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const repoURL = "https://github.com/Sakawat-hossain/V2bX"

// Menu is the interactive control panel shown when v2bx is run with no
// arguments. It's a thin dispatcher over the same actions the subcommands
// expose, for operators who'd rather pick from a numbered list.
func Menu(configPath, version string) error {
	in := bufio.NewScanner(os.Stdin)
	for {
		printMenu(version)
		fmt.Print("Select [0-14]: ")
		if !in.Scan() {
			fmt.Println()
			return nil
		}
		choice := strings.TrimSpace(in.Text())

		var err error
		switch choice {
		case "1":
			err = StartService()
		case "2":
			err = StopService()
		case "3":
			err = RestartService()
		case "4":
			err = StatusService()
		case "5":
			err = TailLogs()
		case "6":
			err = EnableService()
		case "7":
			err = DisableService()
		case "8":
			err = Generate(configPath)
		case "9":
			err = AddNode(configPath)
		case "10":
			err = DeleteNode(configPath)
		case "11":
			err = X25519()
		case "12":
			err = EnableBBR()
		case "13":
			err = Update(version)
		case "14":
			err = Uninstall()
		case "0", "q", "quit", "exit":
			return nil
		case "":
			continue
		default:
			fmt.Printf("Unknown option %q.\n", choice)
			continue
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		fmt.Println()
	}
}

func printMenu(version string) {
	const rule = "  ────────────────────────────────"
	running := statusWord(ServiceActive(), "running", "stopped")
	autostart := statusWord(ServiceEnabled(), "enabled", "disabled")

	fmt.Printf(`
  V2bX %s
  %s
%s
   1) Start                8) Generate config
   2) Stop                 9) Add a node
   3) Restart             10) Delete a node
   4) Status             ──────────────────
   5) Logs               11) X25519 keypair
  ──────────────────     12) Enable BBR
   6) Enable on boot     ──────────────────
   7) Disable on boot    13) Update
                         14) Uninstall
%s
   0) Exit

  Service: %s    Autostart: %s
`, version, repoURL, rule, rule, running, autostart)
}

func statusWord(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}
