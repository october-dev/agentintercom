package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/october-dev/agentintercom/bus"
	"github.com/october-dev/agentintercom/internal/nativewake"
)

const nativeBindingFileEnv = "OCTOBER_BUS_NATIVE_BINDING_FILE"

func sessionCommand(args []string) error {
	if len(args) > 0 && args[0] == "attach" {
		return attachSession(args[1:], os.Stdout)
	}
	if len(args) == 2 && args[0] == "hook" && args[1] == "claude" {
		return installedClaudeHook(os.Stdin, os.Stdout)
	}
	if len(args) > 0 && args[0] == "bind" {
		if err := bindNativeSession(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "Agent Intercom: native inbox binding unavailable; standard MCP remains connected")
		}
		return nil
	}
	if len(args) > 0 && args[0] == "guard" {
		return guardNativeSession(os.Stdin, os.Stdout)
	}
	if len(args) > 0 && args[0] == "status" {
		client, err := executionClient("")
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		status, err := client.WakeStatus(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	if len(args) > 0 && args[0] == "retry" {
		client, err := executionClient("")
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return client.RetryWake(ctx, args[1:])
	}
	if len(args) < 2 || args[0] != "start" || (args[1] != "claude" && args[1] != "codex") {
		return errors.New("use session start <claude|codex> --workspace <name> [--name <label>] [--id <stable-id>] -- [app arguments], or session status/retry")
	}
	return startNativeSession(args[1], args[2:])
}

func nativePrivateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("native session directory must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("native session directory must be private and must not be a symlink")
	}
	return nil
}

