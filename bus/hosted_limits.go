package bus

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func isPostgresQuery(q sqlExecutor) bool {
	switch value := q.(type) {
	case *sqlDatabase:
		return value.backend == StorageBackendPostgres
	case *sqlTransaction:
		return value.backend == StorageBackendPostgres
	}
	return false
}

// Resolving already accepted work must not require new delivery allowance.
// This marker is set only by the trusted human-question resolution path.
type completionDeliveryKey struct{}

// Every hosted execution remains bound to a live device and workspace member,
// including requests arriving directly at the common protocol routes.
func requireHostedExecution(ctx context.Context, q sqlExecutor, p Principal, now int64) error {
	if !isPostgresQuery(q) {
		return nil
	}
	var denied int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM hosted_workspaces w WHERE w.scope_id=? AND (w.disabled_at<>0 OR NOT EXISTS(
SELECT 1 FROM hosted_sessions s JOIN hosted_devices d ON d.device_id=s.device_id
JOIN hosted_members m ON m.scope_id=d.scope_id AND m.user_id=d.user_id
WHERE s.scope_id=w.scope_id AND s.agent_id=? AND d.scope_id=w.scope_id AND d.revoked_at=0 AND d.expires_at>?))`, p.ScopeID, p.AgentID, now).Scan(&denied)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return Errorf(CodeUnauthenticated, "Workspace or connection access has ended")
}

// Charge only a new non-response delivery, in the same transaction that stores
// the message. Retries return before this point. Polls and acknowledgements never
// call it, and responses to accepted work can finish after the allowance ends.
func consumeHostedDelivery(ctx context.Context, tx sqlExecutor, scopeID string, mode MessageMode, now int64) error {
	if !isPostgresQuery(tx) || mode == MessageResponse || ctx.Value(completionDeliveryKey{}) == true {
		return nil
	}
	var trial, paid, disabled, limit int64
	err := tx.QueryRowContext(ctx, "SELECT trial_ends_at,paid_until,disabled_at,delivery_limit FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&trial, &paid, &disabled, &limit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if disabled != 0 || (trial <= now && paid <= now) {
		return Errorf(CodePermissionDenied, "Workspace hosting is inactive; existing work can still be replied to and exported")
	}
	if paid <= now && limit > 2000 {
		limit = 2000
	}
	month := time.UnixMilli(now).UTC().Format("2006-01")
	var used int64
	if paid <= now {
		// A trial crossing a calendar month still has one bounded allowance.
		err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(deliveries),0) FROM hosted_usage WHERE scope_id=?", scopeID).Scan(&used)
	} else {
		err = tx.QueryRowContext(ctx, "SELECT deliveries FROM hosted_usage WHERE scope_id=? AND month=?", scopeID, month).Scan(&used)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if used >= limit {
		return Errorf(CodeBackpressure, "Workspace monthly delivery allowance is full; replies, retries and acknowledgements remain available")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hosted_usage(scope_id,month,deliveries) VALUES(?,?,1)
ON CONFLICT(scope_id,month) DO UPDATE SET deliveries=hosted_usage.deliveries+1`, scopeID, month)
	return err
}

// New shared work requires active hosting even when it has no recipients yet.
// Reading retained work, completing it and exporting it remain available.
func requireHostedNewWork(ctx context.Context, tx sqlExecutor, scopeID string) error {
	if !isPostgresQuery(tx) {
		return nil
	}
	var trial, paid, disabled int64
	err := tx.QueryRowContext(ctx, "SELECT trial_ends_at,paid_until,disabled_at FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&trial, &paid, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if disabled != 0 || (trial <= nowMillis() && paid <= nowMillis()) {
		return Errorf(CodePermissionDenied, "Workspace hosting is inactive; existing work remains available to read and export")
	}
	return nil
}
