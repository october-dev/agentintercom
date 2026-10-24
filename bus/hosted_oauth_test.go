package bus

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type hostedOAuthFixture struct {
	ctx                context.Context
	h                  *HostedServer
	workspace          Workspace
	user               HostedUser
	clientID, verifier string
	t                  *testing.T
}

func newHostedOAuthFixture(t *testing.T) *hostedOAuthFixture {
	ctx, accounts := hostedTestAccounts(t)
	owner := hostedTestUser(t, "oauth")
	workspace, err := accounts.CreateWorkspace(ctx, owner, "MCP team")
	requireNoError(t, err)
	t.Cleanup(func() { accounts.runtime.DeleteScope(context.Background(), workspace.ID) })
	h, err := NewHostedServer(accounts, HostedServerOptions{PublicURL: "https://bus.example.test", Identity: testHostedIdentity{"account": owner}})
	requireNoError(t, err)
	f := &hostedOAuthFixture{ctx: ctx, h: h, workspace: workspace, user: owner, t: t, verifier: strings.Repeat("v", 64)}
	registered := f.call("POST", "/oauth/register", "", `{"client_name":"Test MCP app","redirect_uris":["http://127.0.0.1:5555/callback"],"token_endpoint_auth_method":"none"}`)
	require(t, registered.Code == 201, "register: %s", registered.Body.String())
	var client struct {
		ID string `json:"client_id"`
	}
	requireNoError(t, json.Unmarshal(registered.Body.Bytes(), &client))
	f.clientID = client.ID
	return f
}

func (f *hostedOAuthFixture) call(method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://bus.example.test"+path, strings.NewReader(body))
	if strings.HasPrefix(path, "/oauth/token") || path == "/oauth/revoke" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *hostedOAuthFixture) authorization() string {
	hash := sha256.Sum256([]byte(f.verifier))
	params := url.Values{"response_type": {"code"}, "client_id": {f.clientID}, "redirect_uri": {"http://127.0.0.1:5555/callback"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "scope": {hostedOAuthScope}, "resource": {"https://bus.example.test/mcp"}, "state": {"state-test"}}
	result := f.call("GET", "/oauth/authorize?"+params.Encode(), "", "")
	require(f.t, result.Code == 303, "authorize: %s", result.Body.String())
	location, err := url.Parse(result.Header().Get("Location"))
	requireNoError(f.t, err)
	id := location.Query().Get("authorization_id")
	require(f.t, id != "", "missing consent request")
	return id
}

func (f *hostedOAuthFixture) approve(id string) string {
	body, _ := json.Marshal(map[string]any{"approve": true, "workspaceId": f.workspace.ID})
	response := f.call("POST", "/api/oauth/requests/"+id, "account", string(body))
	require(f.t, response.Code == 200, "consent: %s", response.Body.String())
	var envelope struct {
		Result struct {
			URL string `json:"redirectUrl"`
		} `json:"result"`
	}
	requireNoError(f.t, json.Unmarshal(response.Body.Bytes(), &envelope))
	u, err := url.Parse(envelope.Result.URL)
	requireNoError(f.t, err)
	require(f.t, u.Scheme == "http" && u.Host == "127.0.0.1:5555" && u.Query().Get("state") == "state-test", "consent redirected elsewhere")
	return u.Query().Get("code")
}

func (f *hostedOAuthFixture) exchange(code, verifier string) *httptest.ResponseRecorder {
	params := url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {"http://127.0.0.1:5555/callback"}, "resource": {"https://bus.example.test/mcp"}}
	return f.call("POST", "/oauth/token", "", params.Encode())
}

type issuedOAuthTokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
}

