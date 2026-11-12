package bus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHostedMCPKeyLifecycleAndAuthority(t *testing.T) {
	f := newHostedOAuthFixture(t)
	a, ctx := f.h.accounts, f.ctx
	created := f.call("POST", "/api/workspaces/"+f.workspace.ID+"/mcp-keys", "account", `{"name":"Key-only app"}`)
	require(t, created.Code == 200, "create key: %s", created.Body.String())
	var envelope struct {
		Result WorkspaceDevice `json:"result"`
	}
	requireNoError(t, json.Unmarshal(created.Body.Bytes(), &envelope))
	key := envelope.Result
	require(t, key.Kind == "mcp_key" && strings.HasPrefix(key.Token, "obk_"), "missing dedicated MCP key")
	expires, err := time.Parse(time.RFC3339Nano, key.ExpiresAt)
	requireNoError(t, err)
	require(t, time.Until(expires) > 89*24*time.Hour && time.Until(expires) <= 90*24*time.Hour, "wrong key lifetime")
	for _, route := range []string{"/api/workspaces", "/v1/peers", "/api/workspaces/" + f.workspace.ID + "/devices"} {
		require(t, f.call("GET", route, key.Token, "").Code == 401, "MCP key escaped to %s", route)
	}
	require(t, f.call("POST", "/api/device/sessions", key.Token, `{"name":"Extra identity"}`).Code == 401, "key started arbitrary device sessions")
	listed := f.call("GET", "/api/workspaces/"+f.workspace.ID+"/devices", "account", "")
	require(t, listed.Code == 200 && !strings.Contains(listed.Body.String(), key.Token), "key exposed in connection list")
	var digest string
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT token_hash FROM hosted_devices WHERE device_id=?", key.ID).Scan(&digest))
	require(t, digest == tokenDigest(key.Token), "key not stored as a digest")

	server := httptest.NewServer(f.h)
	defer server.Close()
	connect := func(token string) *mcp.ClientSession {
		client := mcp.NewClient(&mcp.Implementation{Name: "manual-key-client", Version: "test"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: oauthTestTransport{token, "bus.example.test"}}}, nil)
		requireNoError(t, err)
		t.Cleanup(func() { session.Close() })
		return session
	}
	oldClient := connect(key.Token)
	// Concurrent requests retain one stable connector inbox.
	errors := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { _, err := f.h.mcpKeyExecution(ctx, key.Token); errors <- err })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		requireNoError(t, err)
	}
	sessions, err := a.Sessions(ctx, f.user, f.workspace.ID, "")
	requireNoError(t, err)
	require(t, len(sessions.Sessions) == 1, "concurrent MCP requests created duplicate identities")
	connector := sessions.Sessions[0]
	oldExecution, err := f.h.mcpKeyExecution(ctx, key.Token)
	requireNoError(t, err)
	device, err := a.EnrollDevice(ctx, f.user, f.workspace.ID, "Sender machine")
	requireNoError(t, err)
	sender, err := a.StartDeviceSession(ctx, device.Token, "Sender", "")
	requireNoError(t, err)
	receipt, err := a.runtime.SendMessage(ctx, sender.AgentToken, SendMessageInput{To: connector.ID, Body: "Keep through key rotation", Mode: MessageRequest})
	requireNoError(t, err)
	stranger := hostedTestUser(t, "key-stranger")
	invite, err := a.Invite(ctx, f.user, f.workspace.ID, stranger.Email, "member")
	requireNoError(t, err)
	_, err = a.AcceptInvite(ctx, stranger, invite.Token)
	requireNoError(t, err)
	_, err = a.CreateMCPKey(ctx, stranger, f.workspace.ID, "Takeover", key.ID)
	requireCode(t, err, CodePermissionDenied)
	_, err = a.CreateMCPKey(ctx, f.user, f.workspace.ID, "Wrong kind", device.ID)
	requireCode(t, err, CodePermissionDenied)
	replacement, err := a.CreateMCPKey(ctx, f.user, f.workspace.ID, "Rotated app", key.ID)
	requireNoError(t, err)
	require(t, replacement.ID == key.ID && replacement.Token != key.Token, "replacement did not rotate same grant")
	_, err = a.runtime.Principal(ctx, oldExecution)
	requireCode(t, err, CodeUnauthenticated)
	_, err = oldClient.CallTool(ctx, &mcp.CallToolParams{Name: "check_inbox", Arguments: map[string]any{}})
	require(t, err != nil, "old key still accessed MCP after rotation")
	client := connect(replacement.Token)
	inbox, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "check_inbox", Arguments: map[string]any{}})
	requireNoError(t, err)
	data, _ := json.Marshal(inbox)
	require(t, !inbox.IsError && strings.Contains(string(data), receipt.MessageID), "rotation lost pending inbox")
	reply, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "message_peer", Arguments: map[string]any{"peer": sender.AgentID, "message": "Recovered", "mode": "response", "responseTo": receipt.MessageID}})
	requireNoError(t, err)
	require(t, !reply.IsError, "reply after rotation failed")
	status, err := a.runtime.Receipt(ctx, sender.AgentToken, receipt.MessageID)
	requireNoError(t, err)
	require(t, status.ResponseMessageID != "", "reply receipt missing")
	// Expiry rejects the key even if the protocol execution lease is still live.
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_devices SET expires_at=1 WHERE device_id=?", key.ID)
	requireNoError(t, err)
	_, err = f.h.mcpKeyExecution(ctx, replacement.Token)
	requireCode(t, err, CodeUnauthenticated)
	recovered, err := a.CreateMCPKey(ctx, f.user, f.workspace.ID, "Recovered app", key.ID)
	requireNoError(t, err)
	_, err = f.h.mcpKeyExecution(ctx, recovered.Token)
	requireNoError(t, err)
	requireNoError(t, a.RevokeDevice(ctx, f.user, f.workspace.ID, key.ID))
	_, err = f.h.mcpKeyExecution(ctx, recovered.Token)
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.runtime.Principal(ctx, sender.AgentToken)
	requireNoError(t, err)
}
