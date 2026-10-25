package bus

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ChangeMember removes a member when role is empty. All their active execution
// credentials and device grants are revoked in the same workspace transaction.
func (a *HostedAccounts) ChangeMember(ctx context.Context, user HostedUser, scopeID, targetID, role string) error {
	if role != "" && role != "owner" && role != "admin" && role != "member" {
		return Errorf(CodeInvalidArgument, "Unknown workspace role")
	}
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actorRole, err := hostedMember(ctx, tx, scopeID, user.ID)
	if err != nil {
		return err
	}
	targetRole, err := hostedMember(ctx, tx, scopeID, targetID)
	if err != nil {
		return err
	}
	selfLeave := user.ID == targetID && role == ""
	if !selfLeave && actorRole != "owner" && !(actorRole == "admin" && targetRole == "member" && (role == "" || role == "member")) {
		return Errorf(CodePermissionDenied, "Your workspace role cannot change this membership")
	}
	if targetRole == "owner" && role != "owner" {
		var owners int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_members WHERE scope_id=? AND role='owner'", scopeID).Scan(&owners); err != nil {
			return err
		}
		if owners <= 1 {
			return Errorf(CodeConflict, "Assign another owner before leaving or removing the last owner")
		}
	}
	if role == "" {
		if err := retireHostedSessions(ctx, tx, scopeID, targetID, ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hosted_invites SET revoked_at=? WHERE scope_id=? AND (invited_by=? OR email IN (SELECT email FROM hosted_members WHERE scope_id=? AND user_id=?))`, nowMillis(), scopeID, targetID, scopeID, targetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM hosted_members WHERE scope_id=? AND user_id=?", scopeID, targetID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, "UPDATE hosted_members SET role=? WHERE scope_id=? AND user_id=?", role, scopeID, targetID); err != nil {
			return err
		}
	}
	if err := appendEvent(ctx, tx, scopeID, "workspace.member_changed", targetID, eventAttributes("role", role), nowMillis()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.runtime.signals.notifyAllScope(scopeID)
	return nil
}

func retireHostedSessions(ctx context.Context, tx sqlExecutor, scopeID, userID, deviceID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.agent_id FROM hosted_sessions s JOIN hosted_devices d ON d.device_id=s.device_id
WHERE s.scope_id=? AND (?='' OR d.user_id=?) AND (?='' OR d.device_id=?)`, scopeID, userID, userID, deviceID, deviceID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	now := nowMillis()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET lifecycle='offline',ready=0,lease_expires_at=0,updated_at=? WHERE scope_id=? AND agent_id=?", now, scopeID, id); err != nil {
			return err
		}
		if err := releaseAgentReservations(ctx, tx, scopeID, id); err != nil {
			return err
		}
		if err := appendEvent(ctx, tx, scopeID, "agent.lifecycle_changed", id, eventAttributes("lifecycle", "offline", "reason", "access_revoked"), now); err != nil {
			return err
		}
	}
	return releaseStaleTaskClaims(ctx, tx, scopeID)
}

