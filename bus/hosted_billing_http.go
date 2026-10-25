package bus

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type WorkspaceBilling struct {
	RetainedBytes     int64  `json:"retainedBytes"`
	TextLimit         int64  `json:"textLimit"`
	RetentionDays     int    `json:"retentionDays"`
	Configured        bool   `json:"configured"`
	Live              bool   `json:"live"`
	CanManage         bool   `json:"canManage"`
	Active            bool   `json:"active"`
	Status            string `json:"status"`
	Interval          string `json:"interval,omitempty"`
	TrialEndsAt       string `json:"trialEndsAt"`
	PaidUntil         string `json:"paidUntil,omitempty"`
	CancelAtPeriodEnd bool   `json:"cancelAtPeriodEnd"`
	CurrentPeriodEnd  string `json:"currentPeriodEnd,omitempty"`
	PendingCheckout   bool   `json:"pendingCheckout"`
	PendingInterval   string `json:"pendingInterval,omitempty"`
	MonthlyPriceCents int64  `json:"monthlyPriceCents"`
	AnnualPriceCents  int64  `json:"annualPriceCents"`
	Currency          string `json:"currency"`
	Deliveries        int64  `json:"deliveries"`
	DeliveryLimit     int64  `json:"deliveryLimit"`
	Month             string `json:"month"`
	FileBytes         int64  `json:"fileBytes"`
	FileStorageLimit  int64  `json:"fileStorageLimit"`
	UploadedBytes     int64  `json:"uploadedBytes"`
	DownloadedBytes   int64  `json:"downloadedBytes"`
	FileTransferLimit int64  `json:"fileTransferLimit"`
	Members           int    `json:"members"`
	MemberLimit       int    `json:"memberLimit"`
	Sessions          int    `json:"sessions"`
	SessionLimit      int    `json:"sessionLimit"`
}

func (a *HostedAccounts) BillingSummary(ctx context.Context, user HostedUser, scopeID string, configured, live bool) (WorkspaceBilling, error) {
	result := WorkspaceBilling{Configured: configured, Live: live, MonthlyPriceCents: 1900, AnnualPriceCents: 19000, Currency: "usd", Month: time.Now().UTC().Format("2006-01"), Status: "none", RetentionDays: 90}
	tx, err := a.store.beginTx(withHostedMember(ctx, user), scopeID)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	role, err := hostedMember(ctx, tx, scopeID, user.ID)
	if err != nil {
		return result, err
	}
	result.CanManage = role == "owner"
	var trial, paid, disabled int64
	if err := tx.QueryRowContext(ctx, "SELECT trial_ends_at,paid_until,GREATEST(disabled_at,paused_at),delivery_limit,member_limit,session_limit FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&trial, &paid, &disabled, &result.DeliveryLimit, &result.MemberLimit, &result.SessionLimit); err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(u.retained_bytes,0),w.text_limit FROM hosted_workspaces w LEFT JOIN hosted_storage_usage u ON u.scope_id=w.scope_id WHERE w.scope_id=?", scopeID).Scan(&result.RetainedBytes, &result.TextLimit); err != nil {
		return result, err
	}
	result.TrialEndsAt = instant(trial)
	result.PaidUntil = instant(paid)
	result.Active = disabled == 0 && (trial > nowMillis() || paid > nowMillis())
	row, err := billingFrom(ctx, tx, scopeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if err == nil {
		result.Status = row.Status
		result.Interval = row.Interval
		result.CancelAtPeriodEnd = row.Cancel
		result.CurrentPeriodEnd = instant(row.PeriodEnd)
	}
	attempt, err := pendingCheckout(ctx, tx, scopeID)
	if err != nil {
		return result, err
	}
	if attempt != nil {
		result.PendingCheckout = true
		result.PendingInterval = attempt.Interval
	}
	if paid > nowMillis() {
		err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(deliveries),0) FROM hosted_usage WHERE scope_id=? AND month=?", scopeID, result.Month).Scan(&result.Deliveries)
	} else {
		result.DeliveryLimit = min(result.DeliveryLimit, 2000)
		err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(deliveries),0) FROM hosted_usage WHERE scope_id=?", scopeID).Scan(&result.Deliveries)
	}
	if err != nil {
		return result, err
	}
	result.FileStorageLimit, result.FileTransferLimit, err = attachmentLimits(ctx, tx, scopeID)
	if err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(size_bytes),0) FROM attachments WHERE scope_id=? AND deleted_at=0", scopeID).Scan(&result.FileBytes); err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(uploaded_bytes),0),COALESCE(SUM(downloaded_bytes),0) FROM attachment_usage WHERE scope_id=? AND month=?", scopeID, result.Month).Scan(&result.UploadedBytes, &result.DownloadedBytes); err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_members WHERE scope_id=?", scopeID).Scan(&result.Members); err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents WHERE scope_id=? AND lease_expires_at>? AND lifecycle!='offline'", scopeID, nowMillis()).Scan(&result.Sessions); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (h *HostedServer) registerBillingRoutes() {
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/billing", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return h.accounts.BillingSummary(ctx, user, r.PathValue("workspace"), h.billing != nil, h.billing != nil && h.billing.live)
	}))
	route := func(fn func(context.Context, HostedUser, string, http.ResponseWriter, *http.Request) (any, error)) http.HandlerFunc {
		return h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
			if h.billing == nil {
				return nil, Errorf(CodeInternal, "Paid hosting is not configured on this service")
			}
			bounded, release, err := h.billing.operation(ctx)
			if err != nil {
				return nil, err
			}
			defer release()
			return fn(bounded, user, r.PathValue("workspace"), w, r)
		})
	}
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/billing/checkout", route(func(ctx context.Context, user HostedUser, scopeID string, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Interval string `json:"interval"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		return h.billing.Checkout(ctx, user, scopeID, input.Interval)
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/billing/portal", route(func(ctx context.Context, user HostedUser, scopeID string, w http.ResponseWriter, r *http.Request) (any, error) {
		return h.billing.Portal(ctx, user, scopeID)
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/billing/refresh", route(func(ctx context.Context, user HostedUser, scopeID string, w http.ResponseWriter, r *http.Request) (any, error) {
		if err := h.billing.Reconcile(ctx, user, scopeID); err != nil {
			return nil, err
		}
		return h.accounts.BillingSummary(ctx, user, scopeID, true, h.billing.live)
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/billing/cancel-checkout", route(func(ctx context.Context, user HostedUser, scopeID string, w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]bool{"cancelled": true}, h.billing.CancelCheckout(ctx, user, scopeID)
	}))
	h.routes.HandleFunc("POST /api/billing/webhook", func(w http.ResponseWriter, r *http.Request) {
		if h.billing == nil {
			writeFailure(w, Errorf(CodeInternal, "Billing is not configured"))
			return
		}
		h.billing.serveWebhook(w, r)
	})
}
