package bus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
)

type scopeCredentialKey struct{}
type executionKey struct{}
type hostedMemberKey struct{}
type hostedOwnerKey struct{}

// The HTTP identity verifier supplies this context, never a request body.
func withHostedMember(ctx context.Context, user HostedUser) context.Context {
	return context.WithValue(ctx, hostedMemberKey{}, user)
}

func withHostedOwner(ctx context.Context, user HostedUser) context.Context {
	return context.WithValue(withHostedMember(ctx, user), hostedOwnerKey{}, true)
}

func withExecution(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, executionKey{}, p)
}

func withScopeCredential(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, scopeCredentialKey{}, tokenDigest(token))
}

func (s *Store) beginTx(ctx context.Context, scopeID string) (*sqlTransaction, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if s.Backend() == StorageBackendPostgres {
		// Domain operations may inspect several rows before changing them.
		// Serialize them per scope across processes, not across the deployment.
		// READ COMMITTED gives the statements after this lock a fresh snapshot.
		digest := sha256.Sum256([]byte("october_bus/scope/" + scopeID))
		key := int64(binary.BigEndian.Uint64(digest[:8]))
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(?)", key); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if digest, ok := ctx.Value(scopeCredentialKey{}).(string); ok {
		var found int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM scopes WHERE token_hash=?", digest).Scan(&found)
		if err != nil {
			tx.Rollback()
			if errors.Is(err, sql.ErrNoRows) {
				return nil, Errorf(CodeUnauthenticated, "Scope credential is no longer current")
			}
			return nil, err
		}
	}
	if p, ok := ctx.Value(executionKey{}).(Principal); ok {
		if err := requireCurrentExecution(ctx, tx, p, nowMillis()); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if user, ok := ctx.Value(hostedMemberKey{}).(HostedUser); ok {
		role, err := hostedMember(ctx, tx, scopeID, user.ID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if ctx.Value(hostedOwnerKey{}) == true && role != "owner" {
			tx.Rollback()
			return nil, Errorf(CodePermissionDenied, "Only a workspace owner can manage retained data")
		}
	}
	return tx, nil
}
