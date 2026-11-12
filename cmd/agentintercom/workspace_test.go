package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/agentintercom/bus"
)

func startWorkspaceTest(t *testing.T) (context.Context, bus.Client) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(root, "run"))
	// Onboarding must use local discovery, not inherited remote credentials.
	t.Setenv("OCTOBER_BUS_ADDRESS", "http://untrusted.invalid")
	t.Setenv("OCTOBER_BUS_ADMIN_TOKEN", "untrusted-admin")
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "untrusted-owner")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	daemon, err := bus.StartDaemon(ctx, 0, nil)
	requireNoError(t, err)
	t.Cleanup(func() { _ = daemon.Stop(context.Background()) })
	var output bytes.Buffer
	requireNoError(t, workspaceCommand([]string{"create", "project"}, &output))
	owner, err := localScopeClient("project")
	requireNoError(t, err)
	require(t, !strings.Contains(output.String(), owner.Token), "workspace creation leaked owner credential")
	require(t, strings.Contains(output.String(), "connect --workspace project"), "missing next step: %s", output.String())
	return ctx, owner
}

func TestWorkspaceConnectionConfiguration(t *testing.T) {
	_, owner := startWorkspaceTest(t)
	binary, err := os.Executable()
	requireNoError(t, err)
	var output bytes.Buffer
	requireNoError(t, connectCommand([]string{"--workspace", "project", "--name", `My "app"`, "--command", binary}, &output))
	var config struct {
		Servers map[string]stdioConfig `json:"mcpServers"`
	}
	requireNoError(t, json.Unmarshal(output.Bytes(), &config))
	entry := config.Servers["agent_intercom"]
	require(t, entry.Command == binary, "wrong executable: %#v", entry)
	joined := strings.Join(entry.Args, " ")
	require(t, strings.Contains(joined, "--new-session") && !strings.Contains(joined, "--agent"), "configuration binds concurrent windows to one identity: %#v", entry)
	require(t, !strings.Contains(output.String(), owner.Token), "configuration leaked workspace credential")
	for _, format := range []string{"toml", "entry"} {
		output.Reset()
		requireNoError(t, connectCommand([]string{"--workspace", "project", "--format", format}, &output))
		if format == "toml" {
			require(t, strings.HasPrefix(output.String(), "[mcp_servers.agent_intercom]"), "missing Codex section")
		} else {
			requireNoError(t, json.Unmarshal(output.Bytes(), &entry))
		}
	}
	file := filepath.Join(t.TempDir(), "mcp.json")
	args := []string{"--workspace", "project", "--output", file}
	requireNoError(t, connectCommand(args, &output))
	before, err := os.ReadFile(file)
	requireNoError(t, err)
	require(t, connectCommand(args, &output) != nil, "overwrote an existing configuration")
	after, err := os.ReadFile(file)
	requireNoError(t, err)
	require(t, bytes.Equal(before, after), "existing configuration was changed")
	for _, args := range [][]string{
		{}, {"--workspace", "missing"}, {"--workspace", "project", "--format", "unknown"},
		{"--workspace", "project", "--name", "$(command)"},
		{"--workspace", "project", "--command", "relative-bus"},
	} {
		require(t, connectCommand(args, &output) != nil, "accepted invalid connection: %v", args)
	}
	output.Reset()
	requireNoError(t, workspaceCommand([]string{"list", "--json"}, &output))
	require(t, strings.Contains(output.String(), "project") && !strings.Contains(output.String(), owner.Token), "unsafe/missing workspace list: %s", output.String())
}

func TestNewMCPSessionsDoNotReplaceEachOther(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	connect := func() *mcp.ClientSession {
		stderr := new(syncBuffer)
		command := mcpBridgeCommand("", "", stderr)
		args, _ := json.Marshal([]string{"--scope", "project", "--new-session", "--name", "My app"})
		command.Env = setEnvironment(command.Env, "OCTOBER_BUS_MCP_STDIO_TEST_ARGS", string(args))
		client := mcp.NewClient(&mcp.Implementation{Name: "generic-client", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
		require(t, err == nil, "connect new MCP session: %v; stderr: %s", err, stderr.String())
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	first, second := connect(), connect()
	agents, err := owner.ListAgents(ctx)
	requireNoError(t, err)
	require(t, len(agents) == 2 && agents[0].ID != agents[1].ID, "concurrent configurations reused an identity: %#v", agents)
	for _, agent := range agents {
		require(t, agent.Reachable && agent.DisplayName == "My app", "session missing or fenced: %#v", agent)
	}
	// Both processes must still have live execution authority, not just rows
	// left in the directory after one replaced the other.
	callMCPBridgeTool(t, ctx, first, "get_node_status", map[string]any{})
	callMCPBridgeTool(t, ctx, second, "get_node_status", map[string]any{})
	var output bytes.Buffer
	requireNoError(t, sessionsCommand([]string{"--workspace", "project", "--json"}, &output))
	var listed []bus.Agent
	requireNoError(t, json.Unmarshal(output.Bytes(), &listed))
	require(t, len(listed) == 2 && !strings.Contains(output.String(), owner.Token), "session list missing or unsafe")
	requireNoError(t, first.Close())
	callMCPBridgeTool(t, ctx, second, "get_node_status", map[string]any{})
	agents, err = owner.ListAgents(ctx)
	requireNoError(t, err)
	online := 0
	for _, agent := range agents {
		if agent.Reachable {
			online++
		}
	}
	require(t, online == 1, "closing a session retired a different one or leaked its own lease: %#v", agents)
}

func TestNewSessionRejectsMixedIdentity(t *testing.T) {
	for _, args := range [][]string{
		{"--scope", "s", "--agent", "a", "--new-session"},
		{"--new-session"},
	} {
		require(t, runMCPStdio(context.Background(), args...) != nil, "accepted invalid session identity: %v", args)
	}
}
