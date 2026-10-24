package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/go-oauth2/oauth2/v4"
	oautherrors "github.com/go-oauth2/oauth2/v4/errors"
	"github.com/go-oauth2/oauth2/v4/manage"
	"github.com/go-oauth2/oauth2/v4/models"
	"github.com/go-oauth2/oauth2/v4/server"
)

const hostedOAuthScope = "bus:connect"

type oauthTransactionKey struct{}
type oauthGrantKey struct{}
type oauthGrant struct{ DeviceID, UserID, Resource string }

type hostedOAuthStore struct{ accounts *HostedAccounts }

func oauthTx(ctx context.Context) (sqlExecutor, error) {
	tx, ok := ctx.Value(oauthTransactionKey{}).(sqlExecutor)
	if !ok {
		return nil, errors.New("OAuth storage requires an authorization transaction")
	}
	return tx, nil
}

func (s *hostedOAuthStore) GetByID(ctx context.Context, id string) (oauth2.ClientInfo, error) {
	var redirects, method string
	var secret sql.NullString
	q := sqlExecutor(s.accounts.store.db)
	if tx, err := oauthTx(ctx); err == nil {
		q = tx
	}
	if err := q.QueryRowContext(ctx, "SELECT redirects_json,auth_method,secret_hash FROM oauth_clients WHERE client_id=?", id).Scan(&redirects, &method, &secret); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, oautherrors.ErrInvalidClient
		}
		return nil, err
	}
	// The library's URI validator compares each registered URI exactly.
	return &hostedOAuthClient{Client: models.Client{ID: id, Domain: redirects, Public: method == "none"}, method: method, secretHash: secret.String}, nil
}

