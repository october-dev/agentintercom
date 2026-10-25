//go:build unix

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/agent-intercom/bus"
)

const testCodexThread = "11111111-2222-4333-8444-555555555555"

func testCodexConnectionPath(args []string) string {
	for _, arg := range args {
		_, encoded, ok := strings.Cut(arg, ",args=")
		if !ok {
			continue
		}
		var bridgeArgs []string
		if json.NewDecoder(strings.NewReader(encoded)).Decode(&bridgeArgs) != nil {
			return ""
		}
		for i, value := range bridgeArgs {
			if value == "--connection-file" && i+1 < len(bridgeArgs) {
				return bridgeArgs[i+1]
			}
		}
	}
	return ""
}

func codexTestExecutable(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OCTOBER_BUS_CODEX_TEST", dir)
	path := filepath.Join(dir, "codex")
	requireNoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexec "+shellArgument(os.Args[0])+" -test.run=^TestCodexNativeChild$ -- \"$@\"\n"), 0700))
	return path, dir
}

// The subprocess speaks the app-server protocol without credentials or a model.
func TestCodexNativeChild(t *testing.T) {
	dir := os.Getenv("OCTOBER_BUS_CODEX_TEST")
	if dir == "" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "app-server":
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				os.Exit(3)
			}
			switch request.Method {
			case "initialize":
				fmt.Println(`{"id":1,"result":{}}`)
			case "initialized":
			case "thread/start":
				fmt.Printf("{\"id\":2,\"result\":{\"thread\":{\"id\":%q}}}\n", testCodexThread)
			default:
				os.Exit(4) // In particular, a bootstrap must never start a turn.
			}
		}
	case "resume":
		if len(args) < 2 || args[1] != testCodexThread {
			os.Exit(5)
		}
		for _, key := range []string{"OCTOBER_BUS_AGENT_TOKEN", "OCTOBER_BUS_SCOPE_TOKEN", "OCTOBER_BUS_ADMIN_TOKEN", "OCTOBER_BUS_DATABASE_URL", "OCTOBER_BUS_STRIPE_SECRET_KEY"} {
			if os.Getenv(key) != "" {
				os.Exit(6)
			}
		}
		data, _ := json.Marshal(args)
		if os.WriteFile(filepath.Join(dir, "ready.json"), data, 0600) != nil {
			os.Exit(7)
		}
		// Starting the process is not enough: no wake may be sent until its MCP
		// client finishes initialization with the correct execution identity.
		time.Sleep(250 * time.Millisecond)
		if _, err := os.Stat(filepath.Join(dir, "queue.json")); err == nil {
			os.Exit(12)
		}
		connectionPath := testCodexConnectionPath(args)
		connection, err := readManagedMCPConnection(connectionPath)
		if err != nil {
			os.Exit(13)
		}
		if writeNativeJSON(filepath.Join(filepath.Dir(connectionPath), "ready.json"), nativeBridgeReady{ExecutionID: connection.ExecutionID}) != nil {
			os.Exit(14)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "queue.json")); err == nil {
				os.Exit(0)
			}
			time.Sleep(20 * time.Millisecond)
		}
		os.Exit(8)
	case "queue":
		data, _ := json.Marshal(args)
		if os.WriteFile(filepath.Join(dir, "queue.json"), data, 0600) != nil {
			os.Exit(9)
		}
		if os.Getenv("OCTOBER_BUS_CODEX_TEST_QUEUE_FAIL") != "" {
			os.Exit(10)
		}
	default:
		os.Exit(11)
	}
	os.Exit(0)
}

