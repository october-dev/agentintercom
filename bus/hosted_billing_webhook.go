package bus

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
)

var billingEventTypes = map[stripe.EventType]bool{
	"checkout.session.completed": true, "checkout.session.async_payment_succeeded": true, "checkout.session.async_payment_failed": true, "checkout.session.expired": true,
	"customer.subscription.created": true, "customer.subscription.updated": true, "customer.subscription.deleted": true,
	"invoice.paid": true, "invoice.payment_failed": true, "invoice.voided": true, "invoice.marked_uncollectible": true,
	"charge.refunded": true, "charge.dispute.created": true, "charge.dispute.updated": true, "charge.dispute.closed": true,
	"credit_note.created": true, "credit_note.updated": true, "credit_note.voided": true,
}

func (b *HostedBilling) HandleWebhook(ctx context.Context, payload []byte, signature string) error {
	event, err := webhook.ConstructEventWithOptions(payload, signature, b.options.WebhookSecret, webhook.ConstructEventOptions{Tolerance: 5 * time.Minute})
	if err != nil {
		return Errorf(CodeUnauthenticated, "Webhook signature, age or API version could not be verified")
	}
	if event.Livemode != b.live || event.Account != "" {
		return Errorf(CodePermissionDenied, "Webhook billing account or mode does not match")
	}
	if event.ID == "" || event.Data == nil {
		return Errorf(CodeInvalidArgument, "Incomplete billing event")
	}
	if !billingEventTypes[event.Type] {
		return nil
	}
	ctx, release, err := b.operation(ctx)
	if err != nil {
		return err
	}
	defer release()
	var done int
	if err := b.accounts.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_billing_events WHERE event_id=?", event.ID).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	var object struct {
		Customer *stripe.Customer `json:"customer"`
		Charge   *stripe.Charge   `json:"charge"`
	}
	if err := json.Unmarshal(event.Data.Raw, &object); err != nil {
		return Errorf(CodeInvalidArgument, "Invalid billing event object")
	}
	if object.Customer == nil && object.Charge != nil {
		charge, err := b.stripe.V1Charges.Retrieve(ctx, object.Charge.ID, nil)
		if err != nil {
			return billingUnavailable()
		}
		if charge.Livemode != b.live {
			return Errorf(CodeConflict, "Webhook charge mode mismatch")
		}
		object.Customer = charge.Customer
	}
	if object.Customer == nil || object.Customer.ID == "" {
		return Errorf(CodeInvalidArgument, "Billing event has no customer identity")
	}
	scopeID, err := b.customerScope(ctx, object.Customer.ID)
	if err != nil || scopeID == "" {
		return err
	}
	tx, err := b.begin(ctx, scopeID, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_billing_events WHERE event_id=?", event.ID).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return tx.Commit()
	}
	if _, err := b.reconcileLocked(ctx, tx, scopeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_billing_events(event_id,scope_id,event_type,processed_at) VALUES(?,?,?,?)", event.ID, scopeID, string(event.Type), nowMillis()); err != nil {
		return err
	}
	return tx.Commit()
}
func (b *HostedBilling) serveWebhook(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Stripe-Signature")) != 1 {
		writeFailure(w, Errorf(CodeUnauthenticated, "One webhook signature is required"))
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeFailure(w, Errorf(CodeInvalidArgument, "Webhook exceeds 1 MiB or could not be read"))
		return
	}
	if err := b.HandleWebhook(r.Context(), payload, r.Header.Get("Stripe-Signature")); err != nil {
		writeFailure(w, err)
		return
	}
	writeResult(w, 200, map[string]bool{"received": true})
}
