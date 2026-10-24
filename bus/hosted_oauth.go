package bus

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/go-oauth2/oauth2/v4"
	oautherrors "github.com/go-oauth2/oauth2/v4/errors"
	"github.com/go-oauth2/oauth2/v4/server"
)

var oauthVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func oauthFailure(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func validOAuthRedirect(value string) bool {
	if len(value) > 2048 {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))
}

func (h *HostedServer) registerOAuthRoutes() {
	h.oauth = newHostedOAuth(h.accounts)
	metadata := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"resource": h.publicURL + "/mcp", "authorization_servers": []string{h.publicURL}, "scopes_supported": []string{hostedOAuthScope}, "bearer_methods_supported": []string{"header"}, "resource_name": "October Bus"})
	}
	h.routes.HandleFunc("GET /.well-known/oauth-protected-resource", metadata)
	h.routes.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", metadata)
	h.routes.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"issuer": h.publicURL, "authorization_endpoint": h.publicURL + "/oauth/authorize", "token_endpoint": h.publicURL + "/oauth/token", "registration_endpoint": h.publicURL + "/oauth/register", "revocation_endpoint": h.publicURL + "/oauth/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{hostedOAuthScope}})
	})
	h.routes.HandleFunc("POST /oauth/register", h.oauthRegister)
	h.routes.HandleFunc("GET /oauth/authorize", h.oauthAuthorize)
	h.routes.HandleFunc("POST /oauth/token", h.oauthToken)
	h.routes.HandleFunc("POST /oauth/revoke", h.oauthRevoke)
	h.routes.HandleFunc("GET /api/oauth/requests/{request}", h.userRoute(h.oauthConsentDetails))
	h.routes.HandleFunc("POST /api/oauth/requests/{request}", h.userRoute(h.oauthConsent))
}

func (h *HostedServer) oauthRegister(w http.ResponseWriter, r *http.Request) {
	if !h.oauthRegistration.Allow() {
		oauthFailure(w, 429, "temporarily_unavailable", "Client registration is busy; retry shortly")
		return
	}
	var input struct {
		ClientName    string   `json:"client_name"`
		RedirectURIs  []string `json:"redirect_uris"`
		GrantTypes    []string `json:"grant_types"`
		ResponseTypes []string `json:"response_types"`
		AuthMethod    string   `json:"token_endpoint_auth_method"`
		Scope         string   `json:"scope"`
		ClientURI     string   `json:"client_uri"`
		LogoURI       string   `json:"logo_uri"`
		Contacts      []string `json:"contacts"`
	}
	// Optional informational URLs are never fetched or rendered as trusted UI.
	if err := decodeBoundedBody(w, r, &input, 16*1024, "16 KiB"); err != nil {
		oauthFailure(w, 400, "invalid_client_metadata", "Invalid registration document")
		return
	}
	if input.AuthMethod == "" {
		input.AuthMethod = "client_secret_basic"
	}
	if input.ClientName == "" {
		input.ClientName = "MCP application"
	}
	if validateText(input.ClientName, "client_name", 100, false) != nil || len(input.RedirectURIs) == 0 || len(input.RedirectURIs) > 10 || (input.AuthMethod != "none" && input.AuthMethod != "client_secret_basic" && input.AuthMethod != "client_secret_post") || (input.Scope != "" && input.Scope != hostedOAuthScope) {
		oauthFailure(w, 400, "invalid_client_metadata", "Register a client with up to ten redirect URLs and bus:connect scope")
		return
	}
	for _, redirect := range input.RedirectURIs {
		if !validOAuthRedirect(redirect) {
			oauthFailure(w, 400, "invalid_redirect_uri", "Redirect URLs must use HTTPS or loopback HTTP, without credentials or fragments")
			return
		}
	}
	for _, grant := range input.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			oauthFailure(w, 400, "invalid_client_metadata", "Only authorization_code and refresh_token are supported")
			return
		}
	}
	for _, response := range input.ResponseTypes {
		if response != "code" {
			oauthFailure(w, 400, "invalid_client_metadata", "Only code responses are supported")
			return
		}
	}
	id, err := randomID("client_")
	if err != nil {
		oauthFailure(w, 500, "server_error", "Registration failed")
		return
	}
	tx, err := h.accounts.store.beginTx(r.Context(), "oauth/clients")
	if err != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Registration unavailable")
		return
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM oauth_clients").Scan(&count); err != nil || count >= 50000 {
		oauthFailure(w, 503, "temporarily_unavailable", "Registration capacity is unavailable")
		return
	}
	secret := ""
	if input.AuthMethod != "none" {
		secret, err = randomValue(32)
		if err != nil {
			oauthFailure(w, 500, "server_error", "Registration failed")
			return
		}
	}
	redirects, _ := json.Marshal(unique(input.RedirectURIs))
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO oauth_clients(client_id,name,redirects_json,created_at,auth_method,secret_hash) VALUES(?,?,?,?,?,?)", id, input.ClientName, string(redirects), nowMillis(), input.AuthMethod, oauthHash(secret)); err != nil {
		oauthFailure(w, 500, "server_error", "Registration failed")
		return
	}
	if tx.Commit() != nil {
		oauthFailure(w, 500, "server_error", "Registration failed")
		return
	}
	response := map[string]any{"client_id": id, "client_name": input.ClientName, "redirect_uris": unique(input.RedirectURIs), "token_endpoint_auth_method": input.AuthMethod, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "scope": hostedOAuthScope}
	if secret != "" {
		response["client_secret"] = secret
		response["client_secret_expires_at"] = 0
	}
	writeJSON(w, 201, response)
}