func writeNativeJSON(path string, value any) error {
	if err := nativePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return errors.New("native session file must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readPrivateSessionJSON(path string, value any) error {
	if err := nativePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 4096 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("session file must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) {
		return errors.New("session file changed during read")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("session file has trailing data")
	}
	return nil
}
func readNativeBinding(path string) (nativewake.Binding, error) {
	var binding nativewake.Binding
	if err := readPrivateSessionJSON(path, &binding); err != nil {
		return binding, err
	}
	return binding, nativewake.ValidateBinding(binding)
}

func bindNativeSession(args []string) error {
	if len(args) != 1 || args[0] != "claude" {
		return errors.New("session bind requires claude")
	}
	path := os.Getenv(nativeBindingFileEnv)
	if path == "" {
		return nil
	} // A shared hook outside an owned session is inert.
	if filepath.Base(path) != "binding.json" {
		return errors.New("native binding file was not created by the session launcher")
	}
	binding := nativewake.Binding{Socket: os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET"), Token: os.Getenv("CLAUDE_CODE_MESSAGING_TOKEN")}
	if err := nativewake.ValidateBinding(binding); err != nil {
		return err
	}
	return writeNativeJSON(path, binding)
}

func shellArgument(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func startClaudeSession(args []string) error {
	return startNativeSession("claude", args)
}

func startNativeSession(provider string, args []string) error {
	if runtime.GOOS == "windows" {
		return errors.New("native session wake currently requires macOS, Linux or WSL; standard MCP remains available")
	}
	label := "Claude Code"
	if provider == "codex" {
		label = "Codex"
	}
	flags := flag.NewFlagSet("session start "+provider, flag.ContinueOnError)
	workspace := flags.String("workspace", "", "local workspace name")
	hosted := flags.String("hosted", "", "saved hosted device profile")
	name := flags.String("name", label, "session label")
	id := flags.String("id", "", "optional stable Bus identity to resume")
	binary := flags.String("command", provider, "app executable")
	thread := flags.String("thread", "", "Codex session UUID to resume instead of creating an empty session")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if (*workspace == "") == (*hosted == "") {
		return errors.New("choose --workspace <local-name> or --hosted <profile>")
	}
	if *thread != "" && (provider != "codex" || !codexThreadID.MatchString(*thread)) {
		return errors.New("--thread requires a Codex session UUID")
	}
	if provider == "codex" {
		if err := validateCodexNativeArgs(flags.Args()); err != nil {
			return err
		}
	}
	for _, arg := range flags.Args() {
		for _, owned := range []string{"--settings", "--mcp-config", "--bare"} {
			if provider == "claude" && (arg == owned || strings.HasPrefix(arg, owned+"=")) {
				return fmt.Errorf("%s is managed by this launcher; normal user and project settings are preserved", owned)
			}
		}
	}
	var owner bus.Client
	var err error
	if *hosted != "" {
		owner, err = hostedDeviceClient(*hosted)
	} else {
		owner, err = localScopeClient(*workspace)
	}
	if err != nil {
		return err
	}
	// Redirects must never carry the execution credential to another service.
	owner.HTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Bus redirect refused") }}
	if *id == "" && *hosted == "" {
		var value [12]byte
		if _, err := rand.Read(value[:]); err != nil {
			return err
		}
		*id = fmt.Sprintf("session-%x", value[:])
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(*binary); err != nil {
		return fmt.Errorf("%s executable was not found; install it and sign in first", label)
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.RuntimeDir, 0700); err != nil {
		return err
	}
	if err := nativePrivateDirectory(paths.RuntimeDir); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(paths.RuntimeDir, "session-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if provider == "claude" {
		settings := map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": shellArgument(executable) + " session bind claude", "timeout": 5}}}}}}
		settings["hooks"].(map[string]any)["Stop"] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": shellArgument(executable) + " session guard", "timeout": 4}}}}
		if err := writeNativeJSON(filepath.Join(dir, "settings.json"), settings); err != nil {
			return err
		}
		config := map[string]any{"mcpServers": map[string]any{"agent_intercom": stdioConfig{Command: executable, Args: []string{"mcp", "stdio"}}}}
		if err := writeNativeJSON(filepath.Join(dir, "mcp.json"), config); err != nil {
			return err
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	options := bus.AgentSessionOptions{Address: owner.Address, ScopeToken: owner.Token, HTTP: owner.HTTP, Registration: bus.RegisterAgentInput{ID: *id, DisplayName: *name, ConnectWorkspace: true, LeaseMS: 300000}, InitialLifecycle: bus.LifecycleReady, InitialReady: true}
	if *hosted != "" {
		options.ScopeToken, options.DeviceToken = "", owner.Token
	}
	session, err := bus.StartAgentSession(ctx, options)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = session.Close(cleanup)
	}()
	childCtx, stopChild := context.WithCancel(ctx)
	defer stopChild()
	providerArgs := append([]string{"--settings", filepath.Join(dir, "settings.json"), "--mcp-config", filepath.Join(dir, "mcp.json")}, flags.Args()...)
	childEnv := removeEnvironment(os.Environ(), "OCTOBER_BUS_SCOPE_TOKEN", "OCTOBER_BUS_ADMIN_TOKEN", "OCTOBER_BUS_DATABASE_URL", "OCTOBER_BUS_ENCRYPTION_KEY", "OCTOBER_BUS_BILLING_SECRET", managedConnectionFileEnv, "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "OCTOBER_BUS_ADDRESS", "OCTOBER_BUS_MCP_URL", "OCTOBER_BUS_AGENT_TOKEN", "OCTOBER_BUS_AGENT_ID", "OCTOBER_BUS_EXECUTION_ID", nativeBindingFileEnv)
	listener := nativewake.ListenerOptions{Client: session.Client, Binding: func() (nativewake.Binding, error) { return readNativeBinding(filepath.Join(dir, "binding.json")) }, Diagnostic: func(message string) { fmt.Fprintln(os.Stderr, "Agent Intercom:", message) }}
	var renewalDone <-chan error
	if provider == "codex" {
		connectionPath := filepath.Join(dir, "connection.json")
		if err := writeSessionConnection(connectionPath, session); err != nil {
			return err
		}
		config := codexConnectionConfig(executable, connectionPath)
		if *thread == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			// The bootstrap must not announce MCP readiness for the TUI or accept
			// queued work before that TUI applies the user's invocation settings.
			bootstrapConfig := append(append([]string(nil), config...), "-c", "mcp_servers.agent_intercom.enabled=false")
			*thread, err = bootstrapCodexThread(childCtx, *binary, bootstrapConfig, childEnv, cwd)
			if err != nil {
				return err
			}
		}
		providerArgs = append([]string{"resume", *thread}, config...)
		providerArgs = append(providerArgs, flags.Args()...)
		listener.Provider = "codex-app-server"
		listener.Ready = func() error {
			var ready nativeBridgeReady
			if err := readPrivateSessionJSON(filepath.Join(dir, "ready.json"), &ready); err != nil {
				return err
			}
			if ready.ExecutionID != session.Registration.ExecutionID {
				return errors.New("MCP bridge belongs to another execution")
			}
			return nil
		}
		listener.Post = func(ctx context.Context, prompt string) (string, error) {
			return postCodexQueue(ctx, *binary, *thread, childEnv, prompt)
		}
		renewalCtx, stopRenewal := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { defer close(done); done <- renewSessionConnection(renewalCtx, connectionPath, session) }()
		renewalDone = done
		defer func() { stopRenewal(); <-done }()
		fmt.Fprintf(os.Stderr, "Codex session: %s. Native wake uses the experimental app-server integration.\n", *thread)
	} else {
		childEnv = setEnvironment(childEnv, "OCTOBER_BUS_ADDRESS", owner.Address, "OCTOBER_BUS_MCP_URL", owner.Address+"/mcp", "OCTOBER_BUS_AGENT_TOKEN", session.Registration.AgentToken, "OCTOBER_BUS_AGENT_ID", session.Registration.AgentID, "OCTOBER_BUS_EXECUTION_ID", session.Registration.ExecutionID, nativeBindingFileEnv, filepath.Join(dir, "binding.json"))
	}
	command := exec.CommandContext(childCtx, *binary, providerArgs...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 3 * time.Second
	command.Env = childEnv
	if err := command.Start(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Connected %q as %s. Native wake follows this session's own permission settings.\n", *name, session.Registration.AgentID)
	childDone := make(chan error, 1)
	go func() { childDone <- command.Wait() }()
	listenerCtx, stopListener := context.WithCancel(ctx)
	defer stopListener()
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- nativewake.Run(listenerCtx, listener)
	}()
	var result error
	select {
	case result = <-childDone:
	case <-session.Done():
		stopChild()
		<-childDone
		result = errors.New("Bus session authorization or heartbeat ended; reconnect the session")
	case listenerErr := <-listenerDone:
		stopChild()
		<-childDone
		return listenerErr
	case renewalErr := <-renewalDone:
		stopChild()
		<-childDone
		result = renewalErr
	case <-ctx.Done():
		stopChild()
		<-childDone
		result = ctx.Err()
	}
	stopListener()
	<-listenerDone
	return result
}

func guardNativeSession(input io.Reader, output io.Writer) error {
	if os.Getenv(nativeBindingFileEnv) == "" {
		return nil
	}
	var payload struct {
		Active bool `json:"stop_hook_active"`
	}
	if json.NewDecoder(io.LimitReader(input, 65536)).Decode(&payload) != nil || payload.Active {
		return nil
	}
	client, err := executionClient("")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := client.WakeStatus(ctx)
	if err != nil || status.Pending == 0 {
		return nil
	}
	return json.NewEncoder(output).Encode(map[string]string{"decision": "block", "reason": "Agent Intercom still has pending messages. Check the agent_intercom inbox, acknowledge work you accept, and reply to requests. Keep your normal permissions. If you cannot act, explain that to the sender; do not bypass a permission prompt."})
}
