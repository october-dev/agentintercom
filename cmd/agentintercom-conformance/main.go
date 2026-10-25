package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/october-dev/agent-intercom/bus"
	"github.com/october-dev/agent-intercom/conformance"
)

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type isolatedRuntime struct {
	daemon *bus.RunningDaemon
	root   string
}

func startIsolatedRuntime(ctx context.Context) (*isolatedRuntime, error) {
	root, err := os.MkdirTemp("", "agentintercom-conformance-")
	if err != nil {
		return nil, err
	}
	paths := bus.DaemonPaths{
		DataDir: filepath.Join(root, "data"), RuntimeDir: filepath.Join(root, "run"),
		Database: filepath.Join(root, "data", "bus.db"), RunFile: filepath.Join(root, "run", "bus.json"),
		LockFile: filepath.Join(root, "run", "bus.lock"),
	}
	daemon, err := bus.StartDaemon(ctx, 0, &paths)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &isolatedRuntime{daemon: daemon, root: root}, nil
}

func (runtime *isolatedRuntime) close() error {
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	stopErr := runtime.daemon.Stop(shutdownContext)
	removeErr := os.RemoveAll(runtime.root)
	return errors.Join(stopErr, removeErr)
}

func run() error {
	flags := flag.NewFlagSet("agentintercom-conformance", flag.ContinueOnError)
	profile := flags.String("profile", conformance.ProfileLocalRuntime, "conformance profile: local-runtime or mcp-adapter")
	address := flags.String("address", os.Getenv("OCTOBER_BUS_ADDRESS"), "Agent Intercom address")
	adminTokenEnv := flags.String("admin-token-env", "OCTOBER_BUS_ADMIN_TOKEN", "environment variable containing the admin token")
	startRuntime := flags.Bool("start-runtime", false, "start an isolated runtime for this run")
	selfRegistration := flags.Bool("check-self-registration", false, "also verify reference bridge self-registration; requires mcp-adapter and start-runtime")
	adapterCommand := flags.String("adapter-command", "", "executable for the mcp-adapter profile")
	var adapterArgs stringList
	flags.Var(&adapterArgs, "adapter-arg", "adapter argument, repeatable")
	timeout := flags.Duration("timeout", 2*time.Minute, "conformance timeout")
	format := flags.String("format", "json", "output format: json or text")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	addressSet := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == "address" {
			addressSet = true
		}
	})
	if *format != "json" && *format != "text" {
		return errors.New("format must be json or text")
	}
	if *profile != conformance.ProfileLocalRuntime && *profile != conformance.ProfileMCPAdapter {
		return errors.New("profile must be local-runtime or mcp-adapter")
	}
	if *profile == conformance.ProfileMCPAdapter && *adapterCommand == "" {
		return errors.New("adapter-command is required for the mcp-adapter profile")
	}
	if *profile == conformance.ProfileLocalRuntime && (*adapterCommand != "" || len(adapterArgs) != 0) {
		return errors.New("adapter-command and adapter-arg require the mcp-adapter profile")
	}
	if *startRuntime && addressSet {
		return errors.New("address cannot be combined with start-runtime")
	}
	if *selfRegistration && (!*startRuntime || *profile != conformance.ProfileMCPAdapter) {
		return errors.New("check-self-registration requires --start-runtime and --profile mcp-adapter")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var temporaryRuntime *isolatedRuntime
	if *startRuntime {
		var err error
		*address = ""
		temporaryRuntime, err = startIsolatedRuntime(ctx)
		if err != nil {
			return err
		}
		*address = temporaryRuntime.daemon.RunFile.Address
		defer temporaryRuntime.close()
	}

	adminToken := os.Getenv(*adminTokenEnv)
	if temporaryRuntime != nil {
		adminToken = temporaryRuntime.daemon.RunFile.AdminToken
	}
	if *address == "" || adminToken == "" {
		paths, err := bus.DefaultDaemonPaths()
		if err != nil {
			return err
		}
		runFile, err := bus.ReadRunFile(paths.RunFile)
		if err != nil {
			return err
		}
		if *address == "" {
			*address = runFile.Address
		}
		if adminToken == "" && *address == runFile.Address {
			adminToken = runFile.AdminToken
		}
	}
	if *address == "" || adminToken == "" {
		return errors.New("address and admin token are required")
	}
	var result conformance.Result
	var runErr error
	if *profile == conformance.ProfileMCPAdapter {
		var localPaths *bus.DaemonPaths
		if *selfRegistration {
			localPaths = &temporaryRuntime.daemon.Paths
		}
		result, runErr = conformance.RunMCPAdapter(ctx, conformance.MCPAdapterOptions{
			Address: *address, AdminToken: adminToken, Command: *adapterCommand, Args: adapterArgs,
			LocalPaths: localPaths,
		})
	} else {
		result, runErr = conformance.Run(ctx, conformance.Options{Address: *address, AdminToken: adminToken})
	}
	if *format == "text" {
		fmt.Printf("Agent Intercom %s conformance\n", result.Profile)
		for _, name := range result.Passed {
			fmt.Printf("PASS %s\n", name)
		}
		for _, failure := range result.Failed {
			fmt.Printf("FAIL %s: %s\n", failure.Check, failure.Error)
		}
		fmt.Printf("Runtime %s, protocol %s\n", result.RuntimeVersion, result.ProtocolVersion)
	} else {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return err
		}
	}
	return runErr
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
