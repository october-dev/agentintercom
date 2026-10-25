package bus

import (
	"context"
	"net/http"
)

type WorkspaceSession struct {
	Agent
	DeviceID     string     `json:"deviceId"`
	DeviceName   string     `json:"deviceName"`
	UserID       string     `json:"userId"`
	DeliveryMode string     `json:"deliveryMode"`
	Wake         WakeStatus `json:"wake"`
}

type WorkspaceSessionPage struct {
	Sessions   []WorkspaceSession `json:"sessions"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

func (a *HostedAccounts) Sessions(ctx context.Context, user HostedUser, scopeID, after string) (WorkspaceSessionPage, error) {
	var result WorkspaceSessionPage
	if err := validateIdentity(after, "after", true); err != nil {
		return result, err
	}
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err := hostedMember(ctx, tx, scopeID, user.ID); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.agent_id,a.display_name,a.capabilities_json,a.lifecycle,a.ready,a.execution_id,a.lease_expires_at,a.registered_at,a.updated_at,
d.device_id,d.name,d.user_id,COALESCE(w.provider,''),COALESCE(w.last_seen_at,0),COALESCE(w.last_attempt_at,0),COALESCE(w.last_outcome,''),
(SELECT COUNT(*) FROM messages m WHERE m.scope_id=a.scope_id AND m.to_kind='agent' AND m.to_id=a.agent_id AND m.state IN ('queued','delivered') AND (m.expires_at IS NULL OR m.expires_at>?)),
(SELECT COUNT(*) FROM wake_attempts wa JOIN messages m ON m.message_id=wa.message_id WHERE wa.scope_id=a.scope_id AND wa.agent_id=a.agent_id AND wa.execution_id=a.execution_id AND (wa.outcome='uncertain' OR (wa.outcome='dispatching' AND wa.updated_at<?)) AND m.state IN ('queued','delivered') AND (m.expires_at IS NULL OR m.expires_at>?))
FROM agents a JOIN hosted_sessions s ON s.scope_id=a.scope_id AND s.agent_id=a.agent_id
JOIN hosted_devices d ON d.device_id=s.device_id LEFT JOIN wake_connections w ON w.scope_id=a.scope_id AND w.agent_id=a.agent_id AND w.execution_id=a.execution_id
WHERE a.scope_id=? AND a.agent_id>? ORDER BY a.agent_id LIMIT 101`, nowMillis(), nowMillis()-30000, nowMillis(), scopeID, after)
	if err != nil {
		return result, err
	}
	result.Sessions = []WorkspaceSession{}
	for rows.Next() {
		var value WorkspaceSession
		agent, err := scanAgent(hostedSessionRow{row: rows, session: &value})
		if err != nil {
			rows.Close()
			return result, err
		}
		value.Agent = agent
		value.DeliveryMode = "poll"
		if value.Wake.Connected && value.Reachable {
			value.DeliveryMode = value.Wake.Provider
		}
		result.Sessions = append(result.Sessions, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Sessions) > 100 {
		result.NextCursor = result.Sessions[99].ID
		result.Sessions = result.Sessions[:100]
	}
	return result, tx.Commit()
}

// Append connection fields to the shared agent decoder's destinations.
type hostedSessionRow struct {
	row     rowScanner
	session *WorkspaceSession
}

func (r hostedSessionRow) Scan(dest ...any) error {
	var seen, attempt int64
	status := &r.session.Wake
	err := r.row.Scan(append(dest, &r.session.DeviceID, &r.session.DeviceName, &r.session.UserID, &status.Provider, &seen, &attempt, &status.LastOutcome, &status.Pending, &status.Uncertain)...)
	status.LastSeenAt = instant(seen)
	status.LastAttemptAt = instant(attempt)
	status.Connected = seen > nowMillis()-90000
	if status.LastOutcome == "dispatching" && attempt < nowMillis()-30000 {
		status.LastOutcome = "uncertain"
	}
	return err
}

func (a *HostedAccounts) Invites(ctx context.Context, user HostedUser, scopeID string) ([]WorkspaceInvite, error) {
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	role, err := hostedMember(ctx, tx, scopeID, user.ID)
	if err != nil {
		return nil, err
	}
	if role == "member" {
		return nil, Errorf(CodePermissionDenied, "An admin or owner must manage invitations")
	}
	rows, err := tx.QueryContext(ctx, "SELECT invite_id,email,role,expires_at FROM hosted_invites WHERE scope_id=? AND consumed_at=0 AND revoked_at=0 AND expires_at>? ORDER BY created_at,invite_id", scopeID, nowMillis())
	if err != nil {
		return nil, err
	}
	result := []WorkspaceInvite{}
	for rows.Next() {
		var v WorkspaceInvite
		var expires int64
		if err := rows.Scan(&v.ID, &v.Email, &v.Role, &expires); err != nil {
			rows.Close()
			return nil, err
		}
		v.ExpiresAt = instant(expires)
		result = append(result, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (a *HostedAccounts) RevokeInvite(ctx context.Context, user HostedUser, scopeID, id string) error {
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := hostedMember(ctx, tx, scopeID, user.ID)
	if err != nil {
		return err
	}
	if role == "member" {
		return Errorf(CodePermissionDenied, "An admin or owner must manage invitations")
	}
	_, err = tx.ExecContext(ctx, "UPDATE hosted_invites SET revoked_at=? WHERE scope_id=? AND invite_id=? AND revoked_at=0", nowMillis(), scopeID, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (h *HostedServer) registerWorkspaceViews() {
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/sessions", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return h.accounts.Sessions(ctx, u, r.PathValue("workspace"), r.URL.Query().Get("after"))
	}))
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/invites", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return h.accounts.Invites(ctx, u, r.PathValue("workspace"))
	}))
	h.routes.HandleFunc("DELETE /api/workspaces/{workspace}/invites/{invite}", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]bool{"revoked": true}, h.accounts.RevokeInvite(ctx, u, r.PathValue("workspace"), r.PathValue("invite"))
	}))
}

// Close cancels protocol long polls and closes the underlying runtime. The
// caller owns the outer HTTP listener and should first stop accepting traffic.
func (h *HostedServer) Close(ctx context.Context) error { return h.core.Stop(ctx) }

func (h *HostedServer) CancelWaits() { h.core.cancelWaits() }
