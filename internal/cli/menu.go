package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const repoURL = "https://github.com/Sakawat-hossain/V2bX"

// Menu is the interactive control panel shown when v2bx is run with no
// arguments — a numbered management menu over the same actions the
// subcommands expose, with a live service status line.
func Menu(configPath, version string) error {
	in := bufio.NewScanner(os.Stdin)
	for {
		printMenu(version)
		fmt.Print("Select [0-17]: ")
		if !in.Scan() {
			fmt.Println()
			return nil
		}

		var err error
		switch strings.TrimSpace(in.Text()) {
		case "1":
			err = Generate(configPath)
		case "2":
			err = AddNode(configPath)
		case "3":
			err = DeleteNode(configPath)
		case "4":
			err = EditConfig(configPath)
		case "5":
			err = StartService()
		case "6":
			err = StopService()
		case "7":
			err = RestartService()
		case "8":
			err = StatusService()
		case "9":
			err = TailLogs()
		case "10":
			err = EnableService()
		case "11":
			err = DisableService()
		case "12":
			err = X25519()
		case "13":
			err = EnableBBR()
		case "14":
			err = OpenFirewall()
		case "15":
			err = Update(version)
		case "16":
			err = Uninstall()
		case "17":
			fmt.Println("v2bx", version)
		case "0", "q", "quit", "exit":
			return nil
		case "":
			continue
		default:
			fmt.Println("Unknown option.")
			continue
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		pause(in)
	}
}

func printMenu(version string) {
	const rule = "  ──────────────────────────────"
	running := statusWord(ServiceActive(), "\033[32mrunning\033[0m", "\033[31mstopped\033[0m")
	autostart := statusWord(ServiceEnabled(), "enabled", "disabled")

	fmt.Printf(`
  V2bX %s
  %s
%s
   1) Generate config
   2) Add node
   3) Delete node
   4) Edit config
%s
   5) Start
   6) Stop
   7) Restart
   8) Status
   9) Logs
%s
  10) Enable on boot
  11) Disable on boot
%s
  12) X25519 key
  13) Install BBR
  14) Open all ports
%s
  15) Update
  16) Uninstall
  17) Version
%s
   0) Exit

  Service: %s     Autostart: %s
`, version, repoURL, rule, rule, rule, rule, rule, rule, running, autostart)
}

func statusWord(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

// pause waits for Enter so command output stays on screen before the menu
// redraws. Skipped when stdin isn't a terminal (e.g. piped input).
func pause(in *bufio.Scanner) {
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Print("\nPress Enter to return to the menu… ")
	in.Scan()
}
