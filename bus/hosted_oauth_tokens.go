package bus

import (
	"context"
	"database/sql"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-oauth2/oauth2/v4"
	oautherrors "github.com/go-oauth2/oauth2/v4/errors"
	"github.com/go-oauth2/oauth2/v4/models"
)

type oauthTokenRecord struct {
	ID, ClientID, DeviceID, ScopeID, UserID, Name, Payload        string
	Consumed, Revoked, DeviceRevoked, DeviceExpires, TokenExpires int64
}

func oauthRecord(ctx context.Context, q sqlExecutor, column, value string) (oauthTokenRecord, error) {
	var record oauthTokenRecord
	consumed := "t.code_consumed"
	if column == "refresh_hash" {
		consumed = "t.refresh_consumed"
	}
	if column == "access_hash" {
		consumed = "t.access_revoked"
	}
	err := q.QueryRowContext(ctx, `SELECT t.token_id,t.client_id,t.device_id,d.scope_id,d.user_id,d.name,t.payload,`+consumed+`,t.access_revoked,d.revoked_at,d.expires_at,t.expires_at
FROM oauth_tokens t JOIN hosted_devices d ON d.device_id=t.device_id JOIN hosted_members m ON m.scope_id=d.scope_id AND m.user_id=d.user_id
WHERE t.`+column+`=?`, tokenDigest(value)).Scan(&record.ID, &record.ClientID, &record.DeviceID, &record.ScopeID, &record.UserID, &record.Name, &record.Payload, &record.Consumed, &record.Revoked, &record.DeviceRevoked, &record.DeviceExpires, &record.TokenExpires)
	return record, err
}

