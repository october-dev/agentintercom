package bus

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
)

const outputStreamResource = "outputStream"

func outputPrincipalFrom(record scopedCredentialRecord, streamID string, permissions []OutputPermission) OutputPrincipal {
	if permissions == nil {
		permissions = []OutputPermission{}
	}
	return OutputPrincipal{
		ID: record.ID, ScopeID: record.ScopeID, StreamID: streamID, Label: record.Label,
		Permissions: permissions, Enabled: record.Enabled, CreatedAt: instant(record.CreatedAt), UpdatedAt: instant(record.UpdatedAt),
	}
}

func validateOutputPermissions(values []OutputPermission) ([]OutputPermission, error) {
	if len(values) == 0 || len(values) > 2 {
		return nil, Errorf(CodeInvalidArgument, "permissions must contain read, publish, or both")
	}
	seen := map[OutputPermission]bool{}
	result := make([]OutputPermission, 0, len(values))
	for _, value := range values {
		if value != OutputRead && value != OutputPublish {
			return nil, Errorf(CodeInvalidArgument, "permissions contains an invalid value")
		}
		if seen[value] {
			return nil, Errorf(CodeInvalidArgument, "permissions must be unique")
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result, nil
}

func (s *Store) CreateOutputPrincipal(ctx context.Context, scopeID string, input CreateOutputPrincipalInput) (IssuedOutputPrincipal, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM output_streams WHERE scope_id=? AND stream_id=?`, scopeID, input.StreamID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return IssuedOutputPrincipal{}, Errorf(CodeNotFound, "Output stream "+input.StreamID+" was not found")
	} else if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	grants := make([]scopedCredentialGrant, 0, len(input.Permissions))
	for _, permission := range input.Permissions {
		grants = append(grants, scopedCredentialGrant{ResourceType: outputStreamResource, ResourceID: input.StreamID, Permission: string(permission)})
	}
	now := nowMillis()
	record, credential, err := createScopedCredential(ctx, tx, scopeID, input.Label, grants, now)
	if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "credential.created", record.ID, eventAttributes(
		"resourceType", outputStreamResource, "resourceId", input.StreamID, "permissions", outputPermissionString(input.Permissions), "enabled", "true",
	), now); err != nil {
		return IssuedOutputPrincipal{}, err
	}
	if err := tx.Commit(); err != nil {
		return IssuedOutputPrincipal{}, err
	}
	return IssuedOutputPrincipal{Principal: outputPrincipalFrom(record, input.StreamID, input.Permissions), Credential: credential}, nil
}

func outputPermissionString(values []OutputPermission) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = string(value)
	}
	return strings.Join(parts, ",")
}

func (s *Store) ListOutputPrincipals(ctx context.Context, scopeID string) ([]OutputPrincipal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.credential_id,c.scope_id,c.label,c.enabled,c.created_at,c.updated_at,g.resource_id,g.permission
FROM scoped_credentials c
JOIN scoped_credential_grants g ON g.credential_id=c.credential_id
WHERE c.scope_id=? AND g.resource_type=?
ORDER BY c.created_at,c.credential_id,g.permission`, scopeID, outputStreamResource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	principals := []OutputPrincipal{}
	for rows.Next() {
		var record scopedCredentialRecord
		var enabled int
		var streamID string
		var permission OutputPermission
		if err := rows.Scan(&record.ID, &record.ScopeID, &record.Label, &enabled, &record.CreatedAt, &record.UpdatedAt, &streamID, &permission); err != nil {
			return nil, err
		}
		record.Enabled = enabled == 1
		last := len(principals) - 1
		if last >= 0 && principals[last].ID == record.ID {
			principals[last].Permissions = append(principals[last].Permissions, permission)
			continue
		}
		principals = append(principals, outputPrincipalFrom(record, streamID, []OutputPermission{permission}))
	}
	return principals, rows.Err()
}

func outputPrincipalDetails(ctx context.Context, tx sqlExecutor, scopeID, principalID string) (scopedCredentialRecord, string, []OutputPermission, error) {
	record, err := scanScopedCredential(tx.QueryRowContext(ctx, `SELECT `+scopedCredentialColumns+` FROM scoped_credentials WHERE scope_id=? AND credential_id=?`, scopeID, principalID))
	if errors.Is(err, sql.ErrNoRows) {
		return scopedCredentialRecord{}, "", nil, Errorf(CodeNotFound, "Output principal was not found")
	}
	if err != nil {
		return scopedCredentialRecord{}, "", nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT resource_id,permission FROM scoped_credential_grants WHERE credential_id=? AND resource_type=? ORDER BY permission`, principalID, outputStreamResource)
	if err != nil {
		return scopedCredentialRecord{}, "", nil, err
	}
	defer rows.Close()
	streamID := ""
	permissions := []OutputPermission{}
	for rows.Next() {
		var resourceID string
		var permission OutputPermission
		if err := rows.Scan(&resourceID, &permission); err != nil {
			return scopedCredentialRecord{}, "", nil, err
		}
		if streamID != "" && streamID != resourceID {
			return scopedCredentialRecord{}, "", nil, Errorf(CodeInternal, "Output principal has inconsistent grants")
		}
		streamID = resourceID
		permissions = append(permissions, permission)
	}
	if err := rows.Err(); err != nil {
		return scopedCredentialRecord{}, "", nil, err
	}
	if streamID == "" {
		return scopedCredentialRecord{}, "", nil, Errorf(CodeNotFound, "Output principal was not found")
	}
	return record, streamID, permissions, nil
}

func (s *Store) RotateOutputPrincipal(ctx context.Context, scopeID, principalID string) (IssuedOutputPrincipal, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	defer tx.Rollback()
	_, streamID, permissions, err := outputPrincipalDetails(ctx, tx, scopeID, principalID)
	if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	now := nowMillis()
	record, credential, err := rotateScopedCredential(ctx, tx, scopeID, principalID, now)
	if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	if err := appendEvent(ctx, tx, scopeID, "credential.rotated", record.ID, eventAttributes(
		"resourceType", outputStreamResource, "resourceId", streamID, "permissions", outputPermissionString(permissions),
	), now); err != nil {
		return IssuedOutputPrincipal{}, err
	}
	if err := tx.Commit(); err != nil {
		return IssuedOutputPrincipal{}, err
	}
	return IssuedOutputPrincipal{Principal: outputPrincipalFrom(record, streamID, permissions), Credential: credential}, nil
}

func (s *Store) SetOutputPrincipalEnabled(ctx context.Context, scopeID, principalID string, enabled bool) (OutputPrincipal, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return OutputPrincipal{}, err
	}
	defer tx.Rollback()
	_, streamID, permissions, err := outputPrincipalDetails(ctx, tx, scopeID, principalID)
	if err != nil {
		return OutputPrincipal{}, err
	}
	now := nowMillis()
	record, changed, err := setScopedCredentialEnabled(ctx, tx, scopeID, principalID, enabled, now)
	if err != nil {
		return OutputPrincipal{}, err
	}
	if changed {
		eventType := "credential.disabled"
		if enabled {
			eventType = "credential.enabled"
		}
		if err := appendEvent(ctx, tx, scopeID, eventType, record.ID, eventAttributes(
			"resourceType", outputStreamResource, "resourceId", streamID, "permissions", outputPermissionString(permissions), "enabled", boolString(enabled),
		), now); err != nil {
			return OutputPrincipal{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return OutputPrincipal{}, err
	}
	return outputPrincipalFrom(record, streamID, permissions), nil
}

func (r *Runtime) CreateOutputPrincipal(ctx context.Context, scopeToken string, input CreateOutputPrincipalInput) (IssuedOutputPrincipal, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	if err := validateIdentity(input.StreamID, "streamId", false); err != nil {
		return IssuedOutputPrincipal{}, err
	}
	if err := validateText(input.Label, "label", 256, false); err != nil {
		return IssuedOutputPrincipal{}, err
	}
	input.Permissions, err = validateOutputPermissions(input.Permissions)
	if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	result, err := r.store.CreateOutputPrincipal(ctx, scopeID, input)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) ListOutputPrincipals(ctx context.Context, scopeToken string) ([]OutputPrincipal, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return nil, err
	}
	return r.store.ListOutputPrincipals(ctx, scopeID)
}

func (r *Runtime) RotateOutputPrincipal(ctx context.Context, scopeToken, principalID string) (IssuedOutputPrincipal, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return IssuedOutputPrincipal{}, err
	}
	if err := validateIdentity(principalID, "principalId", false); err != nil {
		return IssuedOutputPrincipal{}, err
	}
	result, err := r.store.RotateOutputPrincipal(ctx, scopeID, principalID)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) SetOutputPrincipalEnabled(ctx context.Context, scopeToken, principalID string, enabled bool) (OutputPrincipal, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return OutputPrincipal{}, err
	}
	if err := validateIdentity(principalID, "principalId", false); err != nil {
		return OutputPrincipal{}, err
	}
	result, err := r.store.SetOutputPrincipalEnabled(ctx, scopeID, principalID, enabled)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}