func (a *HostedAccounts) EnrollDevice(ctx context.Context, user HostedUser, scopeID, name string) (WorkspaceDevice, error) {
	if err := validateText(name, "name", 100, false); err != nil {
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
	device, err := a.enrollDeviceTx(ctx, tx, user, scopeID, name, "device")
	if err != nil {
		return WorkspaceDevice{}, err
	}
	return device, tx.Commit()
}

func (a *HostedAccounts) enrollDeviceTx(ctx context.Context, tx sqlExecutor, user HostedUser, scopeID, name, kind string) (WorkspaceDevice, error) {
	if err := checkHostedConnectionLimit(ctx, tx, scopeID, user.ID, ""); err != nil {
		return WorkspaceDevice{}, err
	}
	id, err := randomID("device_")
	if err != nil {
		return WorkspaceDevice{}, err
	}
	token, expires, err := newHostedConnectionCredential(kind)
	if err != nil {
		return WorkspaceDevice{}, err
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, `INSERT INTO hosted_devices(device_id,scope_id,user_id,name,kind,token_hash,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, id, scopeID, user.ID, name, kind, oauthHash(token), now, expires); err != nil {
		return WorkspaceDevice{}, err
	}
	return WorkspaceDevice{ID: id, UserID: user.ID, Name: name, Kind: kind, CreatedAt: instant(now), ExpiresAt: instant(expires), Token: token}, nil
}

func newHostedConnectionCredential(kind string) (string, int64, error) {
	prefix, days := "obd_", 365
	switch kind {
	case "mcp":
		return "", nowMillis() + (30 * 24 * time.Hour).Milliseconds(), nil
	case "mcp_key":
		prefix, days = "obk_", 90
	case "device":
	default:
		return "", 0, Errorf(CodeInvalidArgument, "Unknown connection kind")
	}
	secret, err := randomValue(32)
	return prefix + secret, nowMillis() + (time.Duration(days) * 24 * time.Hour).Milliseconds(), err
}

func checkHostedConnectionLimit(ctx context.Context, tx sqlExecutor, scopeID, userID, exceptID string) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_devices WHERE scope_id=? AND user_id=? AND revoked_at=0 AND expires_at>? AND device_id<>?", scopeID, userID, nowMillis(), exceptID).Scan(&count); err != nil {
		return err
	}
	if count >= 20 {
		return Errorf(CodeBackpressure, "Revoke an unused connection before adding another; each member can have 20 connections per workspace")
	}
	return nil
}

// replaceConnectionTx is reachable only through verified account approval of a
// fresh connection. A device or app token alone cannot recover another grant.
// Keeping the device ID preserves its session ownership and queued inboxes;
// retiring every execution prevents the lost credential from running alongside
// the replacement. Even workspace owners cannot take over another user's device.
func (a *HostedAccounts) replaceConnectionTx(ctx context.Context, tx sqlExecutor, user HostedUser, scopeID, deviceID, name, expectedKind string) (WorkspaceDevice, error) {
	var ownerID, kind string
	var created int64
	err := tx.QueryRowContext(ctx, "SELECT user_id,kind,created_at FROM hosted_devices WHERE scope_id=? AND device_id=?", scopeID, deviceID).Scan(&ownerID, &kind, &created)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (ownerID != user.ID || kind != expectedKind)) {
		return WorkspaceDevice{}, Errorf(CodePermissionDenied, "Only your own connections of the same kind can be replaced here")
	}
	if err != nil {
		return WorkspaceDevice{}, err
	}
	if err := checkHostedConnectionLimit(ctx, tx, scopeID, user.ID, deviceID); err != nil {
		return WorkspaceDevice{}, err
	}
	token, expires, err := newHostedConnectionCredential(kind)
	if err != nil {
		return WorkspaceDevice{}, err
	}
	if err := retireHostedSessions(ctx, tx, scopeID, "", deviceID); err != nil {
		return WorkspaceDevice{}, err
	}
	// Remove the previous OAuth family before replacing its grant. Replaying an
	// old refresh token must not revoke the new explicitly approved connection.
	if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_tokens WHERE device_id=?", deviceID); err != nil {
		return WorkspaceDevice{}, err
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_devices SET name=?,token_hash=?,expires_at=?,revoked_at=0 WHERE scope_id=? AND device_id=?", name, oauthHash(token), expires, scopeID, deviceID); err != nil {
		return WorkspaceDevice{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "workspace.connection_replaced", deviceID, eventAttributes("userId", user.ID), now); err != nil {
		return WorkspaceDevice{}, err
	}
	return WorkspaceDevice{ID: deviceID, UserID: user.ID, Name: name, Kind: kind, CreatedAt: instant(created), ExpiresAt: instant(expires), Token: token}, nil
}

func (a *HostedAccounts) Devices(ctx context.Context, user HostedUser, scopeID string) ([]WorkspaceDevice, error) {
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := hostedMember(ctx, tx, scopeID, user.ID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT device_id,user_id,name,kind,created_at,expires_at,revoked_at FROM hosted_devices WHERE scope_id=? ORDER BY created_at,device_id", scopeID)
	if err != nil {
		return nil, err
	}
	devices := []WorkspaceDevice{}
	for rows.Next() {
		var d WorkspaceDevice
		var created, expires, revoked int64
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.Kind, &created, &expires, &revoked); err != nil {
			rows.Close()
			return nil, err
		}
		d.CreatedAt, d.ExpiresAt, d.RevokedAt = instant(created), instant(expires), instant(revoked)
		devices = append(devices, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return devices, tx.Commit()
}

func (a *HostedAccounts) RevokeDevice(ctx context.Context, user HostedUser, scopeID, deviceID string) error {
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := hostedMember(ctx, tx, scopeID, user.ID)
	if err != nil {
		return err
	}
	var ownerID string
	err = tx.QueryRowContext(ctx, "SELECT user_id FROM hosted_devices WHERE scope_id=? AND device_id=?", scopeID, deviceID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return Errorf(CodeNotFound, "Connection was not found")
	}
	if err != nil {
		return err
	}
	ownerRole, err := hostedMember(ctx, tx, scopeID, ownerID)
	if err != nil {
		return err
	}
	if ownerID != user.ID && role != "owner" && !(role == "admin" && ownerRole == "member") {
		return Errorf(CodePermissionDenied, "Your workspace role cannot revoke this connection")
	}
	if err := retireHostedSessions(ctx, tx, scopeID, "", deviceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_devices SET revoked_at=? WHERE device_id=? AND revoked_at=0", nowMillis(), deviceID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.runtime.signals.notifyAllScope(scopeID)
	return nil
}

type hostedDeviceIdentity struct {
	ID, ScopeID, UserID string
}

func authenticateHostedDevice(ctx context.Context, q sqlExecutor, token string) (hostedDeviceIdentity, error) {
	var d hostedDeviceIdentity
	err := q.QueryRowContext(ctx, `SELECT d.device_id,d.scope_id,d.user_id FROM hosted_devices d JOIN hosted_members m ON m.scope_id=d.scope_id AND m.user_id=d.user_id
WHERE d.token_hash=? AND d.kind='device' AND d.revoked_at=0 AND d.expires_at>?`, tokenDigest(token), nowMillis()).Scan(&d.ID, &d.ScopeID, &d.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return d, Errorf(CodeUnauthenticated, "Connection has expired or been revoked; reconnect this device")
	}
	return d, err
}

// StartDeviceSession starts a fresh agent or replaces only this device's own
// named session. Resume preserves its durable inbox while fencing its old token.
func (a *HostedAccounts) StartDeviceSession(ctx context.Context, token, name, resumeID string) (RegisterAgentResult, error) {
	if err := validateText(name, "name", 256, false); err != nil {
		return RegisterAgentResult{}, err
	}
	if err := validateIdentity(resumeID, "sessionId", true); err != nil {
		return RegisterAgentResult{}, err
	}
	d, err := authenticateHostedDevice(ctx, a.store.db, token)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	tx, err := a.store.beginTx(ctx, d.ScopeID)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	defer tx.Rollback()
	// Recheck inside the lock so revocation cannot race session registration.
	d, err = authenticateHostedDevice(ctx, tx, token)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	result, err := a.startSessionTx(ctx, tx, d, name, resumeID)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RegisterAgentResult{}, err
	}
	a.runtime.signals.notifyAllScope(d.ScopeID)
	return result, nil
}

func (a *HostedAccounts) startSessionTx(ctx context.Context, tx sqlExecutor, d hostedDeviceIdentity, name, resumeID string) (RegisterAgentResult, error) {
	var err error
	var trial, paid, disabled int64
	var limit int
	if err := tx.QueryRowContext(ctx, "SELECT trial_ends_at,paid_until,GREATEST(disabled_at,paused_at),session_limit FROM hosted_workspaces WHERE scope_id=?", d.ScopeID).Scan(&trial, &paid, &disabled, &limit); err != nil {
		return RegisterAgentResult{}, err
	}
	now := nowMillis()
	if disabled != 0 || (trial <= now && paid <= now) {
		return RegisterAgentResult{}, Errorf(CodePermissionDenied, "Workspace hosting is inactive; an owner can renew it in Billing")
	}
	if resumeID != "" {
		var found int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM hosted_sessions WHERE scope_id=? AND agent_id=? AND device_id=?", d.ScopeID, resumeID, d.ID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return RegisterAgentResult{}, Errorf(CodePermissionDenied, "This connection does not own that session")
		}
		if err != nil {
			return RegisterAgentResult{}, err
		}
	} else {
		resumeID, err = randomID("session_")
		if err != nil {
			return RegisterAgentResult{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT agent_id FROM agents WHERE scope_id=? AND lease_expires_at>? AND agent_id<>? ORDER BY agent_id", d.ScopeID, now, resumeID)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	peers := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return RegisterAgentResult{}, err
		}
		peers = append(peers, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RegisterAgentResult{}, err
	}
	if len(peers) >= limit {
		return RegisterAgentResult{}, Errorf(CodeBackpressure, "Workspace concurrent session limit is full")
	}
	result, err := registerAgentTx(ctx, tx, d.ScopeID, RegisterAgentInput{ID: resumeID, DisplayName: name, Capabilities: []AgentCapability{}, ConnectTo: peers, LeaseMS: (5 * time.Minute).Milliseconds()})
	if err != nil {
		return RegisterAgentResult{}, err
	}
	sealed, err := a.seal(result.AgentToken, "session/"+d.ScopeID+"/"+result.AgentID)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hosted_sessions(scope_id,agent_id,device_id,credential,created_at) VALUES(?,?,?,?,?)
ON CONFLICT(scope_id,agent_id) DO UPDATE SET credential=excluded.credential`, d.ScopeID, result.AgentID, d.ID, sealed, now); err != nil {
		return RegisterAgentResult{}, err
	}
	return result, nil
}