func TestCodexNativeLaunchQueuesOnlyOwnedSessionAndRetires(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	binary, dir := codexTestExecutable(t)
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "inherited-other-execution")
	t.Setenv("OCTOBER_BUS_STRIPE_SECRET_KEY", "must-not-reach-agent")
	sender, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "sender", DisplayName: "Sender"})
	requireNoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- startNativeSession("codex", []string{"--workspace", "project", "--name", "My Codex", "--id", "native-codex", "--command", binary})
	}()
	deadline := time.Now().Add(8 * time.Second)
	var ready []byte
	for time.Now().Before(deadline) {
		ready, err = os.ReadFile(filepath.Join(dir, "ready.json"))
		if err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("launch ended early: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	requireNoError(t, err)
	var args []string
	requireNoError(t, json.Unmarshal(ready, &args))
	connectionPath := testCodexConnectionPath(args)
	require(t, connectionPath != "", "managed connection missing")
	connection, err := readManagedMCPConnection(connectionPath)
	requireNoError(t, err)
	require(t, connection.AgentID == "native-codex" && connection.ExpiresAt.After(time.Now()), "wrong connection identity or lease")
	sendClient := bus.Client{Address: owner.Address, Token: sender.AgentToken}
	message, err := sendClient.SendMessage(ctx, bus.SendMessageInput{To: "native-codex", Body: "private message content must not enter queue argv"})
	requireNoError(t, err)
	select {
	case err := <-done:
		requireNoError(t, err)
	case <-time.After(12 * time.Second):
		t.Fatal("launcher did not exit after its app")
	}
	queue, err := os.ReadFile(filepath.Join(dir, "queue.json"))
	requireNoError(t, err)
	require(t, strings.Contains(string(queue), testCodexThread) && strings.Contains(string(queue), message.MessageID), "queue did not name owned thread and message")
	require(t, !strings.Contains(string(queue), "private message content") && !strings.Contains(string(ready), connection.AgentToken), "content or credential leaked into argv")
	_, err = os.Stat(connectionPath)
	require(t, os.IsNotExist(err), "connection survived launcher shutdown")
	agents, err := owner.ListAgents(ctx)
	requireNoError(t, err)
	for _, agent := range agents {
		if agent.ID == "native-codex" {
			require(t, !agent.Reachable && agent.Lifecycle == bus.LifecycleOffline, "session was not retired")
		}
	}
}

func TestNativeBridgeAnnouncesInitializationAndWithdrawsOnExit(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	session, err := bus.StartAgentSession(ctx, bus.AgentSessionOptions{Address: owner.Address, ScopeToken: owner.Token, Registration: bus.RegisterAgentInput{ID: "managed-codex", DisplayName: "Managed"}})
	requireNoError(t, err)
	defer session.Close(context.Background())
	dir := t.TempDir()
	requireNoError(t, os.Chmod(dir, 0700))
	connectionPath, readyPath := filepath.Join(dir, "connection.json"), filepath.Join(dir, "ready.json")
	requireNoError(t, writeSessionConnection(connectionPath, session))
	command := mcpBridgeCommand("", "", os.Stderr)
	args, _ := json.Marshal([]string{"--connection-file", connectionPath, "--ready-file", readyPath})
	command.Env = setEnvironment(command.Env, "OCTOBER_BUS_MCP_STDIO_TEST_ARGS", string(args))
	client := mcp.NewClient(&mcp.Implementation{Name: "readiness-test", Version: "1"}, nil)
	bridge, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	requireNoError(t, err)
	_, err = bridge.ListTools(ctx, nil)
	requireNoError(t, err)
	var ready nativeBridgeReady
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = readPrivateSessionJSON(readyPath, &ready)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	requireNoError(t, err)
	require(t, ready.ExecutionID == session.Registration.ExecutionID, "bridge announced another execution")
	requireNoError(t, bridge.Close())
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err = os.Stat(readyPath)
		if os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require(t, os.IsNotExist(err), "readiness survived MCP shutdown")
}

func TestCodexQueueFailureIsUncertainAndDestinationCannotBeOverridden(t *testing.T) {
	binary, _ := codexTestExecutable(t)
	t.Setenv("OCTOBER_BUS_CODEX_TEST_QUEUE_FAIL", "1")
	outcome, err := postCodexQueue(context.Background(), binary, testCodexThread, os.Environ(), "static nudge")
	require(t, err != nil && outcome == "uncertain", "post-start error may have delivered: %s %v", outcome, err)
	outcome, err = postCodexQueue(context.Background(), "/missing/codex", testCodexThread, os.Environ(), "static nudge")
	require(t, err != nil && outcome == "unavailable", "start failure was not unavailable")
	for _, args := range [][]string{{"--remote=unix://other"}, {"--last"}, {"--no-daemon"}, {"-c", `mcp_servers."agent_intercom".command="other"`}, {"--config=mcp_servers={}"}} {
		require(t, validateCodexNativeArgs(args) != nil, "accepted destination override %v", args)
	}
	requireNoError(t, validateCodexNativeArgs([]string{"-c", "model_reasoning_effort=low", "--sandbox", "read-only", "-c", "mcp_servers.agent_intercom.default_tools_approval_mode=prompt"}))
}

func TestCodexBootstrapCancellationDoesNotStartTurns(t *testing.T) {
	binary, _ := codexTestExecutable(t)
	thread, err := bootstrapCodexThread(context.Background(), binary, nil, os.Environ(), t.TempDir())
	requireNoError(t, err)
	require(t, thread == testCodexThread, "bootstrap returned another thread")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = bootstrapCodexThread(ctx, binary, nil, os.Environ(), t.TempDir())
	require(t, err != nil, "canceled bootstrap succeeded")
}
