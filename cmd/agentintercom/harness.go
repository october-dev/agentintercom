package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/agent-intercom/adapters"
	"github.com/october-dev/agent-intercom/bus"
)

func harness(args []string) error {
	if len(args) == 1 && args[0] == "list" {
		all, err := adapters.List()
		if err != nil {
			return err
		}
		for _, adapter := range all {
			fmt.Printf("%s\t%s\t%s\n", adapter.Directory, adapter.HarnessFamily, adapter.Status)
		}
		return nil
	}
	if len(args) < 2 || args[0] != "config" {
		return errors.New("use harness list or harness config <host> --scope <id> --agent <id> [--name <display>] [--output <new-file>]")
	}
	adapter, err := adapters.Lookup(args[1])
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("harness config", flag.ContinueOnError)
	scope := flags.String("scope", "", "local scope ID")
	agent := flags.String("agent", "", "unique agent ID per window/project")
	name := flags.String("name", "", "display name (defaults to agent ID)")
	command := flags.String("command", "", "Bus executable (defaults to this installed binary)")
	output := flags.String("output", "", "new configuration file; never overwritten")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected configuration arguments")
	}
	for _, id := range []string{*scope, *agent} {
		if _, err := bus.ScopeTokenPath("", id); err != nil {
			return fmt.Errorf("config requires valid --scope and --agent IDs: %w", err)
		}
	}
	if *name == "" {
		*name = *agent
	}
	if len(*name) > 256 {
		return errors.New("display name exceeds 256 bytes")
	}
	if *command == "" {
		*command, err = os.Executable()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(*command) {
		return errors.New("--command must be an absolute executable path so the host does not depend on PATH")
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
	data, err := adapters.Render(adapter, map[string]string{"scope": *scope, "agent": *agent, "name": *name, "command": *command, "dataDir": dataDir, "runtimeDir": runtimeDir})
	if err != nil {
		return err
	}
	if *output == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot create new config (existing files are never overwritten): %w", err)
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			_ = os.Remove(*output)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	fmt.Printf("Created %s. Review and add only the Agent Intercom entry to %s. This is not harness certification.\n", *output, adapter.Configuration.Location)
	return nil
}

type harnessDiagnostic struct {
	Harness     string   `json:"harness"`
	Healthy     bool     `json:"healthy"`
	Version     string   `json:"hostVersion,omitempty"`
	BridgeTools int      `json:"bridgeTools"`
	Problems    []string `json:"problems"`
	Note        string   `json:"note"`
}

type boundedOutput struct{ value []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - len(b.value); remaining > 0 {
		b.value = append(b.value, p[:min(remaining, n)]...)
	}
	return n, nil
}

func doctorHarness(host, scope string, jsonOutput bool) error {
	adapter, err := adapters.Lookup(host)
	if err != nil {
		return err
	}
	if scope == "" {
		return errors.New("doctor --harness requires --scope <local-scope-id>")
	}
	report := harnessDiagnostic{Harness: adapter.HarnessFamily, Problems: []string{}, Note: "Checks local setup and a temporary bridge only; does not certify host authentication, tool approvals, or model behavior."}
	if executable := adapter.Configuration.Executable; executable != "" {
		path, err := exec.LookPath(executable)
		if err != nil {
			report.Problems = append(report.Problems, "Host executable not found: "+executable)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			command := exec.CommandContext(ctx, path, "--version")
			command.Env = removeEnvironment(os.Environ(), "OCTOBER_BUS_ADMIN_TOKEN", "OCTOBER_BUS_SCOPE_TOKEN", "OCTOBER_BUS_AGENT_TOKEN")
			command.WaitDelay = 200 * time.Millisecond
			var output boundedOutput
			command.Stdout = &output
			err := command.Run()
			cancel()
			report.Version = regexp.MustCompile(`\bv?\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?`).FindString(string(output.value))
			if err != nil || report.Version == "" {
				report.Problems = append(report.Problems, "Host version probe failed; check the installed host manually")
			}
		}
	} else {
		report.Note += " Editor extension/version must be checked in its UI."
	}
	owner, err := localScopeClient(scope)
	if err != nil {
		report.Problems = append(report.Problems, err.Error())
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Validate authority before starting a temporary execution.
		if _, err := owner.ListAgents(ctx); err != nil {
			report.Problems = append(report.Problems, "Scope authentication failed; check the daemon and rotate stale credentials")
		} else {
			report.BridgeTools, err = probeLocalBridge(ctx, scope)
			if err != nil {
				report.Problems = append(report.Problems, "Bridge probe failed: "+err.Error())
			}
		}
	}
	report.Healthy = len(report.Problems) == 0
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s: local setup healthy=%t, version=%s, bridge tools=%d\n", report.Harness, report.Healthy, report.Version, report.BridgeTools)
		for _, problem := range report.Problems {
			fmt.Println("Problem: " + problem)
		}
		fmt.Println(report.Note)
	}
	if !report.Healthy {
		return errors.New("harness diagnostics did not pass")
	}
	return nil
}

func probeLocalBridge(ctx context.Context, scope string) (count int, resultErr error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return 0, err
	}
	id := fmt.Sprintf("doctor-%d-%d", os.Getpid(), time.Now().UnixNano())
	command := exec.CommandContext(ctx, executable, "mcp", "stdio", "--scope", scope, "--agent", id, "--data-dir", paths.DataDir, "--runtime-dir", paths.RuntimeDir)
	command.Env = removeEnvironment(os.Environ(), "OCTOBER_BUS_ADDRESS", "OCTOBER_BUS_ADMIN_TOKEN", "OCTOBER_BUS_SCOPE_TOKEN", "OCTOBER_BUS_AGENT_TOKEN")
	command.Stderr = io.Discard
	client := mcp.NewClient(&mcp.Implementation{Name: "agentintercom-doctor", Version: bus.Version}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return 0, errors.New("could not start the configured stdio bridge")
	}
	defer func() { resultErr = errors.Join(resultErr, session.Close()) }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return 0, errors.New("could not list bridge tools")
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, name := range strings.Fields("list_peers message_peer message_receipt check_inbox acknowledge_messages add_task claim_task release_task complete_task add_task_progress list_task_progress list_tasks publish_output ask_user get_node_status") {
		if !names[name] {
			return len(tools.Tools), fmt.Errorf("missing core tool %s; upgrade the daemon and CLI together", name)
		}
	}
	return len(tools.Tools), nil
}
