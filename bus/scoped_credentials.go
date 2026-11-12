package bus

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const scopedCredentialCapPerScope = 1000

type scopedCredentialGrant struct {
	ResourceType string
	ResourceID   string
	Permission   string
}

type scopedCredentialRecord struct {
	ID        string
	ScopeID   string
	Label     string
	Enabled   bool
	CreatedAt int64
	UpdatedAt int64
}

const scopedCredentialColumns = `credential_id,scope_id,label,enabled,created_at,updated_at`

func scanScopedCredential(row rowScanner) (scopedCredentialRecord, error) {
	var credential scopedCredentialRecord
	var enabled int
	err := row.Scan(&credential.ID, &credential.ScopeID, &credential.Label, &enabled, &credential.CreatedAt, &credential.UpdatedAt)
	credential.Enabled = enabled == 1
	return credential, err
}

func createScopedCredential(ctx context.Context, tx sqlExecutor, scopeID, label string, grants []scopedCredentialGrant, now int64) (scopedCredentialRecord, string, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scoped_credentials WHERE scope_id=?`, scopeID).Scan(&count); err != nil {
		return scopedCredentialRecord{}, "", err
	}
	if count >= scopedCredentialCapPerScope {
		return scopedCredentialRecord{}, "", Errorf(CodeBackpressure, "Scope credential limit is full")
	}
	credentialID, err := randomID("cred_")
	if err != nil {
		return scopedCredentialRecord{}, "", err
	}
	secret, err := randomValue(32)
	if err != nil {
		return scopedCredentialRecord{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scoped_credentials(credential_id,scope_id,label,token_hash,enabled,created_at,updated_at) VALUES(?,?,?,?,1,?,?)`,
		credentialID, scopeID, label, tokenDigest(secret), now, now); err != nil {
		return scopedCredentialRecord{}, "", err
	}
	for _, grant := range grants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scoped_credential_grants(credential_id,resource_type,resource_id,permission) VALUES(?,?,?,?)`,
			credentialID, grant.ResourceType, grant.ResourceID, grant.Permission); err != nil {
			return scopedCredentialRecord{}, "", err
		}
	}
	record := scopedCredentialRecord{ID: credentialID, ScopeID: scopeID, Label: label, Enabled: true, CreatedAt: now, UpdatedAt: now}
	return record, credentialID + "." + secret, nil
}

func rotateScopedCredential(ctx context.Context, tx sqlExecutor, scopeID, credentialID string, now int64) (scopedCredentialRecord, string, error) {
	record, err := scanScopedCredential(tx.QueryRowContext(ctx, `SELECT `+scopedCredentialColumns+` FROM scoped_credentials WHERE scope_id=? AND credential_id=?`, scopeID, credentialID))
	if errors.Is(err, sql.ErrNoRows) {
		return scopedCredentialRecord{}, "", Errorf(CodeNotFound, "Scoped credential was not found")
	}
	if err != nil {
		return scopedCredentialRecord{}, "", err
	}
	secret, err := randomValue(32)
	if err != nil {
		return scopedCredentialRecord{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scoped_credentials SET token_hash=?,updated_at=? WHERE scope_id=? AND credential_id=?`, tokenDigest(secret), now, scopeID, credentialID); err != nil {
		return scopedCredentialRecord{}, "", err
	}
	record.UpdatedAt = now
	return record, credentialID + "." + secret, nil
}

func setScopedCredentialEnabled(ctx context.Context, tx sqlExecutor, scopeID, credentialID string, enabled bool, now int64) (scopedCredentialRecord, bool, error) {
	record, err := scanScopedCredential(tx.QueryRowContext(ctx, `SELECT `+scopedCredentialColumns+` FROM scoped_credentials WHERE scope_id=? AND credential_id=?`, scopeID, credentialID))
	if errors.Is(err, sql.ErrNoRows) {
		return scopedCredentialRecord{}, false, Errorf(CodeNotFound, "Scoped credential was not found")
	}
	if err != nil {
		return scopedCredentialRecord{}, false, err
	}
	if record.Enabled == enabled {
		return record, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scoped_credentials SET enabled=?,updated_at=? WHERE scope_id=? AND credential_id=?`, enabled, now, scopeID, credentialID); err != nil {
		return scopedCredentialRecord{}, false, err
	}
	record.Enabled = enabled
	record.UpdatedAt = now
	return record, true, nil
}

func splitScopedCredential(value string) (string, string, bool) {
	credentialID, secret, found := strings.Cut(value, ".")
	if !found || credentialID == "" || secret == "" || strings.Contains(secret, ".") {
		return "", "", false
	}
	return credentialID, secret, true
}

type scopedCredentialQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func authenticateScopedCredential(ctx context.Context, query scopedCredentialQuery, supplied string, grant scopedCredentialGrant) (scopedCredentialRecord, error) {
	credentialID, secret, valid := splitScopedCredential(supplied)
	if !valid {
		secureEqual(tokenDigest("invalid"), tokenDigest(supplied))
		return scopedCredentialRecord{}, Errorf(CodeUnauthenticated, "Invalid scoped credential")
	}
	var record scopedCredentialRecord
	var enabled int
	var expectedHash string
	err := query.QueryRowContext(ctx, `SELECT c.credential_id,c.scope_id,c.label,c.enabled,c.created_at,c.updated_at,c.token_hash
FROM scoped_credentials c
JOIN scoped_credential_grants g ON g.credential_id=c.credential_id
WHERE c.credential_id=? AND g.resource_type=? AND g.resource_id=? AND g.permission=?`,
		credentialID, grant.ResourceType, grant.ResourceID, grant.Permission).
		Scan(&record.ID, &record.ScopeID, &record.Label, &enabled, &record.CreatedAt, &record.UpdatedAt, &expectedHash)
	record.Enabled = enabled == 1
	actualHash := tokenDigest(secret)
	if errors.Is(err, sql.ErrNoRows) {
		secureEqual(tokenDigest("invalid"), actualHash)
		return scopedCredentialRecord{}, Errorf(CodeUnauthenticated, "Invalid scoped credential")
	}
	if err != nil {
		return scopedCredentialRecord{}, err
	}
	if !record.Enabled || !secureEqual(expectedHash, actualHash) {
		return scopedCredentialRecord{}, Errorf(CodeUnauthenticated, "Invalid scoped credential")
	}
	return record, nil
}

func (s *Store) authenticateScopedCredential(ctx context.Context, supplied string, grant scopedCredentialGrant) (scopedCredentialRecord, error) {
	return authenticateScopedCredential(ctx, s.db, supplied, grant)
}
