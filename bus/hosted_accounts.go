package bus

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"
	"time"
)

// HostedUser is returned by the configured identity verifier. Request bodies
// must never be decoded into it or used as a substitute for verified identity.
type HostedUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type Workspace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	CreatedAt   string `json:"createdAt"`
	TrialEndsAt string `json:"trialEndsAt"`
	PaidUntil   string `json:"paidUntil,omitempty"`
	Paused      bool   `json:"paused"`
	Disabled    bool   `json:"disabled"`
	MemberLimit int    `json:"memberLimit"`
}

type WorkspaceMember struct {
	UserID   string `json:"userId"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	JoinedAt string `json:"joinedAt"`
}

type WorkspaceInvite struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expiresAt"`
	// Token is returned only when the invite is created. It is stored as a hash.
	Token string `json:"token,omitempty"`
}

type WorkspaceDevice struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	RevokedAt string `json:"revokedAt,omitempty"`
	Token     string `json:"token,omitempty"`
}

// HostedAccounts adds account ownership to the existing runtime. The encryption
// key protects service-owned credentials needed to call the same runtime APIs;
// user device/invite secrets are stored only as digests. Back up this key outside
// the database. A runtime identity without the key cannot decrypt these grants.
type HostedAccounts struct {
	runtime *Runtime
	store   *Store
	sealer  cipher.AEAD
}

func NewHostedAccounts(runtime *Runtime, encryptionKey []byte) (*HostedAccounts, error) {
	store, ok := runtime.store.(*Store)
	if !ok || store.Backend() != StorageBackendPostgres {
		return nil, errors.New("hosted accounts require PostgreSQL storage")
	}
	if len(encryptionKey) != 32 {
		return nil, errors.New("hosted credential encryption requires a 32-byte key")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	sealer, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &HostedAccounts{runtime: runtime, store: store, sealer: sealer}, nil
}

func (a *HostedAccounts) seal(value, purpose string) (string, error) {
	nonce := make([]byte, a.sealer.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(a.sealer.Seal(nonce, nonce, []byte(value), []byte(purpose))), nil
}

func (a *HostedAccounts) unseal(value, purpose string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	n := a.sealer.NonceSize()
	if err != nil || len(data) < n+a.sealer.Overhead() {
		return "", errors.New("hosted credential is unavailable")
	}
	plain, err := a.sealer.Open(nil, data[:n], data[n:], []byte(purpose))
	if err != nil {
		return "", errors.New("hosted credential is unavailable; verify the service encryption key")
	}
	return string(plain), nil
}

func normalizedEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n\x00") {
		return "", Errorf(CodeInvalidArgument, "A valid email address is required")
	}
	return value, nil
}

func validateHostedUser(user HostedUser) error {
	if err := validateIdentity(user.ID, "account", false); err != nil {
		return Errorf(CodeUnauthenticated, "Sign in to October")
	}
	if _, err := normalizedEmail(user.Email); err != nil {
		return Errorf(CodeUnauthenticated, "A verified account email is required")
	}
	return nil
}

func hostedMember(ctx context.Context, q sqlExecutor, scopeID, userID string) (string, error) {
	var role string
	err := q.QueryRowContext(ctx, "SELECT role FROM hosted_members WHERE scope_id=? AND user_id=?", scopeID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Errorf(CodePermissionDenied, "Workspace access is unavailable")
	}
	return role, err
}

func (a *HostedAccounts) CreateWorkspace(ctx context.Context, user HostedUser, name string) (Workspace, error) {
	if err := validateHostedUser(user); err != nil {
		return Workspace{}, err
	}
	if err := validateText(name, "name", 100, false); err != nil {
		return Workspace{}, err
	}
	id, err := randomID("ws_")
	if err != nil {
		return Workspace{}, err
	}
	token, err := randomValue(32)
	if err != nil {
		return Workspace{}, err
	}
	sealed, err := a.seal(token, "workspace/"+id)
	if err != nil {
		return Workspace{}, err
	}
	// Account-level serialization bounds concurrent trial creation. The new
	// workspace cannot be reached before this transaction commits.
	tx, err := a.store.beginTx(ctx, "account/"+user.ID)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_members WHERE user_id=? AND role='owner'", user.ID).Scan(&count); err != nil {
		return Workspace{}, err
	}
	if count >= 3 {
		return Workspace{}, Errorf(CodeBackpressure, "An account can own up to three workspaces")
	}
	now := nowMillis()
	trialEnd := now + (14 * 24 * time.Hour).Milliseconds()
	// Additional workspaces share the account's original trial window. Deleting
	// a workspace or creating another one cannot restart a free trial.
	if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_trial_accounts(user_id,started_at,ends_at) VALUES(?,?,?) ON CONFLICT(user_id) DO NOTHING", user.ID, now, trialEnd); err != nil {
		return Workspace{}, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT ends_at FROM hosted_trial_accounts WHERE user_id=?", user.ID).Scan(&trialEnd); err != nil {
		return Workspace{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO scopes(scope_id,token_hash,created_at) VALUES(?,?,?)", id, tokenDigest(token), now); err != nil {
		return Workspace{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_workspaces(scope_id,name,owner_credential,created_at,trial_ends_at) VALUES(?,?,?,?,?)", id, strings.TrimSpace(name), sealed, now, trialEnd); err != nil {
		return Workspace{}, err
	}
	email, _ := normalizedEmail(user.Email)
	if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_members(scope_id,user_id,email,role,joined_at) VALUES(?,?,?,'owner',?)", id, user.ID, email, now); err != nil {
		return Workspace{}, err
	}
	return Workspace{ID: id, Name: strings.TrimSpace(name), Role: "owner", CreatedAt: instant(now), TrialEndsAt: instant(trialEnd), MemberLimit: 5}, tx.Commit()
}

