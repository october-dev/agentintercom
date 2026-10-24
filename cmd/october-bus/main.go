package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/october-dev/october-bus/bus"
)

const usage = `October Bus

Usage:
  october-bus database migrate [--url-env OCTOBER_BUS_DATABASE_URL]
  october-bus database check [--url-env OCTOBER_BUS_DATABASE_URL]
  october-bus workspace create <name>
  october-bus workspace list [--json]
  october-bus connect --workspace <name> [--name <session-label>] [--format json|toml|entry] [--output <new-file>]
  october-bus sessions --workspace <name> [--json]
  october-bus start [--port <port>]
  october-bus stop
  october-bus status
  october-bus doctor [--json]
  october-bus doctor --harness <host> --scope <scope-id> [--json]
  october-bus harness list
  october-bus harness config <host> --scope <scope-id> --agent <id> [--name <display>] [--output <new-file>]
  october-bus scope create [scope-id]
  october-bus scope list [--address <addr>]
  october-bus scope rotate-token --id <scope-id> [--address <addr>]
  october-bus scope delete --id <scope-id> --confirm <scope-id> [--address <addr>]
  october-bus backup --output <new-path> [--address <addr>]
  october-bus scope export --id <scope-id> --output <path> [--address <addr>]
  october-bus scope import --input <path> [--address <addr>]
  october-bus scope storage [--json] [--address <addr>]
  october-bus scope prune --before <timestamp> [--yes] [--json] [--address <addr>]
  october-bus message receipt <message-id> [--json] [--address <addr>]
  october-bus agent list [--json] [--address <addr>]
  october-bus link --scope <scope-id> <agent-a> <agent-b>
  october-bus agent run --id <id> --name <name> [--scope <local-scope-id>] [--connect-to <peer>] -- <command> [args...]
  october-bus task add --title <title> [--description <text>] [--depends-on <task-id>] [--json] [--address <addr>]
  october-bus task list [--ready] [--json] [--limit <1-500>] [--after <cursor>] [--address <addr>]
  october-bus mcp stdio [--scope <scope-id> --agent <id> --name <display>]
  october-bus mcp stdio --scope <scope-id> --new-session [--name <display>]
  october-bus mcp stdio --connection-file </absolute/private/connection.json>
  october-bus mcp stdio --remote <https-base-url> --scope-token-file <file> --agent <id> [--connect-to <peer>]
  october-bus mcp check [--connection-file <path>] [--json]
  october-bus hook [--connection-file <path>] <event> [<flavor>]
  october-bus gateway init --public-url <https-origin> --scope <id> --config <new-file> --key-file <new-file>
  october-bus gateway key --output <new-private-file>
  october-bus gateway start --config <file> [--listen 127.0.0.1:8787]
  october-bus demo
  october-bus version [--json]
`

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func start(args []string) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	port := flags.Int("port", 0, "loopback port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *port < 0 || *port > 65535 {
		return errors.New("port must be between 0 and 65535")
	}
	daemon, err := bus.StartDaemon(context.Background(), *port, nil)
	if err != nil {
		return err
	}
	fmt.Printf("October Bus listening on %s\n", daemon.RunFile.Address)
	fmt.Printf("Run file: %s\n", daemon.Paths.RunFile)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var serveErr error
	select {
	case <-ctx.Done():
	case <-daemon.Server.ShutdownRequested():
	case serveErr = <-daemon.Server.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopErr := daemon.Stop(shutdown)
	if serveErr != nil {
		return fmt.Errorf("October Bus stopped unexpectedly: %w", serveErr)
	}
	return stopErr
}

