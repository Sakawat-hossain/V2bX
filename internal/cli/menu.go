package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Menu is the interactive control panel shown when v2bx is run with no
// arguments. It's a thin dispatcher over the same actions the subcommands
// expose, for operators who'd rather pick from a list than memorize verbs.
func Menu(configPath, version string) error {
	in := bufio.NewScanner(os.Stdin)
	for {
		printMenu(version)
		fmt.Print("Select an option: ")
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
			err = Generate(configPath)
		case "7":
			err = EnableService()
		case "8":
			err = DisableService()
		case "9":
			err = ReloadService()
		case "10":
			err = Update(version)
		case "11":
			err = X25519()
		case "12":
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
	fmt.Printf(`
 V2bX %s

   Service          Setup
   1) start          6) generate config
   2) stop           7) enable on boot
   3) restart        8) disable on boot
   4) status         9) reload (resync panel)
   5) logs          10) update to latest
                    11) x25519 keypair
                    12) uninstall

   0) exit
`, version)
}
