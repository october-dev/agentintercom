package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/october-dev/october-bus/bus"
)

func workspaceCommand(args []string, out io.Writer) error {
	if len(args) == 2 && args[0] == "create" {
		result, err := createLocalScope(args[1])
		if err != nil {
			return err
		}
		// Owner credentials stay in the private data directory. Ordinary setup
		// output can safely be copied into an issue or an agent conversation.
		_, err = fmt.Fprintf(out, "Created local workspace %q.\nNext: october-bus connect --workspace %s\n", result.ScopeID, result.ScopeID)
		return err
	}
	if len(args) >= 1 && args[0] == "list" {
		flags := flag.NewFlagSet("workspace list", flag.ContinueOnError)
		jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("workspace list does not accept positional arguments")
		}
		paths, err := bus.DefaultDaemonPaths()
		if err != nil {
			return err
		}
		run, err := bus.ReadRunFile(paths.RunFile)
		if err != nil {
			return fmt.Errorf("local daemon is unavailable; run october-bus start: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		scopes, err := (bus.Client{Address: run.Address, Token: run.AdminToken}).ListScopes(ctx)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return json.NewEncoder(out).Encode(scopes)
		}
		for _, scope := range scopes {
			if _, err := fmt.Fprintln(out, scope.ScopeID); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("use workspace create <name> or workspace list [--json]")
}

type stdioConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// connectCommand emits standard configuration, without guessing at a host's
// config file or touching other MCP servers. The same config may be reused by
// concurrent host processes: each bridge registers its own session identity.
func connectCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "local workspace name")
	name := flags.String("name", "MCP session", "optional app or session label; no role required")
	format := flags.String("format", "json", "json (mcpServers), toml (Codex), or entry (server object)")
	command := flags.String("command", "", "absolute Bus executable path (defaults to this binary)")
	output := flags.String("output", "", "write a new configuration file; never overwrite")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *workspace == "" {
		return errors.New("connect requires --workspace <name> and no positional arguments")
	}
	if *format != "json" && *format != "toml" && *format != "entry" {
		return errors.New("--format must be json, toml, or entry")
	}
	if strings.TrimSpace(*name) == "" || len(*name) > 256 {
		return errors.New("--name must contain 1 to 256 bytes")
	}
	if *command == "" {
		var err error
		*command, err = os.Executable()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(*command) {
		return errors.New("--command must be an absolute executable path")
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	dataDir, err := filepath.Abs(paths.DataDir)
	if err != nil {
		return err
	}
	runtimeDir, err := filepath.Abs(paths.RuntimeDir)
	if err != nil {
		return err
	}
	// Some hosts expand environment or shell expressions inside configuration.
	// Do not generate values with a different meaning when loaded by the host.
	for _, value := range []string{*command, *workspace, *name, dataDir, runtimeDir} {
		if strings.ContainsAny(value, "$`\x00\r\n") {
			return errors.New("configuration values cannot contain shell expansion or control characters")
		}
	}
	owner, err := localScopeClientAt(paths, *workspace)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := owner.ListAgents(ctx); err != nil {
		return fmt.Errorf("workspace is unavailable: %w", err)
	}
	entry := stdioConfig{Command: *command, Args: []string{
		"mcp", "stdio", "--scope", *workspace, "--new-session", "--name", *name,
		"--data-dir", dataDir, "--runtime-dir", runtimeDir,
	}}
	var data []byte
	switch *format {
	case "toml":
		// JSON strings/arrays are valid for these TOML basic values. No shell
		// command string is assembled or executed.
		encodedCommand, _ := json.Marshal(entry.Command)
		encodedArgs, _ := json.Marshal(entry.Args)
		data = []byte(fmt.Sprintf("[mcp_servers.october_bus]\ncommand = %s\nargs = %s\nstartup_timeout_sec = 20\ntool_timeout_sec = 60\ndefault_tools_approval_mode = \"prompt\"\n", encodedCommand, encodedArgs))
	case "entry":
		data, err = json.MarshalIndent(entry, "", "  ")
	case "json":
		data, err = json.MarshalIndent(map[string]any{"mcpServers": map[string]stdioConfig{"october_bus": entry}}, "", "  ")
	}
	if err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if *output == "" {
		_, err = out.Write(data)
		return err
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create new configuration (existing files are never overwritten): %w", err)
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			_ = os.Remove(*output)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	complete = true
	_, err = fmt.Fprintf(out, "Created %q. Add the October Bus entry to your app's MCP settings, then reconnect it.\n", *output)
	return err
}

func sessionsCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("sessions", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "local workspace name")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *workspace == "" {
		return errors.New("sessions requires --workspace <name> and no positional arguments")
	}
	owner, err := localScopeClient(*workspace)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agents, err := owner.ListAgents(ctx)
	if err != nil {
		return err
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	if *jsonOutput {
		return json.NewEncoder(out).Encode(agents)
	}
	if len(agents) == 0 {
		_, err = fmt.Fprintln(out, "No connected sessions. Use october-bus connect --workspace", *workspace)
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION\tNAME\tCONNECTION\tLIFECYCLE")
	for _, agent := range agents {
		connection := "offline"
		if agent.Reachable {
			connection = "connected"
		}
		// Quote untrusted display text so it cannot control the terminal.
		fmt.Fprintf(w, "%s\t%q\t%s\t%s\n", agent.ID, agent.DisplayName, connection, agent.Lifecycle)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Connected does not mean the app can wake automatically. MCP sessions receive work when the app checks its inbox.")
	return err
}
