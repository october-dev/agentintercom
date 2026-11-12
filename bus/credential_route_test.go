package bus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// GET /v1/credential classifies every credential kind the gateway's allowlisted
// routes accept from the Authorization header alone, and refuses everything else.
func TestCredentialRouteClassifiesEveryKind(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	store := sqliteStore(t, agents.runtime)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	defer server.Close()
	credential := func(token, purpose string) (Credential, error) {
		return Client{Address: server.URL, Token: token}.Credential(ctx, purpose)
	}
	expectKind := func(t *testing.T, token, purpose, want string) {
		t.Helper()
		got, err := credential(token, purpose)
		if err != nil || got.Kind != want {
			t.Fatalf("Credential(purpose=%q) = %+v, %v; want kind %q", purpose, got, err, want)
		}
	}
	expectCode := func(t *testing.T, token, purpose string, code ErrorCode) {
		t.Helper()
		_, err := credential(token, purpose)
		requireCode(t, err, code)
	}

	_, a2aPrincipal := setupA2APrincipal(t, agents)
	stream, err := agents.runtime.CreateOutputStream(ctx, agents.scope.ScopeToken, CreateOutputStreamInput{Name: "credential-route"})
	requireNoError(t, err)
	outputPrincipal, err := agents.runtime.CreateOutputPrincipal(ctx, agents.scope.ScopeToken, CreateOutputPrincipalInput{
		StreamID: stream.ID, Label: "Publisher", Permissions: []OutputPermission{OutputPublish},
	})
	requireNoError(t, err)
	retiree, err := agents.runtime.RegisterAgent(ctx, agents.scope.ScopeToken, RegisterAgentInput{ID: "retiree", DisplayName: "Retiree"})
	requireNoError(t, err)

	t.Run("classifies current credentials", func(t *testing.T) {
		expectKind(t, agents.scope.ScopeToken, "", CredentialScope)
		expectKind(t, agents.plannerToken, "", CredentialAgent)
		expectKind(t, agents.plannerToken, "retire", CredentialAgent)
		expectKind(t, outputPrincipal.Credential, "", CredentialPrincipal)
		expectKind(t, a2aPrincipal.Credential, "", CredentialPrincipal)
	})
	t.Run("classification has its own admission pool", func(t *testing.T) {
		server := NewServer(agents.runtime, ServerOptions{})
		for i := 0; i < cap(server.requests); i++ {
			server.requests <- struct{}{}
		}
		for i := 0; i < cap(server.controlRequests); i++ {
			server.controlRequests <- struct{}{}
		}
		public := httptest.NewServer(server)
		defer public.Close()
		if _, err := (Client{Address: public.URL, Token: agents.scope.ScopeToken}).Credential(ctx, ""); err != nil {
			t.Fatalf("classification was blocked by full request and control pools: %v", err)
		}
		for i := 0; i < cap(server.credentialRequests); i++ {
			server.credentialRequests <- struct{}{}
		}
		_, err := (Client{Address: public.URL, Token: agents.scope.ScopeToken}).Credential(ctx, "")
		requireCode(t, err, CodeBackpressure)
	})
	t.Run("rejects unknown credentials", func(t *testing.T) {
		for _, token := range []string{"malformed", "cred_unknown.secret", a2aPrincipal.Principal.ID + ".wrong-secret"} {
			expectCode(t, token, "", CodeUnauthenticated)
			expectCode(t, token, "retire", CodeUnauthenticated)
		}
		expectCode(t, agents.scope.ScopeToken, "retire", CodeUnauthenticated)
		expectCode(t, outputPrincipal.Credential, "retire", CodeUnauthenticated)
		expectCode(t, "", "", CodeUnauthenticated)
		expectCode(t, agents.plannerToken, "bogus", CodeInvalidArgument)
	})
	t.Run("expired lease may only retire", func(t *testing.T) {
		if _, err := store.db.ExecContext(ctx, `UPDATE agents SET lease_expires_at=? WHERE token_hash=?`, nowMillis(), tokenDigest(agents.reviewerToken)); err != nil {
			t.Fatal(err)
		}
		expectCode(t, agents.reviewerToken, "", CodeUnauthenticated)
		expectKind(t, agents.reviewerToken, "retire", CredentialAgent)
	})
	t.Run("retired token may only retire again", func(t *testing.T) {
		requireNoError(t, agents.runtime.RetireAgent(ctx, retiree.AgentToken))
		expectCode(t, retiree.AgentToken, "", CodeUnauthenticated)
		expectKind(t, retiree.AgentToken, "retire", CredentialAgent)
	})
	t.Run("replaced execution is unknown for every purpose", func(t *testing.T) {
		if _, err := agents.runtime.RegisterAgent(ctx, agents.scope.ScopeToken, RegisterAgentInput{ID: agents.planner.AgentID, DisplayName: "Replacement"}); err != nil {
			t.Fatal(err)
		}
		expectCode(t, agents.plannerToken, "", CodeUnauthenticated)
		expectCode(t, agents.plannerToken, "retire", CodeUnauthenticated)
	})
	t.Run("rotated scope token is unknown", func(t *testing.T) {
		rotated, err := agents.runtime.RotateScopeToken(ctx, agents.scope.ScopeID)
		requireNoError(t, err)
		expectCode(t, agents.scope.ScopeToken, "", CodeUnauthenticated)
		expectKind(t, rotated.ScopeToken, "", CredentialScope)
	})
	t.Run("storage failure is not an authentication failure", func(t *testing.T) {
		base, err := OpenStore(":memory:")
		requireNoError(t, err)
		defer base.Close()
		failing := &authorityFailureStore{storageBackend: base, authErr: errors.New("storage unavailable")}
		server := httptest.NewServer(NewServer(openWithStorage(failing, A2APrincipalLimits{}), ServerOptions{}))
		defer server.Close()
		request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/credential", nil)
		requireNoError(t, err)
		request.Header.Set("Authorization", "Bearer anything")
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		requireNoError(t, err)
		response.Body.Close()
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf("storage failure returned HTTP %d, want 500", response.StatusCode)
		}
		if failing.kindCalled {
			t.Fatal("classifier ran after a storage failure")
		}
	})
}
