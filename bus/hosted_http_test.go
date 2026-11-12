package bus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testHostedIdentity map[string]HostedUser

func (v testHostedIdentity) Authenticate(_ context.Context, token string) (HostedUser, error) {
	if u, ok := v[token]; ok {
		return u, nil
	}
	return HostedUser{}, Errorf(CodeUnauthenticated, "Sign in")
}

func TestHostedHTTPAccountAndBrowserBoundaries(t *testing.T) {
	ctx, accounts := hostedTestAccounts(t)
	owner, other := hostedTestUser(t, "owner-http"), hostedTestUser(t, "other-http")
	h, err := NewHostedServer(accounts, HostedServerOptions{PublicURL: "https://bus.example.test", AllowedOrigins: []string{"https://www.example.test"}, Identity: testHostedIdentity{"owner-account": owner, "other-account": other}})
	requireNoError(t, err)
	call := func(method, path, token, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://bus.example.test"+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	require(t, call("POST", "/api/workspaces", "owner-account", "https://attacker.example", `{"name":"Bad"}`).Code == 403, "accepted a foreign browser mutation")
	require(t, call("POST", "/api/workspaces", "owner-account", "", `{"name":"Bad","ownerId":"another"}`).Code == 400, "accepted caller-selected account identity")
	created := call("POST", "/api/workspaces", "owner-account", "https://www.example.test", `{"name":"Team"}`)
	require(t, created.Code == 200, "create failed: %s", created.Body.String())
	var envelope struct {
		Result Workspace `json:"result"`
	}
	requireNoError(t, json.Unmarshal(created.Body.Bytes(), &envelope))
	defer accounts.runtime.DeleteScope(context.Background(), envelope.Result.ID)
	base := "/api/workspaces/" + envelope.Result.ID
	require(t, call("GET", base+"/members", "other-account", "", "").Code == 403, "cross-workspace member list was allowed")
	require(t, call("GET", base+"/sessions", "owner-account", "", "").Code == 200, "session list failed")
	require(t, call("GET", "/api/workspaces", "", "", "").Code == 401, "account endpoint allowed anonymous access")
	require(t, call("GET", "/v1/admin/scopes", "owner-account", "", "").Code == 404, "admin endpoint was exposed")
	device, err := accounts.EnrollDevice(ctx, owner, envelope.Result.ID, "Machine")
	requireNoError(t, err)
	session := call("POST", "/api/device/sessions", device.Token, "", `{"name":"App"}`)
	require(t, session.Code == 200, "device session failed: %s", session.Body.String())
	listed := call("GET", base+"/sessions", "owner-account", "", "")
	require(t, listed.Code == 200 && strings.Contains(listed.Body.String(), `"deliveryMode":"poll"`), "missing honest delivery capability: %s", listed.Body.String())
	require(t, !strings.Contains(listed.Body.String(), "agentToken") && !strings.Contains(listed.Body.String(), device.Token), "session directory exposed a credential")
	require(t, call("GET", "/api/workspaces", device.Token, "", "").Code == 401, "device token acted as account authority")
	r := httptest.NewRequest(http.MethodGet, "https://attacker.example/api/workspaces", nil)
	r.Header.Set("Authorization", "Bearer owner-account")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require(t, w.Code == 403, "accepted a foreign Host")
}
