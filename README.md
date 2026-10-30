# Bazaar BD — E-commerce platform

A production-ready e-commerce store for Bangladesh with:
- guest checkout and Cash on Delivery
- an admin dashboard
- a campaign/offer engine
- Steadfast Courier integration

| Layer | Choice |
|---|---|
| Backend | Go 1.25, chi, pgx, goose migrations, slog |
| Frontend | server-rendered `html/template` + **HTMX**, Tailwind CSS (standalone CLI, no Node) |
| Data | PostgreSQL 16 · Redis 7 (rate limits and cache) · MinIO (images) |
| Runtime | Docker Compose; each service is its own container. Production adds Caddy (HTTPS) and nightly backups |

**Docs:**
- [workflow.md](workflow.md): the original spec
- [TODO.md](TODO.md): phase checklist
- [docs/architecture.md](docs/architecture.md)
- [docs/schema.md](docs/schema.md)
- [docs/steadfast.md](docs/steadfast.md)
- [docs/deployment.md](docs/deployment.md)
- API: `/api/docs` and `/api/openapi.yaml`

## Quick start (local)

```bash
make up        # creates .env from .env.example, builds and starts app + postgres + redis + minio
make seed      # demo catalog, a demo campaign, and the first admin (SEED_ADMIN_* in .env)
```

| What | URL |
|---|---|
| Storefront | http://localhost:8080 |
| Admin | http://localhost:8080/admin (`admin@example.com` / `change-me-please`) |
| API docs | http://localhost:8080/api/docs |
| MinIO console | http://localhost:9001 |
| Health | http://localhost:8080/healthz |

## Managing services

```bash
make ps / make logs s=app / make restart      # status, logs of one service, rebuild + restart app
docker compose stop minio; docker compose up -d minio
make psql / make redis-cli
make backup / make restore file=backups/…     # database dump / restore
make down        # stop (keep data)   ·   make clean   # stop and delete volumes
```

**Faster iteration on the host:**
```bash
docker compose up -d postgres redis minio
make css-watch      # terminal 1
make run            # terminal 2
```

## Admin users

```bash
make create-admin email=owner@shop.com name="Owner" password='a-long-password' role=admin
```

You can also manage users in **Admin → Admin users**: create, edit, change role, deactivate, reset password. The last active admin can't be demoted or deactivated.

| Role | Access |
|---|---|
| admin | everything |
| staff | dashboard, orders, customers, sending orders to the courier |

## Steadfast Courier

Set `STEADFAST_API_KEY` and `STEADFAST_SECRET_KEY` in `.env`, then run `make restart`.

On a confirmed order, the admin page then offers:
- **Send to Steadfast**
- live courier status, with background sync
- an API log
- the customer's delivery history across Steadfast

`STEADFAST_AUTO_SEND=true` books the courier automatically when an admin confirms an order. Details: [docs/steadfast.md](docs/steadfast.md).

## Tests

```bash
make test               # unit tests (no services needed)
make test-integration   # + PostgreSQL integration tests (needs `docker compose up -d postgres`)
make e2e                # Playwright browser tests against the running stack (`make up && make seed` first)
```

CI (`.github/workflows/ci.yml`) runs gofmt, vet, all Go tests with the race detector against Postgres, a Docker build, and the Playwright suite.

| Spec test requirement | Where |
|---|---|
| Product create/update, search & filtering | `orders` + `courier` integration setup, Playwright admin spec, offers pricing test |
| Cart, cart persistence | Playwright storefront spec (reload keeps the cart), `orders/pricing_test.go` |
| Guest checkout, invalid phone | `phone_test.go`, `TestPlaceOrderValidation`, Playwright |
| Order creation, duplicate submission | `TestPlaceOrderComputesTotalsServerSide`, `TestPlaceOrderIsIdempotent` |
| Concurrent inventory | `TestConcurrentCheckoutNeverOversells` (20 buyers, 5 units) |
| Discounts | `TestComputeTotals`, `TestOffersApplyBestSingleDiscount`, `offers_test.go` |
| Out of stock | `TestOutOfStockRejected`, Playwright |
| Cancellation & inventory restoration | `TestStatusFlowAdjustsInventory` |
| Admin auth & permissions | `auth_test.go`, Playwright admin spec |
| Steadfast integration & API failures | `steadfast/client_test.go`, `courier_integration_test.go` |
| Order tracking | `TestTrackRequiresMatchingPhone`, Playwright |

## Production

See [docs/deployment.md](docs/deployment.md). In short:

```bash
cp .env.production.example .env.production   # fill in every value
docker compose -f docker-compose.prod.yml --env-file .env.production up -d --build
docker compose -f docker-compose.prod.yml --env-file .env.production run --rm app create-admin -email … -password …
```

## Assumptions

- **Money and pricing:**
  - All amounts are stored as integer **poisha**.
  - All variants of a product share the product's price.
  - Campaign discounts apply to the regular price. The customer pays the lower of the product's own selling price and the best running campaign, so discounts never stack.
- **Order numbers:** `YYMMDD-NNNNN` (Dhaka date plus 5 random digits, unique). Tracking needs the order number and the phone used at checkout.
- **Delivery:** the charge comes from the zone the customer picks (default zones: Inside Dhaka ৳70, Dhaka Suburbs ৳100, Outside Dhaka ৳130). Free delivery applies when the amount after discounts reaches the configured threshold.
- **Reporting:**
  - "Revenue" counts every order that is not cancelled or returned.
  - "Popularity" sorting uses the featured flag plus units sold.
  - "Trending" uses units sold in the last 30 days.
- **Stock:** reserved at order placement and deducted at shipping. A cancellation releases the reservation; a return restocks.
- **Payment status:** `paid` on delivery (cash collected), `refunded` on return.
- **Steadfast:** the API documents no webhooks, so delivery status is polled. Only `delivered` changes the order automatically.
- **Images:** stored as JPEG (or PNG when the image has transparency) at up to 1600 px, with 480 px thumbnails. WebP uploads are accepted but re-encoded. Animated GIFs keep only their first frame.
- **Customers:** identified by phone number; a later order updates the stored name and address.
