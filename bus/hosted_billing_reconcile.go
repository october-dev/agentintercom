package bus

import (
	"context"
	"database/sql"
	"errors"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

func (b *HostedBilling) subscriptionIdentity(sub *stripe.Subscription, row billingRow) (string, error) {
	if sub == nil || sub.ID == "" || sub.Livemode != b.live || sub.Customer == nil || sub.Customer.ID != row.Customer || sub.Metadata["feature"] != hostedBillingFeature || sub.Metadata["workspace_id"] != row.ScopeID || sub.Items == nil || sub.Items.HasMore || len(sub.Items.Data) != 1 || sub.Items.Data[0] == nil || sub.Items.Data[0].Quantity != 1 || sub.Items.Data[0].Price == nil || sub.Currency != "usd" {
		return "", Errorf(CodeConflict, "Subscription does not match the workspace billing contract")
	}
	interval, err := b.priceInterval(sub.Items.Data[0].Price.ID)
	if err != nil {
		return "", err
	}
	if sub.Metadata["interval"] != interval {
		return "", Errorf(CodeConflict, "Subscription interval does not match its purchase")
	}
	return interval, nil
}
func (b *HostedBilling) bindCompletedCheckout(ctx context.Context, tx sqlExecutor, row *billingRow, attempt *checkoutAttempt, session *stripe.CheckoutSession) error {
	if err := b.checkoutIdentity(session, *row, attempt); err != nil {
		return err
	}
	if session.Status == "expired" {
		if session.Subscription != nil || session.PaymentStatus == "paid" {
			return Errorf(CodeConflict, "Expired checkout has a payment requiring reconciliation")
		}
		_, err := tx.ExecContext(ctx, "UPDATE hosted_checkout_attempts SET session_id=?,state='expired' WHERE attempt_id=?", session.ID, attempt.ID)
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_checkout_attempts SET session_id=? WHERE attempt_id=?", session.ID, attempt.ID); err != nil {
		return err
	}
	if session.Status != "complete" {
		return nil
	}
	if session.Subscription == nil || session.Subscription.ID == "" {
		return Errorf(CodeConflict, "Completed checkout is missing its subscription")
	}
	sub, err := b.stripe.V1Subscriptions.Retrieve(ctx, session.Subscription.ID, nil)
	if err != nil {
		return billingUnavailable()
	}
	interval, err := b.subscriptionIdentity(sub, *row)
	if err != nil {
		return err
	}
	if interval != attempt.Interval || sub.Metadata["checkout_attempt"] != attempt.ID {
		return Errorf(CodeConflict, "Subscription purchase identity mismatch")
	}
	if row.Subscription != "" && row.Subscription != sub.ID && row.Status != "canceled" && row.Status != "incomplete_expired" {
		return Errorf(CodeConflict, "Multiple subscriptions require reconciliation; manage the existing subscription")
	}
	row.Subscription = sub.ID
	row.Status = string(sub.Status)
	row.Interval = interval
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_billing SET subscription_id=?,status=?,interval=? WHERE scope_id=?", sub.ID, row.Status, interval, row.ScopeID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE hosted_checkout_attempts SET state='complete' WHERE attempt_id=?", attempt.ID)
	return err
}