func (s *hostedOAuthStore) Create(ctx context.Context, info oauth2.TokenInfo) error {
	tx, err := oauthTx(ctx)
	if err != nil {
		return err
	}
	grant, ok := ctx.Value(oauthGrantKey{}).(oauthGrant)
	if !ok || grant.DeviceID == "" || info.GetUserID() != grant.UserID {
		return oautherrors.ErrAccessDenied
	}
	id, err := randomID("token_")
	if err != nil {
		return err
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	payload, err := s.accounts.seal(string(data), "oauth/"+id)
	if err != nil {
		return err
	}
	expires := info.GetAccessCreateAt().Add(info.GetAccessExpiresIn())
	if info.GetCode() != "" {
		expires = info.GetCodeCreateAt().Add(info.GetCodeExpiresIn())
	}
	if info.GetRefresh() != "" {
		expires = info.GetRefreshCreateAt().Add(info.GetRefreshExpiresIn())
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO oauth_tokens(token_id,client_id,device_id,code_hash,access_hash,refresh_hash,payload,expires_at) VALUES(?,?,?,?,?,?,?,?)`,
		id, info.GetClientID(), grant.DeviceID, oauthHash(info.GetCode()), oauthHash(info.GetAccess()), oauthHash(info.GetRefresh()), payload, expires.UnixMilli())
	return err
}

func oauthHash(value string) any {
	if value == "" {
		return nil
	}
	return tokenDigest(value)
}

func (s *hostedOAuthStore) remove(ctx context.Context, column, flag, value string) error {
	tx, err := oauthTx(ctx)
	if err != nil {
		return err
	}
	// Column names are fixed call-site constants, never request values.
	_, err = tx.ExecContext(ctx, "UPDATE oauth_tokens SET "+flag+"=1 WHERE "+column+"=?", tokenDigest(value))
	return err
}
func (s *hostedOAuthStore) RemoveByCode(ctx context.Context, v string) error {
	return s.remove(ctx, "code_hash", "code_consumed", v)
}
func (s *hostedOAuthStore) RemoveByAccess(ctx context.Context, v string) error {
	return s.remove(ctx, "access_hash", "access_revoked", v)
}
func (s *hostedOAuthStore) RemoveByRefresh(ctx context.Context, v string) error {
	return s.remove(ctx, "refresh_hash", "refresh_consumed", v)
}

func (s *hostedOAuthStore) get(ctx context.Context, column, flag, value string) (oauth2.TokenInfo, error) {
	tx, err := oauthTx(ctx)
	if err != nil {
		return nil, err
	}
	var id, payload string
	err = tx.QueryRowContext(ctx, "SELECT token_id,payload FROM oauth_tokens WHERE "+column+"=? AND "+flag+"=0 AND expires_at>?", tokenDigest(value), nowMillis()).Scan(&id, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.accounts.unseal(payload, "oauth/"+id)
	if err != nil {
		return nil, err
	}
	var token models.Token
	if err := json.Unmarshal([]byte(plain), &token); err != nil {
		return nil, err
	}
	return &token, nil
}
func (s *hostedOAuthStore) GetByCode(ctx context.Context, v string) (oauth2.TokenInfo, error) {
	return s.get(ctx, "code_hash", "code_consumed", v)
}
func (s *hostedOAuthStore) GetByAccess(ctx context.Context, v string) (oauth2.TokenInfo, error) {
	return s.get(ctx, "access_hash", "access_revoked", v)
}
func (s *hostedOAuthStore) GetByRefresh(ctx context.Context, v string) (oauth2.TokenInfo, error) {
	return s.get(ctx, "refresh_hash", "refresh_consumed", v)
}

type busAuthorizationCode struct{}

func (busAuthorizationCode) Token(context.Context, *oauth2.GenerateBasic) (string, error) {
	v, err := randomValue(32)
	return "obc_" + v, err
}

type busAccessToken struct{}

func (busAccessToken) Token(_ context.Context, _ *oauth2.GenerateBasic, refresh bool) (string, string, error) {
	access, err := randomValue(32)
	if err != nil {
		return "", "", err
	}
	var next string
	if refresh {
		next, err = randomValue(32)
		if err != nil {
			return "", "", err
		}
		next = "obr_" + next
	}
	return "oba_" + access, next, nil
}

func newHostedOAuth(accounts *HostedAccounts) *server.Server {
	storage := &hostedOAuthStore{accounts: accounts}
	manager := manage.NewDefaultManager()
	manager.MapClientStorage(storage)
	manager.MapTokenStorage(storage)
	manager.MapAuthorizeGenerate(busAuthorizationCode{})
	manager.MapAccessGenerate(busAccessToken{})
	manager.SetAuthorizeCodeExp(5 * time.Minute)
	manager.SetAuthorizeCodeTokenCfg(&manage.Config{AccessTokenExp: time.Hour, RefreshTokenExp: 30 * 24 * time.Hour, IsGenerateRefresh: true})
	manager.SetRefreshTokenCfg(&manage.RefreshingConfig{AccessTokenExp: time.Hour, RefreshTokenExp: 30 * 24 * time.Hour, IsGenerateRefresh: true, IsResetRefreshTime: false, IsRemoveAccess: true, IsRemoveRefreshing: true})
	manager.SetValidateURIHandler(func(registered, redirect string) error {
		var values []string
		if json.Unmarshal([]byte(registered), &values) != nil {
			return oautherrors.ErrInvalidRedirectURI
		}
		for _, value := range values {
			if value == redirect {
				return nil
			}
		}
		return oautherrors.ErrInvalidRedirectURI
	})
	manager.SetExtractExtensionHandler(func(request *oauth2.TokenGenerateRequest, token oauth2.ExtendableTokenInfo) {
		if request.Request == nil {
			return
		}
		// Resource is fixed by the server and checked before token exchange.
		if grant, ok := request.Request.Context().Value(oauthGrantKey{}).(oauthGrant); ok {
			token.SetExtension(url.Values{"resource": {grant.Resource}, "device_id": {grant.DeviceID}})
		}
	})
	return server.NewServer(&server.Config{TokenType: "Bearer", AllowedResponseTypes: []oauth2.ResponseType{oauth2.Code}, AllowedGrantTypes: []oauth2.GrantType{oauth2.AuthorizationCode, oauth2.Refreshing}, AllowedCodeChallengeMethods: []oauth2.CodeChallengeMethod{oauth2.CodeChallengeS256}, ForcePKCE: true}, manager)
}

func decodeOAuthToken(data string) (*models.Token, error) {
	var token models.Token
	err := json.Unmarshal([]byte(data), &token)
	return &token, err
}

type hostedOAuthClient struct {
	models.Client
	method     string
	secretHash string
}

func (c *hostedOAuthClient) VerifyPassword(secret string) bool {
	if c.Public {
		return secret == ""
	}
	return c.secretHash != "" && secureEqual(tokenDigest(secret), c.secretHash)
}