func (h *HostedServer) oauthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if !parseOAuthForm(r) {
		oauthFailure(w, 400, "invalid_request", "Invalid token request")
		return
	}
	// Token endpoints accept parameters only in the POST body. No secrets in URLs.
	if r.URL.RawQuery != "" {
		oauthFailure(w, 400, "invalid_request", "Token parameters must be in the POST body")
		return
	}
	clientID, clientSecret, err := h.authenticateOAuthClient(r)
	if err != nil {
		oauthClientFailure(w, err)
		return
	}
	grantType := oauth2.GrantType(r.PostForm.Get("grant_type"))
	column, secret := "code_hash", r.PostForm.Get("code")
	if grantType == oauth2.Refreshing {
		column, secret = "refresh_hash", r.PostForm.Get("refresh_token")
	} else if grantType != oauth2.AuthorizationCode {
		oauthFailure(w, 400, "unsupported_grant_type", "Only authorization_code and refresh_token are supported")
		return
	}
	resource := r.PostForm.Get("resource")
	if resource != "" && resource != h.publicURL+"/mcp" {
		oauthFailure(w, 400, "invalid_target", "Resource must be this Agent Intercom MCP endpoint")
		return
	}
	if len(secret) < 20 || len(secret) > 256 || clientID == "" {
		oauthFailure(w, 400, "invalid_grant", "Grant is invalid or expired")
		return
	}
	if scope := r.PostForm.Get("scope"); scope != "" && scope != hostedOAuthScope {
		oauthFailure(w, 400, "invalid_scope", "Only bus:connect is supported")
		return
	}
	if grantType == oauth2.AuthorizationCode && !oauthVerifierPattern.MatchString(r.PostForm.Get("code_verifier")) {
		oauthFailure(w, 400, "invalid_grant", "PKCE S256 verification is required")
		return
	}
	record, err := oauthRecord(r.Context(), h.accounts.store.db, column, secret)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			oauthFailure(w, 400, "invalid_grant", "Grant is invalid or expired")
		} else {
			oauthFailure(w, 503, "temporarily_unavailable", "Token service is unavailable")
		}
		return
	}
	tx, err := h.accounts.store.beginTx(r.Context(), record.ScopeID)
	if err != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Token service is unavailable")
		return
	}
	defer tx.Rollback()
	record, err = oauthRecord(r.Context(), tx, column, secret)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		oauthFailure(w, 503, "temporarily_unavailable", "Token service is unavailable")
		return
	}
	if err != nil || record.ClientID != clientID || record.DeviceRevoked != 0 || record.DeviceExpires <= nowMillis() || record.TokenExpires <= nowMillis() {
		oauthFailure(w, 400, "invalid_grant", "Grant is invalid or expired")
		return
	}
	ctx := context.WithValue(context.WithValue(r.Context(), oauthTransactionKey{}, sqlExecutor(tx)), oauthGrantKey{}, oauthGrant{DeviceID: record.DeviceID, UserID: record.UserID, Resource: h.publicURL + "/mcp"})
	plain, err := h.accounts.unseal(record.Payload, "oauth/"+record.ID)
	if err != nil {
		oauthFailure(w, 500, "server_error", "Token service is unavailable")
		return
	}
	stored, err := decodeOAuthToken(plain)
	if err != nil || stored.Extension.Get("resource") != h.publicURL+"/mcp" {
		oauthFailure(w, 400, "invalid_target", "Grant is for another resource")
		return
	}
	if record.Consumed != 0 {
		if grantType == oauth2.Refreshing {
			// A rotated refresh token was replayed. Retire the entire connection,
			// including the current replacement token, before returning the error.
			if retireHostedSessions(ctx, tx, record.ScopeID, "", record.DeviceID) != nil {
				oauthFailure(w, 500, "server_error", "Could not revoke replayed grant")
				return
			}
			if _, err := tx.ExecContext(ctx, "UPDATE hosted_devices SET revoked_at=? WHERE device_id=?", nowMillis(), record.DeviceID); err != nil {
				oauthFailure(w, 500, "server_error", "Could not revoke replayed grant")
				return
			}
			if tx.Commit() != nil {
				oauthFailure(w, 500, "server_error", "Could not revoke replayed grant")
				return
			}
			h.accounts.runtime.signals.notifyAllScope(record.ScopeID)
		}
		oauthFailure(w, 400, "invalid_grant", "Grant was already used; reconnect the application")
		return
	}
	request := r.WithContext(ctx)
	generate := &oauth2.TokenGenerateRequest{ClientID: record.ClientID, ClientSecret: clientSecret, Code: r.PostForm.Get("code"), CodeVerifier: r.PostForm.Get("code_verifier"), RedirectURI: r.PostForm.Get("redirect_uri"), Refresh: r.PostForm.Get("refresh_token"), Scope: hostedOAuthScope, Request: request}
	if grantType == oauth2.AuthorizationCode && generate.RedirectURI == "" {
		oauthFailure(w, 400, "invalid_request", "redirect_uri is required")
		return
	}
	// Client ID is verified above for refresh as well as authorization code.
	// The library handles PKCE and generates/rotates tokens in this transaction.
	issued, err := h.oauth.GetAccessToken(ctx, grantType, generate)
	if err != nil {
		// Storage/crypto outages must not tell clients to discard a valid grant.
		// The uncommitted transaction leaves a code/refresh usable for retry.
		invalid := false
		for _, known := range []error{oautherrors.ErrInvalidGrant, oautherrors.ErrInvalidClient, oautherrors.ErrInvalidRedirectURI, oautherrors.ErrInvalidAuthorizeCode, oautherrors.ErrInvalidRefreshToken, oautherrors.ErrExpiredRefreshToken, oautherrors.ErrInvalidCodeChallenge, oautherrors.ErrMissingCodeChallenge, oautherrors.ErrMissingCodeVerifier, oautherrors.ErrInvalidScope, oautherrors.ErrAccessDenied, oautherrors.ErrUnauthorizedClient} {
			invalid = invalid || errors.Is(err, known)
		}
		if invalid {
			oauthFailure(w, 400, "invalid_grant", "Grant, redirect URL or PKCE verifier is invalid")
		} else {
			oauthFailure(w, 503, "temporarily_unavailable", "Token service is unavailable; retry shortly")
		}
		return
	}
	if tx.Commit() != nil {
		oauthFailure(w, 500, "server_error", "Token issue did not commit; reconnect the application")
		return
	}
	writeJSON(w, 200, h.oauth.GetTokenData(issued))
}

