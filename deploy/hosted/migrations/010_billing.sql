-- Standalone workspace subscriptions. No entitlement writes to other products.
CREATE TABLE hosted_trial_accounts (
 user_id TEXT COLLATE "C" PRIMARY KEY,
 started_at BIGINT NOT NULL,
 ends_at BIGINT NOT NULL
);
INSERT INTO hosted_trial_accounts(user_id,started_at,ends_at)
SELECT m.user_id,MIN(w.created_at),MIN(w.trial_ends_at)
FROM hosted_members m JOIN hosted_workspaces w ON w.scope_id=m.scope_id
WHERE m.role='owner' GROUP BY m.user_id;

CREATE TABLE hosted_billing (
 scope_id TEXT COLLATE "C" PRIMARY KEY REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
 customer_id TEXT COLLATE "C" UNIQUE,
 subscription_id TEXT COLLATE "C" UNIQUE,
 status TEXT NOT NULL DEFAULT 'none',
 interval TEXT NOT NULL DEFAULT '',
 cancel_at_period_end INTEGER NOT NULL DEFAULT 0,
 current_period_end BIGINT NOT NULL DEFAULT 0,
 reconciled_at BIGINT NOT NULL DEFAULT 0,
 reconcile_attempted_at BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE hosted_checkout_attempts (
 attempt_id TEXT COLLATE "C" PRIMARY KEY,
 scope_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
 requested_by TEXT COLLATE "C" NOT NULL,
 interval TEXT NOT NULL CHECK(interval IN ('month','year')),
 price_id TEXT COLLATE "C" NOT NULL,
 created_at BIGINT NOT NULL,
 expires_at BIGINT NOT NULL,
 session_id TEXT COLLATE "C" UNIQUE,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','complete','expired'))
);
CREATE UNIQUE INDEX hosted_checkout_pending ON hosted_checkout_attempts(scope_id) WHERE state='pending';
CREATE TABLE hosted_paid_invoices (
 invoice_id TEXT COLLATE "C" PRIMARY KEY,
 scope_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
 subscription_id TEXT COLLATE "C" NOT NULL,
 price_id TEXT COLLATE "C" NOT NULL,
 period_start BIGINT NOT NULL,
 period_end BIGINT NOT NULL,
 funded INTEGER NOT NULL CHECK(funded IN (0,1)),
 checked_at BIGINT NOT NULL
);
CREATE INDEX hosted_invoices_current ON hosted_paid_invoices(scope_id,subscription_id,period_end);
CREATE TABLE hosted_billing_events (
 event_id TEXT COLLATE "C" PRIMARY KEY,
 scope_id TEXT COLLATE "C" NOT NULL REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
 event_type TEXT NOT NULL,
 processed_at BIGINT NOT NULL
);