func TestHostedOAuthPKCERotationAndReplay(t *testing.T) {
	f := newHostedOAuthFixture(t)
	challenge := f.call("POST", "/mcp", "", `{}`)
	require(t, challenge.Code == 401 && strings.Contains(challenge.Header().Get("WWW-Authenticate"), "oauth-protected-resource/mcp"), "MCP discovery challenge missing")
	id := f.authorization()
	bad := f.call("POST", "/api/oauth/requests/"+id, "account", `{"approve":true,"workspaceId":"another-workspace"}`)
	require(t, bad.Code == 403, "consent selected someone else's workspace")
	code := f.approve(id)
	require(t, f.call("POST", "/api/oauth/requests/"+id, "account", `{"approve":false}`).Code == 404, "authorization was completed twice")
	require(t, f.exchange(code, strings.Repeat("x", 64)).Code == 400, "accepted wrong PKCE verifier")
	// A failed verifier did not consume the real client's authorization code.
	result := f.exchange(code, f.verifier)
	require(t, result.Code == 200, "exchange: %s", result.Body.String())
	var issued issuedOAuthTokens
	requireNoError(t, json.Unmarshal(result.Body.Bytes(), &issued))
	require(t, strings.HasPrefix(issued.Access, "oba_") && strings.HasPrefix(issued.Refresh, "obr_"), "missing scoped opaque tokens")
	require(t, f.exchange(code, f.verifier).Code == 400, "authorization code replay succeeded")
	_, err := f.h.oauthExecution(f.ctx, issued.Access)
	requireNoError(t, err)
	params := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.clientID}, "refresh_token": {issued.Refresh}, "resource": {"https://other.example/mcp"}}
	require(t, f.call("POST", "/oauth/token", "", params.Encode()).Code == 400, "accepted a different resource")
	params.Set("resource", "https://bus.example.test/mcp")
	params.Set("client_id", "other-client")
	require(t, f.call("POST", "/oauth/token", "", params.Encode()).Code == 401, "accepted a different refresh client")
	params.Set("client_id", f.clientID)
	refreshed := f.call("POST", "/oauth/token", "", params.Encode())
	require(t, refreshed.Code == 200, "refresh: %s", refreshed.Body.String())
	var next issuedOAuthTokens
	requireNoError(t, json.Unmarshal(refreshed.Body.Bytes(), &next))
	require(t, next.Access != issued.Access && next.Refresh != issued.Refresh, "refresh did not rotate tokens")
	_, err = f.h.oauthExecution(f.ctx, issued.Access)
	requireCode(t, err, CodeUnauthenticated)
	_, err = f.h.oauthExecution(f.ctx, next.Access)
	requireNoError(t, err)
	// Replaying the old refresh retires the full grant, including the new token.
	require(t, f.call("POST", "/oauth/token", "", params.Encode()).Code == 400, "refresh replay succeeded")
	_, err = f.h.oauthExecution(f.ctx, next.Access)
	requireCode(t, err, CodeUnauthenticated)
}

func TestHostedOAuthConcurrentCodeRedemption(t *testing.T) {
	f := newHostedOAuthFixture(t)
	code := f.approve(f.authorization())
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.exchange(code, f.verifier).Code }()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for result := range results {
		counts[result]++
	}
	require(t, counts[200] == 1 && counts[400] == 1, "code exchange was not atomic: %v", counts)
}

type oauthTestTransport struct{ token, host string }

func (t oauthTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = t.host
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestHostedOAuthRealMCPMessageAndRevocation(t *testing.T) {
	f := newHostedOAuthFixture(t)
	issuedResponse := f.exchange(f.approve(f.authorization()), f.verifier)
	require(t, issuedResponse.Code == 200, "exchange failed: %s", issuedResponse.Body.String())
	var issued issuedOAuthTokens
	requireNoError(t, json.Unmarshal(issuedResponse.Body.Bytes(), &issued))
	server := httptest.NewServer(f.h)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "hosted-client", Version: "test"}, nil)
	session, err := client.Connect(f.ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: oauthTestTransport{issued.Access, "bus.example.test"}}}, nil)
	requireNoError(t, err)
	defer session.Close()
	sessions, err := f.h.accounts.Sessions(f.ctx, f.user, f.workspace.ID, "")
	requireNoError(t, err)
	require(t, len(sessions.Sessions) == 1, "MCP initialize did not bind one connector identity")
	connector := sessions.Sessions[0]
	device, err := f.h.accounts.EnrollDevice(f.ctx, f.user, f.workspace.ID, "Other machine")
	requireNoError(t, err)
	sender, err := f.h.accounts.StartDeviceSession(f.ctx, device.Token, "Sender", "")
	requireNoError(t, err)
	receipt, err := f.h.accounts.runtime.SendMessage(f.ctx, sender.AgentToken, SendMessageInput{To: connector.ID, Body: "Across machines", Mode: MessageRequest})
	requireNoError(t, err)
	inbox, err := session.CallTool(f.ctx, &mcp.CallToolParams{Name: "check_inbox", Arguments: map[string]any{}})
	requireNoError(t, err)
	data, _ := json.Marshal(inbox)
	require(t, !inbox.IsError && strings.Contains(string(data), receipt.MessageID), "MCP did not receive durable message: %s", data)
	reply, err := session.CallTool(f.ctx, &mcp.CallToolParams{Name: "message_peer", Arguments: map[string]any{"peer": sender.AgentID, "message": "Received", "mode": "response", "responseTo": receipt.MessageID}})
	requireNoError(t, err)
	require(t, !reply.IsError, "MCP reply failed: %#v", reply)
	status, err := f.h.accounts.runtime.Receipt(f.ctx, sender.AgentToken, receipt.MessageID)
	requireNoError(t, err)
	require(t, status.ResponseMessageID != "", "MCP reply receipt was not persisted")
	requireNoError(t, f.h.accounts.RevokeDevice(f.ctx, f.user, f.workspace.ID, connector.DeviceID))
	_, err = session.CallTool(f.ctx, &mcp.CallToolParams{Name: "get_node_status", Arguments: map[string]any{}})
	require(t, err != nil, "revoked MCP connection remained authorized")
	_, err = f.h.accounts.runtime.Principal(f.ctx, sender.AgentToken)
	requireNoError(t, err)
}