func (a *HostedAccounts) ListWorkspaces(ctx context.Context, user HostedUser) ([]Workspace, error) {
	if err := validateHostedUser(user); err != nil {
		return nil, err
	}
	rows, err := a.store.db.QueryContext(ctx, `SELECT w.scope_id,w.name,m.role,w.created_at,w.trial_ends_at,w.paid_until,w.disabled_at,w.paused_at,w.member_limit
FROM hosted_workspaces w JOIN hosted_members m ON m.scope_id=w.scope_id WHERE m.user_id=? ORDER BY w.created_at,w.scope_id`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Workspace{}
	for rows.Next() {
		var v Workspace
		var created, trial, paid, disabled, paused int64
		if err := rows.Scan(&v.ID, &v.Name, &v.Role, &created, &trial, &paid, &disabled, &paused, &v.MemberLimit); err != nil {
			return nil, err
		}
		v.CreatedAt, v.TrialEndsAt, v.PaidUntil, v.Disabled, v.Paused = instant(created), instant(trial), instant(paid), disabled != 0, paused != 0
		values = append(values, v)
	}
	return values, rows.Err()
}

func (a *HostedAccounts) Members(ctx context.Context, user HostedUser, scopeID string) ([]WorkspaceMember, error) {
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := hostedMember(ctx, tx, scopeID, user.ID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT user_id,email,role,joined_at FROM hosted_members WHERE scope_id=? ORDER BY joined_at,user_id", scopeID)
	if err != nil {
		return nil, err
	}
	values := []WorkspaceMember{}
	for rows.Next() {
		var v WorkspaceMember
		var joined int64
		if err := rows.Scan(&v.UserID, &v.Email, &v.Role, &joined); err != nil {
			rows.Close()
			return nil, err
		}
		v.JoinedAt = instant(joined)
		values = append(values, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return values, tx.Commit()
}

func (a *HostedAccounts) Invite(ctx context.Context, user HostedUser, scopeID, email, role string) (WorkspaceInvite, error) {
	email, err := normalizedEmail(email)
	if err != nil {
		return WorkspaceInvite{}, err
	}
	if role != "member" && role != "admin" {
		return WorkspaceInvite{}, Errorf(CodeInvalidArgument, "Invite role must be member or admin")
	}
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return WorkspaceInvite{}, err
	}
	defer tx.Rollback()
	actorRole, err := hostedMember(ctx, tx, scopeID, user.ID)
	if err != nil {
		return WorkspaceInvite{}, err
	}
	if actorRole != "owner" && !(actorRole == "admin" && role == "member") {
		return WorkspaceInvite{}, Errorf(CodePermissionDenied, "Your workspace role cannot issue this invitation")
	}
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_invites WHERE scope_id=? AND consumed_at=0 AND revoked_at=0 AND expires_at>?", scopeID, nowMillis()).Scan(&pending); err != nil {
		return WorkspaceInvite{}, err
	}
	if pending >= 20 {
		return WorkspaceInvite{}, Errorf(CodeBackpressure, "Workspace has too many pending invitations")
	}
	id, err := randomID("invite_")
	if err != nil {
		return WorkspaceInvite{}, err
	}
	token, err := randomValue(32)
	if err != nil {
		return WorkspaceInvite{}, err
	}
	now := nowMillis()
	expires := now + (7 * 24 * time.Hour).Milliseconds()
	if _, err := tx.ExecContext(ctx, `INSERT INTO hosted_invites(invite_id,scope_id,token_hash,email,role,invited_by,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, id, scopeID, tokenDigest(token), email, role, user.ID, now, expires); err != nil {
		return WorkspaceInvite{}, err
	}
	return WorkspaceInvite{ID: id, Email: email, Role: role, ExpiresAt: instant(expires), Token: token}, tx.Commit()
}

func (a *HostedAccounts) AcceptInvite(ctx context.Context, user HostedUser, token string) (string, error) {
	if err := validateHostedUser(user); err != nil {
		return "", err
	}
	var scopeID string
	err := a.store.db.QueryRowContext(ctx, "SELECT scope_id FROM hosted_invites WHERE token_hash=?", tokenDigest(token)).Scan(&scopeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Errorf(CodeNotFound, "Invitation is unavailable")
	}
	if err != nil {
		return "", err
	}
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id, email, role string
	err = tx.QueryRowContext(ctx, `SELECT invite_id,email,role FROM hosted_invites WHERE scope_id=? AND token_hash=? AND consumed_at=0 AND revoked_at=0 AND expires_at>?`, scopeID, tokenDigest(token), nowMillis()).Scan(&id, &email, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Errorf(CodeNotFound, "Invitation is unavailable")
	}
	if err != nil {
		return "", err
	}
	verifiedEmail, _ := normalizedEmail(user.Email)
	if verifiedEmail != email {
		return "", Errorf(CodePermissionDenied, "Sign in with the invited email address")
	}
	var count, limit int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_members WHERE scope_id=?", scopeID).Scan(&count); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, "SELECT member_limit FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&limit); err != nil {
		return "", err
	}
	var exists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM hosted_members WHERE scope_id=? AND user_id=?", scopeID, user.ID).Scan(&exists)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if exists == 0 && count >= limit {
		return "", Errorf(CodeBackpressure, "Workspace member limit is full")
	}
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_members(scope_id,user_id,email,role,joined_at) VALUES(?,?,?,?,?) ON CONFLICT(scope_id,user_id) DO NOTHING", scopeID, user.ID, email, role, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_invites SET consumed_at=? WHERE invite_id=?", now, id); err != nil {
		return "", err
	}
	return scopeID, tx.Commit()
}
