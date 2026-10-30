# E-commerce Platform — TODO

Source spec: [workflow.md](workflow.md) · Docs: [docs/](docs/)

## Stack

| Layer | Choice |
|---|---|
| Backend | Go 1.25, `chi` router, `log/slog` structured logging |
| Frontend | Server-rendered Go `html/template` + **HTMX**, Tailwind CSS (standalone CLI, no Node build) |
| Database | PostgreSQL 16, `pgx` (hand-written, parameterised SQL), `goose` migrations |
| Cache / rate limit | Redis |
| Object storage | MinIO (product, category and campaign images) |
| Auth (admin) | Server-side sessions (`scs` + Postgres), bcrypt, CSRF (`nosurf`), RBAC |
| Courier | Steadfast (official API only) |
| Tests | `go test` (unit), PostgreSQL integration tests, Playwright E2E (Docker) |
| Deployment | Docker Compose (dev) · Compose + Caddy auto-TLS + nightly backups (prod) · GitHub Actions CI |

**Where this differs from the original plan:**
- `templ` → `html/template` and `sqlc` → hand-written SQL, which avoids code-generation steps.
- The sales chart is drawn in CSS instead of Chart.js, so the page has no third-party JavaScript and a strict CSP.
- Images are re-encoded to JPEG or PNG, not WebP, because Go has no pure-Go WebP encoder. WebP uploads are still accepted.

---

## Phase 0 — Planning
- [x] `docs/architecture.md`: modules, request flow, package layout
- [x] `docs/schema.md`, including an ERD
- [x] API contract: `web/static/openapi.yaml`, served at `/api/openapi.yaml`, with a docs page at `/api/docs`
- [x] Official Steadfast API checked and recorded in `docs/steadfast.md`
- [x] Assumptions recorded (README → Assumptions)

## Phase 1 — Setup, database, architecture, auth
- [x] Module layout, env config, `.env.example`, Docker Compose (app, Postgres, Redis, MinIO)
- [x] Migrations; middleware: request ID, logging, panic recovery, error pages, security headers, CSRF, rate limiting
- [x] Admin login/logout, bcrypt, sessions, RBAC (admin and staff), audit log, `create-admin` CLI
- [x] Admin user management (create, edit, deactivate, reset password, protection for the last admin) and self-service password change
- [x] Tests: permissions (unit), auth flows (E2E)

## Phase 2 — Catalog
- [x] Categories (two levels, ordering, enable/disable, images)
- [x] Products: variants, specifications, images in MinIO, featured/best-seller flags, status, soft delete, preview
- [x] Search (full-text and trigram) and filters (category, price, availability, discount); sorting (price, newest, popularity, best-selling, trending); pagination
- [x] Image optimisation: resize to 1600 px, 480 px thumbnails, EXIF stripped, pixel-bomb guard
- [x] Tests: product create/update and search/filter (integration and E2E), image processing (unit)

## Phase 3 — Cart & checkout
- [x] Guest cart in localStorage, revalidated by the server on every view
- [x] Offers engine: percentage or fixed, date windows, product and category targets, no stacking, expired and invalid campaigns rejected
- [x] Delivery zones, free-delivery threshold, COD on/off switch
- [x] BD phone validation; atomic order creation; row locks; idempotency; HMAC-signed confirmation link; tracking
- [x] Tests: cart, checkout, invalid phone, duplicate submits, 20 concurrent buyers, discounts and offers, out-of-stock, tracking

## Phase 4 — Orders & inventory
- [x] Order state machine with history (who and when); notes; cancel and return with automatic stock handling
- [x] Inventory: stock, reserved and available; adjustments; movement ledger; low-stock alerts
- [x] Invoice printing, CSV export (formula-injection safe), customers derived from orders, with notes
- [x] Tests: cancellation and return restocking, transitions

## Phase 5 — Admin dashboard
- [x] Live KPIs, 14-day sales chart, order-status distribution, best sellers, recent orders, low stock
- [x] Campaign management UI (banner, schedule, targets, homepage promotion)
- [x] Homepage section manager (enable and reorder sections)
- [x] Store settings, delivery zones, audit log viewer

## Phase 6 — Steadfast Courier
- [x] Server-side credentials (env); settings panel shows connection status and balance
- [x] Admin "Send to Steadfast" for confirmed orders only; optional auto-send when an order is confirmed
- [x] Duplicate prevention (unique row per order, claimed before the API call)
- [x] Consignment ID, tracking code, status, and the raw request/response log stored
- [x] Status sync: background polling with an advisory lock, plus a manual refresh. `delivered` completes the order. The API documents no webhooks.
- [x] Ambiguous-failure reconciliation via `trackings_by_invoice`; retries for read calls
- [x] Delivery eligibility: customer courier history from `fraud_check`
- [x] Tests: client (`httptest`), and service with PostgreSQL plus a fake Steadfast

## Phase 7 — SEO, performance, security, testing
- [x] Slug URLs, per-page titles and descriptions, Open Graph, product JSON-LD, `sitemap.xml`, `robots.txt`
- [x] Lazy-loaded thumbnails with fixed dimensions; immutable media caching; gzip
- [x] Redis cache for the homepage and category tree, invalidated on any admin write
- [x] Indexes for search, filters, orders and courier sync
- [x] Security pass: CSP, CSRF, `TRUST_PROXY` (no IP spoofing for rate limits), cookie flags, upload hardening, HSTS (Caddy)
- [x] Playwright E2E: guest purchase journey on mobile and desktop, admin login and order fulfilment, draft preview
- [x] GitHub Actions CI: gofmt, vet, unit and integration tests with the race detector, Docker build, E2E

## Phase 8 — Deployment & documentation
- [x] `docker-compose.prod.yml` + Caddy (TLS, HSTS) + nightly `pg_dump` service
- [x] Seed data (demo catalog, campaign, first admin)
- [x] README, `docs/deployment.md` (setup, first admin, updates, backup and restore, operations)
- [x] OpenAPI served at `/api/openapi.yaml`, with a human-readable page at `/api/docs`

## Possible next steps (outside the original spec)
- Online payments (bKash, Nagad, SSLCommerz)
- SMS order notifications
- Bulk Steadfast booking (`create_order/bulk-order`) and return requests
- Product reviews and wishlists
