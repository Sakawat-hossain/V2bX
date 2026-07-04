package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Sakawat-hossain/V2bX/internal/config"
)

// Generate runs an interactive wizard that asks for panel and node details
// and writes a ready-to-use config.json to path. It never hand-waves past a
// TLS-required node without collecting a cert, so the resulting file starts
// cleanly.
func Generate(path string) error {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)

	fmt.Println("========================================")
	fmt.Println(" V2bX config wizard")
	fmt.Println("========================================")
	fmt.Println(" Works with XBoard, V2Board, and any panel that speaks the")
	fmt.Println(" UniProxy node API. Press Enter to accept the [default].")
	fmt.Println()

	if _, err := os.Stat(path); err == nil {
		if !askYesNo(in, fmt.Sprintf("%s already exists. Overwrite it?", path), false) {
			fmt.Println("Left the existing config untouched.")
			return nil
		}
	}

	var cfg config.Config
	// Log defaults are filled in by Validate; no need to ask.

	fmt.Println("Panel connection")
	cfg.Panel.ApiHost = askRequired(in, "  Panel URL (e.g. https://panel.example.com)")
	cfg.Panel.ApiKey = askRequired(in, "  Panel API key / node token")
	fmt.Println()

	fmt.Println("Nodes — add one for each node ID this server should run.")
	for {
		entry, err := promptNode(in)
		if err != nil {
			return err
		}
		cfg.Nodes = append(cfg.Nodes, entry)
		fmt.Println()
		if !askYesNo(in, "Add another node?", false) {
			break
		}
		fmt.Println()
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("the answers produced an invalid config: %w", err)
	}

	data, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	fmt.Printf("\nWrote %s with %d node(s). Start the agent with: v2bx start\n", path, len(cfg.Nodes))
	return nil
}

// commonNodeTypes are the protocols most panels hand out; the wizard lists
// these first, with everything else behind "more".
var commonNodeTypes = []string{"shadowsocks", "vless", "vmess", "trojan", "hysteria2"}

func promptNode(in *bufio.Scanner) (config.NodeEntry, error) {
	var e config.NodeEntry
	e.NodeID = int64(askInt(in, "  Node ID (must match the panel)", 0))

	choices := append(append([]string{}, commonNodeTypes...), "more…")
	pick := askChoice(in, "  Protocol", choices, "shadowsocks")
	if pick == "more…" {
		pick = askChoice(in, "  Protocol (all)", config.NodeTypes, "shadowsocks")
	}
	e.NodeType = pick

	e.ListenIP = ask(in, "  Listen IP", "0.0.0.0")

	// TLS: forced on for protocols that require it, otherwise offered.
	wantTLS := config.TLSRequired(e.NodeType)
	if wantTLS {
		fmt.Printf("  (%s always uses TLS)\n", e.NodeType)
	} else {
		wantTLS = askYesNo(in, "  Configure TLS for this node?", false)
	}
	if wantTLS {
		e.CertMode = "self"
		fmt.Println("  Provide a certificate, or leave both blank to auto-generate a self-signed one.")
		e.CertFile = ask(in, "  Certificate file path (PEM)", "")
		if e.CertFile != "" {
			e.KeyFile = askRequired(in, "  Private key file path (PEM)")
		}
	} else {
		e.CertMode = "none"
	}

	e.Enabled = askYesNo(in, "  Enable this node now?", true)
	return e, nil
}

// --- prompt helpers ---

func readLine(in *bufio.Scanner) string {
	if !in.Scan() {
		return ""
	}
	return strings.TrimSpace(in.Text())
}

func ask(in *bufio.Scanner, question, def string) string {
	fmt.Printf("%s [%s]: ", question, def)
	v := readLine(in)
	if v == "" {
		return def
	}
	return v
}

func askRequired(in *bufio.Scanner, question string) string {
	for {
		fmt.Printf("%s: ", question)
		if v := readLine(in); v != "" {
			return v
		}
		fmt.Println("  (required)")
	}
}

func askInt(in *bufio.Scanner, question string, def int) int {
	for {
		v := ask(in, question, strconv.Itoa(def))
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
		fmt.Println("  (enter a number)")
	}
}

func askYesNo(in *bufio.Scanner, question string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	for {
		fmt.Printf("%s [%s]: ", question, d)
		v := strings.ToLower(readLine(in))
		switch v {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		fmt.Println("  (answer y or n)")
	}
}

// askChoice presents a numbered list; the user may type a number or the value
// itself. An optional default is used on empty input.
func askChoice(in *bufio.Scanner, question string, choices []string, def ...string) string {
	fallback := ""
	if len(def) > 0 {
		fallback = def[0]
	}
	fmt.Printf("%s:\n", question)
	for i, c := range choices {
		marker := " "
		if c == fallback {
			marker = "*"
		}
		fmt.Printf("   %s %2d) %s\n", marker, i+1, c)
	}
	for {
		prompt := "  choose"
		if fallback != "" {
			prompt = fmt.Sprintf("  choose [%s]", fallback)
		}
		fmt.Printf("%s: ", prompt)
		v := readLine(in)
		if v == "" && fallback != "" {
			return fallback
		}
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(choices) {
			return choices[n-1]
		}
		for _, c := range choices {
			if strings.EqualFold(c, v) {
				return c
			}
		}
		fmt.Println("  (pick a number from the list or type the name)")
	}
}
