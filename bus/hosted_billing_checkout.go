package bus

import (
	"context"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

func (b *HostedBilling) Checkout(ctx context.Context, user HostedUser, scopeID, interval string) (BillingLink, error) {
	if _, _, err := b.price(interval); err != nil {
		return BillingLink{}, err
	}
	// Each pass either returns the existing checkout/portal or positively expires
	// an older selection. No second pending purchase is created on uncertain state.
	for range 3 {
		if err := b.prepareCheckout(ctx, user, scopeID, interval); err != nil {
			return BillingLink{}, err
		}
		result, again, err := b.checkoutPass(ctx, user, scopeID, interval)
		if err != nil || !again {
			return result, err
		}
	}
	return BillingLink{}, Errorf(CodeConflict, "Billing changed while starting checkout; refresh and try again")
}
func (b *HostedBilling) checkoutPass(ctx context.Context, user HostedUser, scopeID, interval string) (BillingLink, bool, error) {
	var zero BillingLink
	tx, err := b.begin(ctx, scopeID, &user)
	if err != nil {
		return zero, false, err
	}
	defer tx.Rollback()
	if err := workspaceOpen(ctx, tx, scopeID); err != nil {
		return zero, false, err
	}
	row, err := b.reconcileLocked(ctx, tx, scopeID)
	if err != nil {
		return zero, false, err
	}
	if row.Subscription != "" && row.Status != "canceled" && row.Status != "incomplete_expired" {
		result, err := b.portal(ctx, row.Customer, scopeID)
		if err != nil {
			return zero, false, err
		}
		return result, false, tx.Commit()
	}
	attempt, err := pendingCheckout(ctx, tx, scopeID)
	if err != nil {
		return zero, false, err
	}
	if attempt == nil {
		return zero, true, tx.Commit()
	}
	if err := b.verifyPrice(ctx, attempt.Interval, true); err != nil {
		return zero, false, err
	}
	session, err := b.checkoutSession(ctx, row, attempt, true)
	if err != nil {
		return zero, false, err
	}
	if err := b.bindCompletedCheckout(ctx, tx, &row, attempt, session); err != nil {
		return zero, false, err
	}
	if session.Status == "complete" || session.Status == "expired" {
		return zero, true, tx.Commit()
	}
	if session.Status != "open" {
		return zero, false, Errorf(CodeConflict, "Checkout is not ready; refresh its payment status")
	}
	if attempt.Interval != interval {
		expired, err := b.stripe.V1CheckoutSessions.Expire(ctx, session.ID, nil)
		if err != nil {
			return zero, false, Errorf(CodeConflict, "The earlier checkout may have completed; refresh billing before choosing another plan")
		}
		if err := b.bindCompletedCheckout(ctx, tx, &row, attempt, expired); err != nil {
			return zero, false, err
		}
		if expired.Status != "expired" {
			return zero, false, Errorf(CodeConflict, "Earlier checkout could not be expired")
		}
		return zero, true, tx.Commit()
	}
	result, err := checkedBillingLink("checkout", session.URL)
	if err != nil {
		return zero, false, err
	}
	return result, false, tx.Commit()
}

func (b *HostedBilling) CancelCheckout(ctx context.Context, user HostedUser, scopeID string) error {
	tx, err := b.begin(ctx, scopeID, &user)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := b.reconcileLocked(ctx, tx, scopeID)
	if err != nil {
		return err
	}
	attempt, err := pendingCheckout(ctx, tx, scopeID)
	if err != nil {
		return err
	}
	if attempt == nil {
		return tx.Commit()
	}
	// Replaying the original immutable request recovers an unrecorded session;
	// creation itself never charges the customer. Then expire that exact session.
	session, err := b.checkoutSession(ctx, row, attempt, true)
	if err != nil {
		return err
	}
	if session.Status == "open" {
		session, err = b.stripe.V1CheckoutSessions.Expire(ctx, session.ID, &stripe.CheckoutSessionExpireParams{})
		if err != nil {
			return Errorf(CodeConflict, "Checkout may have completed; refresh billing to check its payment status")
		}
	}
	if err := b.bindCompletedCheckout(ctx, tx, &row, attempt, session); err != nil {
		return err
	}
	if session.Status == "complete" {
		if _, err := b.reconcileLocked(ctx, tx, scopeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (b *HostedBilling) operation(ctx context.Context) (context.Context, func(), error) {
	select {
	case b.requests <- struct{}{}:
	default:
		return nil, nil, Errorf(CodeBackpressure, "Billing is busy; retry shortly")
	}
	bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
	return bounded, func() { cancel(); <-b.requests }, nil
}
