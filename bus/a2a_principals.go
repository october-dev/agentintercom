package bus

import (
	"context"
	"database/sql"
	"errors"
)

const (
	a2aPublicationResource = "a2aPublication"
	a2aInvokePermission    = "invoke"
)

func a2aPrincipalFrom(record scopedCredentialRecord, publicationID string) A2APrincipal {
	return A2APrincipal{
		ID: record.ID, ScopeID: record.ScopeID, PublicationID: publicationID, Label: record.Label, Enabled: record.Enabled,
		CreatedAt: instant(record.CreatedAt), UpdatedAt: instant(record.UpdatedAt),
	}
}

func (s *Store) CreateA2APrincipal(ctx context.Context, scopeID string, input CreateA2APrincipalInput) (IssuedA2APrincipal, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return IssuedA2APrincipal{}, err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM a2a_publications WHERE scope_id=? AND publication_id=?`, scopeID, input.PublicationID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return IssuedA2APrincipal{}, Errorf(CodeNotFound, "Agent Card publication was not found")
	} else if err != nil {
		return IssuedA2APrincipal{}, err
	}
	now := nowMillis()
	record, credential, err := createScopedCredential(ctx, tx, scopeID, input.Label, []scopedCredentialGrant{{
		ResourceType: a2aPublicationResource, ResourceID: input.PublicationID, Permission: a2aInvokePermission,
	}}, now)
	if err != nil {
		return IssuedA2APrincipal{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "credential.created", record.ID, eventAttributes(
		"resourceType", a2aPublicationResource, "resourceId", input.PublicationID, "permission", a2aInvokePermission, "enabled", "true",
	), now); err != nil {
		return IssuedA2APrincipal{}, err
	}
	if err := tx.Commit(); err != nil {
		return IssuedA2APrincipal{}, err
	}
	return IssuedA2APrincipal{Principal: a2aPrincipalFrom(record, input.PublicationID), Credential: credential}, nil
}

func (s *Store) ListA2APrincipals(ctx context.Context, scopeID string) ([]A2APrincipal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.credential_id,c.scope_id,c.label,c.enabled,c.created_at,c.updated_at,g.resource_id
FROM scoped_credentials c
JOIN scoped_credential_grants g ON g.credential_id=c.credential_id
WHERE c.scope_id=? AND g.resource_type=? AND g.permission=?
ORDER BY c.created_at,c.credential_id`, scopeID, a2aPublicationResource, a2aInvokePermission)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	principals := []A2APrincipal{}
	for rows.Next() {
		var record scopedCredentialRecord
		var enabled int
		var publicationID string
		if err := rows.Scan(&record.ID, &record.ScopeID, &record.Label, &enabled, &record.CreatedAt, &record.UpdatedAt, &publicationID); err != nil {
			return nil, err
		}
		record.Enabled = enabled == 1
		principals = append(principals, a2aPrincipalFrom(record, publicationID))
	}
	return principals, rows.Err()
}

func (s *Store) ListA2APrincipalUsage(ctx context.Context, scopeID string, limits A2APrincipalLimits) ([]A2APrincipalUsage, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := expireMessages(ctx, tx, scopeID, nowMillis()); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT credentials.credential_id,grants.resource_id,
COALESCE(SUM(CASE WHEN tasks.state NOT IN ('completed','failed','canceled','rejected') THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN tasks.state NOT IN ('completed','failed','canceled','rejected') THEN octet_length(messages.body) ELSE 0 END),0)
FROM scoped_credentials AS credentials
JOIN scoped_credential_grants AS grants ON grants.credential_id=credentials.credential_id
LEFT JOIN a2a_message_correlations AS correlations ON correlations.principal_id=credentials.credential_id
LEFT JOIN a2a_tasks AS tasks ON tasks.task_id=correlations.task_id
LEFT JOIN messages ON messages.message_id=correlations.bus_request_message_id
WHERE credentials.scope_id=? AND grants.resource_type=? AND grants.permission=?
GROUP BY credentials.credential_id,grants.resource_id,credentials.created_at
ORDER BY credentials.created_at,credentials.credential_id`, scopeID, a2aPublicationResource, a2aInvokePermission)
	if err != nil {
		return nil, err
	}
	usage := []A2APrincipalUsage{}
	for rows.Next() {
		var item A2APrincipalUsage
		if err := rows.Scan(&item.PrincipalID, &item.PublicationID, &item.UnfinishedMessages, &item.UnfinishedBytes); err != nil {
			rows.Close()
			return nil, err
		}
		item.MessageLimit = limits.MessageLimit
		item.ByteLimit = limits.ByteLimit
		usage = append(usage, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return usage, nil
}

func a2aPrincipalPublication(ctx context.Context, tx sqlExecutor, scopeID, principalID string) (string, error) {
	var publicationID string
	err := tx.QueryRowContext(ctx, `SELECT g.resource_id FROM scoped_credentials c
JOIN scoped_credential_grants g ON g.credential_id=c.credential_id
WHERE c.scope_id=? AND c.credential_id=? AND g.resource_type=? AND g.permission=?`,
		scopeID, principalID, a2aPublicationResource, a2aInvokePermission).Scan(&publicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Errorf(CodeNotFound, "A2A principal was not found")
	}
	return publicationID, err
}

func (s *Store) RotateA2APrincipal(ctx context.Context, scopeID, principalID string) (IssuedA2APrincipal, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return IssuedA2APrincipal{}, err
	}
	defer tx.Rollback()
	publicationID, err := a2aPrincipalPublication(ctx, tx, scopeID, principalID)
	if err != nil {
		return IssuedA2APrincipal{}, err
	}
	now := nowMillis()
	record, credential, err := rotateScopedCredential(ctx, tx, scopeID, principalID, now)
	if err != nil {
		return IssuedA2APrincipal{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "credential.rotated", record.ID, eventAttributes(
		"resourceType", a2aPublicationResource, "resourceId", publicationID, "permission", a2aInvokePermission,
	), now); err != nil {
		return IssuedA2APrincipal{}, err
	}
	if err := tx.Commit(); err != nil {
		return IssuedA2APrincipal{}, err
	}
	return IssuedA2APrincipal{Principal: a2aPrincipalFrom(record, publicationID), Credential: credential}, nil
}

func (s *Store) SetA2APrincipalEnabled(ctx context.Context, scopeID, principalID string, enabled bool) (A2APrincipal, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return A2APrincipal{}, err
	}
	defer tx.Rollback()
	publicationID, err := a2aPrincipalPublication(ctx, tx, scopeID, principalID)
	if err != nil {
		return A2APrincipal{}, err
	}
	now := nowMillis()
	record, changed, err := setScopedCredentialEnabled(ctx, tx, scopeID, principalID, enabled, now)
	if err != nil {
		return A2APrincipal{}, err
	}
	if changed {
		eventType := "credential.disabled"
		if enabled {
			eventType = "credential.enabled"
		}
		if err := appendEvent(ctx, tx, scopeID, eventType, record.ID, eventAttributes(
			"resourceType", a2aPublicationResource, "resourceId", publicationID, "permission", a2aInvokePermission, "enabled", boolString(enabled),
		), now); err != nil {
			return A2APrincipal{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return A2APrincipal{}, err
	}
	return a2aPrincipalFrom(record, publicationID), nil
}

func (s *Store) AuthenticateA2APrincipal(ctx context.Context, credential, publicationID string) (A2APrincipal, error) {
	record, err := s.authenticateScopedCredential(ctx, credential, scopedCredentialGrant{
		ResourceType: a2aPublicationResource, ResourceID: publicationID, Permission: a2aInvokePermission,
	})
	if err != nil {
		return A2APrincipal{}, err
	}
	var enabled int
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM a2a_publications WHERE scope_id=? AND publication_id=?`, record.ScopeID, publicationID).Scan(&enabled); err != nil || enabled != 1 {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return A2APrincipal{}, err
		}
		return A2APrincipal{}, Errorf(CodeUnauthenticated, "Invalid scoped credential")
	}
	principal := a2aPrincipalFrom(record, publicationID)
	_, secret, _ := splitScopedCredential(credential)
	principal.tokenHash = tokenDigest(secret)
	return principal, nil
}

