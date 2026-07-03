package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Sakawat-hossain/V2bX/internal/config"
)

// AddNode appends one node to an existing config via the same prompts the
// wizard uses, then offers to restart the service so the node comes up.
func AddNode(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load %s (run 'v2bx generate' first): %w", path, err)
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)

	entry, err := promptNode(in)
	if err != nil {
		return err
	}
	cfg.Nodes = append(cfg.Nodes, entry)

	if err := writeConfig(path, cfg); err != nil {
		return err
	}
	fmt.Printf("Added node %d (%s). %d node(s) total.\n", entry.NodeID, entry.NodeType, len(cfg.Nodes))
	offerRestart(in)
	return nil
}

// DeleteNode lists the configured nodes and removes the one chosen.
func DeleteNode(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	if len(cfg.Nodes) == 0 {
		fmt.Println("No nodes configured.")
		return nil
	}

	in := bufio.NewScanner(os.Stdin)
	fmt.Println("Configured nodes:")
	for i, n := range cfg.Nodes {
		state := "enabled"
		if !n.Enabled {
			state = "disabled"
		}
		fmt.Printf("  %2d) node %d — %s (%s)\n", i+1, n.NodeID, n.NodeType, state)
	}
	idx := askInt(in, "Remove which (number, 0 to cancel)", 0)
	if idx < 1 || idx > len(cfg.Nodes) {
		fmt.Println("Cancelled.")
		return nil
	}

	removed := cfg.Nodes[idx-1]
	cfg.Nodes = append(cfg.Nodes[:idx-1], cfg.Nodes[idx:]...)
	if len(cfg.Nodes) == 0 {
		return fmt.Errorf("refusing to remove the last node — a config needs at least one; edit %s or add a node first", path)
	}

	if err := writeConfig(path, cfg); err != nil {
		return err
	}
	fmt.Printf("Removed node %d (%s).\n", removed.NodeID, removed.NodeType)
	offerRestart(in)
	return nil
}

func writeConfig(path string, cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("resulting config is invalid: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func offerRestart(in *bufio.Scanner) {
	if !ServiceActive() {
		return
	}
	if askYesNo(in, "Restart the service to apply?", true) {
		if err := RestartService(); err != nil {
			fmt.Fprintln(os.Stderr, "restart failed:", err)
		}
	}
}
