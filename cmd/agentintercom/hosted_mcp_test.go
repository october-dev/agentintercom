package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/agent-intercom/bus"
)

type noBrowserIdentity struct{}

func (noBrowserIdentity) Authenticate(context.Context, string) (bus.HostedUser, error) {
	return bus.HostedUser{}, bus.Errorf(bus.CodeUnauthenticated, "test browser login unavailable")
}

func TestHostedMCPProfilesConnectDistinctWindows(t *testing.T) {
	dsn := os.Getenv("OCTOBER_BUS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("requires isolated local PostgreSQL")
	}
	u, err := url.Parse(dsn)
	require(t, err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") && u.Path == "/october_bus_test", "refusing non-test database")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	requireNoError(t, bus.MigratePostgres(ctx, dsn))
	runtime, err := bus.OpenPostgres(ctx, dsn)
	requireNoError(t, err)
	defer runtime.Close()
	accounts, err := bus.NewHostedAccounts(runtime, bytes.Repeat([]byte{42}, 32))
	requireNoError(t, err)
	user := bus.HostedUser{ID: "mcp-profile-test", Email: "mcp-profile@example.test"}
	workspace, err := accounts.CreateWorkspace(ctx, user, "Profile qualification")
	requireNoError(t, err)
	defer runtime.DeleteScope(context.Background(), workspace.ID)
	device, err := accounts.EnrollDevice(ctx, user, workspace.ID, "Machine")
	requireNoError(t, err)
	server := httptest.NewUnstartedServer(nil)
	handler, err := bus.NewHostedServer(accounts, bus.HostedServerOptions{PublicURL: "https://" + server.Listener.Addr().String(), Identity: noBrowserIdentity{}})
	requireNoError(t, err)
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	t.Setenv("OCTOBER_BUS_DATA_DIR", t.TempDir())
	path, err := hostedProfilePath("team", true)
	requireNoError(t, err)
	requireNoError(t, saveHostedProfile(path, hostedProfile{Version: 1, Server: server.URL, WorkspaceID: workspace.ID, DeviceID: device.ID, Token: device.Token, ExpiresAt: device.ExpiresAt}))
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	requireNoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	connect := func() *mcp.ClientSession {
		stderr := new(syncBuffer)
		command := mcpBridgeCommand("", "", stderr)
		args, _ := json.Marshal([]string{"--hosted", "team", "--new-session", "--name", "My app"})
		command.Env = setEnvironment(command.Env, "OCTOBER_BUS_MCP_STDIO_TEST_ARGS", string(args), "OCTOBER_BUS_MCP_TEST_CA", caPath)
		session, err := mcp.NewClient(&mcp.Implementation{Name: "hosted-profile-test", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
		require(t, err == nil, "hosted MCP connection failed: %v; %s", err, stderr.String())
		return session
	}
	first, second := connect(), connect()
	defer first.Close()
	defer second.Close()
	directory, err := accounts.Sessions(ctx, user, workspace.ID, "")
	requireNoError(t, err)
	require(t, len(directory.Sessions) == 2 && directory.Sessions[0].ID != directory.Sessions[1].ID, "windows replaced each other")
	callMCPBridgeTool(t, ctx, first, "get_node_status", map[string]any{})
	callMCPBridgeTool(t, ctx, second, "list_peers", map[string]any{})
	requireNoError(t, first.Close())
	directory, err = accounts.Sessions(ctx, user, workspace.ID, "")
	requireNoError(t, err)
	live := 0
	for _, session := range directory.Sessions {
		if session.Reachable {
			live++
		}
	}
	require(t, live == 1, "closing bridge did not retire its own hosted execution")
	requireNoError(t, accounts.RevokeDevice(ctx, user, workspace.ID, device.ID))
	result, err := second.CallTool(ctx, &mcp.CallToolParams{Name: "get_node_status", Arguments: map[string]any{}})
	require(t, err != nil || result.IsError, "revoked bridge retained execution authority")
}