// Fence both revocation and rotation in the protected operation's transaction.
func requireCurrentA2APrincipal(ctx context.Context, tx sqlExecutor, principal A2APrincipal) error {
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT c.token_hash FROM scoped_credentials c
JOIN scoped_credential_grants g ON g.credential_id=c.credential_id
JOIN a2a_publications p ON p.publication_id=g.resource_id AND p.scope_id=c.scope_id
WHERE c.credential_id=? AND c.scope_id=? AND c.enabled=1 AND p.enabled=1
AND g.resource_type=? AND g.resource_id=? AND g.permission=?`, principal.ID, principal.ScopeID,
		a2aPublicationResource, principal.PublicationID, a2aInvokePermission).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (principal.tokenHash == "" || !secureEqual(digest, principal.tokenHash))) {
		return Errorf(CodeUnauthenticated, "A2A credential is no longer current")
	}
	return err
}

func (r *Runtime) CreateA2APrincipal(ctx context.Context, scopeToken string, input CreateA2APrincipalInput) (IssuedA2APrincipal, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return IssuedA2APrincipal{}, err
	}
	if err := validateIdentity(input.PublicationID, "publicationId", false); err != nil {
		return IssuedA2APrincipal{}, err
	}
	if err := validateText(input.Label, "label", 256, false); err != nil {
		return IssuedA2APrincipal{}, err
	}
	result, err := r.store.CreateA2APrincipal(ctx, scopeID, input)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) ListA2APrincipals(ctx context.Context, scopeToken string) ([]A2APrincipal, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return nil, err
	}
	return r.store.ListA2APrincipals(ctx, scopeID)
}

func (r *Runtime) ListA2APrincipalUsage(ctx context.Context, scopeToken string) ([]A2APrincipalUsage, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return nil, err
	}
	return r.store.ListA2APrincipalUsage(ctx, scopeID, r.a2aPrincipalLimits)
}

func (r *Runtime) RotateA2APrincipal(ctx context.Context, scopeToken, principalID string) (IssuedA2APrincipal, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return IssuedA2APrincipal{}, err
	}
	if err := validateIdentity(principalID, "principalId", false); err != nil {
		return IssuedA2APrincipal{}, err
	}
	result, err := r.store.RotateA2APrincipal(ctx, scopeID, principalID)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) SetA2APrincipalEnabled(ctx context.Context, scopeToken, principalID string, enabled bool) (A2APrincipal, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return A2APrincipal{}, err
	}
	if err := validateIdentity(principalID, "principalId", false); err != nil {
		return A2APrincipal{}, err
	}
	result, err := r.store.SetA2APrincipalEnabled(ctx, scopeID, principalID, enabled)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) AuthenticateA2APrincipal(ctx context.Context, credential, publicationID string) (A2APrincipal, error) {
	if err := validateIdentity(publicationID, "publicationId", false); err != nil {
		return A2APrincipal{}, Errorf(CodeUnauthenticated, "Invalid scoped credential")
	}
	return r.store.AuthenticateA2APrincipal(ctx, credential, publicationID)
}