func TestHostedOAuthConfidentialClients(t *testing.T) {
	for _, method := range []string{"client_secret_basic", "client_secret_post", ""} {
		t.Run("method="+method, func(t *testing.T) {
			f := newHostedOAuthFixture(t)
			body, _ := json.Marshal(map[string]any{"client_name": "Desktop application", "redirect_uris": []string{"http://127.0.0.1:5555/callback"}, "token_endpoint_auth_method": method})
			response := f.call("POST", "/oauth/register", "", string(body))
			require(t, response.Code == 201, "registration failed: %s", response.Body.String())
			var client struct {
				ID     string `json:"client_id"`
				Secret string `json:"client_secret"`
				Method string `json:"token_endpoint_auth_method"`
			}
			requireNoError(t, json.Unmarshal(response.Body.Bytes(), &client))
			require(t, len(client.Secret) > 32, "missing client secret")
			f.clientID = client.ID
			code := f.approve(f.authorization())
			params := url.Values{"grant_type": {"authorization_code"}, "client_id": {client.ID}, "code": {code}, "code_verifier": {f.verifier}, "redirect_uri": {"http://127.0.0.1:5555/callback"}}
			request := func(secret string) *httptest.ResponseRecorder {
				if client.Method == "client_secret_post" {
					params.Set("client_secret", secret)
				}
				r := httptest.NewRequest("POST", "https://bus.example.test/oauth/token", strings.NewReader(params.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if client.Method == "client_secret_basic" {
					r.SetBasicAuth(client.ID, secret)
				}
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, r)
				return w
			}
			require(t, request("incorrect").Code == 401, "accepted wrong client secret")
			result := request(client.Secret)
			require(t, result.Code == 200, "confidential exchange: %s", result.Body.String())
			var issued issuedOAuthTokens
			requireNoError(t, json.Unmarshal(result.Body.Bytes(), &issued))
			params = url.Values{"grant_type": {"refresh_token"}, "client_id": {client.ID}, "refresh_token": {issued.Refresh}}
			require(t, request("incorrect").Code == 401, "refresh skipped client authentication")
			result = request(client.Secret)
			require(t, result.Code == 200, "confidential refresh: %s", result.Body.String())
		})
	}
}

func TestHostedOAuthConsentAndRedirectBoundaries(t *testing.T) {
	f := newHostedOAuthFixture(t)
	id := f.authorization()
	denied := f.call("POST", "/api/oauth/requests/"+id, "account", `{"approve":false}`)
	require(t, denied.Code == 200 && strings.Contains(denied.Body.String(), "error=access_denied") && strings.Contains(denied.Body.String(), "state=state-test"), "consent denial failed: %s", denied.Body.String())
	devices, err := f.h.accounts.Devices(f.ctx, f.user, f.workspace.ID)
	requireNoError(t, err)
	require(t, len(devices) == 0, "denied consent enrolled a device")
	code := f.approve(f.authorization())
	params := url.Values{"grant_type": {"authorization_code"}, "client_id": {f.clientID}, "code": {code}, "code_verifier": {f.verifier}, "redirect_uri": {"https://wrong.example/callback"}}
	require(t, f.call("POST", "/oauth/token", "", params.Encode()).Code == 400, "accepted redirect substitution")
	params.Set("redirect_uri", "http://127.0.0.1:5555/callback")
	params.Add("client_id", f.clientID)
	require(t, f.call("POST", "/oauth/token", "", params.Encode()).Code == 400, "accepted repeated token parameters")
	result := f.exchange(code, f.verifier)
	require(t, result.Code == 200, "invalid requests consumed a valid grant: %s", result.Body.String())
	var issued issuedOAuthTokens
	requireNoError(t, json.Unmarshal(result.Body.Bytes(), &issued))
	_, err = f.h.accounts.store.db.ExecContext(f.ctx, "UPDATE hosted_devices SET expires_at=1 WHERE scope_id=?", f.workspace.ID)
	requireNoError(t, err)
	_, err = f.h.oauthExecution(f.ctx, issued.Access)
	requireCode(t, err, CodeUnauthenticated)
}
