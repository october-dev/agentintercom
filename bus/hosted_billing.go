package bus

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

const hostedBillingFeature = "october_bus"

type HostedBillingOptions struct {
	SecretKey             string
	WebhookSecret         string
	MonthlyPriceID        string
	AnnualPriceID         string
	PortalConfigurationID string
	PublicURL             string
	// AutomaticTax requires a configured Stripe Tax account and price tax behavior.
	AutomaticTax bool
}
type HostedBilling struct {
	accounts *HostedAccounts
	stripe   *stripe.Client
	options  HostedBillingOptions
	live     bool
	requests chan struct{}
}
type BillingLink struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}
type billingRow struct {
	ScopeID, Customer, Subscription, Status, Interval string
	Cancel                                            bool
	PeriodEnd, Reconciled                             int64
}
type checkoutAttempt struct {
	ID, ScopeID, UserID, Interval, PriceID, SessionID, State string
	Created, Expires                                         int64
}

var stripeKeyPattern = regexp.MustCompile(`^(sk|rk)_(test|live)_[A-Za-z0-9_]+$`)
var stripeObjectPattern = regexp.MustCompile(`^[a-z]+_[A-Za-z0-9_]{3,240}$`)

func NewHostedBilling(accounts *HostedAccounts, options HostedBillingOptions) (*HostedBilling, error) {
	match := stripeKeyPattern.FindStringSubmatch(options.SecretKey)
	origin, err := url.Parse(options.PublicURL)
	if accounts == nil || match == nil || !strings.HasPrefix(options.WebhookSecret, "whsec_") || len(options.WebhookSecret) < 20 || err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return nil, errors.New("hosted billing requires Stripe credentials and the HTTPS service origin")
	}
	for _, id := range []string{options.MonthlyPriceID, options.AnnualPriceID, options.PortalConfigurationID} {
		if !stripeObjectPattern.MatchString(id) {
			return nil, errors.New("hosted billing price and portal IDs must be configured")
		}
	}
	if options.MonthlyPriceID == options.AnnualPriceID {
		return nil, errors.New("monthly and annual prices must differ")
	}
	options.PublicURL = strings.TrimRight(options.PublicURL, "/")
	backend := stripe.NewBackendsWithConfig(&stripe.BackendConfig{HTTPClient: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, MaxNetworkRetries: stripe.Int64(0), EnableTelemetry: stripe.Bool(false), LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull}})
	return &HostedBilling{accounts: accounts, options: options, live: match[2] == "live", stripe: stripe.NewClient(options.SecretKey, stripe.WithBackends(backend)), requests: make(chan struct{}, 4)}, nil
}
func (b *HostedBilling) price(interval string) (string, int64, error) {
	switch interval {
	case "month":
		return b.options.MonthlyPriceID, 1900, nil
	case "year":
		return b.options.AnnualPriceID, 19000, nil
	default:
		return "", 0, Errorf(CodeInvalidArgument, "Billing interval must be month or year")
	}
}
func (b *HostedBilling) priceInterval(id string) (string, error) {
	if id == b.options.MonthlyPriceID {
		return "month", nil
	}
	if id == b.options.AnnualPriceID {
		return "year", nil
	}
	return "", Errorf(CodeConflict, "Subscription price does not belong to this Bus plan")
}
func billingUnavailable() error {
	return Errorf(CodeInternal, "Billing could not be verified. No new purchase was started; try again shortly")
}
func (b *HostedBilling) begin(ctx context.Context, scopeID string, user *HostedUser) (*sqlTransaction, error) {
	if err := validateIdentity(scopeID, "workspace", false); err != nil {
		return nil, err
	}
	// Billing has its own lock namespace. Slow provider calls must not hold the
	// workspace message/receipt lock. API concurrency is separately bounded.
	tx, err := b.accounts.store.beginTx(ctx, "billing/"+scopeID)
	if err != nil {
		return nil, err
	}
	if user != nil {
		if err := validateHostedUser(*user); err != nil {
			tx.Rollback()
			return nil, err
		}
		role, err := hostedMember(ctx, tx, scopeID, user.ID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if role != "owner" {
			tx.Rollback()
			return nil, Errorf(CodePermissionDenied, "Only a workspace owner can manage billing")
		}
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&exists); err != nil {
		tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Errorf(CodeNotFound, "Workspace is no longer available")
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_billing(scope_id) VALUES(?) ON CONFLICT(scope_id) DO NOTHING", scopeID); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func billingFrom(ctx context.Context, q sqlExecutor, scopeID string) (billingRow, error) {
	value := billingRow{ScopeID: scopeID}
	err := q.QueryRowContext(ctx, "SELECT COALESCE(customer_id,''),COALESCE(subscription_id,''),status,interval,cancel_at_period_end,current_period_end,reconciled_at FROM hosted_billing WHERE scope_id=?", scopeID).Scan(&value.Customer, &value.Subscription, &value.Status, &value.Interval, &value.Cancel, &value.PeriodEnd, &value.Reconciled)
	return value, err
}
func pendingCheckout(ctx context.Context, q sqlExecutor, scopeID string) (*checkoutAttempt, error) {
	var value checkoutAttempt
	value.ScopeID = scopeID
	err := q.QueryRowContext(ctx, "SELECT attempt_id,requested_by,interval,price_id,created_at,expires_at,COALESCE(session_id,''),state FROM hosted_checkout_attempts WHERE scope_id=? AND state='pending'", scopeID).Scan(&value.ID, &value.UserID, &value.Interval, &value.PriceID, &value.Created, &value.Expires, &value.SessionID, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &value, err
}
func (b *HostedBilling) verifyPrice(ctx context.Context, interval string, sellable bool) error {
	id, amount, err := b.price(interval)
	if err != nil {
		return err
	}
	params := &stripe.PriceRetrieveParams{}
	params.AddExpand("product")
	price, err := b.stripe.V1Prices.Retrieve(ctx, id, params)
	if err != nil {
		return billingUnavailable()
	}
	if price.ID != id || price.Livemode != b.live || price.Currency != "usd" || price.UnitAmount != amount || price.Recurring == nil || string(price.Recurring.Interval) != interval || price.Recurring.IntervalCount != 1 || price.Recurring.UsageType != "licensed" || price.BillingScheme != "per_unit" || price.Type != "recurring" || price.Product == nil || price.Product.Metadata["feature"] != hostedBillingFeature || (sellable && (!price.Active || !price.Product.Active)) {
		return Errorf(CodeConflict, "Stripe price configuration does not match the published Bus plan")
	}
	return nil
}
func (b *HostedBilling) portal(ctx context.Context, customer, scopeID string) (BillingLink, error) {
	if customer == "" {
		return BillingLink{}, Errorf(CodeNotFound, "This workspace has no billing account yet")
	}
	config, err := b.stripe.V1BillingPortalConfigurations.Retrieve(ctx, b.options.PortalConfigurationID, nil)
	if err != nil {
		return BillingLink{}, billingUnavailable()
	}
	f := config.Features
	if !config.Active || config.Livemode != b.live || f == nil || f.SubscriptionUpdate == nil || f.SubscriptionUpdate.Enabled || f.SubscriptionCancel == nil || !f.SubscriptionCancel.Enabled || f.SubscriptionCancel.Mode != "at_period_end" || f.PaymentMethodUpdate == nil || !f.PaymentMethodUpdate.Enabled || f.InvoiceHistory == nil || !f.InvoiceHistory.Enabled {
		return BillingLink{}, Errorf(CodeConflict, "Billing portal must support payment methods, invoices and cancellation at the period end, without subscription changes")
	}
	session, err := b.stripe.V1BillingPortalSessions.Create(ctx, &stripe.BillingPortalSessionCreateParams{Configuration: stripe.String(config.ID), Customer: stripe.String(customer), ReturnURL: stripe.String(b.options.PublicURL + "/app?billing=return&workspace=" + url.QueryEscape(scopeID))})
	if err != nil {
		return BillingLink{}, billingUnavailable()
	}
	return checkedBillingLink("portal", session.URL)
}
func checkedBillingLink(kind, address string) (BillingLink, error) {
	expected := "checkout.stripe.com"
	if kind == "portal" {
		expected = "billing.stripe.com"
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != expected || u.User != nil {
		return BillingLink{}, Errorf(CodeInternal, "Billing provider returned an invalid destination")
	}
	return BillingLink{Kind: kind, URL: address}, nil
}
func (b *HostedBilling) Portal(ctx context.Context, user HostedUser, scopeID string) (BillingLink, error) {
	tx, err := b.begin(ctx, scopeID, &user)
	if err != nil {
		return BillingLink{}, err
	}
	defer tx.Rollback()
	row, err := billingFrom(ctx, tx, scopeID)
	if err != nil {
		return BillingLink{}, err
	}
	result, err := b.portal(ctx, row.Customer, scopeID)
	if err != nil {
		return BillingLink{}, err
	}
	return result, tx.Commit()
}

// Prepare durably records the exact selection before any checkout creation.
// Creating an unbilled customer can be retried independently; no Checkout can
// be created until its chosen customer has committed to the workspace.
func (b *HostedBilling) prepareCheckout(ctx context.Context, user HostedUser, scopeID, interval string) error {
	price, _, err := b.price(interval)
	if err != nil {
		return err
	}
	tx, err := b.begin(ctx, scopeID, &user)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := workspaceOpen(ctx, tx, scopeID); err != nil {
		return err
	}
	row, err := billingFrom(ctx, tx, scopeID)
	if err != nil {
		return err
	}
	if row.Customer == "" {
		params := &stripe.CustomerCreateParams{Email: stripe.String(user.Email), Metadata: map[string]string{"feature": hostedBillingFeature, "workspace_id": scopeID}}
		params.SetIdempotencyKey("agentintercom-customer:" + scopeID)
		customer, err := b.stripe.V1Customers.Create(ctx, params)
		if err != nil {
			return billingUnavailable()
		}
		if customer.ID == "" || customer.Livemode != b.live || customer.Metadata["workspace_id"] != scopeID || customer.Metadata["feature"] != hostedBillingFeature {
			return Errorf(CodeConflict, "Billing customer identity mismatch")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE hosted_billing SET customer_id=? WHERE scope_id=?", customer.ID, scopeID); err != nil {
			return err
		}
	}
	pending, err := pendingCheckout(ctx, tx, scopeID)
	if err != nil {
		return err
	}
	if pending == nil && (row.Subscription == "" || row.Status == "canceled" || row.Status == "incomplete_expired") {
		id, err := randomID("purchase_")
		if err != nil {
			return err
		}
		now := nowMillis()
		_, err = tx.ExecContext(ctx, "INSERT INTO hosted_checkout_attempts(attempt_id,scope_id,requested_by,interval,price_id,created_at,expires_at) VALUES(?,?,?,?,?,?,?)", id, scopeID, user.ID, interval, price, now, now+int64(24*time.Hour/time.Millisecond))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (b *HostedBilling) checkoutIdentity(session *stripe.CheckoutSession, row billingRow, attempt *checkoutAttempt) error {
	if session.ID == "" || (attempt.SessionID != "" && session.ID != attempt.SessionID) || session.Livemode != b.live || session.Customer == nil || session.Customer.ID != row.Customer || session.ClientReferenceID != row.ScopeID || session.Mode != "subscription" || session.Metadata["feature"] != hostedBillingFeature || session.Metadata["workspace_id"] != row.ScopeID || session.Metadata["checkout_attempt"] != attempt.ID || session.Metadata["interval"] != attempt.Interval {
		return Errorf(CodeConflict, "Checkout identity could not be verified")
	}
	return nil
}
func (b *HostedBilling) checkoutSession(ctx context.Context, row billingRow, attempt *checkoutAttempt, create bool) (*stripe.CheckoutSession, error) {
	if attempt.SessionID != "" {
		session, err := b.stripe.V1CheckoutSessions.Retrieve(ctx, attempt.SessionID, nil)
		if err != nil {
			return nil, billingUnavailable()
		}
		return session, b.checkoutIdentity(session, row, attempt)
	}
	// Recover a lost response before replaying a creation request. Bound the scan
	// and fail closed rather than inventing a fresh purchase after provider expiry.
	params := &stripe.CheckoutSessionListParams{Customer: stripe.String(row.Customer)}
	params.Limit = stripe.Int64(100)
	var found *stripe.CheckoutSession
	count := 0
	for session, err := range b.stripe.V1CheckoutSessions.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, billingUnavailable()
		}
		count++
		if count > 1000 {
			return nil, Errorf(CodeConflict, "Checkout history needs reconciliation before another purchase")
		}
		if session.Metadata["checkout_attempt"] == attempt.ID {
			if found != nil {
				return nil, Errorf(CodeConflict, "Multiple checkout records require reconciliation")
			}
			found = session
		}
	}
	if found != nil {
		return found, b.checkoutIdentity(found, row, attempt)
	}
	if !create {
		return nil, nil
	}
	if nowMillis()-attempt.Created >= int64(23*time.Hour/time.Millisecond) {
		return nil, Errorf(CodeConflict, "This checkout needs a payment status check before a new purchase can start")
	}
	metadata := map[string]string{"feature": hostedBillingFeature, "workspace_id": row.ScopeID, "checkout_attempt": attempt.ID, "interval": attempt.Interval}
	input := &stripe.CheckoutSessionCreateParams{Mode: stripe.String("subscription"), Customer: stripe.String(row.Customer), ClientReferenceID: stripe.String(row.ScopeID), LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{Price: stripe.String(attempt.PriceID), Quantity: stripe.Int64(1)}}, Metadata: metadata, SubscriptionData: &stripe.CheckoutSessionCreateSubscriptionDataParams{Metadata: metadata}, SuccessURL: stripe.String(b.options.PublicURL + "/app?billing=success&workspace=" + url.QueryEscape(row.ScopeID)), CancelURL: stripe.String(b.options.PublicURL + "/app?billing=cancelled&workspace=" + url.QueryEscape(row.ScopeID)), ExpiresAt: stripe.Int64(attempt.Expires / 1000), AllowPromotionCodes: stripe.Bool(false), AutomaticTax: &stripe.CheckoutSessionCreateAutomaticTaxParams{Enabled: stripe.Bool(b.options.AutomaticTax)}, CustomerUpdate: &stripe.CheckoutSessionCreateCustomerUpdateParams{Address: stripe.String("auto")}}
	input.SetIdempotencyKey("agentintercom-checkout:" + attempt.ID)
	session, err := b.stripe.V1CheckoutSessions.Create(ctx, input)
	if err != nil {
		return nil, Errorf(CodeInternal, "Checkout status could not be confirmed. Retry the same selection to recover it safely")
	}
	return session, b.checkoutIdentity(session, row, attempt)
}