func (h *HostedServer) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if !h.oauthAuthorization.Allow() {
		oauthFailure(w, 429, "temporarily_unavailable", "Authorization is busy; retry shortly")
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		oauthFailure(w, 400, "invalid_request", "Authorization request is too large")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		oauthFailure(w, 400, "invalid_request", "Invalid authorization parameters")
		return
	}
	for _, items := range values {
		if len(items) != 1 {
			oauthFailure(w, 400, "invalid_request", "Repeated authorization parameters are not supported")
			return
		}
	}
	req, err := h.oauth.ValidationAuthorizeRequest(r)
	if err != nil {
		oauthFailure(w, 400, "invalid_request", "Use an authorization code request with PKCE S256")
		return
	}
	resource := r.FormValue("resource")
	if resource != "" && resource != h.publicURL+"/mcp" {
		oauthFailure(w, 400, "invalid_target", "Resource must be this October Bus MCP endpoint")
		return
	}
	if req.Scope != "" && req.Scope != hostedOAuthScope {
		oauthFailure(w, 400, "invalid_scope", "Only bus:connect is supported")
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(req.CodeChallenge)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != req.CodeChallenge || len(req.State) > 1024 {
		oauthFailure(w, 400, "invalid_request", "Invalid PKCE challenge or state")
		return
	}
	client, err := h.oauth.Manager.GetClient(r.Context(), req.ClientID)
	if err != nil && !errors.Is(err, oautherrors.ErrInvalidClient) {
		oauthFailure(w, 503, "temporarily_unavailable", "Authorization unavailable")
		return
	}
	if err != nil || !oauthRedirectMatches(client.GetDomain(), req.RedirectURI) {
		oauthFailure(w, 400, "invalid_request", "Client and redirect URL must match the registration")
		return
	}
	id, err := randomID("authorization_")
	if err != nil {
		oauthFailure(w, 500, "server_error", "Authorization unavailable")
		return
	}
	now := nowMillis()
	tx, err := h.accounts.store.beginTx(r.Context(), "oauth/requests")
	if err != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Authorization unavailable")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), "DELETE FROM oauth_requests WHERE expires_at<=?", now); err != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Authorization unavailable")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO oauth_requests(request_id,client_id,redirect_uri,state,challenge,resource,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, id, req.ClientID, req.RedirectURI, req.State, req.CodeChallenge, h.publicURL+"/mcp", now, now+(10*time.Minute).Milliseconds()); err != nil {
		oauthFailure(w, 500, "server_error", "Authorization unavailable")
		return
	}
	if tx.Commit() != nil {
		oauthFailure(w, 500, "server_error", "Authorization unavailable")
		return
	}
	http.Redirect(w, r, h.publicURL+"/app?authorization_id="+url.QueryEscape(id), http.StatusSeeOther)
}

