package bus

import (
	"context"
	"log"
	"time"
)

// ReconcileDue repairs missed notifications without relying on a browser visit.
// Failed attempts move to the back of the queue; one bad account cannot starve
// the rest. Existing paid access lasts only through its verified paid period.
func (b *HostedBilling) ReconcileDue(ctx context.Context) error {
	now := nowMillis()
	rows, err := b.accounts.store.db.QueryContext(ctx, `SELECT b.scope_id FROM hosted_billing b
WHERE b.customer_id IS NOT NULL AND b.reconciled_at<? AND b.reconcile_attempted_at<?
AND ((b.subscription_id IS NOT NULL AND b.status NOT IN ('canceled','incomplete_expired')) OR EXISTS(SELECT 1 FROM hosted_checkout_attempts a WHERE a.scope_id=b.scope_id AND a.state='pending'))
ORDER BY b.reconcile_attempted_at,b.scope_id LIMIT 20`, now-int64(30*time.Minute/time.Millisecond), now-int64(5*time.Minute/time.Millisecond))
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
	for _, scopeID := range scopes {
		if err := ctx.Err(); err != nil {
			return err
		}
		bounded, release, err := b.operation(ctx)
		if err != nil {
			return err
		}
		err = func() error {
			if _, err := b.accounts.store.db.ExecContext(bounded, "UPDATE hosted_billing SET reconcile_attempted_at=? WHERE scope_id=?", nowMillis(), scopeID); err != nil {
				return err
			}
			tx, err := b.begin(bounded, scopeID, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if _, err := b.reconcileLocked(bounded, tx, scopeID); err != nil {
				return err
			}
			return tx.Commit()
		}()
		release()
		if err != nil {
			lastError = err
		}
	}
	return lastError
}

func (b *HostedBilling) StartMaintenance() func() {
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
				err := b.ReconcileDue(run)
				stop()
				if err != nil && ctx.Err() == nil {
					log.Print("October Bus billing reconciliation failed; pending accounts will be retried")
				}
				timer.Reset(time.Minute)
			}
		}
	}()
	return func() { cancel(); <-done }
}