func stop() error {
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	run, err := bus.ReadRunFile(paths.RunFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (bus.Client{Address: run.Address, Token: run.AdminToken}).Shutdown(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(paths.RunFile); errors.Is(err, os.ErrNotExist) {
			fmt.Println("October Bus stopped")
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("October Bus did not stop within 10 seconds")
}

type diagnosticReport struct {
	Healthy          bool   `json:"healthy"`
	RuntimeVersion   string `json:"runtimeVersion"`
	ProtocolVersion  string `json:"protocolVersion"`
	Address          string `json:"address,omitempty"`
	PID              int    `json:"pid,omitempty"`
	DataDirectory    string `json:"dataDirectory"`
	RuntimeDirectory string `json:"runtimeDirectory"`
	DatabaseExists   bool   `json:"databaseExists"`
	RunFileExists    bool   `json:"runFileExists"`
	StorageBackend   string `json:"storageBackend,omitempty"`
	StorageStatus    string `json:"storageStatus,omitempty"`
	Problem          string `json:"problem,omitempty"`
}

func doctor(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	host := flags.String("harness", "", "check a configured harness")
	scope := flags.String("scope", "", "local scope for the temporary bridge probe")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor does not accept positional arguments")
	}
	if *host != "" {
		return doctorHarness(*host, *scope, *jsonOutput)
	}
	if *scope != "" {
		return errors.New("doctor --scope requires --harness")
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	report := diagnosticReport{
		RuntimeVersion: bus.Version, ProtocolVersion: bus.ProtocolVersion,
		DataDirectory: paths.DataDir, RuntimeDirectory: paths.RuntimeDir,
	}
	if _, err := os.Stat(paths.Database); err == nil {
		report.DatabaseExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		report.Problem = err.Error()
	}
	if _, err := os.Stat(paths.RunFile); err == nil {
		report.RunFileExists = true
	} else if !errors.Is(err, os.ErrNotExist) && report.Problem == "" {
		report.Problem = err.Error()
	}
	if report.RunFileExists {
		run, readErr := bus.ReadRunFile(paths.RunFile)
		if readErr != nil {
			report.Problem = readErr.Error()
		} else {
			report.Address, report.PID = run.Address, run.PID
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			health, healthErr := (bus.Client{Address: run.Address}).Health(ctx)
			cancel()
			report.StorageBackend = string(health.Storage.Backend)
			report.StorageStatus = health.Storage.Status
			if healthErr != nil {
				if health.Storage.Status == bus.StorageUnavailable {
					report.Problem = "Storage backend is unavailable"
				} else {
					report.Problem = healthErr.Error()
				}
			} else if health.ProtocolVersion != run.ProtocolVersion {
				report.Problem = "run file and daemon protocol versions differ"
			} else {
				report.Healthy = health.Status == "ready"
				report.RuntimeVersion = health.RuntimeVersion
				report.ProtocolVersion = health.ProtocolVersion
			}
		}
	} else if report.Problem == "" {
		report.Problem = "October Bus is not running"
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
	} else {
		state := "not ready"
		if report.Healthy {
			state = "ready"
		}
		fmt.Printf("October Bus is %s\n", state)
		fmt.Printf("Runtime %s, protocol %s\n", report.RuntimeVersion, report.ProtocolVersion)
		fmt.Printf("Data: %s\n", report.DataDirectory)
		fmt.Printf("Runtime: %s\n", report.RuntimeDirectory)
		if report.Address != "" {
			fmt.Printf("Endpoint: %s, pid %d\n", report.Address, report.PID)
		}
		if report.StorageBackend != "" {
			fmt.Printf("Storage: %s (%s)\n", report.StorageBackend, report.StorageStatus)
		}
		if report.Problem != "" {
			fmt.Printf("Problem: %s\n", report.Problem)
		}
	}
	if !report.Healthy {
		return errors.New("October Bus diagnostics did not pass")
	}
	return nil
}

func status() error {
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	run, err := bus.ReadRunFile(paths.RunFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	health, err := (bus.Client{Address: run.Address}).Health(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("October Bus is %s at %s\n", health.Status, run.Address)
	fmt.Printf("Runtime %s, protocol %s, pid %d\n", health.RuntimeVersion, health.ProtocolVersion, run.PID)
	fmt.Printf("Storage %s (%s)\n", health.Storage.Backend, health.Storage.Status)
	return nil
}

func createScope(id string) error {
	result, err := createLocalScope(id)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// Keep the legacy scope command's output while letting onboarding reuse the
// same credential transaction without printing the workspace owner secret.
func createLocalScope(id string) (bus.CreateScopeResult, error) {
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return bus.CreateScopeResult{}, err
	}
	run, err := bus.ReadRunFile(paths.RunFile)
	if err != nil {
		return bus.CreateScopeResult{}, fmt.Errorf("local daemon is unavailable; run october-bus start: %w", err)
	}
	lock, err := bus.LockScopeCredentials(paths.DataDir)
	if err != nil {
		return bus.CreateScopeResult{}, err
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := (bus.Client{Address: run.Address, Token: run.AdminToken}).CreateScope(ctx, bus.CreateScopeInput{ID: id})
	if err != nil {
		return bus.CreateScopeResult{}, err
	}
	if err := bus.SaveScopeToken(paths.DataDir, result.ScopeID, result.ScopeToken); err != nil {
		return bus.CreateScopeResult{}, fmt.Errorf("scope was created but its local credential could not be saved; fix the data directory and rotate the scope token: %w", err)
	}
	return result, nil
}

func adminClient(explicitAddress string) (bus.Client, error) {
	if token := os.Getenv("OCTOBER_BUS_ADMIN_TOKEN"); token != "" {
		address, err := resolveBusAddress(explicitAddress)
		if err != nil {
			return bus.Client{}, err
		}
		return bus.Client{Address: address, Token: token, HTTP: &http.Client{Timeout: 2 * time.Minute}}, nil
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return bus.Client{}, err
	}
	run, err := bus.ReadRunFile(paths.RunFile)
	if err != nil {
		return bus.Client{}, err
	}
	if explicitAddress != "" && explicitAddress != run.Address {
		return bus.Client{}, errors.New("OCTOBER_BUS_ADMIN_TOKEN is required for a remote daemon")
	}
	return bus.Client{Address: run.Address, Token: run.AdminToken, HTTP: &http.Client{Timeout: 2 * time.Minute}}, nil
}

func exportScope(args []string) error {
	flags := flag.NewFlagSet("scope export", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	scopeID := flags.String("id", "", "scope id to export")
	output := flags.String("output", "", "archive output path")
	address := flags.String("address", "", "October Bus address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *scopeID == "" || *output == "" {
		return errors.New("scope export requires --id and --output")
	}
	client, err := adminClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	archive, err := client.ExportScope(ctx, *scopeID)
	if err != nil {
		return fmt.Errorf("could not export scope: %w", err)
	}
	data, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("could not create archive: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(*output)
		}
	}()
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("could not write archive: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	fmt.Printf("Exported scope %s to %s\n", archive.Scope.ID, *output)
	return nil
}

func importScope(args []string) error {
	flags := flag.NewFlagSet("scope import", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	input := flags.String("input", "", "archive input path")
	address := flags.String("address", "", "October Bus address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" {
		return errors.New("scope import requires --input")
	}
	file, err := os.Open(*input)
	if err != nil {
		return fmt.Errorf("could not open archive: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("could not inspect archive: %w", err)
	}
	if info.Size() > 64*1024*1024 {
		return errors.New("archive exceeds 64 MiB")
	}
	var archive bus.ScopeArchive
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&archive); err != nil {
		return fmt.Errorf("could not read archive: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("archive must contain one JSON value")
	}
	client, err := adminClient(*address)
	if err != nil {
		return err
	}
	unlock, err := lockLocalScopeCredentials(client.Address)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := client.ImportScope(ctx, archive)
	if err != nil {
		return fmt.Errorf("could not import scope: %w", err)
	}
	if result.Imported {
		if err := updateLocalScopeCredential(client.Address, result.ScopeID, result.ScopeToken); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	if !result.Imported {
		fmt.Fprintln(os.Stderr, "Archive was already imported. The original scope token is not returned again.")
	}
	return nil
}

// resolveBusAddress returns the daemon address to talk to for a CLI subcommand.
// Precedence: explicit flag, then the
// OCTOBER_BUS_ADDRESS environment variable, then the daemon run file.
func resolveBusAddress(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("OCTOBER_BUS_ADDRESS"); env != "" {
		return env, nil
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return "", err
	}
	run, err := bus.ReadRunFile(paths.RunFile)
	if err != nil {
		return "", err
	}
	return run.Address, nil
}

// inspectReceipt implements `october-bus message receipt <message-id>`.
//
// It reads the agent credential from OCTOBER_BUS_AGENT_TOKEN and fetches the
// delivery receipt through the public client API. The receipt deliberately
// exposes only delivery state and timestamps; message bodies and shared
// context are never included.
func inspectReceipt(args []string) error {
	flags := flag.NewFlagSet("message receipt", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	address := flags.String("address", "", "October Bus address")
	if err := flags.Parse(receiptFlagArgs(args)); err != nil {
		return err
	}
	positional := flags.Args()
	if len(positional) != 1 {
		return errors.New("message receipt requires a single <message-id> argument")
	}
	messageID := strings.TrimSpace(positional[0])
	if messageID == "" {
		return errors.New("message id must not be empty")
	}
	token := os.Getenv("OCTOBER_BUS_AGENT_TOKEN")
	if token == "" {
		return errors.New("OCTOBER_BUS_AGENT_TOKEN is required")
	}
	resolved, err := resolveBusAddress(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipt, err := (bus.Client{Address: resolved, Token: token}).Receipt(ctx, messageID)
	if err != nil {
		return fmt.Errorf("could not inspect message receipt: %w", err)
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	return printReceiptHuman(receipt)
}

// receiptFlagArgs moves supported flags before the message id so the command
// accepts flags on either side of its single positional argument. The standard
// flag package otherwise stops parsing at the first positional argument.
func receiptFlagArgs(args []string) []string {
	flags := make([]string, 0, len(args))
	positional := make([]string, 0, 1)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			positional = append(positional, args[index+1:]...)
			break
		}
		if !strings.HasPrefix(argument, "-") || argument == "-" {
			positional = append(positional, argument)
			continue
		}
		flags = append(flags, argument)
		if (argument == "-address" || argument == "--address") && index+1 < len(args) {
			index++
			flags = append(flags, args[index])
		}
	}
	return append(flags, positional...)
}

// printReceiptHuman renders a DeliveryReceipt as a stable, multi-line summary.
// Only state, timestamps, and the linked response message ID are shown; the
// receipt struct contains no body or shared-context fields, so this view
// cannot leak message contents.
func printReceiptHuman(receipt bus.DeliveryReceipt) error {
	fmt.Printf("Message %s\n", receipt.MessageID)
	fmt.Printf("State: %s\n", receipt.State)
	for _, ts := range []struct {
		label string
		value string
	}{
		{"AcceptedAt", receipt.AcceptedAt},
		{"DeliveredAt", receipt.DeliveredAt},
		{"AcknowledgedAt", receipt.AcknowledgedAt},
		{"RepliedAt", receipt.RepliedAt},
	} {
		if ts.value != "" {
			fmt.Printf("%s: %s\n", ts.label, ts.value)
		}
	}
	if receipt.ResponseMessageID != "" {
		fmt.Printf("ResponseMessageID: %s\n", receipt.ResponseMessageID)
	}
	return nil
}

// listAgents implements `october-bus agent list [--json] [--address <addr>]`.
// It reads the scope credential from OCTOBER_BUS_SCOPE_TOKEN and returns only
// the agent metadata visible to that scope.
func listAgents(args []string) error {
	flags := flag.NewFlagSet("agent list", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	address := flags.String("address", "", "October Bus address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("agent list does not accept positional arguments")
	}
	token := os.Getenv("OCTOBER_BUS_SCOPE_TOKEN")
	if token == "" {
		return errors.New("OCTOBER_BUS_SCOPE_TOKEN is required")
	}
	resolved, err := resolveBusAddress(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agents, err := (bus.Client{Address: resolved, Token: token}).ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("could not list agents: %w", err)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	if *jsonOutput {
		encoded, err := json.MarshalIndent(agents, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	return printAgentsHuman(agents)
}

// printAgentsHuman renders a stable summary. Display names are quoted because
// they are user-controlled text and may contain terminal control characters.
func printAgentsHuman(agents []bus.Agent) error {
	if len(agents) == 0 {
		fmt.Println("No agents in scope.")
		return nil
	}
	for _, agent := range agents {
		fmt.Printf("%s (%q)\n", agent.ID, agent.DisplayName)
		fmt.Printf("  lifecycle:  %s\n", agent.Lifecycle)
		fmt.Printf("  ready:      %s\n", yesNo(agent.Ready))
		fmt.Printf("  reachable:  %s\n", yesNo(agent.Reachable))
		if len(agent.Capabilities) == 0 {
			fmt.Println("  capabilities: (none)")
		} else {
			names := make([]string, 0, len(agent.Capabilities))
			for _, capability := range agent.Capabilities {
				names = append(names, capability.Name)
			}
			fmt.Printf("  capabilities: %s\n", strings.Join(names, ", "))
		}
		if agent.UpdatedAt != "" {
			fmt.Printf("  updatedAt:  %s\n", agent.UpdatedAt)
		}
	}
	return nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func scopeClient(address string) (bus.Client, error) {
	token := os.Getenv("OCTOBER_BUS_SCOPE_TOKEN")
	if token == "" {
		return bus.Client{}, errors.New("OCTOBER_BUS_SCOPE_TOKEN is required")
	}
	resolved, err := resolveBusAddress(address)
	if err != nil {
		return bus.Client{}, err
	}
	return bus.Client{Address: resolved, Token: token}, nil
}

func addTask(args []string) error {
	flags := flag.NewFlagSet("task add", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	title := flags.String("title", "", "short task title")
	description := flags.String("description", "", "task description")
	address := flags.String("address", "", "October Bus address")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	var dependencies stringList
	flags.Var(&dependencies, "depends-on", "dependency task id, repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("task add does not accept positional arguments")
	}
	if strings.TrimSpace(*title) == "" {
		return errors.New("task add requires --title")
	}
	client, err := scopeClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := client.AddTask(ctx, bus.AddTaskInput{Title: *title, Description: *description, Dependencies: dependencies})
	if err != nil {
		return fmt.Errorf("could not add task: %w", err)
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Added %s %q\n", task.ID, task.Title)
	return nil
}

func listTasks(args []string) error {
	flags := flag.NewFlagSet("task list", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	readyOnly := flags.Bool("ready", false, "show only dependency-ready tasks")
	pageLimit := flags.Int("limit", 0, "return a page of 1-500 tasks as JSON")
	after := flags.String("after", "", "task page cursor")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	address := flags.String("address", "", "October Bus address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("task list does not accept positional arguments")
	}
	client, err := scopeClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if *pageLimit != 0 || *after != "" {
		if *readyOnly {
			return errors.New("--ready cannot be combined with task pagination")
		}
		page, err := client.TaskPage(ctx, *after, *pageLimit)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(page)
	}
	tasks, err := client.ListTasks(ctx, *readyOnly)
	if err != nil {
		return fmt.Errorf("could not list tasks: %w", err)
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(tasks, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	return printTasksHuman(tasks)
}

func printTasksHuman(tasks []bus.Task) error {
	if len(tasks) == 0 {
		fmt.Println("No tasks found.")
		return nil
	}
	for _, task := range tasks {
		state := task.Status
		if task.Ready {
			state = "ready"
		}
		fmt.Printf("%s [%s] %q\n", task.ID, state, task.Title)
		if task.Description != "" {
			fmt.Printf("  description: %q\n", task.Description)
		}
		if len(task.Dependencies) > 0 {
			fmt.Printf("  depends on: %s\n", strings.Join(task.Dependencies, ", "))
		}
		if task.ClaimedBy != "" {
			fmt.Printf("  claimed by: %s\n", task.ClaimedBy)
		}
		for _, progress := range task.RecentProgress {
			fmt.Printf("  progress #%d [%s] by %s: %q\n", progress.Sequence, progress.Kind, progress.AgentID, progress.Text)
		}
	}
	return nil
}

func scopeStorage(args []string) error {
	flags := flag.NewFlagSet("scope storage", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	address := flags.String("address", "", "October Bus address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("scope storage does not accept positional arguments")
	}
	client, err := scopeClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := client.StorageSummary(ctx)
	if err != nil {
		return fmt.Errorf("could not inspect scope storage: %w", err)
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	return printStorageSummaryHuman(summary)
}

func printStorageSummaryHuman(summary bus.StorageSummary) error {
	fmt.Printf("Storage for scope %s\n", summary.ScopeID)
	if len(summary.Records) == 0 {
		fmt.Println("No messages, tasks, or escalations.")
	} else {
		for _, record := range summary.Records {
			fmt.Printf("%s/%s: %d records, %d estimated bytes", record.RecordType, record.State, record.Count, record.EstimatedBytes)
			if record.OldestAt != "" {
				fmt.Printf(", oldest %s", record.OldestAt)
			}
			fmt.Println()
		}
	}
	fmt.Printf("Total estimated bytes: %d\n", summary.TotalEstimatedBytes)
	return nil
}

func scopePrune(args []string) error {
	flags := flag.NewFlagSet("scope prune", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	before := flags.String("before", "", "remove terminal records older than this RFC 3339 timestamp")
	execute := flags.Bool("yes", false, "execute the retention operation")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	address := flags.String("address", "", "October Bus address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("scope prune does not accept positional arguments")
	}
	if *before == "" {
		return errors.New("scope prune requires --before")
	}
	client, err := scopeClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := client.PruneScope(ctx, bus.PruneScopeInput{Before: *before, Execute: *execute})
	if err != nil {
		return fmt.Errorf("could not prune scope: %w", err)
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	return printPruneResultHuman(result)
}

func printPruneResultHuman(result bus.PruneScopeResult) error {
	action := "Would remove"
	if !result.DryRun {
		action = "Removed"
	}
	fmt.Printf("%s %d messages, %d tasks, %d task progress entries, and %d escalations older than %s.\n",
		action, result.Records.Messages, result.Records.Tasks, result.Records.TaskProgress, result.Records.Escalations, result.Before)
	if result.DryRun {
		fmt.Println("Dry run only. Pass --yes to remove these records.")
	}
	return nil
}

func setEnvironment(base []string, values ...string) []string {
	replacements := map[string]bool{}
	for i := 0; i < len(values); i += 2 {
		replacements[strings.ToUpper(values[i])] = true
	}
	result := make([]string, 0, len(base)+len(values)/2)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !replacements[strings.ToUpper(key)] {
			result = append(result, entry)
		}
	}
	for i := 0; i < len(values); i += 2 {
		result = append(result, values[i]+"="+values[i+1])
	}
	return result
}

func removeEnvironment(base []string, names ...string) []string {
	removed := map[string]bool{}
	for _, name := range names {
		removed[strings.ToUpper(name)] = true
	}
	result := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !removed[strings.ToUpper(key)] {
			result = append(result, entry)
		}
	}
	return result
}

func runAgent(args []string) error {
	flags := flag.NewFlagSet("agent run", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	id := flags.String("id", "", "stable agent id")
	name := flags.String("name", "", "agent display name")
	address := flags.String("address", "", "October Bus address")
	localScope := flags.String("scope", "", "local scope ID; uses protected cached credentials, not environment authority")
	scopeTokenEnv := flags.String("scope-token-env", "OCTOBER_BUS_SCOPE_TOKEN", "environment variable containing the scope token")
	lease := flags.Duration("lease", 5*time.Minute, "execution lease")
	heartbeat := flags.Duration("heartbeat", 0, "heartbeat interval")
	var connectTo stringList
	var capabilities stringList
	flags.Var(&connectTo, "connect-to", "agent id to link, repeatable")
	flags.Var(&capabilities, "capability", "capability name, repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	commandArgs := flags.Args()
	if *id == "" || *name == "" || len(commandArgs) == 0 {
		return errors.New("agent run requires --id, --name, and a command after --")
	}
	if *scopeTokenEnv == "" {
		return errors.New("scope token environment variable name is required")
	}
	scopeToken := os.Getenv(*scopeTokenEnv)
	resolvedAddress := *address
	if *localScope != "" {
		if *address != "" || scopeToken != "" || os.Getenv("OCTOBER_BUS_AGENT_TOKEN") != "" {
			return errors.New("--scope uses local discovery only; do not mix --address or inherited scope/agent credentials")
		}
		owner, err := localScopeClient(*localScope)
		if err != nil {
			return err
		}
		scopeToken, resolvedAddress = owner.Token, owner.Address
	} else if scopeToken == "" {
		return fmt.Errorf("use --scope <local-scope-id> or provide %s", *scopeTokenEnv)
	}
	if resolvedAddress == "" {
		resolvedAddress = os.Getenv("OCTOBER_BUS_ADDRESS")
	}
	if resolvedAddress == "" {
		paths, err := bus.DefaultDaemonPaths()
		if err != nil {
			return err
		}
		run, err := bus.ReadRunFile(paths.RunFile)
		if err != nil {
			return err
		}
		resolvedAddress = run.Address
	}
	declaredCapabilities := make([]bus.AgentCapability, 0, len(capabilities))
	for _, capability := range capabilities {
		declaredCapabilities = append(declaredCapabilities, bus.AgentCapability{Name: capability})
	}
	session, err := bus.StartAgentSession(context.Background(), bus.AgentSessionOptions{
		Address: resolvedAddress, ScopeToken: scopeToken, HeartbeatInterval: *heartbeat,
		Registration: bus.RegisterAgentInput{
			ID: *id, DisplayName: *name, ConnectTo: connectTo,
			Capabilities: declaredCapabilities, LeaseMS: lease.Milliseconds(),
		},
	})
	if err != nil {
		return err
	}
	closeSession := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return session.Close(ctx)
	}

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	childCtx, stopChild := context.WithCancel(context.Background())
	defer stopChild()
	command := exec.CommandContext(childCtx, commandArgs[0], commandArgs[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = setEnvironment(removeEnvironment(os.Environ(), *scopeTokenEnv, "OCTOBER_BUS_SCOPE_TOKEN", "OCTOBER_BUS_ADMIN_TOKEN"),
		"OCTOBER_BUS_ADDRESS", resolvedAddress,
		"OCTOBER_BUS_MCP_URL", resolvedAddress+"/mcp",
		"OCTOBER_BUS_AGENT_ID", session.Registration.AgentID,
		"OCTOBER_BUS_EXECUTION_ID", session.Registration.ExecutionID,
		"OCTOBER_BUS_AGENT_TOKEN", session.Registration.AgentToken,
	)
	if err := command.Start(); err != nil {
		_ = closeSession()
		return err
	}
	fmt.Fprintf(os.Stderr, "Agent %s connected to %s\n", session.Registration.AgentID, resolvedAddress)
	childDone := make(chan error, 1)
	go func() { childDone <- command.Wait() }()

	select {
	case childErr := <-childDone:
		if closeErr := closeSession(); childErr == nil {
			return closeErr
		}
		return childErr
	case <-session.Done():
		stopChild()
		<-childDone
		if sessionErr := session.Err(); sessionErr != nil {
			_ = closeSession()
			return fmt.Errorf("agent session ended: %w", sessionErr)
		}
		return closeSession()
	case <-signalCtx.Done():
		stopChild()
		<-childDone
		return closeSession()
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "database":
		return databaseCommand(args[1:], os.Stdout)
	case "workspace":
		return workspaceCommand(args[1:], os.Stdout)
	case "connect":
		return connectCommand(args[1:], os.Stdout)
	case "sessions":
		return sessionsCommand(args[1:], os.Stdout)
	case "start":
		return start(args[1:])
	case "gateway":
		return gatewayCommand(args[1:])
	case "stop":
		return stop()
	case "status":
		return status()
	case "doctor":
		return doctor(args[1:])
	case "link":
		return linkAgents(args[1:])
	case "harness":
		return harness(args[1:])
	case "scope":
		if len(args) >= 2 && (args[1] == "list" || args[1] == "rotate-token" || args[1] == "delete") {
			return scopeAdmin(args[1], args[2:])
		}
		if len(args) >= 2 && args[1] == "create" {
			if len(args) > 3 {
				return errors.New("scope create accepts at most one scope ID")
			}
			id := ""
			if len(args) >= 3 {
				id = args[2]
			}
			return createScope(id)
		}
		if len(args) >= 2 && args[1] == "storage" {
			return scopeStorage(args[2:])
		}
		if len(args) >= 2 && args[1] == "export" {
			return exportScope(args[2:])
		}
		if len(args) >= 2 && args[1] == "import" {
			return importScope(args[2:])
		}
		if len(args) >= 2 && args[1] == "prune" {
			return scopePrune(args[2:])
		}
	case "backup":
		return backupDatabase(args[1:])
	case "message":
		if len(args) >= 2 && args[1] == "receipt" {
			return inspectReceipt(args[2:])
		}
	case "agent":
		if len(args) >= 2 && args[1] == "run" {
			return runAgent(args[2:])
		}
		if len(args) >= 2 && args[1] == "list" {
			return listAgents(args[2:])
		}
	case "task":
		if len(args) >= 2 && args[1] == "add" {
			return addTask(args[2:])
		}
		if len(args) >= 2 && args[1] == "list" {
			return listTasks(args[2:])
		}
	case "mcp":
		if len(args) >= 2 && args[1] == "stdio" {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runMCPStdio(ctx, args[2:]...)
		}
		if len(args) >= 2 && args[1] == "check" {
			return runMCPCheck(args[2:], os.Stdout)
		}
	case "hook":
		// A lifecycle hook must never block or fail a harness session. Problems
		// are reported on stderr and the process still exits 0.
		runHook(args[1:], os.Stdin, stdinIsTerminal(), os.Stdout, os.Stderr)
		return nil
	case "demo":
		return bus.RunDemo(context.Background())
	case "version", "--version":
		return printVersion(args[1:], os.Stdout)
	}
	return fmt.Errorf("unknown command: %v\n\n%s", args, usage)
}

func printVersion(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("version does not accept positional arguments")
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(map[string]string{"runtimeVersion": bus.Version, "protocolVersion": bus.ProtocolVersion}, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	_, err := fmt.Fprintf(out, "october-bus %s (protocol %s)\n", bus.Version, bus.ProtocolVersion)
	return err
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