func oauthRedirectMatches(registered, redirect string) bool {
	var values []string
	if json.Unmarshal([]byte(registered), &values) != nil {
		return false
	}
	for _, value := range values {
		if value == redirect {
			return true
		}
	}
	return false
}

type oauthConsentRequest struct {
	ID               string `json:"id"`
	ClientID         string `json:"clientId"`
	ClientName       string `json:"clientName"`
	RedirectURI      string `json:"redirectUri"`
	Resource         string `json:"resource"`
	ExpiresAt        string `json:"expiresAt"`
	state, challenge string
}

func oauthPending(ctx context.Context, q sqlExecutor, id string) (oauthConsentRequest, error) {
	v := oauthConsentRequest{ID: id}
	var expires int64
	err := q.QueryRowContext(ctx, `SELECT r.client_id,c.name,r.redirect_uri,r.resource,r.state,r.challenge,r.expires_at FROM oauth_requests r JOIN oauth_clients c ON c.client_id=r.client_id
WHERE r.request_id=? AND r.consumed_at=0 AND r.expires_at>?`, id, nowMillis()).Scan(&v.ClientID, &v.ClientName, &v.RedirectURI, &v.Resource, &v.state, &v.challenge, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return v, Errorf(CodeNotFound, "Authorization request expired or was already completed")
	}
	v.ExpiresAt = instant(expires)
	return v, err
}

func (h *HostedServer) oauthConsentDetails(ctx context.Context, _ HostedUser, _ http.ResponseWriter, r *http.Request) (any, error) {
	return oauthPending(ctx, h.accounts.store.db, r.PathValue("request"))
}

func (h *HostedServer) oauthConsent(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
	var input struct {
		Approve     bool   `json:"approve"`
		WorkspaceID string `json:"workspaceId"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return nil, err
	}
	// Lock the pending request as well as the chosen workspace. A second approval
	// choosing a different workspace cannot issue another grant for the same code.
	tx, err := h.accounts.store.beginTx(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var unused string
	if err := tx.QueryRowContext(ctx, "SELECT request_id FROM oauth_requests WHERE request_id=? FOR UPDATE", r.PathValue("request")).Scan(&unused); errors.Is(err, sql.ErrNoRows) {
		return nil, Errorf(CodeNotFound, "Authorization request expired")
	} else if err != nil {
		return nil, err
	}
	pending, err := oauthPending(ctx, tx, r.PathValue("request"))
	if err != nil {
		return nil, err
	}
	redirect, _ := url.Parse(pending.RedirectURI)
	query := redirect.Query()
	if pending.state != "" {
		query.Set("state", pending.state)
	}
	if input.Approve {
		if _, err := hostedMember(ctx, tx, input.WorkspaceID, user.ID); err != nil {
			return nil, err
		}
		device, err := h.accounts.enrollDeviceTx(ctx, tx, user, input.WorkspaceID, pending.ClientName, "mcp")
		if err != nil {
			return nil, err
		}
		grant := oauthGrant{DeviceID: device.ID, UserID: user.ID, Resource: pending.Resource}
		ctx = context.WithValue(context.WithValue(ctx, oauthTransactionKey{}, sqlExecutor(tx)), oauthGrantKey{}, grant)
		req := &server.AuthorizeRequest{ResponseType: oauth2.Code, ClientID: pending.ClientID, Scope: hostedOAuthScope, RedirectURI: pending.RedirectURI, State: pending.state, UserID: user.ID, CodeChallenge: pending.challenge, CodeChallengeMethod: oauth2.CodeChallengeS256, Request: r.WithContext(ctx)}
		token, err := h.oauth.GetAuthorizeToken(ctx, req)
		if err != nil {
			return nil, Errorf(CodeInternal, "Could not authorize this application")
		}
		query.Set("code", token.GetCode())
	} else {
		query.Set("error", "access_denied")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE oauth_requests SET consumed_at=? WHERE request_id=?", nowMillis(), pending.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	redirect.RawQuery = query.Encode()
	return map[string]string{"redirectUrl": redirect.String()}, nil
}
