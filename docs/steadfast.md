# Steadfast Courier integration

## Source of truth

Official API documentation (Postman): <https://documenter.getpostman.com/view/26211192/2sAYJ1mhpk>. I checked it on 2026-10-30 by reading the collection JSON behind that page.

The client (`internal/courier/steadfast`) uses **only** the endpoints and fields shown below. Nothing here was taken from third-party SDKs.

| Item | Value (from the official collection) |
|---|---|
| Base URL | `https://portal.packzy.com/api/v1` (override with `STEADFAST_BASE_URL`) |
| Auth headers | `Api-Key`, `Secret-Key` |
| Create order | `POST /create_order` with body `invoice`, `recipient_name`, `recipient_phone`, `recipient_address`, `cod_amount`, and optionally `alternative_phone`, `recipient_email`, `item_description`, `note` |
| Create response | `{"status":200,"message":…,"consignment":{"consignment_id","invoice","tracking_code","status":"in_review",…}}` |
| Status | `GET /status_by_cid/{consignment_id}` → `{"status":200,"delivery_status":"…"}` |
| Trackings by invoice | `GET /trackings_by_invoice/{invoice}` → `{"tracking":[{"consignment_id",…}]}` |
| Balance | `GET /get_balance` → `{"current_balance":…}` |
| Fraud check | `GET /fraud_check/{phone}` → `{"total_parcels","total_delivered","total_cancelled",…}` |

The collection also documents bulk orders, return requests, pickup requests, payments and police stations. Those aren't needed yet.

**Not in the official collection:**
- **Webhooks.** So status sync is done by polling `status_by_cid`.
- **A full list of delivery-status values.** The examples show `in_review` and `partial_delivered`. The app stores whatever value it receives and shows it to the admin, and it acts automatically only on `delivered`.

## Behaviour

1. **Credentials** come from `STEADFAST_API_KEY` and `STEADFAST_SECRET_KEY` on the server. The admin settings page shows only whether they're configured, the last four characters of the key, and the live balance.
2. **Eligibility:** only orders that are `confirmed`, `processing` or `ready_to_ship` can be sent. Nothing is sent at checkout. Setting `STEADFAST_AUTO_SEND=true` sends the order automatically at the moment an admin confirms it.
3. **Request mapping:**
   - `invoice` = our order number
   - `cod_amount` = order total in taka
   - `recipient_address` = "address, area, district"
   - `item_description` = "Product (Variant) xQty, …"
   - `note` = the customer's delivery instructions plus the admin's note
4. **Duplicate prevention:**
   - A `courier_consignments` row with a unique `order_id` is inserted in state `creating` before the HTTP call, so concurrent clicks fail fast.
   - A retry is only allowed when the previous attempt is `failed`.
   - Steadfast also enforces unique invoices.
5. **Errors and retries:**
   - `create_order` is never retried automatically, because a lost response could otherwise create two parcels.
   - **Ambiguous failures** (timeouts, 5xx, 429): the app looks up `trackings_by_invoice/{invoice}`. If Steadfast did create the parcel, the consignment is recorded as created. Otherwise it is marked `failed` and the admin can retry.
   - **Validation errors (4xx):** the order is marked `failed` with Steadfast's message.
   - Read-only calls (status, trackings, balance, fraud check) are retried up to twice with backoff on network errors, 429 and 5xx.
6. **Audit trail:** every request and response is stored in `courier_events` (HTTP status, body, error) and shown on the order page under "API log". Admin actions also go to the audit log.
7. **Status sync:**
   - A background job runs every `STEADFAST_SYNC_INTERVAL` (default 30 minutes) and polls consignments that aren't final and are less than 60 days old.
   - A Postgres advisory lock makes sure only one app instance syncs at a time.
   - Admins can also click "Refresh delivery status".
   - When the courier reports `delivered`, the order moves through `shipped → delivered`, which deducts stock and marks the payment `paid`. Other statuses (`hold`, `cancelled`, `partial_delivered`, …) are only displayed, because the right reaction needs a human (contact the customer, start a return).
8. **Delivery eligibility:** the order page loads the customer's Steadfast history (`fraud_check`) and shows a delivered-percentage badge. This helps the admin decide whether to call the customer before confirming a COD order.

## Testing

- `internal/courier/steadfast/client_test.go` tests the client against `httptest` servers:
  - documented request shape and headers
  - parsing 422 validation errors
  - 502 and timeouts treated as ambiguous and not retried
  - connection refused treated as "not sent"
  - retries on status reads
  - trackings and fraud-check parsing
- `internal/courier/courier_integration_test.go` tests the full service with PostgreSQL and a fake Steadfast:
  - confirmation required before sending
  - 5 concurrent sends produce 1 API call
  - a failed send can be retried
  - a 502 after creation is reconciled without a second parcel
  - a `hold` status leaves the order unchanged, `delivered` completes it, and final consignments stop being polled