func (b *HostedBilling) chargeFunded(ctx context.Context, id, customer string) (bool, error) {
	charge, err := b.stripe.V1Charges.Retrieve(ctx, id, nil)
	if err != nil {
		return false, billingUnavailable()
	}
	if charge.ID != id || charge.Livemode != b.live || charge.Customer == nil || charge.Customer.ID != customer || charge.Currency != "usd" {
		return false, Errorf(CodeConflict, "Invoice charge identity mismatch")
	}
	if !charge.Paid || charge.Status != "succeeded" || charge.Refunded {
		return false, nil
	}
	if charge.Disputed {
		params := &stripe.DisputeListParams{Charge: stripe.String(id)}
		params.Limit = stripe.Int64(100)
		list := b.stripe.V1Disputes.List(ctx, params)
		if err := list.Err(); err != nil {
			return false, billingUnavailable()
		}
		if list.Meta().HasMore || len(list.Data()) == 0 {
			return false, Errorf(CodeConflict, "Charge dispute status could not be verified")
		}
		for _, dispute := range list.Data() {
			if dispute.Charge == nil || dispute.Charge.ID != id || dispute.Livemode != b.live {
				return false, Errorf(CodeConflict, "Dispute identity mismatch")
			}
			if dispute.Status != "won" && dispute.Status != "warning_closed" {
				return false, nil
			}
		}
	}
	return true, nil
}
func (b *HostedBilling) invoiceFunded(ctx context.Context, invoice *stripe.Invoice, row billingRow) (bool, error) {
	if invoice.Status != "paid" || invoice.AmountRemaining != 0 {
		return false, nil
	}
	if invoice.Total > 0 && invoice.PostPaymentCreditNotesAmount >= invoice.Total {
		return false, nil
	}
	params := &stripe.InvoicePaymentListParams{Invoice: stripe.String(invoice.ID), Status: stripe.String("paid")}
	params.Limit = stripe.Int64(100)
	list := b.stripe.V1InvoicePayments.List(ctx, params)
	if err := list.Err(); err != nil {
		return false, billingUnavailable()
	}
	if list.Meta().HasMore {
		return false, Errorf(CodeConflict, "Invoice payment history needs reconciliation")
	}
	for _, payment := range list.Data() {
		if payment.Livemode != b.live || payment.Invoice == nil || payment.Invoice.ID != invoice.ID || payment.Status != "paid" || payment.Payment == nil {
			return false, Errorf(CodeConflict, "Invoice payment identity mismatch")
		}
		chargeID := ""
		switch payment.Payment.Type {
		case "payment_intent":
			if payment.Payment.PaymentIntent == nil {
				return false, Errorf(CodeConflict, "Invoice payment is missing its intent")
			}
			params := &stripe.PaymentIntentRetrieveParams{}
			params.AddExpand("latest_charge")
			intent, err := b.stripe.V1PaymentIntents.Retrieve(ctx, payment.Payment.PaymentIntent.ID, params)
			if err != nil {
				return false, billingUnavailable()
			}
			if intent.Livemode != b.live || intent.Customer == nil || intent.Customer.ID != row.Customer || intent.Currency != "usd" {
				return false, Errorf(CodeConflict, "Payment intent identity mismatch")
			}
			if intent.Status != "succeeded" || intent.LatestCharge == nil {
				return false, nil
			}
			chargeID = intent.LatestCharge.ID
		case "charge":
			if payment.Payment.Charge == nil {
				return false, Errorf(CodeConflict, "Invoice payment is missing its charge")
			}
			chargeID = payment.Payment.Charge.ID
		default:
			return false, Errorf(CodeConflict, "This invoice payment method requires reconciliation")
		}
		funded, err := b.chargeFunded(ctx, chargeID, row.Customer)
		if err != nil || !funded {
			return false, err
		}
	}
	// A zero-due invoice can be funded by Stripe customer credits. A manual
	// off-Stripe payment is accepted only when explicitly recorded by Stripe.
	if len(list.Data()) == 0 && invoice.AmountDue > 0 && invoice.AmountPaidOffStripe < invoice.AmountDue {
		return false, Errorf(CodeConflict, "Paid invoice has no verifiable funding")
	}
	return true, nil
}
func (b *HostedBilling) reconcileInvoice(ctx context.Context, tx sqlExecutor, row billingRow, id string) error {
	invoice, err := b.stripe.V1Invoices.Retrieve(ctx, id, nil)
	if err != nil {
		return billingUnavailable()
	}
	if invoice.ID != id || invoice.Customer == nil || invoice.Customer.ID != row.Customer || invoice.Livemode != b.live || invoice.Currency != "usd" || invoice.Parent == nil || invoice.Parent.SubscriptionDetails == nil || invoice.Parent.SubscriptionDetails.Subscription == nil || invoice.Parent.SubscriptionDetails.Subscription.ID != row.Subscription || invoice.Lines == nil || invoice.Lines.HasMore || len(invoice.Lines.Data) != 1 {
		return Errorf(CodeConflict, "Invoice does not match its workspace subscription")
	}
	line := invoice.Lines.Data[0]
	if line == nil || line.Pricing == nil || line.Pricing.PriceDetails == nil || line.Pricing.PriceDetails.Price == nil || line.Period == nil || line.Quantity != 1 || line.Currency != "usd" || line.Parent == nil || line.Parent.SubscriptionItemDetails == nil || line.Parent.SubscriptionItemDetails.Proration || line.Parent.SubscriptionItemDetails.Subscription != row.Subscription {
		return Errorf(CodeConflict, "Invoice line does not match the Bus plan")
	}
	priceID, amount, err := b.price(row.Interval)
	if err != nil {
		return err
	}
	if line.Pricing.PriceDetails.Price.ID != priceID || line.Subtotal != amount || line.Period.Start <= 0 || line.Period.End <= line.Period.Start || line.Period.End-line.Period.Start > int64(400*24*time.Hour/time.Second) || line.Period.End > time.Now().Add(400*24*time.Hour).Unix() {
		return Errorf(CodeConflict, "Invoice terms do not match the Bus plan")
	}
	funded, err := b.invoiceFunded(ctx, invoice, row)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hosted_paid_invoices(invoice_id,scope_id,subscription_id,price_id,period_start,period_end,funded,checked_at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(invoice_id) DO UPDATE SET funded=excluded.funded,checked_at=excluded.checked_at`, id, row.ScopeID, row.Subscription, priceID, line.Period.Start*1000, line.Period.End*1000, funded, nowMillis())
	return err
}

// Re-read Stripe while holding the billing lock. Event timestamps never decide
// access: duplicate or out-of-order notifications converge on current objects.
func (b *HostedBilling) reconcileLocked(ctx context.Context, tx *sqlTransaction, scopeID string) (billingRow, error) {
	row, err := billingFrom(ctx, tx, scopeID)
	if err != nil {
		return row, err
	}
	if row.Customer == "" {
		return row, nil
	}
	pending, err := pendingCheckout(ctx, tx, scopeID)
	if err != nil {
		return row, err
	}
	if pending != nil {
		session, err := b.checkoutSession(ctx, row, pending, false)
		if err != nil {
			return row, err
		}
		if session != nil {
			if err := b.bindCompletedCheckout(ctx, tx, &row, pending, session); err != nil {
				return row, err
			}
		} else if nowMillis() > pending.Expires+int64(time.Hour/time.Millisecond) {
			// Original immutable expiry is in the past, and a complete bounded provider
			// lookup found no session. A late original request can no longer create one.
			if _, err := tx.ExecContext(ctx, "UPDATE hosted_checkout_attempts SET state='expired' WHERE attempt_id=?", pending.ID); err != nil {
				return row, err
			}
		}
	}
	if row.Subscription == "" {
		return row, nil
	}
	sub, err := b.stripe.V1Subscriptions.Retrieve(ctx, row.Subscription, nil)
	if err != nil {
		return row, billingUnavailable()
	}
	interval, err := b.subscriptionIdentity(sub, row)
	if err != nil {
		return row, err
	}
	row.Interval = interval
	row.Status = string(sub.Status)
	row.Cancel = sub.CancelAtPeriodEnd
	row.PeriodEnd = sub.Items.Data[0].CurrentPeriodEnd * 1000
	ids := map[string]bool{}
	if sub.LatestInvoice != nil && sub.LatestInvoice.ID != "" {
		ids[sub.LatestInvoice.ID] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT invoice_id FROM hosted_paid_invoices WHERE scope_id=? AND subscription_id=? AND period_end>? ORDER BY period_end LIMIT 11", scopeID, sub.ID, nowMillis())
	if err != nil {
		return row, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return row, err
		}
		ids[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return row, err
	}
	if len(ids) > 10 {
		return row, Errorf(CodeConflict, "Multiple overlapping billing periods require reconciliation")
	}
	for id := range ids {
		if err := b.reconcileInvoice(ctx, tx, row, id); err != nil {
			return row, err
		}
	}
	var paidUntil int64
	if sub.Status == "active" || sub.Status == "past_due" {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(period_end),0) FROM hosted_paid_invoices WHERE scope_id=? AND subscription_id=? AND funded=1 AND period_start<=?", scopeID, sub.ID, nowMillis()).Scan(&paidUntil); err != nil {
			return row, err
		}
		if row.PeriodEnd > 0 {
			paidUntil = min(paidUntil, row.PeriodEnd)
		}
	}
	row.Reconciled = nowMillis()
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_billing SET status=?,interval=?,cancel_at_period_end=?,current_period_end=?,reconciled_at=? WHERE scope_id=?", row.Status, row.Interval, row.Cancel, row.PeriodEnd, row.Reconciled, scopeID); err != nil {
		return row, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_workspaces SET paid_until=?,trial_ends_at=CASE WHEN CAST(? AS BIGINT)>0 THEN LEAST(trial_ends_at,?) ELSE trial_ends_at END WHERE scope_id=?", paidUntil, paidUntil, row.Reconciled, scopeID); err != nil {
		return row, err
	}
	return row, nil
}
func (b *HostedBilling) Reconcile(ctx context.Context, user HostedUser, scopeID string) error {
	tx, err := b.begin(ctx, scopeID, &user)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := b.reconcileLocked(ctx, tx, scopeID); err != nil {
		return err
	}
	return tx.Commit()
}

// Keep SQL's no-row distinction explicit for webhook routing: an event for a
// customer from another product is not an error or a Bus entitlement grant.
func (b *HostedBilling) customerScope(ctx context.Context, id string) (string, error) {
	var scope string
	err := b.accounts.store.db.QueryRowContext(ctx, "SELECT scope_id FROM hosted_billing WHERE customer_id=?", id).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return scope, err
}
