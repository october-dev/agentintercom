package bus

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

func workspaceOpen(ctx context.Context, tx sqlExecutor, scopeID string) error {
	var closing int64
	if err := tx.QueryRowContext(ctx, "SELECT deletion_started_at FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&closing); err != nil {
		return err
	}
	if closing != 0 {
		return Errorf(CodeConflict, "Workspace deletion is in progress; it cannot accept new connections, purchases or membership changes")
	}
	return nil
}

// Record intent before contacting a payment provider. A lost cancellation
// response or restart leaves a paused workspace that can only finish deletion.
// Both phases serialize with checkout; the scope lock is taken second.
func (a *HostedAccounts) requestWorkspaceDeletion(ctx context.Context, user HostedUser, scopeID, confirmName string, billingConfigured bool) (bool, error) {
	if err := validateHostedUser(user); err != nil {
		return false, err
	}
	tx, err := a.store.beginTx(ctx, "billing/"+scopeID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := lockPostgresScope(ctx, tx, scopeID); err != nil {
		return false, err
	}
	var requester string
	err = tx.QueryRowContext(ctx, "SELECT requested_by_hash FROM hosted_closed_workspaces WHERE scope_id=? AND closed_at>0", scopeID).Scan(&requester)
	if err == nil && requester == tokenDigest(user.ID) {
		return true, tx.Commit()
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	role, err := hostedMember(ctx, tx, scopeID, user.ID)
	if err != nil {
		return false, err
	}
	if role != "owner" {
		return false, Errorf(CodePermissionDenied, "Only a workspace owner can delete it")
	}
	var name string
	var closing int64
	if err := tx.QueryRowContext(ctx, "SELECT name,deletion_started_at FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&name, &closing); err != nil {
		return false, err
	}
	if confirmName != name {
		return false, Errorf(CodeInvalidArgument, "Enter the workspace name exactly to confirm permanent deletion")
	}
	if !billingConfigured {
		var purchases int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM hosted_billing WHERE scope_id=? AND (customer_id IS NOT NULL OR subscription_id IS NOT NULL)) +
(SELECT COUNT(*) FROM hosted_checkout_attempts WHERE scope_id=?)`, scopeID, scopeID).Scan(&purchases); err != nil {
			return false, err
		}
		if purchases != 0 {
			return false, Errorf(CodeConflict, "Billing must be available to close this workspace safely; contact support")
		}
	}
	if closing == 0 {
		now := nowMillis()
		if _, err := tx.ExecContext(ctx, "UPDATE hosted_workspaces SET deletion_started_at=?,paused_at=? WHERE scope_id=?", now, now, scopeID); err != nil {
			return false, err
		}
		if err := retireHostedSessions(ctx, tx, scopeID, "", ""); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE hosted_devices SET revoked_at=? WHERE scope_id=? AND revoked_at=0", now, scopeID); err != nil {
			return false, err
		}
		// Retain the requesting owner's digest for idempotent completion without
		// retaining account data after the content has been removed.
		if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_closed_workspaces(scope_id,requested_by_hash,closed_at) VALUES(?,?,0)", scopeID, tokenDigest(user.ID)); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_billing(scope_id) VALUES(?) ON CONFLICT(scope_id) DO NOTHING", scopeID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	a.runtime.signals.notifyAllScope(scopeID)
	return false, nil
}

func (a *HostedAccounts) finishWorkspaceDeletion(ctx context.Context, scopeID string, billing *HostedBilling) error {
	tx, err := a.store.beginTx(ctx, "billing/"+scopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var closing int64
	err = tx.QueryRowContext(ctx, "SELECT deletion_started_at FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&closing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if closing == 0 {
		return Errorf(CodeConflict, "Workspace deletion has not been requested")
	}
	row, err := billingFrom(ctx, tx, scopeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if row.Customer != "" || row.Subscription != "" {
		if billing == nil {
			return Errorf(CodeConflict, "Billing must be available to finish workspace deletion")
		}
		if err := billing.closeWorkspaceBilling(ctx, tx, &row); err != nil {
			return err
		}
	}
	// Provider requests finish before taking the message lock. Scope content
	// deletion and its minimal payment receipt commit atomically.
	if err := lockPostgresScope(ctx, tx, scopeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_closed_workspaces SET customer_id=NULLIF(?,''),subscription_id=NULLIF(?,''),closed_at=? WHERE scope_id=?", row.Customer, row.Subscription, nowMillis(), scopeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM a2a_tasks WHERE scope_id=?", scopeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM scopes WHERE scope_id=?", scopeID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// File metadata is gone immediately; the existing bounded orphan collector
	// removes bytes, respecting the backup lock. No public path exposes them.
	a.runtime.signals.notifyAllScope(scopeID)
	return nil
}

func (b *HostedBilling) closeWorkspaceBilling(ctx context.Context, tx sqlExecutor, row *billingRow) error {
	attempt, err := pendingCheckout(ctx, tx, row.ScopeID)
	if err != nil {
		return err
	}
	if attempt != nil {
		// Recover the exact checkout, including a lost create response. An uncertain
		// lookup must never be treated as absence or permission to discard data.
		session, err := b.checkoutSession(ctx, *row, attempt, false)
		if err != nil {
			return err
		}
		if session == nil && nowMillis() > attempt.Expires+int64(time.Hour/time.Millisecond) {
			// A complete lookup plus the original request's expired immutable
			// deadline proves that no late request can still create a checkout.
			_, err = tx.ExecContext(ctx, "UPDATE hosted_checkout_attempts SET state='expired' WHERE attempt_id=?", attempt.ID)
			if err != nil {
				return err
			}
		} else {
			if session == nil {
				session, err = b.checkoutSession(ctx, *row, attempt, true)
				if err != nil {
					return err
				}
			}
			if session.Status == "open" {
				session, err = b.stripe.V1CheckoutSessions.Expire(ctx, session.ID, nil)
				if err != nil {
					return Errorf(CodeConflict, "Checkout may have completed; retry deletion to check and close it")
				}
			}
			if session.Status != "complete" && session.Status != "expired" {
				return Errorf(CodeConflict, "Checkout is not closed; retry deletion shortly")
			}
			if err := b.bindCompletedCheckout(ctx, tx, row, attempt, session); err != nil {
				return err
			}
		}
	}
	if row.Subscription == "" {
		return nil
	}
	sub, err := b.stripe.V1Subscriptions.Retrieve(ctx, row.Subscription, nil)
	if err != nil {
		return billingUnavailable()
	}
	if _, err := b.subscriptionIdentity(sub, *row); err != nil {
		return err
	}
	if sub.Status == "canceled" || sub.Status == "incomplete_expired" {
		return nil
	}
	// This action is immediate cancellation, not end-of-period scheduling. It
	// never creates a final invoice or an automatic refund/proration.
	sub, err = b.stripe.V1Subscriptions.Cancel(ctx, sub.ID, &stripe.SubscriptionCancelParams{InvoiceNow: stripe.Bool(false), Prorate: stripe.Bool(false)})
	if err != nil {
		return Errorf(CodeInternal, "Cancellation could not be confirmed. Workspace deletion remains pending and will retry")
	}
	if _, err := b.subscriptionIdentity(sub, *row); err != nil {
		return err
	}
	if sub.Status != "canceled" {
		return Errorf(CodeConflict, "Subscription cancellation is not confirmed; deletion remains pending")
	}
	return nil
}

func (h *HostedServer) registerDeletionRoute() {
	h.routes.HandleFunc("DELETE /api/workspaces/{workspace}", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			ConfirmName string `json:"confirmName"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		if h.billing != nil {
			var release func()
			var err error
			bounded, release, err = h.billing.operation(bounded)
			if err != nil {
				return nil, err
			}
			defer release()
		}
		scopeID := r.PathValue("workspace")
		done, err := h.accounts.requestWorkspaceDeletion(bounded, user, scopeID, input.ConfirmName, h.billing != nil)
		if err == nil && !done {
			err = h.accounts.finishWorkspaceDeletion(bounded, scopeID, h.billing)
		}
		return map[string]bool{"deleted": err == nil}, err
	}))
}
