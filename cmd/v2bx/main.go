// Command v2bx is a multi-protocol node agent for V2board-family panels
// (XBoard, V2Board, and compatible forks).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Sakawat-hossain/V2bX/internal/cli"

	// Protocol backends register themselves via init(); import for side effects.
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/anytls"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/httpproxy"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/hysteria"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/hysteria2"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/mieru"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/naive"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/shadowsocks"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/socks5"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/trojan"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/tuic"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/vless"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/vmess"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

const defaultConfigPath = "/etc/v2bx/config.json"

func main() {
	// No arguments opens the interactive menu, so a new operator can just run
	// `v2bx` and pick what they need.
	if len(os.Args) < 2 {
		if err := cli.Menu(defaultConfigPath, Version); err != nil {
			fmt.Fprintln(os.Stderr, "v2bx:", err)
			os.Exit(1)
		}
		return
	}

	var err error
	switch os.Args[1] {
	case "server":
		fs := flag.NewFlagSet("server", flag.ExitOnError)
		configPath := fs.String("c", defaultConfigPath, "path to config.json")
		fs.Parse(os.Args[2:])
		err = cli.RunServer(*configPath)
	case "menu":
		err = cli.Menu(defaultConfigPath, Version)
	case "generate":
		fs := flag.NewFlagSet("generate", flag.ExitOnError)
		configPath := fs.String("c", defaultConfigPath, "path to write config.json")
		fs.Parse(os.Args[2:])
		err = cli.Generate(*configPath)
	case "start":
		err = cli.StartService()
	case "stop":
		err = cli.StopService()
	case "restart":
		err = cli.RestartService()
	case "status":
		err = cli.StatusService()
	case "enable":
		err = cli.EnableService()
	case "disable":
		err = cli.DisableService()
	case "reload":
		err = cli.ReloadService()
	case "log":
		err = cli.TailLogs()
	case "update":
		err = cli.Update(Version)
	case "x25519":
		err = cli.X25519()
	case "uninstall":
		err = cli.Uninstall()
	case "version":
		fmt.Println("v2bx", Version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "v2bx: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "v2bx:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `v2bx - multi-protocol node agent for V2board-family panels

Run v2bx with no arguments to open the interactive menu.

Usage:
  v2bx                                     open the interactive menu
  v2bx server [-c /etc/v2bx/config.json]   run the agent in the foreground
  v2bx generate [-c PATH]                  interactive config wizard
  v2bx start                               start the v2bx systemd service
  v2bx stop                                stop the v2bx systemd service
  v2bx restart                             restart the v2bx systemd service
  v2bx status                              show systemd service status
  v2bx enable                              enable the service at boot
  v2bx disable                             disable the service at boot
  v2bx reload                              force an immediate panel resync
  v2bx log                                 follow the service journal
  v2bx update                              update to the latest release
  v2bx x25519                              generate an X25519 key pair
  v2bx uninstall                           remove the service and binary
  v2bx version                             print version information
`)
}
