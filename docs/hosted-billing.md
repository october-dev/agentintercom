# Hosted workspace billing

A workspace subscription costs **US$19 per month or US$190 per year**, plus
applicable tax. It includes five members, 50 concurrent sessions, 100,000 new
recipient deliveries per UTC calendar month, 5 GiB of files, and 25 GiB each of
uploads and downloads per UTC calendar month. Files are limited to 16 MiB each.
Hosted completed history is eligible for cleanup after 90 days. New work pauses
at a 5 GiB estimate of retained coordination data; see [data and recovery](workspace-data.md).
Model and compute charges are separate. There are no automatic usage overages.
Self-hosted and local use do not require this subscription.

The first workspace starts the account's 14-day trial; additional workspaces
share that end date. Each trial workspace includes 2,000 new deliveries in total,
256 MiB of files and 1 GiB in each transfer direction per calendar month. Retrying
messages, replying to accepted requests and acknowledging delivery do not consume
another delivery. A channel notification counts once per recipient.

These are implemented plan settings, not evidence that a particular deployment
is accepting payments. Qualify the full checkout and recovery flow in Stripe
**test mode** before enabling a live catalog.

## Customer flow

Open **Billing & usage** in the workspace. Only owners can start checkout or
manage payments; other members can see usage. Checkout opens Stripe. Returning
to the workspace is not proof of payment: the service checks the subscription
and its funded invoice before extending access. **Check payment status** performs
that check immediately. Signed webhooks and a periodic reconciliation job also
update access without an open browser.

Repeated checkout attempts resume the same pending purchase. Choosing another
billing interval first expires the unfinished checkout. Once a subscription
exists, payment settings open the customer portal. The portal supports invoices,
payment methods and cancellation at the end of the paid period. It does not
support switching plans or quantities; those changes need a separate, explicitly
qualified proration flow.

After the paid period or trial ends, new work and new sessions are refused.
Existing sessions may finish accepted work, and members retain access to history
and workspace management. Cancellation at period end preserves the verified
paid period. An immediate cancellation, fully refunded invoice or unresolved
payment dispute removes that invoice's entitlement. A provider outage cannot
extend access beyond the last verified paid period. Partial refunds do not
shorten the period automatically. Manual off-Stripe payments must be explicitly
recorded as paid in Stripe.

## Operator configuration

Use the existing merchant account with a separate Bus product, prices, portal
configuration and webhook. The service owns only its own workspace entitlements;
it does not modify another product's subscriptions or account tables.

Supply these in an owner-only service environment or secret manager:

| Variable | Required value |
| --- | --- |
| `OCTOBER_BUS_STRIPE_SECRET_KEY` | Stripe secret/restricted key for the selected mode |
| `OCTOBER_BUS_STRIPE_WEBHOOK_SECRET` | Signing secret of this service's webhook |
| `OCTOBER_BUS_STRIPE_MONTHLY_PRICE` | Active USD recurring price, 1900 cents, monthly, quantity one |
| `OCTOBER_BUS_STRIPE_ANNUAL_PRICE` | Active USD recurring price, 19000 cents, yearly, quantity one |
| `OCTOBER_BUS_STRIPE_PORTAL_CONFIGURATION` | Dedicated active portal configuration in the same mode |
| `OCTOBER_BUS_STRIPE_AUTOMATIC_TAX` | `true` only after configuring and qualifying Stripe Tax |

Prices must use per-unit licensed billing with an interval count of one. Their
active product must have metadata `feature=october_bus`. The portal must enable
payment method updates, invoice history and cancellation with `mode=at_period_end`,
and disable subscription updates. The service validates this configuration
before sending a customer to checkout or the portal. Do not change a configured
price ID while existing subscriptions reference it.

Tax registration and the price's tax behavior must match the merchant's setup.
The automatic-tax option does not register the merchant for tax. Validate the
actual checkout totals before enabling live payments. Never use a live payment
method to test this implementation.

Configure a **snapshot webhook** at `<public-origin>/api/billing/webhook` with
Stripe API **2026-09-30.endive**, matching the pinned stripe-go v87 SDK. Subscribe to:

- `checkout.session.completed`, `checkout.session.expired`,
  `checkout.session.async_payment_succeeded`, `checkout.session.async_payment_failed`
- `customer.subscription.created`, `customer.subscription.updated`,
  `customer.subscription.deleted`
- `invoice.paid`, `invoice.payment_failed`, `invoice.voided`,
  `invoice.marked_uncollectible`
- `charge.refunded`, `charge.dispute.created`, `charge.dispute.updated`,
  `charge.dispute.closed`
- `credit_note.created`, `credit_note.updated`, `credit_note.voided`

The endpoint verifies the raw body's signature with five-minute tolerance,
checks test/live mode and rejects Connect-account events. Events for customers
belonging to other products are ignored. Duplicate event IDs are recorded only
after reconciliation commits. Event arrival order never decides access: the
service reads current subscriptions, invoices, payments, refunds and disputes.
Secrets and provider error bodies are not logged or sent to agent subprocesses.

The scheduled job starts after one minute, processes at most 20 due workspaces
per pass and refreshes successful accounts roughly every 30 minutes. Failed
accounts retry after at least five minutes without starving other workspaces.
Monitor its generic failure log and Stripe webhook delivery failures. Repeated
configuration/identity errors require operator repair; do not grant access by
editing a checkout return URL or trusting a client-supplied subscription ID.

## HTTP API

Account authentication and normal workspace authorization apply:

| Method | Path suffix after `/api/workspaces/{workspace}` | Purpose |
| --- | --- | --- |
| GET | `/billing` | Entitlement, published prices and current usage |
| POST | `/billing/checkout` | Start/resume checkout with `{"interval":"month"}` or `year` |
| POST | `/billing/portal` | Open the owner's payment settings |
| POST | `/billing/refresh` | Reconcile current Stripe state |
| POST | `/billing/cancel-checkout` | Expire an unfinished checkout; does not cancel a subscription |

All purchasing state is persisted in migration `010_billing.sql`. Purchase
attempts are immutable and have one pending row per workspace. A lost checkout
response is recovered using its customer and attempt identity before retrying
with the original idempotency key. After the safe replay window, a new purchase
is refused until the previous session is positively reconciled or expired.

## Qualification

The isolated PostgreSQL/Stripe wire-fixture suite covers concurrent purchase
attempts, lost creation responses, interval changes, missed and duplicate
webhooks, out-of-order events after refunds, disputes, payment failure, provider
outages, foreign customer identities, published-price mismatch and owner-only
billing authority. Browser tests cover usage and the distinction between a
checkout return and a verified paid entitlement.

Before selling hosting, additionally verify real Stripe test-mode checkout,
webhook retries, renewal, cancellation and refund against the deployed service,
confirm merchant fees/tax settings, rehearse restoration of billing state, and
check capacity for the published storage limits. Synthetic tests alone do not
establish live payment readiness.
