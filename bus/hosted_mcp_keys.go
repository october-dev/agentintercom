package bus

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

// CreateMCPKey issues a single-connector credential for HTTP MCP clients that
// support bearer authentication but not OAuth. Only a verified account can
// create or replace it; neither the key nor its execution has account authority.
func (a *HostedAccounts) CreateMCPKey(ctx context.Context, user HostedUser, scopeID, name, replaceID string) (WorkspaceDevice, error) {
	if err := validateText(name, "name", 100, false); err != nil {
		return WorkspaceDevice{}, err
	}
	if err := validateIdentity(replaceID, "replaceDeviceId", true); err != nil {
		return WorkspaceDevice{}, err
	}
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return WorkspaceDevice{}, err
	}
	defer tx.Rollback()
	if _, err := hostedMember(ctx, tx, scopeID, user.ID); err != nil {
		return WorkspaceDevice{}, err
	}
	var device WorkspaceDevice
	if replaceID == "" {
		device, err = a.enrollDeviceTx(ctx, tx, user, scopeID, name, "mcp_key")
	} else {
		device, err = a.replaceConnectionTx(ctx, tx, user, scopeID, replaceID, name, "mcp_key")
	}
	if err != nil {
		return WorkspaceDevice{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceDevice{}, err
	}
	a.runtime.signals.notifyAllScope(scopeID)
	return device, nil
}

func authenticateMCPKey(ctx context.Context, q sqlExecutor, token string) (hostedDeviceIdentity, string, error) {
	var d hostedDeviceIdentity
	var name string
	refused := Errorf(CodeUnauthenticated, "MCP key expired or was revoked; replace it in Members & devices")
	if len(token) != 47 || !strings.HasPrefix(token, "obk_") {
		return d, "", refused
	}
	err := q.QueryRowContext(ctx, `SELECT d.device_id,d.scope_id,d.user_id,d.name FROM hosted_devices d
JOIN hosted_members m ON m.scope_id=d.scope_id AND m.user_id=d.user_id
WHERE d.token_hash=? AND d.kind='mcp_key' AND d.revoked_at=0 AND d.expires_at>?`, tokenDigest(token), nowMillis()).Scan(&d.ID, &d.ScopeID, &d.UserID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return d, "", refused
	}
	return d, name, err
}

func (h *HostedServer) mcpKeyExecution(ctx context.Context, token string) (string, error) {
	select {
	case h.oauthRequests <- struct{}{}:
		defer func() { <-h.oauthRequests }()
	default:
		return "", Errorf(CodeBackpressure, "Authentication is busy; retry shortly")
	}
	d, _, err := authenticateMCPKey(ctx, h.accounts.store.db, token)
	if err != nil {
		return "", err
	}
	tx, err := h.accounts.store.beginTx(ctx, d.ScopeID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// Rotation and membership changes use the same workspace lock.
	d, name, err := authenticateMCPKey(ctx, tx, token)
	if err != nil {
		return "", err
	}
	return h.accounts.commitConnectorExecution(ctx, tx, d, name)
}

// commitConnectorExecution shares the one-inbox lifecycle between OAuth and
// manual-key connectors. The caller must authenticate under this workspace
// transaction; this function commits before returning an execution credential.
func (a *HostedAccounts) commitConnectorExecution(ctx context.Context, tx *sqlTransaction, d hostedDeviceIdentity, name string) (string, error) {
	var id, credential, execution string
	var lease int64
	err := tx.QueryRowContext(ctx, `SELECT s.agent_id,s.credential,a.execution_id,a.lease_expires_at FROM hosted_sessions s
JOIN agents a ON a.scope_id=s.scope_id AND a.agent_id=s.agent_id
WHERE s.scope_id=? AND s.device_id=? ORDER BY s.created_at LIMIT 1`, d.ScopeID, d.ID).Scan(&id, &credential, &execution, &lease)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if id == "" || lease <= nowMillis() {
		result, err := a.startSessionTx(ctx, tx, d, name, id)
		if err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		a.runtime.notifyScope(d.ScopeID)
		return result.AgentToken, nil
	}
	if err := requireHostedExecution(ctx, tx, Principal{AgentIdentity: AgentIdentity{ScopeID: d.ScopeID, AgentID: id, ExecutionID: execution}}, nowMillis()); err != nil {
		return "", err
	}
	plain, err := a.unseal(credential, "session/"+d.ScopeID+"/"+id)
	if err != nil {
		return "", err
	}
	// Activity proves a live connector, not that its model can wake.
	if _, err := tx.ExecContext(ctx, "UPDATE agents SET lease_expires_at=?,updated_at=? WHERE scope_id=? AND agent_id=?", nowMillis()+(5*time.Minute).Milliseconds(), nowMillis(), d.ScopeID, id); err != nil {
		return "", err
	}
	return plain, tx.Commit()
}

func (h *HostedServer) registerMCPKeyRoutes() {
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/mcp-keys", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Name            string `json:"name"`
			ReplaceDeviceID string `json:"replaceDeviceId"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		return h.accounts.CreateMCPKey(ctx, user, r.PathValue("workspace"), input.Name, input.ReplaceDeviceID)
	}))
}
