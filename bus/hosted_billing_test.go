package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
)

type billingJSON = map[string]any

type billingFixture struct {
	t                                   *testing.T
	mu                                  sync.Mutex
	scope, attempt, interval, customer  string
	sessions                            []billingJSON
	sub, invoice, charge                billingJSON
	creates                             int
	loseResponse, failReads, wrongPrice bool
	dispute                             string
	requests                            int
	cancellations                       int
	loseCancellation                    bool
}

func billingList(data any) billingJSON {
	return billingJSON{"object": "list", "data": data, "has_more": false}
}
func newBillingFixture(t *testing.T) (context.Context, *HostedBilling, HostedUser, Workspace, *billingFixture) {
	t.Helper()
	ctx, a := hostedTestAccounts(t)
	user := hostedTestUser(t, "billing")
	w, err := a.CreateWorkspace(ctx, user, "Billing workspace")
	requireNoError(t, err)
	t.Cleanup(func() {
		a.runtime.DeleteScope(context.Background(), w.ID)
		a.store.db.ExecContext(context.Background(), "DELETE FROM hosted_closed_workspaces WHERE scope_id=?", w.ID)
	})
	b, err := NewHostedBilling(a, HostedBillingOptions{SecretKey: "sk_test_fixture", WebhookSecret: "whsec_fixture_signing_secret", MonthlyPriceID: "price_month", AnnualPriceID: "price_year", PortalConfigurationID: "bpc_fixture", PublicURL: "https://bus.example.test"})
	requireNoError(t, err)
	f := &billingFixture{t: t, scope: w.ID, customer: "cus_" + w.ID, sessions: []billingJSON{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	backend := stripe.NewBackendsWithConfig(&stripe.BackendConfig{URL: stripe.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripe.Int64(0), EnableTelemetry: stripe.Bool(false), LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull}})
	b.stripe = stripe.NewClient("sk_test_fixture", stripe.WithBackends(backend))
	return ctx, b, user, w, f
}
func (f *billingFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	w.Header().Set("Content-Type", "application/json")
	if f.failReads && r.Method == "GET" {
		w.WriteHeader(503)
		json.NewEncoder(w).Encode(billingJSON{"error": billingJSON{"message": "temporary outage"}})
		return
	}
	if err := r.ParseForm(); err != nil {
		f.t.Error(err)
		w.WriteHeader(400)
		return
	}
	var result any
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/customers":
		if r.Header.Get("Idempotency-Key") != "agentintercom-customer:"+f.scope {
			f.t.Error("customer missing stable retry key")
		}
		result = billingJSON{"id": f.customer, "metadata": billingJSON{"feature": hostedBillingFeature, "workspace_id": f.scope}}
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/prices/"):
		interval := strings.TrimPrefix(r.URL.Path, "/v1/prices/price_")
		amount := 1900
		if interval == "year" {
			amount = 19000
		}
		if f.wrongPrice {
			amount++
		}
		result = billingJSON{"id": "price_" + interval, "currency": "usd", "unit_amount": amount, "active": true, "type": "recurring", "billing_scheme": "per_unit", "recurring": billingJSON{"interval": interval, "interval_count": 1, "usage_type": "licensed"}, "product": billingJSON{"id": "prod_fixture", "active": true, "metadata": billingJSON{"feature": hostedBillingFeature}}}
	case r.Method == "GET" && r.URL.Path == "/v1/checkout/sessions":
		if r.Form.Get("customer") != f.customer {
			f.t.Error("checkout recovery not customer scoped")
		}
		result = billingList(f.sessions)
	case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
		f.attempt = r.Form.Get("metadata[checkout_attempt]")
		f.interval = r.Form.Get("metadata[interval]")
		if r.Header.Get("Idempotency-Key") != "agentintercom-checkout:"+f.attempt || r.Form.Get("line_items[0][quantity]") != "1" || r.Form.Get("line_items[0][price]") != "price_"+f.interval {
			f.t.Error("checkout parameters not fixed")
		}
		if !strings.Contains(r.Form.Get("success_url"), "workspace="+f.scope) {
			f.t.Error("return URL lost workspace")
		}
		f.creates++
		item := billingJSON{"id": fmt.Sprintf("cs_%d", f.creates), "object": "checkout.session", "customer": f.customer, "client_reference_id": f.scope, "mode": "subscription", "status": "open", "payment_status": "unpaid", "url": "https://checkout.stripe.com/c/pay/fixture", "metadata": billingJSON{"feature": hostedBillingFeature, "workspace_id": f.scope, "checkout_attempt": f.attempt, "interval": f.interval}}
		f.sessions = append(f.sessions, item)
		result = item
		if f.loseResponse {
			f.loseResponse = false
			w.WriteHeader(503)
			json.NewEncoder(w).Encode(billingJSON{"error": billingJSON{"message": "response lost after creation"}})
			return
		}
	case strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/"), "/")[0]
		for _, item := range f.sessions {
			if item["id"] == id {
				result = item
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/expire") {
					item["status"] = "expired"
				}
			}
		}
	case r.URL.Path == "/v1/subscriptions/sub_fixture":
		if r.Method == "DELETE" {
			if r.Form.Get("invoice_now") != "false" || r.Form.Get("prorate") != "false" {
				f.t.Error("deletion cancellation must not invoice or prorate")
			}
			f.cancellations++
			f.sub["status"] = "canceled"
			if f.loseCancellation {
				f.loseCancellation = false
				w.WriteHeader(503)
				json.NewEncoder(w).Encode(billingJSON{"error": billingJSON{"message": "cancellation response lost"}})
				return
			}
		}
		result = f.sub
	case r.URL.Path == "/v1/invoices/in_fixture":
		result = f.invoice
	case r.URL.Path == "/v1/invoice_payments":
		result = billingList([]billingJSON{{"id": "inpay_fixture", "invoice": "in_fixture", "status": "paid", "payment": billingJSON{"type": "payment_intent", "payment_intent": "pi_fixture"}}})
	case r.URL.Path == "/v1/payment_intents/pi_fixture":
		result = billingJSON{"id": "pi_fixture", "customer": f.customer, "currency": "usd", "status": "succeeded", "latest_charge": "ch_fixture"}
	case r.URL.Path == "/v1/charges/ch_fixture":
		result = f.charge
	case r.URL.Path == "/v1/disputes":
		result = billingList([]billingJSON{{"id": "dp_fixture", "charge": "ch_fixture", "status": f.dispute}})
	case r.URL.Path == "/v1/billing_portal/configurations/bpc_fixture":
		result = billingJSON{"id": "bpc_fixture", "active": true, "features": billingJSON{"subscription_update": billingJSON{"enabled": false}, "subscription_cancel": billingJSON{"enabled": true, "mode": "at_period_end"}, "payment_method_update": billingJSON{"enabled": true}, "invoice_history": billingJSON{"enabled": true}}}
	case r.URL.Path == "/v1/billing_portal/sessions":
		result = billingJSON{"id": "bps_fixture", "url": "https://billing.stripe.com/p/session/fixture"}
	default:
		f.t.Errorf("unexpected Stripe route %s %s", r.Method, r.URL.Path)
	}
	if result == nil {
		w.WriteHeader(404)
		result = billingJSON{"error": billingJSON{"message": "fixture object missing"}}
	}
	json.NewEncoder(w).Encode(result)
}
func (f *billingFixture) pay() {
	f.mu.Lock()
	defer f.mu.Unlock()
	end := time.Now().Add(30 * 24 * time.Hour).Unix()
	if f.interval == "year" {
		end = time.Now().Add(365 * 24 * time.Hour).Unix()
	}
	amount := 1900
	if f.interval == "year" {
		amount = 19000
	}
	f.sessions[len(f.sessions)-1]["status"] = "complete"
	f.sessions[len(f.sessions)-1]["subscription"] = "sub_fixture"
	f.sessions[len(f.sessions)-1]["payment_status"] = "paid"
	f.sub = billingJSON{"id": "sub_fixture", "customer": f.customer, "currency": "usd", "status": "active", "metadata": billingJSON{"feature": hostedBillingFeature, "workspace_id": f.scope, "checkout_attempt": f.attempt, "interval": f.interval}, "items": billingList([]billingJSON{{"id": "si_fixture", "price": billingJSON{"id": "price_" + f.interval}, "quantity": 1, "current_period_end": end}}), "latest_invoice": "in_fixture"}
	f.invoice = billingJSON{"id": "in_fixture", "customer": f.customer, "currency": "usd", "status": "paid", "amount_due": amount, "amount_paid": amount, "total": amount, "amount_remaining": 0, "parent": billingJSON{"subscription_details": billingJSON{"subscription": "sub_fixture"}}, "lines": billingList([]billingJSON{{"id": "il_fixture", "quantity": 1, "currency": "usd", "subtotal": amount, "period": billingJSON{"start": time.Now().Add(-time.Hour).Unix(), "end": end}, "pricing": billingJSON{"price_details": billingJSON{"price": "price_" + f.interval}}, "parent": billingJSON{"subscription_item_details": billingJSON{"subscription": "sub_fixture", "proration": false}}}})}
	f.charge = billingJSON{"id": "ch_fixture", "customer": f.customer, "currency": "usd", "paid": true, "status": "succeeded", "refunded": false, "disputed": false}
}
func billingState(t *testing.T, b *HostedBilling, user HostedUser, w Workspace) WorkspaceBilling {
	t.Helper()
	result, err := b.accounts.BillingSummary(context.Background(), user, w.ID, true, b.live)
	requireNoError(t, err)
	return result
}
func billingEvent(t *testing.T, b *HostedBilling, id, kind string, object billingJSON) ([]byte, string) {
	t.Helper()
	payload, err := json.Marshal(billingJSON{"id": id, "object": "event", "api_version": stripe.APIVersion, "type": kind, "created": time.Now().Unix(), "livemode": false, "data": billingJSON{"object": object}})
	requireNoError(t, err)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: b.options.WebhookSecret, Timestamp: time.Now()})
	return payload, signed.Header
}
func TestHostedBillingConcurrentCheckoutAndLostResponse(t *testing.T) {
	ctx, b, user, w, f := newBillingFixture(t)
	f.loseResponse = true
	_, err := b.Checkout(ctx, user, w.ID, "month")
	requireCode(t, err, CodeInternal)
	var wg sync.WaitGroup
	results := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			link, err := b.Checkout(ctx, user, w.ID, "month")
			if err == nil && link.Kind != "checkout" {
				err = fmt.Errorf("wrong destination")
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		requireNoError(t, err)
	}
	require(t, f.creates == 1, "retry created %d checkout sessions", f.creates)
	summary := billingState(t, b, user, w)
	require(t, summary.PaidUntil == "" && summary.PendingCheckout, "unfinished checkout granted paid access")
	_, err = b.Checkout(ctx, user, w.ID, "year")
	requireNoError(t, err)
	require(t, f.creates == 2 && f.sessions[0]["status"] == "expired", "interval switch did not expire old checkout")
	requireNoError(t, b.CancelCheckout(ctx, user, w.ID))
	require(t, !billingState(t, b, user, w).PendingCheckout, "checkout cancellation not recorded")
}
func TestHostedBillingCurrentPaymentTruthAndReconciliation(t *testing.T) {
	ctx, b, user, w, f := newBillingFixture(t)
	_, err := b.Checkout(ctx, user, w.ID, "month")
	requireNoError(t, err)
	f.pay()
	// No webhook: the owning service's maintenance repairs missed delivery.
	requireNoError(t, b.ReconcileDue(ctx))
	summary := billingState(t, b, user, w)
	require(t, summary.Status == "active" && summary.PaidUntil != "" && summary.DeliveryLimit == 100000 && !summary.PendingCheckout, "paid invoice not reconciled: %#v", summary)
	oldPaid := summary.PaidUntil
	link, err := b.Checkout(ctx, user, w.ID, "year")
	requireNoError(t, err)
	require(t, link.Kind == "portal" && f.creates == 1, "existing subscriber received another checkout")
	f.sub["cancel_at_period_end"] = true
	requireNoError(t, b.Reconcile(ctx, user, w.ID))
	require(t, billingState(t, b, user, w).PaidUntil == oldPaid, "scheduled cancellation ended paid access early")
	f.sub["status"] = "past_due"
	f.invoice["status"] = "open"
	f.invoice["amount_remaining"] = 1900
	requireNoError(t, b.Reconcile(ctx, user, w.ID))
	require(t, billingState(t, b, user, w).PaidUntil == "", "unpaid invoice granted access")
	f.invoice["status"] = "paid"
	f.invoice["amount_remaining"] = 0
	f.sub["status"] = "active"
	requireNoError(t, b.Reconcile(ctx, user, w.ID))
	f.failReads = true
	require(t, b.Reconcile(ctx, user, w.ID) != nil, "provider outage ignored")
	require(t, billingState(t, b, user, w).PaidUntil == oldPaid, "outage erased verified paid period")
	f.failReads = false
	f.charge["disputed"] = true
	f.dispute = "needs_response"
	payload, signature := billingEvent(t, b, "evt_dispute", "charge.dispute.created", billingJSON{"charge": "ch_fixture"})
	requireNoError(t, b.HandleWebhook(ctx, payload, signature))
	require(t, !billingState(t, b, user, w).Active, "disputed payment fell back to free trial")
	f.dispute = "won"
	payload, signature = billingEvent(t, b, "evt_won", "charge.dispute.closed", billingJSON{"charge": "ch_fixture"})
	requireNoError(t, b.HandleWebhook(ctx, payload, signature))
	require(t, billingState(t, b, user, w).PaidUntil == oldPaid, "won dispute did not restore access")
	f.charge["refunded"] = true
	// An old invoice.paid notification must observe the current refund.
	payload, signature = billingEvent(t, b, "evt_late_paid", "invoice.paid", billingJSON{"customer": f.customer})
	requireNoError(t, b.HandleWebhook(ctx, payload, signature))
	require(t, !billingState(t, b, user, w).Active, "stale paid event undid refund")
	requests := f.requests
	requireNoError(t, b.HandleWebhook(ctx, payload, signature))
	require(t, f.requests == requests, "duplicate event was processed again")
	requireCode(t, b.HandleWebhook(ctx, payload, "bad"), CodeUnauthenticated)
}
func TestHostedBillingAuthorityAndTerms(t *testing.T) {
	ctx, b, user, w, f := newBillingFixture(t)
	stranger := hostedTestUser(t, "stranger")
	_, err := b.Checkout(ctx, stranger, w.ID, "month")
	requireCode(t, err, CodePermissionDenied)
	require(t, f.requests == 0, "unauthorized caller reached Stripe")
	_, err = b.Checkout(ctx, user, w.ID, "weekly")
	requireCode(t, err, CodeInvalidArgument)
	f.wrongPrice = true
	_, err = b.Checkout(ctx, user, w.ID, "month")
	requireCode(t, err, CodeConflict)
	require(t, f.creates == 0, "wrong published price was sold")
	f.wrongPrice = false
	_, err = b.Checkout(ctx, user, w.ID, "month")
	requireNoError(t, err)
	f.pay()
	f.sub["customer"] = "cus_elsewhere"
	requireCode(t, b.Reconcile(ctx, user, w.ID), CodeConflict)
	require(t, billingState(t, b, user, w).PaidUntil == "", "foreign customer granted access")
	f.sub["customer"] = f.customer
	f.invoice["livemode"] = true
	requireCode(t, b.Reconcile(ctx, user, w.ID), CodeConflict)
	payload, signature := billingEvent(t, b, "evt_other", "invoice.paid", billingJSON{"customer": "cus_other_product"})
	requireNoError(t, b.HandleWebhook(ctx, payload, signature))
	_, err = checkedBillingLink("checkout", "https://checkout.stripe.com.evil.test/pay")
	requireCode(t, err, CodeInternal)
}
func TestHostedTrialCannotRestartAndExpiredTasksAreBlocked(t *testing.T) {
	ctx, b, user, w, _ := newBillingFixture(t)
	device, err := b.accounts.EnrollDevice(ctx, user, w.ID, "Task session")
	requireNoError(t, err)
	session, err := b.accounts.StartDeviceSession(ctx, device.Token, "Task session", "")
	requireNoError(t, err)
	_, err = b.accounts.store.db.ExecContext(ctx, "UPDATE hosted_trial_accounts SET ends_at=? WHERE user_id=?", nowMillis()-1000, user.ID)
	requireNoError(t, err)
	other, err := b.accounts.CreateWorkspace(ctx, user, "Second workspace")
	requireNoError(t, err)
	defer b.accounts.runtime.DeleteScope(context.Background(), other.ID)
	require(t, !billingState(t, b, user, other).Active, "creating another workspace restarted free trial")
	_, err = b.accounts.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET trial_ends_at=0,paid_until=0 WHERE scope_id=?", w.ID)
	requireNoError(t, err)
	_, err = b.accounts.runtime.AddTask(ctx, session.AgentToken, AddTaskInput{Title: "New work"})
	requireCode(t, err, CodePermissionDenied)
	_, err = b.accounts.CreateHumanTask(ctx, user, w.ID, AddTaskInput{Title: "New human work"})
	requireCode(t, err, CodePermissionDenied)
}