func (h *HostedServer) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if !parseOAuthForm(r) || r.URL.RawQuery != "" {
		oauthFailure(w, 400, "invalid_request", "Use a form-encoded POST body")
		return
	}
	clientID, _, err := h.authenticateOAuthClient(r)
	if err != nil {
		oauthClientFailure(w, err)
		return
	}
	token := r.PostForm.Get("token")
	column := "access_hash"
	if strings.HasPrefix(token, "obr_") {
		column = "refresh_hash"
	}
	record, err := oauthRecord(r.Context(), h.accounts.store.db, column, token)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		oauthFailure(w, 503, "temporarily_unavailable", "Revocation unavailable")
		return
	}
	if err != nil || record.ClientID != clientID {
		w.WriteHeader(200)
		return
	}
	tx, err := h.accounts.store.beginTx(r.Context(), record.ScopeID)
	if err != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Revocation unavailable")
		return
	}
	defer tx.Rollback()
	record, err = oauthRecord(r.Context(), tx, column, token)
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(200)
		return
	}
	if err != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Revocation unavailable")
		return
	}
	if err := retireHostedSessions(r.Context(), tx, record.ScopeID, "", record.DeviceID); err != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Revocation unavailable")
		return
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE hosted_devices SET revoked_at=? WHERE device_id=? AND revoked_at=0", nowMillis(), record.DeviceID); err != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Revocation unavailable")
		return
	}
	if tx.Commit() != nil {
		oauthFailure(w, 503, "temporarily_unavailable", "Revocation unavailable")
		return
	}
	h.accounts.runtime.signals.notifyAllScope(record.ScopeID)
	w.WriteHeader(200)
}

func (h *HostedServer) oauthExecution(ctx context.Context, access string) (string, error) {
	select {
	case h.oauthRequests <- struct{}{}:
		defer func() { <-h.oauthRequests }()
	default:
		return "", Errorf(CodeBackpressure, "Authentication is busy; retry shortly")
	}
	refused := Errorf(CodeUnauthenticated, "MCP connection expired or was revoked; reconnect the application")
	if len(access) > 256 {
		return "", refused
	}
	record, err := oauthRecord(ctx, h.accounts.store.db, "access_hash", access)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", refused
		}
		return "", err
	}
	tx, err := h.accounts.store.beginTx(ctx, record.ScopeID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	record, err = oauthRecord(ctx, tx, "access_hash", access)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err != nil || record.Consumed != 0 || record.DeviceRevoked != 0 || record.DeviceExpires <= nowMillis() {
		return "", refused
	}
	ctx = context.WithValue(ctx, oauthTransactionKey{}, sqlExecutor(tx))
	info, err := h.oauth.Manager.LoadAccessToken(ctx, access)
	if err != nil {
		if errors.Is(err, oautherrors.ErrInvalidAccessToken) || errors.Is(err, oautherrors.ErrExpiredAccessToken) {
			return "", refused
		}
		return "", err
	}
	token, ok := info.(*models.Token)
	if !ok || token.Extension.Get("resource") != h.publicURL+"/mcp" || token.Extension.Get("device_id") != record.DeviceID || token.Scope != hostedOAuthScope {
		return "", refused
	}
	return h.accounts.commitConnectorExecution(ctx, tx, hostedDeviceIdentity{ID: record.DeviceID, ScopeID: record.ScopeID, UserID: record.UserID}, record.Name)
}

func (h *HostedServer) authenticateOAuthClient(r *http.Request) (string, string, error) {
	id, secret, method := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret"), "none"
	if secret != "" {
		method = "client_secret_post"
	}
	if r.Header.Get("Authorization") != "" {
		basicID, basicSecret, ok := r.BasicAuth()
		if !ok || secret != "" {
			return "", "", oautherrors.ErrInvalidClient
		}
		var err error
		basicID, err = url.QueryUnescape(basicID)
		if err != nil {
			return "", "", oautherrors.ErrInvalidClient
		}
		basicSecret, err = url.QueryUnescape(basicSecret)
		if err != nil {
			return "", "", oautherrors.ErrInvalidClient
		}
		if id != "" && id != basicID {
			return "", "", oautherrors.ErrInvalidClient
		}
		id, secret, method = basicID, basicSecret, "client_secret_basic"
	}
	value, err := h.oauth.Manager.GetClient(r.Context(), id)
	if err != nil {
		return "", "", err
	}
	client, ok := value.(*hostedOAuthClient)
	if !ok || client.method != method || !client.VerifyPassword(secret) {
		return "", "", oautherrors.ErrInvalidClient
	}
	return id, secret, nil
}

func oauthClientFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, oautherrors.ErrInvalidClient) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Agent Intercom"`)
		oauthFailure(w, 401, "invalid_client", "Client authentication failed")
	} else {
		oauthFailure(w, 503, "temporarily_unavailable", "Client authentication is unavailable")
	}
}

func parseOAuthForm(r *http.Request) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" || r.ParseForm() != nil {
		return false
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			return false
		}
	}
	return true
}
