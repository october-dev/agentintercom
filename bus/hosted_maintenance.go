package bus

import (
	"context"
	"log"
	"time"
)

func (a *HostedAccounts) Maintain(ctx context.Context) error {
	now := nowMillis()
	// Expiry is checked by every credential use. Cleanup only removes records
	// that can no longer authorize a request, retaining a replay-detection buffer.
	for _, query := range []struct {
		sql    string
		before int64
	}{
		{"DELETE FROM hosted_pairings WHERE expires_at<?", now - int64(time.Hour/time.Millisecond)},
		{"DELETE FROM oauth_requests WHERE expires_at<?", now - int64(24*time.Hour/time.Millisecond)},
		{"DELETE FROM oauth_tokens WHERE expires_at<?", now - int64(24*time.Hour/time.Millisecond)},
		{"DELETE FROM hosted_invites WHERE expires_at<?", now - int64(7*24*time.Hour/time.Millisecond)},
		{"DELETE FROM output_rate_usage WHERE window_start<?", now - int64(24*time.Hour/time.Millisecond)},
	} {
		if _, err := a.store.db.ExecContext(ctx, query.sql, query.before); err != nil {
			return err
		}
	}
	// Unbilled deletions must also recover on a service with payments disabled.
	// Paid closures are handled by billing maintenance under the same lock.
	closing, err := a.store.db.QueryContext(ctx, `SELECT w.scope_id FROM hosted_workspaces w JOIN hosted_billing b ON b.scope_id=w.scope_id
WHERE w.deletion_started_at>0 AND b.customer_id IS NULL AND b.subscription_id IS NULL
AND NOT EXISTS(SELECT 1 FROM hosted_checkout_attempts c WHERE c.scope_id=w.scope_id) LIMIT 20`)
	if err != nil {
		return err
	}
	var deletions []string
	for closing.Next() {
		var id string
		if err := closing.Scan(&id); err != nil {
			closing.Close()
			return err
		}
		deletions = append(deletions, id)
	}
	err = closing.Err()
	closing.Close()
	if err != nil {
		return err
	}
	for _, id := range deletions {
		if err := a.finishWorkspaceDeletion(ctx, id, nil); err != nil {
			return err
		}
	}
	rows, err := a.store.db.QueryContext(ctx, "SELECT scope_id FROM hosted_workspaces WHERE maintenance_attempted_at<? AND maintenance_completed_at<? ORDER BY maintenance_attempted_at,scope_id LIMIT 20", now-int64(time.Hour/time.Millisecond), now-int64(24*time.Hour/time.Millisecond))
	if err != nil {
		return err
	}
	var scopes []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		scopes = append(scopes, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var lastError error
	for _, id := range scopes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET maintenance_attempted_at=? WHERE scope_id=?", nowMillis(), id); err != nil {
			return err
		}
		if _, err := a.store.PruneScope(ctx, id, now-int64(90*24*time.Hour/time.Millisecond), true); err != nil {
			lastError = err
			continue
		}
		if _, err := a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET maintenance_completed_at=? WHERE scope_id=?", nowMillis(), id); err != nil {
			return err
		}
	}
	return lastError
}
func (a *HostedAccounts) StartMaintenance() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				run, stop := context.WithTimeout(ctx, 2*time.Minute)
				err := a.Maintain(run)
				stop()
				if err != nil && ctx.Err() == nil {
					log.Print("Agent Intercom workspace retention failed; it will retry")
				}
				timer.Reset(time.Minute)
			}
		}
	}()
	return func() { cancel(); <-done }
}
