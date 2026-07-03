// Command v2bx is a multi-protocol node agent for V2board-family panels
// (XBoard, V2Board, and compatible forks).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Sakawat-hossain/V2bX/internal/cli"

	// Protocol backends register themselves via init(); import for side effects.
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/httpproxy"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/hysteria"
	_ "github.com/Sakawat-hossain/V2bX/internal/protocol/hysteria2"
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
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "server":
		fs := flag.NewFlagSet("server", flag.ExitOnError)
		configPath := fs.String("c", defaultConfigPath, "path to config.json")
		fs.Parse(os.Args[2:])
		err = cli.RunServer(*configPath)
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

Usage:
  v2bx server [-c /etc/v2bx/config.json]   run the agent in the foreground
  v2bx start                               start the v2bx systemd service
  v2bx stop                                stop the v2bx systemd service
  v2bx restart                             restart the v2bx systemd service
  v2bx status                              show systemd service status
  v2bx enable                              enable the service at boot
  v2bx disable                             disable the service at boot
  v2bx reload                              force an immediate panel resync
  v2bx log                                 follow the service journal
  v2bx version                             print version information
`)
}
