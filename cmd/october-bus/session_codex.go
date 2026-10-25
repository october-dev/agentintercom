package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/october-dev/october-bus/bus"
)

var codexThreadID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type nativeBridgeReady struct {
	ExecutionID string `json:"executionId"`
}

func codexConnectionConfig(binary, path string) []string {
	command, _ := json.Marshal(binary)
	args, _ := json.Marshal([]string{"mcp", "stdio", "--connection-file", path, "--ready-file", filepath.Join(filepath.Dir(path), "ready.json")})
	// Replace this invocation's complete transport table so an older remote URL
	// or execution-token environment cannot be merged into the managed bridge.
	return []string{"-c", "mcp_servers.october_bus={enabled=true,command=" + string(command) + ",args=" + string(args) + ",env={OCTOBER_BUS_AGENT_TOKEN=\"\",OCTOBER_BUS_SCOPE_TOKEN=\"\",OCTOBER_BUS_ADMIN_TOKEN=\"\",OCTOBER_BUS_CONNECTION_FILE=\"\",OCTOBER_BUS_ADDRESS=\"\",OCTOBER_BUS_MCP_URL=\"\"},startup_timeout_sec=20,tool_timeout_sec=60}"}
}
func validateCodexNativeArgs(args []string) error {
	for i, arg := range args {
		for _, owned := range []string{"--remote", "--remote-auth-token-env", "--last", "--all", "--no-daemon"} {
			if arg == owned || strings.HasPrefix(arg, owned+"=") {
				return errors.New("native Codex launch owns its session destination; use --thread to select a saved local session")
			}
		}
		value := ""
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				value = args[i+1]
			}
		case strings.HasPrefix(arg, "--config="):
			value = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c") && len(arg) > 2:
			value = arg[2:]
		}
		key, _, _ := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		key = strings.NewReplacer("\"", "", "'", "", " ", "", "\t", "").Replace(key)
		if key == "mcp_servers" || key == "mcp_servers.october_bus" || strings.HasPrefix(key, "mcp_servers.october_bus.") && key != "mcp_servers.october_bus.default_tools_approval_mode" {
			return errors.New("the native launcher owns the october_bus MCP connection; other Codex settings remain available")
		}
	}
	return nil
}

func writeSessionConnection(path string, session *bus.AgentSession) error {
	expires := session.LeaseExpiresAt()
	if !expires.After(time.Now()) {
		return errors.New("Bus session lease is no longer confirmed; reconnect the session")
	}
	registration := session.Registration
	return writeNativeJSON(path, managedMCPConnection{Version: 1, Endpoint: strings.TrimRight(session.Address, "/") + "/mcp", ScopeID: registration.ScopeID, AgentID: registration.AgentID, ExecutionID: registration.ExecutionID, AgentToken: registration.AgentToken, ExpiresAt: expires})
}

func renewSessionConnection(ctx context.Context, path string, session *bus.AgentSession) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	last := session.LeaseExpiresAt()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			confirmed := session.LeaseExpiresAt()
			if confirmed.Equal(last) && confirmed.After(time.Now()) {
				continue
			}
			if err := writeSessionConnection(path, session); err != nil {
				return errors.New("could not renew the private Bus connection; reconnect the session")
			}
			last = confirmed
		}
	}
}

// Bootstrap one empty persistent thread, then let the ordinary Codex TUI own
// interaction and permission prompts. No model turn is started here. JSON-RPC is
// newline framed on the documented stdio transport; no terminal output parsing.
func bootstrapCodexThread(parent context.Context, binary string, config, env []string, cwd string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 40*time.Second)
	defer cancel()
	args := append([]string{"app-server", "--stdio"}, config...)
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = env
	command.WaitDelay = 2 * time.Second
	input, err := command.StdinPipe()
	if err != nil {
		return "", err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	// Provider diagnostics may contain unrelated MCP configuration. The interactive
	// client will show its own diagnostics; bootstrap reports fixed failure classes.
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return "", errors.New("could not start the Codex app-server; update Codex and try again")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		input.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			cancel()
			<-done
		}
	}()
	messages := make(chan json.RawMessage, 8)
	go func() {
		defer close(messages)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case messages <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	encoder := json.NewEncoder(input)
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if err := encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, errors.New("Codex bootstrap connection ended")
		}
		for {
			select {
			case <-ctx.Done():
				return nil, errors.New("Codex bootstrap timed out; no model turn was started")
			case data, ok := <-messages:
				if !ok {
					return nil, errors.New("Codex bootstrap connection ended")
				}
				var reply struct {
					ID     int             `json:"id"`
					Method string          `json:"method"`
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if json.Unmarshal(data, &reply) != nil {
					continue
				}
				if reply.Method != "" && reply.ID != 0 {
					return nil, errors.New("Codex bootstrap needs interactive approval; open Codex normally and use --thread with the saved session ID")
				}
				if reply.Method == "" && reply.ID == id {
					if len(reply.Error) > 0 && string(reply.Error) != "null" {
						return nil, errors.New("Codex refused session initialization; check its configuration and sign-in")
					}
					return reply.Result, nil
				}
			}
		}
	}
	if _, err := call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "october_bus", "version": bus.Version}}); err != nil {
		return "", err
	}
	if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
		return "", err
	}
	result, err := call(2, "thread/start", map[string]string{"cwd": cwd})
	if err != nil {
		return "", err
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(result, &response) != nil || !codexThreadID.MatchString(response.Thread.ID) {
		return "", errors.New("Codex did not return a valid owned session ID")
	}
	return response.Thread.ID, nil
}

func postCodexQueue(parent context.Context, binary, threadID string, env []string, prompt string) (string, error) {
	if !codexThreadID.MatchString(threadID) {
		return "unavailable", errors.New("invalid Codex session identity")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	// Only a static nudge and message IDs enter argv. Durable message content,
	// connection tokens and account credentials never do.
	command := exec.CommandContext(ctx, binary, "queue", "--thread", threadID, "--message", prompt)
	command.Env = env
	command.WaitDelay = time.Second
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return "unavailable", errors.New("Codex queue could not start")
	}
	if err := command.Wait(); err != nil {
		return "uncertain", errors.New("Codex queue submission could not be confirmed")
	}
	return "submitted", nil
}
